package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/trace"
)

type delivery struct {
	Mock    bool   `json:"mock"`
	EventID string `json:"event_id"`
	OrderID string `json:"order_id"`
	TraceID string `json:"trace_id,omitempty"`
}
type mockServer struct {
	mu                 sync.Mutex
	deliveries         []delivery
	businessDeliveries []delivery
	delay, timeOut     time.Duration
}

func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func decode(r *http.Request, v any) error {
	d := json.NewDecoder(io.LimitReader(r.Body, 4097))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("one JSON object required")
	}
	return nil
}
func fail(w http.ResponseWriter, r *http.Request, status int, code string) {
	respond(w, status, map[string]any{"mock": true, "code": code, "message": code, "trace_id": trace.SpanContextFromContext(r.Context()).TraceID().String()})
}
func (m *mockServer) payments(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		fail(w, r, 405, "METHOD_NOT_ALLOWED")
		return
	}
	var in struct {
		Scenario string `json:"scenario"`
	}
	if decode(r, &in) != nil {
		fail(w, r, 400, "INVALID_REQUEST")
		return
	}
	var delay time.Duration
	switch in.Scenario {
	case "success":
	case "error":
		fail(w, r, 503, "MOCK_PAYMENT_ERROR")
		return
	case "delay":
		delay = m.delay
	case "timeout":
		delay = m.timeOut
	default:
		fail(w, r, 400, "INVALID_SCENARIO")
		return
	}
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-r.Context().Done():
			fail(w, r, 504, "MOCK_CANCELLED")
			return
		}
	}
	respond(w, 200, map[string]any{"mock": true, "payment_id": "demo-payment-001", "status": "DEMO_SUCCEEDED"})
}
func (m *mockServer) emails(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		fail(w, r, 405, "METHOD_NOT_ALLOWED")
		return
	}
	var in delivery
	if decode(r, &in) != nil || !in.Mock || in.EventID == "" || len(in.EventID) > 128 || in.OrderID != "demo-order-001" || in.TraceID != "" {
		fail(w, r, 400, "INVALID_REQUEST")
		return
	}
	in.TraceID = trace.SpanContextFromContext(r.Context()).TraceID().String()
	m.mu.Lock()
	m.deliveries = append(m.deliveries, in)
	if len(m.deliveries) > 100 {
		m.deliveries = append([]delivery(nil), m.deliveries[len(m.deliveries)-100:]...)
	}
	m.mu.Unlock()
	respond(w, 200, map[string]any{"mock": true, "status": "DEMO_SENT"})
}
func (m *mockServer) list(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		fail(w, r, 405, "METHOD_NOT_ALLOWED")
		return
	}
	m.mu.Lock()
	items := append([]delivery{}, m.deliveries...)
	m.mu.Unlock()
	respond(w, 200, map[string]any{"mock": true, "deliveries": items})
}
func (m *mockServer) handler() http.Handler {
	t := newTelemetry("integration-mock")
	mux := http.NewServeMux()
	mux.HandleFunc("/mock/refunds", t.wrap("/mock/refunds", m.payments))
	mux.HandleFunc("/mock/business-emails", t.wrap("/mock/business-emails", m.businessEmails))
	mux.HandleFunc("/mock/payments", t.wrap("/mock/payments", m.payments))
	mux.HandleFunc("/mock/emails", t.wrap("/mock/emails", m.emails))
	mux.HandleFunc("/mock/business-deliveries", t.wrap("/mock/business-deliveries", m.businessList))
	mux.HandleFunc("/mock/deliveries", t.wrap("/mock/deliveries", m.list))
	for _, p := range []string{"/healthz", "/readyz"} {
		mux.HandleFunc(p, t.wrap(p, func(w http.ResponseWriter, r *http.Request) {
			respond(w, 200, map[string]any{"mock": true, "status": "ok"})
		}))
	}
	mux.Handle("/metrics", t.metrics())
	mux.HandleFunc("/", t.wrap("unmatched", func(w http.ResponseWriter, r *http.Request) { fail(w, r, 404, "NOT_FOUND") }))
	return mux
}
func duration(key, fallback string) (time.Duration, error) {
	d, e := time.ParseDuration(env(key, fallback))
	if e != nil || d <= 0 || d > 5*time.Second {
		return 0, fmt.Errorf("invalid %s (must be >0 and <=5s)", key)
	}
	return d, nil
}
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	tp, err := configure(ctx, "integration-mock")
	if err != nil {
		return err
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = tp.Shutdown(c)
	}()
	delay, err := duration("MOCK_DELAY", "250ms")
	if err != nil {
		return err
	}
	timeout, err := duration("MOCK_TIMEOUT", "1500ms")
	if err != nil {
		return err
	}
	m := &mockServer{delay: delay, timeOut: timeout}
	s := &http.Server{Addr: env("HTTP_ADDR", ":8083"), Handler: m.handler(), ReadHeaderTimeout: 3 * time.Second}
	errs := make(chan error, 1)
	go func() { errs <- s.ListenAndServe() }()
	slog.Info("mock HTTP adapter listening")
	select {
	case <-ctx.Done():
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.Shutdown(c)
	case err := <-errs:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
func main() {
	if err := run(); err != nil {
		slog.Error("service stopped", "error", err)
		os.Exit(1)
	}
}

func (m *mockServer) businessEmails(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		fail(w, r, 405, "METHOD_NOT_ALLOWED")
		return
	}
	var in delivery
	if decode(r, &in) != nil || !in.Mock || len(in.EventID) < 1 || len(in.EventID) > 128 || len(in.OrderID) < 1 || len(in.OrderID) > 128 || in.TraceID != "" {
		fail(w, r, 400, "INVALID_REQUEST")
		return
	}
	in.TraceID = trace.SpanContextFromContext(r.Context()).TraceID().String()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.businessDeliveries {
		if d.EventID == in.EventID {
			respond(w, 200, map[string]any{"mock": true, "status": "DEMO_SENT"})
			return
		}
	}
	m.businessDeliveries = append(m.businessDeliveries, in)
	if len(m.businessDeliveries) > 1000 {
		m.businessDeliveries = append([]delivery(nil), m.businessDeliveries[len(m.businessDeliveries)-1000:]...)
	}
	respond(w, 200, map[string]any{"mock": true, "status": "DEMO_SENT"})
}

func (m *mockServer) businessList(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		fail(w, r, 405, "METHOD_NOT_ALLOWED")
		return
	}
	m.mu.Lock()
	items := append([]delivery{}, m.businessDeliveries...)
	m.mu.Unlock()
	respond(w, 200, map[string]any{"mock": true, "deliveries": items})
}
