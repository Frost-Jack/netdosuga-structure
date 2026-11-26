package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPayments(t *testing.T) {
	m := &mockServer{delay: 25 * time.Millisecond, timeOut: 100 * time.Millisecond}
	handler := m.handler()
	for _, tc := range []struct {
		s      string
		status int
	}{{"success", 200}, {"error", 503}, {"delay", 200}, {"timeout", 200}, {"bad", 400}} {
		t.Run(tc.s, func(t *testing.T) {
			start := time.Now()
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest("POST", "/mock/payments", strings.NewReader(fmt.Sprintf(`{"scenario":%q}`, tc.s))))
			if w.Code != tc.status {
				t.Fatalf("status %d", w.Code)
			}
			if tc.s == "delay" && time.Since(start) < m.delay {
				t.Fatal("delay missing")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	m.payments(w, httptest.NewRequest("POST", "/mock/payments", strings.NewReader(`{"scenario":"timeout"}`)).WithContext(ctx))
	if w.Code != 504 {
		t.Fatal(w.Code)
	}
}

func TestMockRoutesAndMetrics(t *testing.T) {
	handler := (&mockServer{}).handler()
	for _, tc := range []struct {
		method, path, body string
		status             int
		code               string
	}{
		{"GET", "/readyz", "", 200, ""},
		{"GET", "/missing", "", 404, "NOT_FOUND"},
		{"GET", "/mock/payments", "", 405, "METHOD_NOT_ALLOWED"},
		{"PUT", "/mock/emails", "", 405, "METHOD_NOT_ALLOWED"},
		{"POST", "/mock/deliveries", "", 405, "METHOD_NOT_ALLOWED"},
		{"DELETE", "/mock/business-deliveries", "", 405, "METHOD_NOT_ALLOWED"},
		{"GET", "/mock/business-emails", "", 405, "METHOD_NOT_ALLOWED"},
		{"POST", "/mock/business-emails", `{"mock":true,"event_id":"e","order_id":"o","trace_id":"client"}`, 400, "INVALID_REQUEST"},
		{"POST", "/mock/business-emails", strings.Repeat("x", 4097), 400, "INVALID_REQUEST"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			var body struct {
				Mock bool
				Code string
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.status || body.Code != tc.code || !body.Mock {
				t.Fatalf("unexpected mock response: %d %s", w.Code, w.Body.String())
			}
		})
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(w.Body.String(), `netdosuga_http_requests_total{method="OTHER",route="/mock/emails",service="mail-mock",status="405"} 1`) {
		t.Fatal("unsupported method did not use the bounded metrics label")
	}
}

func TestMockLocalStartupValidatesConfiguration(t *testing.T) {
	t.Setenv("APP_ENV", "local")
	t.Setenv("MOCK_MODE", "true")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "127.0.0.1:1")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("HTTP_ADDR", listener.Addr().String())
	for _, tc := range []struct{ key, value string }{{"MOCK_DELAY", "0s"}, {"MOCK_TIMEOUT", "6s"}, {"MOCK_TIMEOUT", "invalid"}} {
		t.Run(tc.key+" "+tc.value, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			if err := run(); err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("startup did not reject %s: %v", tc.key, err)
			}
		})
	}
	t.Setenv("MOCK_DELAY", "1ms")
	t.Setenv("MOCK_TIMEOUT", "2ms")
	var bindError *net.OpError
	if err := run(); !errors.As(err, &bindError) || bindError.Op != "listen" {
		t.Fatalf("startup did not report the occupied port: %v", err)
	}
}
func TestBoundedSyntheticDeliveries(t *testing.T) {
	m := &mockServer{}
	for i := 0; i < 101; i++ {
		w := httptest.NewRecorder()
		m.emails(w, httptest.NewRequest("POST", "/mock/emails", strings.NewReader(fmt.Sprintf(`{"mock":true,"event_id":"event-%d","order_id":"demo-order-001"}`, i))))
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	if len(m.deliveries) != 100 || m.deliveries[0].EventID != "event-1" {
		t.Fatal("history is not bounded")
	}
	for _, body := range []string{`{"mock":false,"event_id":"x","order_id":"demo-order-001"}`, `{"mock":true,"event_id":"x","order_id":"demo-order-001","email":"person@example.test"}`, `{} {}`} {
		w := httptest.NewRecorder()
		m.emails(w, httptest.NewRequest(http.MethodPost, "/mock/emails", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatal("invalid request accepted")
		}
	}
}
func TestDemoGuard(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("MOCK_MODE", "true")
	if _, err := configure(context.Background(), "test"); err == nil {
		t.Fatal("production startup accepted")
	}
}
