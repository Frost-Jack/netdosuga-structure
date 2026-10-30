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
	"testing"
)

func TestLegacyPaidUpgrade(t *testing.T) {
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
	name := "legacy_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, e = admin.Exec(ctx, "CREATE SCHEMA "+name); e != nil {
		t.Fatal(e)
	}
	defer admin.Exec(ctx, "DROP SCHEMA "+name+" CASCADE")
	cfg, e := pgxpool.ParseConfig(url)
	if e != nil {
		t.Fatal(e)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = name
	p, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	// Install exactly the original schema and paid records before the MVP migrations exist.
	if _, e = p.Exec(ctx, migration+businessMigration+`CREATE TABLE schema_migrations(version int PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now()); INSERT INTO schema_migrations(version) VALUES(1),(2);`); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"old-valid", "old-used", "old-uncertain"} {
		if _, e = p.Exec(ctx, "INSERT INTO business_orders(id,customer_id,status,total_cents,commission_cents,idempotency_key) VALUES($1,'customer-demo','PAID',150000,15000,$1)", id); e != nil {
			t.Fatal(e)
		}
		if _, e = p.Exec(ctx, "INSERT INTO business_items VALUES($1,'offer-demo-1','vendor-demo','Old fixture',1,150000,150000,15000)", id); e != nil {
			t.Fatal(e)
		}
		state := "VALID"
		if id == "old-used" {
			state = "REDEEMED"
		}
		if _, e = p.Exec(ctx, "INSERT INTO business_tickets(code,order_id,offer_id,vendor_id,status) VALUES($1,$2,'offer-demo-1','vendor-demo',$3)", id+"-ticket", id, state); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = p.Exec(ctx, "UPDATE business_offers SET available=97 WHERE id='offer-demo-1'"); e != nil {
		t.Fatal(e)
	}
	if e = migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	var marked int
	if e = p.QueryRow(ctx, "SELECT count(*) FROM business_orders WHERE legacy_fixture_payment").Scan(&marked); e != nil || marked != 3 {
		t.Fatal("legacy classification", marked, e)
	}
	legacyCalls, modernCalls := 0, 0
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		legacyCalls++
		if r.URL.Path != "/mock/refunds" {
			t.Error(r.URL.Path)
		}
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		if in["scenario"] == "error" {
			w.WriteHeader(503)
			fmt.Fprint(w, `{"mock":true,"code":"MOCK_PAYMENT_ERROR"}`)
			return
		}
		if in["scenario"] == "delay" {
			fmt.Fprint(w, "lost response")
			return
		}
		fmt.Fprint(w, `{"mock":true,"status":"DEMO_SUCCEEDED"}`)
	}))
	defer legacy.Close()
	modern := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modernCalls++
		w.WriteHeader(409)
		fmt.Fprint(w, `{"code":"PAYMENT_NOT_FOUND"}`)
	}))
	defer modern.Close()
	b := &business{db: p, base: modern.URL, legacyBase: legacy.URL, client: legacy.Client()}
	call := func(op, id, body string) (map[string]any, error) {
		actor, role := "customer-demo", "customer"
		if op == "tickets.redeem" {
			actor, role = "vendor-demo", "vendor"
		}
		return b.command(ctx, &api.ShopRequest{Actor: actor, Operation: op, Id: id, BodyJson: body}, role)
	}
	if _, e = call("orders.cancel", "old-valid", `{"scenario":"error"}`); e == nil {
		t.Fatal("legacy decline accepted")
	}
	var state string
	var pending int
	if e = p.QueryRow(ctx, "SELECT status FROM business_orders WHERE id='old-valid'").Scan(&state); e != nil || state != "PAID" {
		t.Fatal(state, e)
	}
	if e = p.QueryRow(ctx, "SELECT count(*) FROM business_refund_attempts WHERE order_id='old-valid'").Scan(&pending); e != nil || pending != 0 {
		t.Fatal("legacy decline stranded refund fence", pending, e)
	}
	result, e := call("orders.cancel", "old-valid", `{}`)
	if e != nil {
		t.Fatal(e)
	}
	order := result["order"].(map[string]any)
	if order["status"] != "CANCELLED" || order["refund_id"] != "legacy-refund:old-valid" || order["payment_id"] != nil {
		t.Fatal(order)
	}
	if _, e = call("orders.cancel", "old-valid", `{}`); e != nil {
		t.Fatal(e)
	}
	var available int
	if e = p.QueryRow(ctx, "SELECT available FROM business_offers WHERE id='offer-demo-1'").Scan(&available); e != nil || available != 98 {
		t.Fatal("legacy stock release", available, e)
	}
	if e = p.QueryRow(ctx, "SELECT status FROM business_tickets WHERE code='old-valid-ticket'").Scan(&state); e != nil || state != "REVOKED" {
		t.Fatal(state, e)
	}
	if _, e = call("orders.cancel", "old-used", `{}`); e == nil {
		t.Fatal("used legacy ticket cancelled")
	}
	if _, e = call("orders.cancel", "old-uncertain", `{"scenario":"delay"}`); e == nil {
		t.Fatal("lost legacy refund response accepted")
	}
	if _, e = call("tickets.redeem", "old-uncertain-ticket", `{}`); e == nil {
		t.Fatal("uncertain legacy refund ticket redeemed")
	}
	if _, e = call("orders.cancel", "old-uncertain", `{}`); e != nil {
		t.Fatal(e)
	}
	// A new PAID record missing its provider effect must never use the compatibility adapter.
	if _, e = p.Exec(ctx, "INSERT INTO business_orders(id,customer_id,status,total_cents,commission_cents,idempotency_key,payment_id) VALUES('new-missing','customer-demo','PAID',1,0,'new-missing','missing-provider-record')"); e != nil {
		t.Fatal(e)
	}
	before := legacyCalls
	if _, e = call("orders.cancel", "new-missing", `{}`); e == nil {
		t.Fatal("missing modern provider record bypassed")
	}
	if legacyCalls != before || modernCalls != 1 {
		t.Fatal("incorrect provider route", legacyCalls, modernCalls)
	}
	if _, e = p.Exec(ctx, "INSERT INTO business_orders(id,customer_id,status,total_cents,commission_cents,idempotency_key) VALUES('new-null','customer-demo','PAID',1,0,'new-null')"); e != nil {
		t.Fatal(e)
	}
	if _, e = call("orders.cancel", "new-null", `{}`); e == nil {
		t.Fatal("new null payment ID became legacy")
	}
	if legacyCalls != before || modernCalls != 2 {
		t.Fatal("new null row incorrectly routed", legacyCalls, modernCalls)
	}
	if e = migrate(ctx, p); e != nil {
		t.Fatal(e)
	}
	var marker bool
	if e = p.QueryRow(ctx, "SELECT legacy_fixture_payment FROM business_orders WHERE id='new-missing'").Scan(&marker); e != nil || marker {
		t.Fatal("modern row became legacy", marker, e)
	}
	if e = p.QueryRow(ctx, "SELECT legacy_fixture_payment FROM business_orders WHERE id='new-null'").Scan(&marker); e != nil || marker {
		t.Fatal("rerun reclassified new null row", marker, e)
	}
	if e = p.QueryRow(ctx, "SELECT available FROM business_offers WHERE id='offer-demo-1'").Scan(&available); e != nil || available != 99 {
		t.Fatal("legacy stock total", available, e)
	}
	if e = p.QueryRow(ctx, "SELECT status FROM business_tickets WHERE code='old-used-ticket'").Scan(&state); e != nil || state != "REDEEMED" {
		t.Fatal("used legacy ticket mutated", state, e)
	}
	if legacyCalls != 4 {
		t.Fatal("unexpected legacy effect attempts", legacyCalls)
	}
}
