package observe

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestIdentityHTTPReadinessMetricsAndAuthFailure(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	previousProvider, previousPropagator := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		provider.Shutdown(context.Background())
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previousPropagator)
	})
	metrics := NewHTTPMetrics("identity-http-test")
	t.Cleanup(func() {
		prometheus.Unregister(metrics.Requests)
		prometheus.Unregister(metrics.Duration)
	})
	mux := http.NewServeMux()
	writable := false
	Endpoints(mux, metrics, func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("readiness check has no deadline")
		}
		if !writable {
			return errors.New("fixture database unavailable")
		}
		return nil
	})
	mux.Handle("POST /v1/login", metrics.Wrap("/v1/login", AuthDiagnostic("/v1/login", http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) },
	))))
	handler := HTTPHandler(mux, "identity.http")
	call := func(method, path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, strings.NewReader(`{"password":"fixture-secret"}`))
		r.Header.Set("traceparent", "00-11111111111111111111111111111111-2222222222222222-01")
		handler.ServeHTTP(w, r)
		return w
	}
	if w := call("GET", "/healthz"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := call("GET", "/readyz"); w.Code != 503 {
		t.Fatal(w.Code)
	}
	writable = true
	if w := call("GET", "/readyz"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := call("POST", "/v1/login"); w.Code != 401 {
		t.Fatal(w.Code)
	}
	spans := exporter.GetSpans()
	if len(spans) != 4 {
		t.Fatalf("expected one server span per request, got %d", len(spans))
	}
	auth := spans[len(spans)-1]
	if auth.Parent.SpanID().String() != "2222222222222222" || auth.Status.Code != codes.Error {
		t.Fatalf("unexpected auth span: %+v", auth.Status)
	}
	attributes := map[string]string{}
	for _, field := range auth.Attributes {
		attributes[string(field.Key)] = field.Value.Emit()
		if strings.Contains(field.Value.Emit(), "fixture-secret") {
			t.Fatal("authentication secret entered diagnostics")
		}
	}
	if attributes["http.route"] != "/v1/login" || attributes["http.response.status_code"] != "401" {
		t.Fatal("authentication response attributes were not recorded")
	}
	body := call("GET", "/metrics").Body.String()
	for _, sample := range []string{
		`netdosuga_http_requests_total{method="GET",route="/readyz",service="identity-http-test",status="503"} 1`,
		`netdosuga_http_requests_total{method="GET",route="/readyz",service="identity-http-test",status="200"} 1`,
		`netdosuga_http_requests_total{method="POST",route="/v1/login",service="identity-http-test",status="401"} 1`,
	} {
		if !strings.Contains(body, sample) {
			t.Fatalf("missing metric %s", sample)
		}
	}
}
