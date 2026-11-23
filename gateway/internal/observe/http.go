package observe

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"io"
	"net/http"
	"strings"
	"sync"
)

// HTTPRoute exports only known routes; identifiers, queries and credentials never enter spans.
func HTTPRoute(path string) string {
	switch path {
	case "/v1/login", "/v1/register", "/v1/logout", "/v1/session", "/v1/payments", "/v1/refunds", "/mock/payments", "/mock/refunds", "/mock/emails", "/mock/business-emails", "/healthz", "/readyz":
		return path
	}
	if strings.HasPrefix(path, "/v1/payments/") && strings.HasSuffix(path, "/cancel") {
		return "/v1/payments/{order_id}/cancel"
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

type HTTPTransport struct{ Base http.RoundTripper }

func (t HTTPTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	route := HTTPRoute(r.URL.Path)
	ctx, span := otel.Tracer("http").Start(r.Context(), r.Method+" "+route, trace.WithSpanKind(trace.SpanKindClient))

	HTTPAttributes(span, r.Method, route, 0)
	span.SetAttributes(attribute.String("server.address", r.URL.Hostname()), attribute.String("url.scheme", r.URL.Scheme))
	request := r.Clone(ctx)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(request.Header))
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	response, err := base.RoundTrip(request)
	if response != nil {
		HTTPAttributes(span, r.Method, route, response.StatusCode)
	}
	if err != nil {
		span.SetStatus(codes.Error, "HTTP transport failed")
	}
	if err != nil || response == nil {
		span.End()
	} else {
		response.Body = &httpSpanBody{ReadCloser: response.Body, span: span}
	}
	return response, err
}

type httpSpanBody struct {
	io.ReadCloser
	span trace.Span
	once sync.Once
}

func (b *httpSpanBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		if err != io.EOF {
			b.span.SetStatus(codes.Error, "HTTP response read failed")
		}
		b.once.Do(func() { b.span.End() })
	}
	return n, err
}
func (b *httpSpanBody) Close() error {
	err := b.ReadCloser.Close()
	if err != nil {
		b.span.SetStatus(codes.Error, "HTTP response close failed")
	}
	b.once.Do(func() { b.span.End() })
	return err
}
