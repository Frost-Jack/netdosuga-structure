package observe

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSafeNestedSecretsAndBounds(t *testing.T) {
	v := map[string]any{"password": "SECRET_PASSWORD", "demo": "SECRET_DEMO", "operation": "SECRET_OPERATION", "quantity": "SECRET_WRONG_TYPE", "offers": "SECRET_CONTAINER", "scenario": "SECRET_SCENARIO", "status": "SECRET_STATUS", "email": "SECRET_EMAIL", "authorization": "SECRET_BEARER", "cart": map[string]any{"items": []any{"SECRET_ARRAY", map[string]any{"operation": "SECRET_NESTED_OPERATION", "offer_id": "offer-1", "name": "SECRET_NAME", "token": "SECRET_TOKEN", "quantity": 2}, map[string]any{}, map[string]any{}, map[string]any{"id": "fourth"}}}, "tickets": []any{map[string]any{"code": "SECRET_TICKET", "status": "VALID"}}}
	data, _ := json.Marshal(Safe(v))
	if strings.Contains(string(data), "SECRET") || strings.Contains(string(data), "fourth") {
		t.Fatal(string(data))
	}
	if !strings.Contains(string(data), "offer-1") {
		t.Fatal(string(data))
	}
	long := map[string]any{"id": strings.Repeat("a", 300)}
	data, _ = json.Marshal(Safe(long))
	if len(data) > 2048 || strings.Contains(string(data), strings.Repeat("a", 129)) {
		t.Fatal("payload not bounded")
	}
}
