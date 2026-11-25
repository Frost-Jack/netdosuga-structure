package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestO03DiagnosticLimits(t *testing.T) {
	t.Run("first three entries preserve numeric values and IDs", func(t *testing.T) {
		entries := []any{}
		for _, quantity := range []int{1, 2, 3, 4} {
			entries = append(entries, map[string]any{
				"offer_id": "offer-123", "quantity": quantity, "amount_cents": 999,
				"status": "PAID", "description": "private message", "token": "private token",
			})
		}
		var data struct {
			Items []struct {
				OfferID  string `json:"offer_id"`
				Quantity int    `json:"quantity"`
				Amount   int    `json:"amount_cents"`
				Status   string `json:"status"`
			}
		}
		actual := safePayload(map[string]any{"items": entries})
		if err := json.Unmarshal([]byte(actual), &data); err != nil {
			t.Fatal(err)
		}
		if len(data.Items) != 3 || strings.Contains(actual, "private") {
			t.Fatalf("incorrect allowlist or length: %s", actual)
		}
		for index, item := range data.Items {
			if item.OfferID != "offer-123" || item.Quantity != index+1 || item.Amount != 999 || item.Status != "PAID" {
				t.Fatalf("allowed fields lost: %+v", item)
			}
		}
	})

	t.Run("maximum depth", func(t *testing.T) {
		payload := map[string]any{"amount_cents": 6789}
		for range 5 {
			payload = map[string]any{"order": payload}
		}
		if !strings.Contains(safePayload(payload), "6789") {
			t.Fatal("depth five should be kept")
		}
		if strings.Contains(safePayload(map[string]any{"order": payload}), "6789") {
			t.Fatal("depth six should be omitted")
		}
	})

	t.Run("oversized", func(t *testing.T) {
		id := strings.Repeat("a", 128)
		item := map[string]any{"order_id": id, "offer_id": id}
		items := []any{item, item, item}
		payload := map[string]any{"orders": []any{items, items, items}}
		if got := safePayload(payload); got != `{"truncated":true}` {
			t.Fatal("missing truncation marker")
		}
	})
}
