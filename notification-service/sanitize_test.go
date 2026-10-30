package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDiagnosticAllowlist(t *testing.T) {
	raw := map[string]any{"password": "SECRET", "authorization": "SECRET", "cookie": "SECRET", "items": []any{map[string]any{"token": "SECRET", "code": "SECRET", "order_id": "safe", "amount_cents": float64(100)}, map[string]any{"email": "SECRET"}, true, "fourth"}}
	value, _ := json.Marshal(sanitize(raw, 0))
	if strings.Contains(string(value), "SECRET") || strings.Contains(string(value), "fourth") {
		t.Fatal(string(value))
	}
	if !strings.Contains(string(value), "safe") {
		t.Fatal(string(value))
	}
}
