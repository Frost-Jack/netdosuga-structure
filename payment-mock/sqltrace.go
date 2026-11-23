package main

import (
	"context"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"strings"
)

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

func sqlIdentifierByte(c byte) bool {
	return c >= 128 || c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}
