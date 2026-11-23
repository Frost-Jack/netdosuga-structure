package observe

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"net/http"
	"strings"
)

// HTTPRoute exports only known routes; identifiers, queries and credentials never enter spans.
func HTTPRoute(path string) string {
	switch path {
	case "/v1/login", "/v1/register", "/v1/logout", "/v1/session", "/v1/payments", "/v1/refunds", "/mock/payments", "/mock/refunds", "/mock/emails", "/mock/business-emails", "/healthz", "/readyz":
		return path
	}
	if strings.HasPrefix(path, "/v1/payments/") {
		return "/v1/payments/{order_id}"
	}
	return "unmatched"
}
func HTTPAttributes(span trace.Span, method, route string, status int) {
	span.SetAttributes(attribute.String("http.request.method", method), attribute.String("http.route", route))
	if status > 0 {
		span.SetAttributes(attribute.Int("http.response.status_code", status))
		if status >= 400 {
			span.SetStatus(codes.Error, "HTTP request failed")
		}
	}
}

func HTTPHandler(h http.Handler, name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := otel.Tracer("http").Start(ctx, name, trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()
		rw := &response{w, 200}
		h.ServeHTTP(rw, r.WithContext(ctx))
		span.SetAttributes(attribute.String("server.address", "identity-service"))
		HTTPAttributes(span, r.Method, HTTPRoute(r.URL.Path), rw.status)
	})
}
