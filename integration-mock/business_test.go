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
