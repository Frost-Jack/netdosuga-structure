package main

import (
	"bytes"
	"context"
	"encoding/json"
	"example.com/netdosuga/order-service/api"
	"example.com/netdosuga/order-service/internal/observe"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"io"
	"net/http"
	"time"
)

func (b *business) payment(ctx context.Context, kind, id string, amount int64, scenario string) (resultID string, resultErr error) {
	operation := "payment.authorize"
	if kind == "refunds" {
		operation = "payment.refund"
	}
	ctx, span := otel.Tracer("orders-service").Start(ctx, operation)
	defer func() {
		if resultErr != nil {
			observe.Error(ctx, resultErr)
		}
		span.End()
	}()
	if scenario == "" {
		scenario = "success"
	}
	switch scenario {
	case "success", "error", "delay", "timeout":
	default:
		return "", bad("INVALID_SCENARIO")
	}
	payload := map[string]any{"order_id": id, "amount_cents": amount, "scenario": scenario}
	if kind == "payments" {
		payload["currency"] = "RUB"
	}
	data, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, "POST", b.base+"/v1/"+kind, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	key := "payment:"
	if kind == "refunds" {
		key = "refund:"
	}
	req.Header.Set("Idempotency-Key", key+id)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
	res, e := b.client.Do(req)
	if e != nil {
		return "", problem{504, "MOCK_TIMEOUT"}
	}
	defer res.Body.Close()
	if kind == "refunds" && res.StatusCode == 503 {
		var failure struct {
			Code string `json:"code"`
		}
		if json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&failure) == nil && failure.Code == "MOCK_PROVIDER_ERROR" {
			return "", problem{502, "MOCK_DECLINED"}
		}
		return "", problem{502, "MOCK_FAILED"}
	}
	var body struct {
		Mock      bool   `json:"mock"`
		Status    string `json:"status"`
		PaymentID string `json:"payment_id"`
		RefundID  string `json:"refund_id"`
		OrderID   string `json:"order_id"`
		Amount    int64  `json:"amount_cents"`
	}
	if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&body) != nil || !body.Mock || body.Status != "SUCCEEDED" || body.OrderID != id || body.Amount != amount {
		return "", problem{502, "MOCK_FAILED"}
	}
	result := body.PaymentID
	if kind == "refunds" {
		result = body.RefundID
	}
	if result == "" || len(result) > 128 {
		return "", problem{502, "MOCK_FAILED"}
	}
	return result, nil
}

// Order rows are locked before sorted inventory rows, matching cancel and redeem.
func (b *business) releaseExpired(ctx context.Context, tx pgx.Tx, id string) error {
	if e := b.cancelUncertain(ctx, tx, id); e != nil {
		return e
	}
	if _, e := records(ctx, tx, "SELECT o.id FROM business_offers o JOIN business_items i ON i.offer_id=o.id WHERE i.order_id=$1 ORDER BY o.id FOR UPDATE OF o", id); e != nil {
		return e
	}
	if _, e := tx.Exec(ctx, "UPDATE business_offers o SET available=o.available+i.quantity FROM business_items i WHERE i.order_id=$1 AND i.offer_id=o.id", id); e != nil {
		return e
	}
	if _, e := tx.Exec(ctx, "UPDATE business_orders SET status='EXPIRED' WHERE id=$1", id); e != nil {
		return e
	}
	return b.event(ctx, tx, "order.expired", id)
}
func (b *business) expire(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	tx, e := b.db.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	rows, e := records(ctx, tx, "SELECT id FROM business_orders WHERE status='RESERVED' AND expires_at<=now() ORDER BY expires_at,id LIMIT 1 FOR UPDATE SKIP LOCKED")
	if e != nil {
		return e
	}
	if len(rows) > 0 {
		if e = b.releaseExpired(ctx, tx, rows[0]["id"].(string)); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}

// Commit intent before an external effect, so a lost response or outer rollback cannot hide it.
func (b *business) preparePayment(ctx context.Context, r *api.ShopRequest, role string) error {
	if role != "customer" {
		return forbidden()
	}
	tx, e := b.db.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	o, e := one(ctx, tx, "SELECT customer_id,status,total_cents,expires_at FROM business_orders WHERE id=$1 FOR UPDATE", r.Id)
	if e != nil {
		return e
	}
	if o["customer_id"] != r.Actor {
		return forbidden()
	}
	if o["status"] == "RESERVED" && o["expires_at"].(time.Time).After(time.Now()) {
		if _, e = tx.Exec(ctx, "INSERT INTO business_payment_attempts(order_id,amount_cents) VALUES($1,$2) ON CONFLICT DO NOTHING", r.Id, o["total_cents"]); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}

// The provider cancellation tombstone prevents an old delayed authorize request from charging later.
func (b *business) cancelUncertain(ctx context.Context, tx pgx.Tx, id string) (resultErr error) {
	attempts, e := records(ctx, tx, "SELECT amount_cents FROM business_payment_attempts WHERE order_id=$1", id)
	if e != nil || len(attempts) == 0 {
		return e
	}
	ctx, span := otel.Tracer("orders-service").Start(ctx, "payment.reconcile")
	defer func() {
		if resultErr != nil {
			observe.Error(ctx, resultErr)
		}
		span.End()
	}()
	amount := attempts[0]["amount_cents"].(int64)
	data, _ := json.Marshal(map[string]any{"amount_cents": amount})
	req, _ := http.NewRequestWithContext(ctx, "POST", b.base+"/v1/payments/"+id+"/cancel", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "refund:"+id)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
	res, e := b.client.Do(req)
	if e != nil {
		return problem{504, "RECONCILIATION_TIMEOUT"}
	}
	defer res.Body.Close()
	var out struct {
		Mock      bool   `json:"mock"`
		Status    string `json:"status"`
		OrderID   string `json:"order_id"`
		Amount    int64  `json:"amount_cents"`
		RefundID  string `json:"refund_id"`
		Cancelled bool   `json:"cancelled"`
	}
	if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&out) != nil || !out.Mock || out.Status != "SUCCEEDED" || out.OrderID != id || out.Amount != amount || !out.Cancelled {
		return problem{502, "RECONCILIATION_FAILED"}
	}
	if out.RefundID != "" {
		_, e = tx.Exec(ctx, "UPDATE business_orders SET refund_id=$2 WHERE id=$1", id, out.RefundID)
	}
	return e
}

func (b *business) prepareCancel(ctx context.Context, r *api.ShopRequest, role string) error {
	if role != "customer" {
		return forbidden()
	}
	tx, e := b.db.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	o, e := one(ctx, tx, "SELECT customer_id,status FROM business_orders WHERE id=$1 FOR UPDATE", r.Id)
	if e != nil {
		return e
	}
	if o["customer_id"] != r.Actor {
		return forbidden()
	}
	if o["status"] == "PAID" {
		used, e := records(ctx, tx, "SELECT code FROM business_tickets WHERE order_id=$1 AND status='REDEEMED'", r.Id)
		if e != nil {
			return e
		}
		if len(used) > 0 {
			return problem{409, "TICKET_ALREADY_REDEEMED"}
		}
	}
	if o["status"] == "RESERVED" || o["status"] == "PAID" {
		if _, e = tx.Exec(ctx, "INSERT INTO business_refund_attempts(order_id) VALUES($1) ON CONFLICT(order_id) DO UPDATE SET attempt_count=business_refund_attempts.attempt_count+1", r.Id); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}

// Migration-marked pre-MVP payments were stateless local fixtures, with no provider effect.
// Their original refund adapter stays isolated from strict persisted MVP payment/refund records.
func (b *business) legacyRefund(ctx context.Context, id, scenario string) (resultID string, resultErr error) {
	ctx, span := otel.Tracer("orders-service").Start(ctx, "payment.legacy_refund")
	defer func() {
		if resultErr != nil {
			observe.Error(ctx, resultErr)
		}
		span.End()
	}()
	if scenario == "" {
		scenario = "success"
	}
	payload, _ := json.Marshal(map[string]string{"scenario": scenario})
	req, e := http.NewRequestWithContext(ctx, "POST", b.legacyBase+"/mock/refunds", bytes.NewReader(payload))
	if e != nil {
		return "", problem{502, "MOCK_FAILED"}
	}
	req.Header.Set("Content-Type", "application/json")
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
	res, e := b.client.Do(req)
	if e != nil {
		return "", problem{504, "MOCK_TIMEOUT"}
	}
	defer res.Body.Close()
	var out struct {
		Mock   bool   `json:"mock"`
		Status string `json:"status"`
		Code   string `json:"code"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&out) != nil || !out.Mock {
		return "", problem{502, "MOCK_FAILED"}
	}
	if res.StatusCode == 503 && out.Code == "MOCK_PAYMENT_ERROR" {
		return "", problem{502, "MOCK_DECLINED"}
	}
	if res.StatusCode != 200 || out.Status != "DEMO_SUCCEEDED" {
		return "", problem{502, "MOCK_FAILED"}
	}
	return "legacy-refund:" + id, nil
}
