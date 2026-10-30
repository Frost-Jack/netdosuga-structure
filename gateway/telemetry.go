package main

import (
	"context"
	"encoding/json"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"log/slog"
	"math"
	"os"
	"regexp"
	"time"
)

var safeIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// safePayload never copies free text, credentials or ticket data into telemetry.
func safePayload(value any) string {
	numeric := map[string]bool{"quantity": true, "amount_cents": true, "price_cents": true, "total_cents": true, "gross_cents": true, "net_cents": true, "commission_cents": true, "capacity": true, "available": true, "count": true}
	enums := map[string]map[string]bool{
		"scenario": {"success": true, "error": true, "delay": true, "timeout": true},
		"status":   {"RESERVED": true, "PAID": true, "CANCELLED": true, "REFUNDED": true, "EXPIRED": true, "PENDING": true, "APPROVED": true, "REJECTED": true, "DRAFT": true, "PUBLISHED": true, "ACTIVE": true, "ISSUED": true, "REDEEMED": true, "SUCCEEDED": true},
	}
	containers := map[string]bool{"order": true, "orders": true, "offers": true, "cart": true, "items": true, "sales": true}
	operations := map[string]bool{}
	for _, operation := range shopRoutes {
		operations[operation] = true
	}
	var clean func(any, int) any
	clean = func(value any, depth int) any {
		if depth > 5 {
			return nil
		}
		switch value := value.(type) {
		case map[string]any:
			out := map[string]any{}
			for key, value := range value {
				if containers[key] {
					switch value.(type) {
					case map[string]any, []any:
						out[key] = clean(value, depth+1)
					}
				} else if numeric[key] {
					switch value := value.(type) {
					case int, int64:
						out[key] = value
					case float64:
						if !math.IsNaN(value) && !math.IsInf(value, 0) && value == math.Trunc(value) {
							out[key] = value
						}
					}
				} else if text, ok := value.(string); ok && len(text) <= 128 {
					if enums[key][text] || key == "operation" && operations[text] || (key == "offer_id" || key == "order_id") && safeIdentifier.MatchString(text) {
						out[key] = text
					}
				}
			}
			return out
		case []any:
			out := []any{}
			for _, item := range value {
				if len(out) == 3 {
					break
				}
				switch item.(type) {
				case map[string]any, []any:
					out = append(out, clean(item, depth+1))
				}
			}
			return out
		}
		return nil
	}
	b, _ := json.Marshal(clean(value, 0))
	if len(b) > 2048 {
		return `{"truncated":true}`
	}
	return string(b)
}
func (g *gateway) recordShop(ctx context.Context, operation, method, route string, status int, started time.Time, request, response map[string]any) {
	req, res := safePayload(request), safePayload(response)
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(attribute.String("app.request", req), attribute.String("app.response", res), attribute.String("app.operation", operation))
	logger := g.logger
	if logger == nil {
		logger = slog.Default()
	}
	hostname, _ := os.Hostname()
	sc := span.SpanContext()
	logger.InfoContext(ctx, "shop request completed", "service", "api-gateway", "instance", hostname, "operation", operation, "method", method, "route", route, "status", status, "duration_ms", time.Since(started).Milliseconds(), "trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String(), "request", req, "response", res)
}
