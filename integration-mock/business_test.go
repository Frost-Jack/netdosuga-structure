package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBusinessMockFixtures(t *testing.T) {
	m := &mockServer{delay: time.Millisecond, timeOut: time.Millisecond}
	for _, scenario := range []string{"success", "error", "delay", "timeout", "bad"} {
		req := httptest.NewRequest("POST", "/mock/refunds", strings.NewReader(`{"scenario":"`+scenario+`"}`))
		w := httptest.NewRecorder()
		m.payments(w, req)
		want := 200
		if scenario == "error" {
			want = 503
		}
		if scenario == "bad" {
			want = 400
		}
		if w.Code != want {
			t.Fatalf("%s: %d", scenario, w.Code)
		}
	}
	for n := 0; n < 2; n++ {
		w := httptest.NewRecorder()
		m.businessEmails(w, httptest.NewRequest(http.MethodPost, "/mock/business-emails", strings.NewReader(`{"mock":true,"event_id":"event-demo","order_id":"opaque-order"}`)))
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	if len(m.businessDeliveries) != 1 {
		t.Fatal("duplicate mail")
	}
	var body map[string]any
	w := httptest.NewRecorder()
	m.list(w, httptest.NewRequest("GET", "/mock/deliveries", nil))
	if json.Unmarshal(w.Body.Bytes(), &body) != nil {
		t.Fatal("invalid JSON")
	}
}

func TestBusinessDeliveryHistoryRetainsMostRecentEffects(t *testing.T) {
	m := &mockServer{businessDeliveries: make([]delivery, 1000)}
	m.businessDeliveries[0] = delivery{EventID: "oldest", OrderID: "fixture"}
	h := m.handler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/mock/business-emails",
		strings.NewReader(`{"mock":true,"event_id":"newest","order_id":"new-order"}`)))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/mock/business-deliveries", nil))
	var data struct{ Deliveries []delivery }
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Deliveries) != 1000 || data.Deliveries[0].EventID == "oldest" ||
		data.Deliveries[999].EventID != "newest" || data.Deliveries[999].OrderID != "new-order" {
		t.Fatal("unexpected business delivery history")
	}
}
