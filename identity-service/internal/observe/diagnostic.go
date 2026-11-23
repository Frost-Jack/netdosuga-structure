package observe

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"net/http"
	"strings"
	"time"
	"unicode"
)

var operations = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "netdosuga_operations_total", Help: "Business operation outcomes"}, []string{"service", "operation", "outcome"})
var latency = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "netdosuga_operation_duration_seconds", Help: "Business operation duration"}, []string{"service", "operation"})

func init() { prometheus.MustRegister(operations, latency) }
func Operation(service, operation string, start time.Time, failed bool) {
	outcome := "success"
	if failed {
		outcome = "error"
	}
	operations.WithLabelValues(service, operation, outcome).Inc()
	latency.WithLabelValues(service, operation).Observe(time.Since(start).Seconds())
}

// Safe only retains explicitly named diagnostic fields, recursively; free text and credentials never pass.
func Safe(v any) any {
	data, _ := json.Marshal(v)
	var value any
	json.Unmarshal(data, &value)
	var clean func(any) any
	allowed := map[string]bool{"operation": true, "scenario": true, "id": true, "offer_id": true, "order_id": true, "quantity": true, "amount_cents": true, "price_cents": true, "total_cents": true, "commission_cents": true, "gross_cents": true, "net_cents": true, "status": true, "count": true, "available": true, "capacity": true, "role": true, "offers": true, "orders": true, "order": true, "cart": true, "items": true, "sales": true, "ticket": true, "tickets": true, "application": true, "applications": true, "demo": true}
	clean = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			m := map[string]any{}
			for k, v := range x {
				if allowed[k] {
					if k == "demo" {
						if _, ok := v.(bool); !ok {
							continue
						}
					}
					if k == "operation" {
						text, ok := v.(string)
						if !ok {
							continue
						}
						switch text {
						case "catalog.list", "applications.list", "applications.create", "applications.approve", "applications.reject", "offers.list", "offers.update", "offers.publish", "cart.get", "cart.add", "cart.remove", "orders.create", "orders.list", "orders.get", "orders.pay", "orders.cancel", "tickets.redeem", "sales.get", "unknown", "login", "register", "session", "logout":
						default:
							continue
						}
					}
					container := k == "offers" || k == "orders" || k == "order" || k == "cart" || k == "items" || k == "sales" || k == "ticket" || k == "tickets" || k == "application" || k == "applications"
					if container {
						switch v.(type) {
						case map[string]any, []any:
						default:
							continue
						}
					}
					if k == "scenario" {
						text, ok := v.(string)
						if !ok || text != "success" && text != "error" && text != "delay" && text != "timeout" {
							continue
						}
					}
					if k == "role" {
						text, ok := v.(string)
						if !ok || text != "customer" && text != "vendor" && text != "admin" && text != "public" {
							continue
						}
					}
					if k == "status" {
						if text, ok := v.(string); ok {
							switch text {
							case "PENDING", "APPROVED", "REJECTED", "PUBLISHED", "DRAFT", "RESERVED", "PAID", "CANCELLED", "EXPIRED", "VALID", "REDEEMED", "REVOKED", "SUCCEEDED":
							default:
								continue
							}
						} else {
							if _, ok := v.(float64); !ok {
								continue
							}
						}
					}
					if k == "id" || k == "offer_id" || k == "order_id" {
						text, ok := v.(string)
						if !ok || len(text) > 128 || strings.IndexFunc(text, func(r rune) bool {
							return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' && r != '.'
						}) >= 0 {
							continue
						}
					}
					numeric := k == "quantity" || k == "count" || k == "capacity" || k == "available" || strings.HasSuffix(k, "_cents")
					if numeric {
						if _, ok := v.(float64); !ok {
							continue
						}
					}
					m[k] = clean(v)
				}
			}
			return m
		case []any:
			a := []any{}
			for i, v := range x {
				if i == 3 {
					break
				}
				switch v.(type) {
				case map[string]any, []any:
					a = append(a, clean(v))
				}
			}
			return a
		case string:
			if len(x) > 128 {
				return x[:128]
			}
			return x
		case float64, bool, nil:
			return x
		}
		return nil
	}
	result := clean(value)
	b, _ := json.Marshal(result)
	if len(b) > 2048 {
		return map[string]any{"status": "payload_omitted"}
	}
	return result
}
func Payload(ctx context.Context, key string, v any) any {
	v = Safe(v)
	b, _ := json.Marshal(v)
	trace.SpanFromContext(ctx).SetAttributes(attribute.String("app."+key, string(b)))
	return v
}

type QueryTracer struct{}

func (QueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	statement := sqlTemplate(data.SQL)
	fields := strings.Fields(statement)
	op := "SQL"
	if len(fields) > 0 {
		switch strings.ToUpper(fields[0]) {
		case "SELECT", "INSERT", "UPDATE", "DELETE", "CREATE", "ALTER", "BEGIN", "COMMIT", "ROLLBACK", "WITH", "SHOW", "TRUNCATE", "DROP":
			op = strings.ToUpper(fields[0])
		}
	}
	ctx, _ = otel.Tracer("database").Start(ctx, "db."+op, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(
		attribute.String("db.system", "postgresql"), attribute.String("db.operation", op),
		attribute.String("db.query.text", statement), attribute.String("db.statement", statement),
		attribute.Int("db.query.parameter_count", len(data.Args))))
	return ctx
}
func (QueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	span := trace.SpanFromContext(ctx)
	if data.Err != nil { // PostgreSQL errors may contain values or SQL: export a fixed marker only.
		span.SetAttributes(attribute.Bool("db.error", true))
		span.SetStatus(codes.Error, "database error")
	} else {
		span.SetAttributes(attribute.Int64("db.response.returned_rows", data.CommandTag.RowsAffected()))
	}
	span.End()
}

// sqlTemplate retains SQL structure, never argument values. Quoted identifiers are
// redacted too: callers may construct those from input. Only application-owned
// unquoted identifiers should be interpolated into query templates.
func sqlTemplate(sql string) string {
	if len(sql) > 65536 {
		return "[query omitted: too large]"
	}
	var out strings.Builder
	for i := 0; i < len(sql) && out.Len() < 2048; {
		c := sql[i]
		switch {
		case c == '-' && i+1 < len(sql) && sql[i+1] == '-':
			i += 2
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
			out.WriteByte(' ')
		case c == '/' && i+1 < len(sql) && sql[i+1] == '*':
			i += 2
			depth := 1
			for i < len(sql) && depth > 0 {
				if i+1 < len(sql) && sql[i:i+2] == "/*" {
					depth++
					i += 2
				} else if i+1 < len(sql) && sql[i:i+2] == "*/" {
					depth--
					i += 2
				} else {
					i++
				}
			}
			out.WriteByte(' ')
		case c == '\'' || c == '"':
			quote := c
			escaped := i > 0 && (sql[i-1] == 'e' || sql[i-1] == 'E') && (i == 1 || !sqlIdentifierByte(sql[i-2]))
			i++
			for i < len(sql) {
				if sql[i] == '\\' && quote == '\'' {
					// Without E, backslash semantics depend on the server setting. Fail closed.
					if !escaped {
						return "[query omitted: ambiguous string escape]"
					}
					i++
					if i < len(sql) {
						i++
					}
					continue
				}
				if sql[i] == quote {
					i++
					if i < len(sql) && sql[i] == quote {
						i++
						continue
					}
					break
				}
				i++
			}
			if quote == '"' {
				out.WriteString(`"?"`)
			} else {
				out.WriteByte('?')
			}
		case c == '$':
			end := i + 1
			for end < len(sql) && (sql[end] >= 128 || sql[end] == '_' || sql[end] >= 'a' && sql[end] <= 'z' || sql[end] >= 'A' && sql[end] <= 'Z' || end > i+1 && sql[end] >= '0' && sql[end] <= '9') {
				end++
			}
			if end < len(sql) && sql[end] == '$' {
				delimiter := sql[i : end+1]
				tail := strings.Index(sql[end+1:], delimiter)
				if tail < 0 {
					i = len(sql)
				} else {
					i = end + 1 + tail + len(delimiter)
				}
				out.WriteByte('?')
			} else {
				end = i + 1
				for end < len(sql) && sql[end] >= '0' && sql[end] <= '9' {
					end++
				}
				if end == i+1 {
					out.WriteByte('?')
					i++
				} else {
					out.WriteString(sql[i:end])
					i = end
				}
			}
		case c >= '0' && c <= '9' || c == '.' && i+1 < len(sql) && sql[i+1] >= '0' && sql[i+1] <= '9':
			i++
			for i < len(sql) && (sql[i] >= '0' && sql[i] <= '9' || sql[i] >= 'a' && sql[i] <= 'z' || sql[i] >= 'A' && sql[i] <= 'Z' || sql[i] == '_' || sql[i] == '.' || (sql[i] == '+' || sql[i] == '-') && i > 0 && (sql[i-1] == 'e' || sql[i-1] == 'E')) {
				i++
			}
			out.WriteByte('?')
		case c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
			// Consume identifier digits together, so column1 remains readable.
			end := i + 1
			for end < len(sql) && (sql[end] == '_' || sql[end] == '$' || sql[end] >= 'a' && sql[end] <= 'z' || sql[end] >= 'A' && sql[end] <= 'Z' || sql[end] >= '0' && sql[end] <= '9') {
				end++
			}
			out.WriteString(sql[i:end])
			i = end
		default:
			out.WriteByte(c)
			i++
		}
	}
	result := strings.Join(strings.Fields(out.String()), " ")
	if len(result) > 2048 {
		result = result[:2048]
	}
	return result
}

func AuthDiagnostic(route string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		Payload(r.Context(), "request", map[string]any{"operation": strings.TrimPrefix(route, "/v1/")})
		rw := &response{w, 200}
		h.ServeHTTP(rw, r)
		if rw.status >= 400 {
			trace.SpanFromContext(r.Context()).SetStatus(codes.Error, "authentication failed")
		}
		Operation("identity-service", strings.TrimPrefix(route, "/v1/"), start, rw.status >= 400)
		Log(r.Context(), Logger("identity-service"), "auth request", "operation", strings.TrimPrefix(route, "/v1/"), "method", r.Method, "route", route, "status", rw.status, "duration_ms", time.Since(start).Milliseconds())
		Payload(r.Context(), "response", map[string]any{"status": rw.status})
		trace.SpanFromContext(r.Context()).SetAttributes(attribute.Int("app.status", rw.status))
	})
}

func sqlIdentifierByte(c byte) bool {
	return c >= 128 || c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}
