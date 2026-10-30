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
	fields := strings.Fields(data.SQL)
	op := "SQL"
	if len(fields) > 0 {
		switch strings.ToUpper(fields[0]) {
		case "SELECT", "INSERT", "UPDATE", "DELETE", "CREATE", "ALTER":
			op = strings.ToUpper(fields[0])
		}
	}
	ctx, _ = otel.Tracer("database").Start(ctx, "db."+op, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(attribute.String("db.system", "postgresql"), attribute.String("db.operation", op)))
	return ctx
}
func (QueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if data.Err != nil { // SQL errors may contain user input; export a fixed marker only.
		trace.SpanFromContext(ctx).SetAttributes(attribute.Bool("db.error", true))
		trace.SpanFromContext(ctx).SetStatus(codes.Error, "database error")
	}
	trace.SpanFromContext(ctx).End()
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
