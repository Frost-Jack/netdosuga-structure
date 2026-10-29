package main

import (
	"context"
	"encoding/json"
	"example.com/netdosuga/order-service/api"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
)

// TEST_DATABASE_URL enables real SQL concurrency checks in a disposable schema.
func TestBusinessDatabase(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, e := pgxpool.New(ctx, url)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	schema := "test_" + uuid.NewString()
	schema = fmt.Sprintf("%q", schema)
	if _, e = admin.Exec(ctx, "CREATE SCHEMA "+schema); e != nil {
		t.Fatal(e)
	}
	defer admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	cfg, e := pgxpool.ParseConfig(url)
	if e != nil {
		t.Fatal(e)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	p, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	if e = migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]string
		json.NewDecoder(r.Body).Decode(&m)
		if m["scenario"] == "error" {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, `{"mock":true,"status":"DEMO_SUCCEEDED"}`)
	}))
	defer mock.Close()
	b := &business{db: p, base: mock.URL, client: mock.Client()}
	call := func(actor, op, id, key, body string) (map[string]any, error) {
		return b.command(ctx, &api.ShopRequest{Actor: actor, Operation: op, Id: id, IdempotencyKey: key, BodyJson: body}, actorRole(actor))
	}
	must := func(actor, op, id, key, body string) map[string]any {
		t.Helper()
		r, e := call(actor, op, id, key, body)
		if e != nil {
			t.Fatalf("%s: %v", op, e)
		}
		return r
	}
	application := must("vendor-demo", "applications.create", "", "", `{"organization":"Demo lab","title":"New offer","city":"Demo city","category":"Demo category"}`)["application"].(map[string]any)
	approved := must("admin-demo", "applications.approve", application["id"].(string), "", `{}`)
	offerID := approved["offer"].(map[string]any)["id"].(string)
	updated := must("vendor-demo", "offers.update", offerID, "", `{"title":"New offer","description":"Demo description","city":"Demo city","category":"Demo category","price_cents":12345,"capacity":7}`)["offer"].(map[string]any)
	if updated["capacity"] != int32(7) || updated["available"] != int32(7) {
		t.Fatal("offer capacity update", updated)
	}
	published := must("vendor-demo", "offers.publish", offerID, "", `{}`)["offer"].(map[string]any)
	if published["status"] != "PUBLISHED" {
		t.Fatal("offer not published")
	}
	// Both customers compete for one seat. Only one checkout reserves it.
	if _, e = p.Exec(ctx, "UPDATE business_offers SET capacity=1,available=1 WHERE id='offer-demo-1'"); e != nil {
		t.Fatal(e)
	}
	for _, a := range []string{"customer-demo", "customer-two"} {
		must(a, "cart.add", "", "", `{"offer_id":"offer-demo-1","quantity":1}`)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	success := 0
	var order map[string]any
	winner := ""
	for _, a := range []string{"customer-demo", "customer-two"} {
		wg.Add(1)
		go func(a string) {
			defer wg.Done()
			r, e := call(a, "orders.create", "", "same-key", `{}`)
			if e == nil {
				mu.Lock()
				success++
				order = r["order"].(map[string]any)
				winner = a
				mu.Unlock()
			}
		}(a)
	}
	wg.Wait()
	if success != 1 {
		t.Fatalf("successful checkouts=%d", success)
	}
	id := order["id"].(string)
	same := must(winner, "orders.create", "", "same-key", `{}`)["order"].(map[string]any)
	if same["id"] != id {
		t.Fatal("idempotency failed")
	}
	other := "customer-two"
	if winner == other {
		other = "customer-demo"
	}
	if _, e = call(other, "orders.get", id, "", `{}`); e == nil {
		t.Fatal("cross customer read")
	}
	if _, e = call("vendor-two", "offers.update", "offer-demo-1", "", `{}`); e == nil {
		t.Fatal("cross vendor update")
	}
	paid := must(winner, "orders.pay", id, "", `{"scenario":"success"}`)["order"].(map[string]any)
	tickets := paid["tickets"].([]map[string]any)
	if len(tickets) != 1 {
		t.Fatal("ticket quantity")
	}
	must(winner, "orders.pay", id, "", `{}`)
	if _, e = call(winner, "orders.cancel", id, "", `{"scenario":"error"}`); e == nil {
		t.Fatal("refund failure accepted")
	}
	still := must(winner, "orders.get", id, "", `{}`)["order"].(map[string]any)
	if still["status"] != "PAID" {
		t.Fatal("failed refund mutated state")
	}
	code := tickets[0]["code"].(string)
	if _, e = call("vendor-two", "tickets.redeem", code, "", `{}`); e == nil {
		t.Fatal("cross vendor redeem")
	}
	// Redeem and cancel serialize on the order. Exactly one wins.
	success = 0
	for _, op := range []string{"redeem", "cancel"} {
		wg.Add(1)
		go func(op string) {
			defer wg.Done()
			var e error
			if op == "redeem" {
				_, e = call("vendor-demo", "tickets.redeem", code, "", `{}`)
			} else {
				_, e = call(winner, "orders.cancel", id, "", `{}`)
			}
			if e == nil {
				mu.Lock()
				success++
				mu.Unlock()
			}
		}(op)
	}
	wg.Wait()
	if success != 1 {
		t.Fatalf("redeem/cancel winners=%d", success)
	}
	var count int
	if e = p.QueryRow(ctx, "SELECT count(*) FROM business_tickets WHERE order_id=$1", id).Scan(&count); e != nil || count != 1 {
		t.Fatal("duplicate tickets", count, e)
	}
	if e = p.QueryRow(ctx, "SELECT count(*) FROM business_outbox").Scan(&count); e != nil || count != 3 {
		t.Fatal("outbox transitions", count, e)
	}
}
