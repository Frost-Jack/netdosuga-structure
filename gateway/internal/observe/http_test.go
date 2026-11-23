package observe

import (
	"context"
	"fmt"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPTransportRedactionAndPropagation(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	oldProvider, oldPropagator := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer func() {
		otel.SetTracerProvider(oldProvider)
		otel.SetTextMapPropagator(oldPropagator)
		provider.Shutdown(context.Background())
	}()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/payments/SECRET" || r.URL.RawQuery != "password=PRIVATE" {
			t.Error("wire URL changed")
		}
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		if !trace.SpanContextFromContext(ctx).IsValid() {
			t.Error("missing propagation")
		}
		w.WriteHeader(503)
	}))
	defer server.Close()
	ctx, parent := otel.Tracer("test").Start(context.Background(), "parent")
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/v1/payments/SECRET?password=PRIVATE", nil)
	res, err := (&http.Client{Transport: HTTPTransport{}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	parent.End()
	spans := recorder.Ended()
	if len(spans) != 2 {
		t.Fatalf("spans: %d", len(spans))
	}
	span := spans[0]
	if span.Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Error("parent lost")
	}
	attrs := map[string]string{}
	for _, a := range span.Attributes() {
		attrs[string(a.Key)] = a.Value.Emit()
	}
	if attrs["http.route"] != "/v1/payments/{order_id}" || attrs["http.response.status_code"] != "503" || attrs["http.request.method"] != "GET" {
		t.Fatalf("attrs: %v", attrs)
	}
	exported := fmt.Sprint(span.Name(), attrs, span.Events(), span.Status())
	for _, secret := range []string{"SECRET", "PRIVATE", "password="} {
		if strings.Contains(exported, secret) {
			t.Fatalf("leak: %s", secret)
		}
	}
}
