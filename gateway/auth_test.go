package main

import (
	"context"
	"encoding/json"
	"example.com/netdosuga/gateway/internal/observe"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAuthCookieAndRedaction(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"user": map[string]any{"id": "buyer", "role": "customer"}, "session_token": "opaque-secret", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)})
	}))
	defer upstream.Close()
	t.Setenv("IDENTITY_BASE_URL", upstream.URL)
	g := &gateway{}
	r := httptest.NewRequest("POST", "http://example.com/api/v1/auth/login", strings.NewReader(`{"email":"a@example.com","password":"secret"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	g.auth("login")(w, r)
	if w.Header().Get("X-Gateway-Instance") == "" {
		t.Fatal("missing gateway instance")
	}
	if w.Code != 200 || strings.Contains(w.Body.String(), "opaque-secret") {
		t.Fatalf("response %d %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Value != "opaque-secret" {
		t.Fatalf("cookies %+v", cookies)
	}
	r.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	g.auth("login")(w, r)
	if w.Code != 403 {
		t.Fatalf("cross origin %d", w.Code)
	}
}
func TestMutationGuard(t *testing.T) {
	for _, test := range []struct {
		origin, site, content string
		want                  bool
	}{{"http://example.com", "same-origin", "application/json", true}, {"http://evil.com", "cross-site", "application/json", false}, {"", "cross-site", "application/json", false}, {"", "", "text/plain", false}, {"", "", "application/jsonp", false}, {"", "", "application/json; charset=utf-8", true}, {"", "", "application/json", true}} {
		r := httptest.NewRequest("POST", "http://example.com/", strings.NewReader("{}"))
		r.Header.Set("Origin", test.origin)
		r.Header.Set("Sec-Fetch-Site", test.site)
		r.Header.Set("Content-Type", test.content)
		if mutationGuard(r) != test.want {
			t.Fatalf("guard %+v", test)
		}
	}
}
func TestSafePayload(t *testing.T) {
	input := map[string]any{"email": "secret-email", "password": "secret-pass", "token": "secret-token", "order": map[string]any{"status": "PAID", "code": "secret-code", "ticket_code": "secret-ticket", "items": []any{map[string]any{"quantity": float64(2), "name": "secret-name"}}}}
	out := safePayload(input)
	if strings.Contains(out, "secret") || !strings.Contains(out, "PAID") || !strings.Contains(out, "quantity") {
		t.Fatal(out)
	}
	if len(out) > 2048 {
		t.Fatal("unbounded payload")
	}
}
func TestSafePayloadRejectsInvalidTypes(t *testing.T) {
	input := map[string]any{"quantity": "secret-quantity", "amount_cents": "secret-amount", "scenario": "secret-scenario", "status": "secret-status", "order": "secret-order", "cart": 42, "items": []any{"secret-item", map[string]any{"quantity": "secret-nested", "status": "PAID", "capacity": float64(3), "available": float64(1.5)}}}
	out := safePayload(input)
	if strings.Contains(out, "secret") || strings.Contains(out, "quantity") || strings.Contains(out, "amount_cents") || strings.Contains(out, "available") || strings.Contains(out, "cart") || strings.Contains(out, "order") {
		t.Fatal(out)
	}
	if !strings.Contains(out, `"status":"PAID"`) || !strings.Contains(out, `"capacity":3`) {
		t.Fatal(out)
	}
}

func TestAuthRoutesMetricsAndErrorSpan(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	original := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(original)
	defer provider.Shutdown(context.Background())
	// Isolate the package-global Prometheus registry for this route test.
	oldRegisterer, oldGatherer := prometheus.DefaultRegisterer, prometheus.DefaultGatherer
	registry := prometheus.NewRegistry()
	prometheus.DefaultRegisterer = registry
	prometheus.DefaultGatherer = registry
	defer func() { prometheus.DefaultRegisterer = oldRegisterer; prometheus.DefaultGatherer = oldGatherer }()
	metrics := observe.NewHTTPMetrics("api-gateway")
	mux := http.NewServeMux()
	g := &gateway{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	g.registerShop(mux, metrics)
	r := httptest.NewRequest("POST", "http://example.com/api/v1/auth/login", strings.NewReader(`{"password":"secret-body"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 403 || w.Header().Get("X-Gateway-Instance") == "" {
		t.Fatalf("response %d %v", w.Code, w.Header())
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Status.Code != codes.Error {
		t.Fatalf("spans %+v", spans)
	}
	for _, attribute := range spans[0].Attributes {
		if strings.Contains(attribute.Value.AsString(), "secret-body") {
			t.Fatal("auth body in span")
		}
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, family := range families {
		if family.GetName() == "netdosuga_http_requests_total" {
			for _, metric := range family.Metric {
				for _, label := range metric.Label {
					if label.GetName() == "route" && label.GetValue() == "/api/v1/auth/login" {
						found = true
					}
				}
			}
		}
	}
	if !found {
		t.Fatal("auth route missing fixed-label request metric")
	}
}
