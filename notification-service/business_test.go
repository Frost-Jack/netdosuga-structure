package main

import (
	"fmt"
	"testing"
)

func TestParseBusiness(t *testing.T) {
	good := `{"schema_version":1,"demo":true,"event_id":"11111111-1111-1111-1111-111111111111","event_type":"order.paid","order_id":"order-demo","occurred_at":"2026-10-29T12:00:00Z"}`
	if _, e := parseBusiness([]byte(good)); e != nil {
		t.Fatal(e)
	}
	for _, raw := range []string{`{}`, good + `{}`, fmt.Sprintf(`{"schema_version":1,"demo":false}`)} {
		if _, e := parseBusiness([]byte(raw)); e == nil {
			t.Fatal("poison accepted")
		}
	}
}
