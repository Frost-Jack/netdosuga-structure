package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestRejectUnknownSensitiveFields(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/payments", bytes.NewBufferString(`{"order_id":"test","amount_cents":100,"currency":"RUB","password":"must-not-be-accepted"}`))
	r.Header.Set("Idempotency-Key", "payment:test")
	w := httptest.NewRecorder()
	(&server{}).handle("payment")(w, r)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}

func TestPaymentHTTPValidationMetrics(t *testing.T) {
	telemetry := newTelemetry("payment-mock")
	handler := telemetry.wrap("/v1/payments", (&server{}).handle("payment"))
	for _, tc := range []struct {
		name, method, key, body, code string
		status                        int
	}{
		{"method", "PUT", "", "", "METHOD_NOT_ALLOWED", 405},
		{"unknown currency", "POST", "payment:test", `{"order_id":"test","amount_cents":100,"currency":"USD"}`, "INVALID_REQUEST", 400},
		{"zero amount", "POST", "payment:test", `{"order_id":"test","amount_cents":0,"currency":"RUB"}`, "INVALID_REQUEST", 400},
		{"key mismatch", "POST", "payment:other", `{"order_id":"test","amount_cents":100,"currency":"RUB"}`, "IDEMPOTENCY_CONFLICT", 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(tc.method, "/v1/payments", bytes.NewBufferString(tc.body))
			r.Header.Set("Idempotency-Key", tc.key)
			handler(w, r)
			var body struct{ Code string }
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.status || body.Code != tc.code {
				t.Fatalf("status=%d code=%s", w.Code, body.Code)
			}
		})
	}
	w := httptest.NewRecorder()
	telemetry.metrics().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !bytes.Contains(w.Body.Bytes(), []byte(`netdosuga_http_requests_total{method="OTHER",route="/v1/payments",service="payment-mock",status="405"} 1`)) {
		t.Fatal("validation response was not recorded")
	}
}
func TestDurableMockEffects(t *testing.T) {
	url := os.Getenv("PAYMENT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set PAYMENT_TEST_DATABASE_URL to run PostgreSQL durability checks")
	}
	ctx := context.Background()
	db, e := database(ctx, url)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	id := "payment-test-" + uuid.NewString()
	defer db.Exec(ctx, "DELETE FROM payment_mock.effects WHERE order_id=$1", id)
	call := func(s *server, kind, scenario string, amount int64, cancel bool) (int, map[string]any) {
		t.Helper()
		in := request{OrderID: id, Amount: amount, Scenario: scenario}
		if kind == "payment" {
			in.Currency = "RUB"
		}
		raw, _ := json.Marshal(in)
		r := httptest.NewRequest("POST", "/v1/"+kind+"s", bytes.NewReader(raw))
		r.Header.Set("Idempotency-Key", kind+":"+id)
		if cancel {
			cctx, stop := context.WithTimeout(ctx, 20*time.Millisecond)
			defer stop()
			r = r.WithContext(cctx)
		}
		w := httptest.NewRecorder()
		s.handle(kind)(w, r)
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	s := &server{db: db}
	lookup := func(s *server) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.lookup(w, httptest.NewRequest(http.MethodGet, "/v1/payments/"+id, nil))
		return w
	}
	if w := lookup(s); w.Code != 404 || !bytes.Contains(w.Body.Bytes(), []byte("PAYMENT_NOT_FOUND")) {
		t.Fatalf("lookup before authorization returned %d", w.Code)
	}
	if status, _ := call(s, "refund", "success", 100, false); status != 409 {
		t.Fatal(status)
	}
	if status, _ := call(s, "payment", "error", 100, false); status != 503 {
		t.Fatal(status)
	}
	call(s, "payment", "timeout", 100, true)
	var count int
	db.QueryRow(ctx, "SELECT count(*) FROM payment_mock.effects WHERE order_id=$1", id).Scan(&count)
	if count != 0 {
		t.Fatal("timeout persisted effect")
	}
	status, paid := call(s, "payment", "success", 100, false)
	if status != 200 {
		t.Fatal(status, paid)
	}
	// Reopen the database and construct a new process-equivalent server.
	restarted, e := database(ctx, url)
	if e != nil {
		t.Fatal(e)
	}
	defer restarted.Close()
	s = &server{db: restarted}
	var found result
	if w := lookup(s); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &found) != nil ||
		found.OrderID != id || found.Amount != 100 || found.PaymentID != paid["payment_id"] {
		t.Fatalf("lookup after restart returned %d with an unexpected payment", w.Code)
	}
	status, repeat := call(s, "payment", "success", 100, false)
	if status != 200 || repeat["payment_id"] != paid["payment_id"] {
		t.Fatal(status, repeat, paid)
	}
	if status, _ := call(s, "payment", "success", 200, false); status != 409 {
		t.Fatal(status)
	}
	status, refund := call(s, "refund", "success", 100, false)
	if status != 200 {
		t.Fatal(status)
	}
	status, repeat = call(s, "refund", "success", 100, false)
	if status != 200 || repeat["refund_id"] != refund["refund_id"] {
		t.Fatal(status, repeat, refund)
	}
	cancelRequest := httptest.NewRequest("POST", "/v1/payments/"+id+"/cancel", bytes.NewBufferString(`{"amount_cents":100}`))
	cancelRequest.Header.Set("Idempotency-Key", "refund:"+id)
	cancelled := httptest.NewRecorder()
	s.lookup(cancelled, cancelRequest)
	if cancelled.Code != 200 {
		t.Fatal(cancelled.Code, cancelled.Body.String())
	}
	for _, tc := range []struct {
		body string
		code int
	}{{`{"amount_cents":100}`, 200}, {`{"amount_cents":101}`, 409}} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/v1/payments/"+id+"/cancel", bytes.NewBufferString(tc.body))
		r.Header.Set("Idempotency-Key", "refund:"+id)
		s.lookup(w, r)
		if w.Code != tc.code || tc.code == 200 && w.Body.String() != cancelled.Body.String() {
			t.Fatalf("cancellation replay returned %d with an unexpected result", w.Code)
		}
	}
	if status, _ := call(s, "payment", "success", 100, false); status != 409 {
		t.Fatal("authorization bypassed terminal fence", status)
	}
	// A cancellation with no authorization also prevents a delayed first payment.
	lateID := id + "-late"
	defer db.Exec(ctx, "DELETE FROM payment_mock.effects WHERE order_id=$1", lateID)
	cancelRequest = httptest.NewRequest("POST", "/v1/payments/"+lateID+"/cancel", bytes.NewBufferString(`{"amount_cents":100}`))
	cancelRequest.Header.Set("Idempotency-Key", "refund:"+lateID)
	cancelled = httptest.NewRecorder()
	s.lookup(cancelled, cancelRequest)
	if cancelled.Code != 200 {
		t.Fatal(cancelled.Code)
	}
	late := httptest.NewRequest("POST", "/v1/payments", bytes.NewBufferString(`{"order_id":"`+lateID+`","amount_cents":100,"currency":"RUB","scenario":"success"}`))
	late.Header.Set("Idempotency-Key", "payment:"+lateID)
	rejected := httptest.NewRecorder()
	s.handle("payment")(rejected, late)
	if rejected.Code != 409 {
		t.Fatal("late authorization was accepted", rejected.Code)
	}
	db.QueryRow(ctx, "SELECT count(*) FROM payment_mock.effects WHERE order_id=$1", id).Scan(&count)
	if count != 3 {
		t.Fatalf("expected payment, refund and cancellation fence: %d", count)
	}
}
