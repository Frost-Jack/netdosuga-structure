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
	"strings"
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
	var providerMu sync.Mutex
	providerPaid := map[string]bool{}
	providerCancelled := map[string]bool{}
	loseResponse := false
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerMu.Lock()
		defer providerMu.Unlock()
		var m map[string]any
		json.NewDecoder(r.Body).Decode(&m)
		if strings.HasSuffix(r.URL.Path, "/cancel") {
			parts := strings.Split(r.URL.Path, "/")
			id := parts[len(parts)-2]
			providerCancelled[id] = true
			refund := ""
			if providerPaid[id] {
				refund = "reconciled-refund"
			}
			json.NewEncoder(w).Encode(map[string]any{"mock": true, "status": "SUCCEEDED", "order_id": id, "amount_cents": m["amount_cents"], "cancelled": true, "refund_id": refund})
			return
		}
		id, _ := m["order_id"].(string)
		if providerCancelled[id] {
			w.WriteHeader(409)
			return
		}

		if m["scenario"] == "error" {
			w.WriteHeader(503)
			json.NewEncoder(w).Encode(map[string]string{"code": "MOCK_PROVIDER_ERROR"})
			return
		}
		providerPaid[id] = true
		if loseResponse {
			loseResponse = false
			fmt.Fprint(w, "lost response")
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"mock": true, "status": "SUCCEEDED", "payment_id": "mock-payment", "refund_id": "mock-refund", "order_id": m["order_id"], "amount_cents": m["amount_cents"]})
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
	var pendingDecline int
	if e = p.QueryRow(ctx, "SELECT count(*) FROM business_refund_attempts WHERE order_id=$1", id).Scan(&pendingDecline); e != nil || pendingDecline != 0 {
		t.Fatal("definite decline left fence", pendingDecline, e)
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
	// Two replicas race the expired reservation; inventory and event release exactly once.
	must("customer-demo", "cart.add", "", "", `{"offer_id":"offer-demo-2","quantity":2}`)
	expiring := must("customer-demo", "orders.create", "", "expiry-key", `{}`)["order"].(map[string]any)
	expiryID := expiring["id"].(string)
	if _, e = p.Exec(ctx, "UPDATE business_orders SET expires_at=now()-interval '1 minute' WHERE id=$1", expiryID); e != nil {
		t.Fatal(e)
	}
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := b.expire(ctx); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if _, e = call("customer-demo", "orders.pay", expiryID, "", `{}`); e == nil {
		t.Fatal("expired payment accepted")
	}
	var state string
	var available int
	if e = p.QueryRow(ctx, "SELECT status FROM business_orders WHERE id=$1", expiryID).Scan(&state); e != nil || state != "EXPIRED" {
		t.Fatal(state, e)
	}
	if e = p.QueryRow(ctx, "SELECT available FROM business_offers WHERE id='offer-demo-2'").Scan(&available); e != nil || available != 50 {
		t.Fatal("inventory released incorrectly", available, e)
	}
	if e = p.QueryRow(ctx, "SELECT count(*) FROM business_outbox WHERE payload->>'event_type'='order.expired' AND payload->>'order_id'=$1", expiryID).Scan(&count); e != nil || count != 1 {
		t.Fatal("expiry events", count, e)
	}

	// Provider success with a lost response survives outer rollback: expiry refunds before release.
	must("customer-demo", "cart.add", "", "", `{"offer_id":"offer-demo-2","quantity":1}`)
	uncertain := must("customer-demo", "orders.create", "", "uncertain-key", `{}`)["order"].(map[string]any)
	uncertainID := uncertain["id"].(string)
	providerMu.Lock()
	loseResponse = true
	providerMu.Unlock()
	if _, e = call("customer-demo", "orders.pay", uncertainID, "", `{}`); e == nil {
		t.Fatal("lost response accepted")
	}
	if _, e = p.Exec(ctx, "UPDATE business_orders SET expires_at=now()-interval '1 minute' WHERE id=$1", uncertainID); e != nil {
		t.Fatal(e)
	}
	if e = b.expire(ctx); e != nil {
		t.Fatal(e)
	}
	var refund string
	if e = p.QueryRow(ctx, "SELECT status,refund_id FROM business_orders WHERE id=$1", uncertainID).Scan(&state, &refund); e != nil || state != "EXPIRED" || refund != "reconciled-refund" {
		t.Fatal("lost response not reconciled", state, refund, e)
	}
	// An explicit DB rollback after successful authorize is likewise compensated on cancel.
	must("customer-demo", "cart.add", "", "", `{"offer_id":"offer-demo-2","quantity":1}`)
	rollbackOrder := must("customer-demo", "orders.create", "", "rollback-key", `{}`)["order"].(map[string]any)
	rollbackID := rollbackOrder["id"].(string)
	if _, e = p.Exec(ctx, `CREATE FUNCTION reject_paid() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.status='PAID' THEN RAISE EXCEPTION 'simulated rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_paid BEFORE UPDATE ON business_orders FOR EACH ROW EXECUTE FUNCTION reject_paid();`); e != nil {
		t.Fatal(e)
	}
	if _, e = call("customer-demo", "orders.pay", rollbackID, "", `{}`); e == nil {
		t.Fatal("rollback not reproduced")
	}
	if _, e = p.Exec(ctx, `DROP TRIGGER reject_paid ON business_orders; DROP FUNCTION reject_paid();`); e != nil {
		t.Fatal(e)
	}
	cancelled := must("customer-demo", "orders.cancel", rollbackID, "", `{}`)["order"].(map[string]any)
	if cancelled["status"] != "CANCELLED" || cancelled["refund_id"] != "reconciled-refund" {
		t.Fatal("rollback not compensated", cancelled)
	}

	// A refund that succeeds remotely but loses its response fences redemption until retry.
	must("customer-demo", "cart.add", "", "", `{"offer_id":"offer-demo-2","quantity":1}`)
	refundOrder := must("customer-demo", "orders.create", "", "refund-response-key", `{}`)["order"].(map[string]any)
	refundOrderID := refundOrder["id"].(string)
	refundPaid := must("customer-demo", "orders.pay", refundOrderID, "", `{}`)["order"].(map[string]any)
	refundCode := refundPaid["tickets"].([]map[string]any)[0]["code"].(string)
	providerMu.Lock()
	loseResponse = true
	providerMu.Unlock()
	if _, e = call("customer-demo", "orders.cancel", refundOrderID, "", `{}`); e == nil {
		t.Fatal("lost refund response accepted")
	}
	if _, e = call("vendor-demo", "tickets.redeem", refundCode, "", `{}`); e == nil {
		t.Fatal("refunded ticket redeemed")
	}
	// A subsequent decline cannot erase an earlier uncertain refund intent.
	if _, e = call("customer-demo", "orders.cancel", refundOrderID, "", `{"scenario":"error"}`); e == nil {
		t.Fatal("refund decline accepted")
	}
	if _, e = call("vendor-demo", "tickets.redeem", refundCode, "", `{}`); e == nil {
		t.Fatal("uncertain refund intent erased by later decline")
	}
	must("customer-demo", "orders.cancel", refundOrderID, "", `{}`)
	// A database rollback after successful refund also preserves the redemption fence.
	must("customer-demo", "cart.add", "", "", `{"offer_id":"offer-demo-2","quantity":1}`)
	refundRollback := must("customer-demo", "orders.create", "", "refund-rollback-key", `{}`)["order"].(map[string]any)
	refundRollbackID := refundRollback["id"].(string)
	refundRollbackPaid := must("customer-demo", "orders.pay", refundRollbackID, "", `{}`)["order"].(map[string]any)
	refundRollbackCode := refundRollbackPaid["tickets"].([]map[string]any)[0]["code"].(string)
	if _, e = p.Exec(ctx, `CREATE FUNCTION reject_cancelled() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.status='CANCELLED' THEN RAISE EXCEPTION 'simulated rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_cancelled BEFORE UPDATE ON business_orders FOR EACH ROW EXECUTE FUNCTION reject_cancelled();`); e != nil {
		t.Fatal(e)
	}
	if _, e = call("customer-demo", "orders.cancel", refundRollbackID, "", `{}`); e == nil {
		t.Fatal("refund rollback not reproduced")
	}
	if _, e = call("vendor-demo", "tickets.redeem", refundRollbackCode, "", `{}`); e == nil {
		t.Fatal("refunded ticket redeem after rollback")
	}
	if _, e = p.Exec(ctx, `DROP TRIGGER reject_cancelled ON business_orders; DROP FUNCTION reject_cancelled();`); e != nil {
		t.Fatal(e)
	}
	must("customer-demo", "orders.cancel", refundRollbackID, "", `{}`)

	// Cancellation prepared while RESERVED fences a pay arriving between phases.
	must("customer-demo", "cart.add", "", "", `{"offer_id":"offer-demo-2","quantity":1}`)
	interphase := must("customer-demo", "orders.create", "", "interphase-key", `{}`)["order"].(map[string]any)
	interphaseID := interphase["id"].(string)
	if e = b.prepareCancel(ctx, &api.ShopRequest{Actor: "customer-demo", Id: interphaseID}, "customer"); e != nil {
		t.Fatal(e)
	}
	if _, e = call("customer-demo", "orders.pay", interphaseID, "", `{}`); e == nil {
		t.Fatal("payment bypassed cancellation intent")
	}
	must("customer-demo", "orders.cancel", interphaseID, "", `{}`)

}
