package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/google/uuid"
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
