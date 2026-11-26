package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestFailedRequestRecordsSanitizedTelemetry(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	oldProvider, oldPropagator := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(oldProvider)
		otel.SetTextMapPropagator(oldPropagator)
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	metrics := newTelemetry("notification-test")
	handler := metrics.wrap("/fixture", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Traceparent"), "2222222222222222") {
			t.Error("request headers were changed")
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"status":"failed","token":"fixture-secret"}`))
	})
	request := httptest.NewRequest(http.MethodPost, "/fixture", strings.NewReader(`{"count":2,"token":"fixture-secret"}`))
	request.Header.Set("Traceparent", "00-11111111111111111111111111111111-2222222222222222-01")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	spans := exporter.GetSpans()
	if response.Code != 503 || len(spans) != 1 {
		t.Fatalf("status=%d spans=%d", response.Code, len(spans))
	}
	span := spans[0]
	if span.Status.Code != codes.Error || span.Parent.SpanID().String() != "2222222222222222" {
		t.Fatal("missing error status or caller trace context")
	}
	attributes := map[string]string{}
	for _, field := range span.Attributes {
		attributes[string(field.Key)] = field.Value.Emit()
	}
	if attributes["app.request"] != `{"count":2}` || attributes["app.response"] != `{"status":"failed"}` {
		t.Fatal("trace diagnostics did not retain only allowlisted values")
	}
	scrape := httptest.NewRecorder()
	metrics.metrics().ServeHTTP(scrape, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(scrape.Body.String(), `netdosuga_http_requests_total{method="POST",route="/fixture",service="notification-test",status="503"} 1`) {
		t.Fatal("HTTP failure missing from metrics")
	}
}
