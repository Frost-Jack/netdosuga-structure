package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPayments(t *testing.T) {
	m := &mockServer{delay: 25 * time.Millisecond, timeOut: 100 * time.Millisecond}
	for _, tc := range []struct {
		s      string
		status int
	}{{"success", 200}, {"error", 503}, {"delay", 200}, {"timeout", 200}, {"bad", 400}} {
		t.Run(tc.s, func(t *testing.T) {
			start := time.Now()
			w := httptest.NewRecorder()
			m.payments(w, httptest.NewRequest("POST", "/mock/payments", strings.NewReader(fmt.Sprintf(`{"scenario":%q}`, tc.s))))
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
