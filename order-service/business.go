package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"example.com/netdosuga/order-service/api"
	"example.com/netdosuga/order-service/internal/observe"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

type business struct {
	db         *pgxpool.Pool
	client     *http.Client
	base       string
	legacyBase string
	writer     *kafka.Writer
}
type problem struct {
	status int
	code   string
}

func (p problem) Error() string { return p.code }
func bad(code string) error     { return problem{400, code} }
func forbidden() error          { return problem{403, "FORBIDDEN"} }
func actorRole(actor string) string {
	switch actor {
	case "customer-demo", "customer-two":
		return "customer"
	case "vendor-demo", "vendor-two":
		return "vendor"
	case "admin-demo":
		return "admin"
	}
	return ""
}
func (s *orderServer) Shop(ctx context.Context, r *api.ShopRequest) (*api.ShopResponse, error) {
	result := map[string]any{}
	status := 200
	started := time.Now()
	parentCtx := ctx
	ctx, span := otel.Tracer("orders-service").Start(ctx, "operation."+fixedOperation(r.Operation))
	defer span.End()
	requestPayload := observe.Payload(ctx, "request", diagnosticRequest(r))
	role := "public"
	var err error
	clone := *r
	clone.Actor = ""
	r = &clone
	if r.Operation != "catalog.list" {
		r.Actor, role, err = s.resolveActor(ctx, r)
	}
	if err != nil {
	} else if s.business == nil {
		err = problem{503, "UNAVAILABLE"}
	} else {
		result, err = s.business.command(ctx, r, role)
	}
	if err != nil {
		observe.Error(ctx, err)
		observe.Error(parentCtx, err)
		p := problem{503, "UNAVAILABLE"}
		var known problem
		if errors.As(err, &known) {
			p = known
		}
		status = p.status
		result = map[string]any{"code": p.code, "message": p.code}
		if status >= 500 {
			sc := trace.SpanContextFromContext(ctx)
			s.logger.ErrorContext(ctx, "business command failed", "operation", fixedOperation(r.Operation), "trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String())
		}
	}
	if err == nil {
		observe.Log(ctx, s.logger, "business command completed", "operation", fixedOperation(r.Operation), "status", status)
	}
	observe.Operation("orders-service", fixedOperation(r.Operation), started, err != nil)
	responsePayload := observe.Payload(ctx, "response", result)
	observe.Log(ctx, s.logger, "shop request", "operation", fixedOperation(r.Operation), "method", "Shop", "status", status, "duration_ms", time.Since(started).Milliseconds(), "request", requestPayload, "response", responsePayload)
	result["demo"] = true
	result["trace_id"] = trace.SpanContextFromContext(ctx).TraceID().String()
	data, _ := json.Marshal(result)
	host, _ := os.Hostname()
	return &api.ShopResponse{Status: int32(status), BodyJson: string(data), InstanceId: host}, nil
}

type queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func records(ctx context.Context, q queryer, sql string, args ...any) ([]map[string]any, error) {
	rows, e := q.Query(ctx, sql, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		vals, e := rows.Values()
		if e != nil {
			return nil, e
		}
		m := map[string]any{}
		for i, f := range rows.FieldDescriptions() {
			m[f.Name] = vals[i]
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func one(ctx context.Context, q queryer, sql string, args ...any) (map[string]any, error) {
	a, e := records(ctx, q, sql, args...)
	if e != nil {
		return nil, e
	}
	if len(a) == 0 {
		return nil, problem{404, "NOT_FOUND"}
	}
	return a[0], nil
}
func parseBody(raw string) (map[string]json.RawMessage, error) {
	if raw == "" {
		raw = "{}"
	}
	var m map[string]json.RawMessage
	d := json.NewDecoder(strings.NewReader(raw))
	if len(raw) > 16384 || d.Decode(&m) != nil || m == nil || d.Decode(&struct{}{}) != io.EOF {
		return nil, bad("INVALID_JSON")
	}
	return m, nil
}
func text(m map[string]json.RawMessage, key string) string {
	var s string
	json.Unmarshal(m[key], &s)
	return strings.TrimSpace(s)
}
func number(m map[string]json.RawMessage, key string) int64 {
	var v int64
	if json.Unmarshal(m[key], &v) != nil {
		return -1
	}
	return v
}
func valid(s string) bool { return s != "" && len(s) <= 300 }
func (b *business) command(ctx context.Context, r *api.ShopRequest, role string) (map[string]any, error) {
	validationCtx, validationSpan := otel.Tracer("orders-service").Start(ctx, "validation")
	_ = validationCtx
	m, e := parseBody(r.BodyJson)
	validationSpan.End()
	if e != nil {
		return nil, e
	}
	actor := r.Actor
	op := r.Operation
	if len(r.Id) > 128 {
		return nil, bad("INVALID_ID")
	}
	if r.Operation == "orders.pay" || r.Operation == "orders.cancel" {
		switch text(m, "scenario") {
		case "", "success", "error", "delay", "timeout":
		default:
			return nil, bad("INVALID_SCENARIO")
		}
	}
	if r.Operation == "orders.cancel" {
		if e = b.prepareCancel(ctx, r, role); e != nil {
			return nil, e
		}
	}
	if r.Operation == "orders.pay" {
		if e = b.preparePayment(ctx, r, role); e != nil {
			return nil, e
		}
	}
	tx, e := b.db.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	// A customer lock serializes cart mutations and idempotent checkouts across replicas.
	if role == "customer" {
		if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", actor); e != nil {
			return nil, e
		}
	}
	result := map[string]any{}
	switch op {
	case "catalog.list":
		rows, e := records(ctx, tx, "SELECT * FROM business_offers WHERE status='PUBLISHED' AND ($1='' OR city=$1) AND ($2='' OR category=$2) ORDER BY id LIMIT 200", text(m, "city"), text(m, "category"))
		if e != nil {
			return nil, e
		}
		result["offers"] = rows
	case "applications.list", "offers.list":
		if role != "vendor" && role != "admin" {
			return nil, forbidden()
		}
		table, key := "business_applications", "applications"
		if op == "offers.list" {
			table, key = "business_offers", "offers"
		}
		rows, e := records(ctx, tx, "SELECT * FROM "+table+" WHERE vendor_id=$1 OR $2 ORDER BY id LIMIT 200", actor, role == "admin")
		if e != nil {
			return nil, e
		}
		result[key] = rows
	case "applications.create":
		if role != "vendor" {
			return nil, forbidden()
		}
		for _, k := range []string{"organization", "title", "city", "category"} {
			if !valid(text(m, k)) {
				return nil, bad("REQUIRED_FIELDS")
			}
		}
		a, e := one(ctx, tx, "INSERT INTO business_applications(id,vendor_id,organization,title,city,category) VALUES($1,$2,$3,$4,$5,$6) RETURNING *", uuid.NewString(), actor, text(m, "organization"), text(m, "title"), text(m, "city"), text(m, "category"))
		if e != nil {
			return nil, e
		}
		result["application"] = a
	case "applications.approve", "applications.reject":
		if role != "admin" {
			return nil, forbidden()
		}
		a, e := one(ctx, tx, "SELECT * FROM business_applications WHERE id=$1 FOR UPDATE", r.Id)
		if e != nil {
			return nil, e
		}
		if a["status"] != "PENDING" {
			return nil, problem{409, "INVALID_STATE"}
		}
		state := "APPROVED"
		if op == "applications.reject" {
			state = "REJECTED"
			if !valid(text(m, "reason")) {
				return nil, bad("REASON_REQUIRED")
			}
		}
		a, e = one(ctx, tx, "UPDATE business_applications SET status=$2,reason=$3 WHERE id=$1 RETURNING *", r.Id, state, text(m, "reason"))
		if e != nil {
			return nil, e
		}
		result["application"] = a
		if state == "APPROVED" {
			o, e := one(ctx, tx, "INSERT INTO business_offers(id,vendor_id,title,city,category) VALUES($1,$2,$3,$4,$5) RETURNING *", uuid.NewString(), a["vendor_id"], a["title"], a["city"], a["category"])
			if e != nil {
				return nil, e
			}
			result["offer"] = o
		}
	case "offers.update", "offers.publish":
		if role != "vendor" {
			return nil, forbidden()
		}
		o, e := one(ctx, tx, "SELECT * FROM business_offers WHERE id=$1 FOR UPDATE", r.Id)
		if e != nil {
			return nil, e
		}
		if o["vendor_id"] != actor {
			return nil, forbidden()
		}
		if op == "offers.publish" {
			if o["price_cents"].(int64) <= 0 || o["capacity"].(int32) <= 0 || !valid(o["description"].(string)) {
				return nil, bad("INCOMPLETE_OFFER")
			}
			o, e = one(ctx, tx, "UPDATE business_offers SET status='PUBLISHED' WHERE id=$1 RETURNING *", r.Id)
		} else {
			for _, k := range []string{"title", "description", "city", "category"} {
				if !valid(text(m, k)) {
					return nil, bad("REQUIRED_FIELDS")
				}
			}
			price, cap := number(m, "price_cents"), number(m, "capacity")
			used := int64(o["capacity"].(int32) - o["available"].(int32))
			if price < 1 || price > 100000000 || cap < used || cap > 100000 {
				return nil, bad("INVALID_AMOUNT_OR_CAPACITY")
			}
			o, e = one(ctx, tx, "UPDATE business_offers SET title=$2,description=$3,city=$4,category=$5,price_cents=$6,capacity=$7,available=$8 WHERE id=$1 RETURNING *", r.Id, text(m, "title"), text(m, "description"), text(m, "city"), text(m, "category"), price, cap, cap-used)
		}
		if e != nil {
			return nil, e
		}
		result["offer"] = o
	case "cart.get", "cart.add", "cart.remove":
		if role != "customer" {
			return nil, forbidden()
		}
		if op == "cart.add" {
			q := number(m, "quantity")
			if q < 1 || q > 20 {
				return nil, bad("INVALID_QUANTITY")
			}
			o, e := one(ctx, tx, "SELECT * FROM business_offers WHERE id=$1 AND status='PUBLISHED'", text(m, "offer_id"))
			if e != nil {
				return nil, e
			}
			if int64(o["available"].(int32)) < q {
				return nil, problem{409, "UNAVAILABLE_CAPACITY"}
			}
			if _, e = tx.Exec(ctx, "INSERT INTO business_cart(customer_id,offer_id,quantity) VALUES($1,$2,$3) ON CONFLICT(customer_id,offer_id) DO UPDATE SET quantity=excluded.quantity", actor, text(m, "offer_id"), q); e != nil {
				return nil, e
			}
		}
		if op == "cart.remove" {
			if _, e = tx.Exec(ctx, "DELETE FROM business_cart WHERE customer_id=$1 AND offer_id=$2", actor, r.Id); e != nil {
				return nil, e
			}
		}
		items, e := records(ctx, tx, "SELECT o.*,c.offer_id,c.quantity,o.price_cents*c.quantity AS total_cents FROM business_cart c JOIN business_offers o ON o.id=c.offer_id WHERE c.customer_id=$1 ORDER BY o.id", actor)
		if e != nil {
			return nil, e
		}
		var total int64
		for _, i := range items {
			total += i["total_cents"].(int64)
		}
		result["cart"] = map[string]any{"items": items, "total_cents": total}
	case "orders.create":
		if role != "customer" {
			return nil, forbidden()
		}
		if !valid(r.IdempotencyKey) || len(r.IdempotencyKey) > 128 {
			return nil, bad("IDEMPOTENCY_KEY_REQUIRED")
		}
		old, e := records(ctx, tx, "SELECT id FROM business_orders WHERE customer_id=$1 AND idempotency_key=$2", actor, r.IdempotencyKey)
		if e != nil {
			return nil, e
		}
		if len(old) > 0 {
			r.Id = old[0]["id"].(string)
		} else {
			items, e := records(ctx, tx, "SELECT o.*,c.quantity FROM business_cart c JOIN business_offers o ON o.id=c.offer_id WHERE c.customer_id=$1 ORDER BY o.id FOR UPDATE OF o", actor)
			if e != nil {
				return nil, e
			}
			if len(items) == 0 {
				return nil, bad("EMPTY_CART")
			}
			var total, commission int64
			for _, i := range items {
				q := int64(i["quantity"].(int32))
				if i["status"] != "PUBLISHED" || int64(i["available"].(int32)) < q {
					return nil, problem{409, "UNAVAILABLE_CAPACITY"}
				}
				v := q * i["price_cents"].(int64)
				total += v
				commission += v / 10
			}
			r.Id = uuid.NewString()
			if _, e = tx.Exec(ctx, "INSERT INTO business_orders(id,customer_id,status,total_cents,commission_cents,idempotency_key) VALUES($1,$2,'RESERVED',$3,$4,$5)", r.Id, actor, total, commission, r.IdempotencyKey); e != nil {
				return nil, e
			}
			for _, i := range items {
				v := int64(i["quantity"].(int32)) * i["price_cents"].(int64)
				if _, e = tx.Exec(ctx, "INSERT INTO business_items VALUES($1,$2,$3,$4,$5,$6,$7,$8)", r.Id, i["id"], i["vendor_id"], i["title"], i["quantity"], i["price_cents"], v, v/10); e != nil {
					return nil, e
				}
				if _, e = tx.Exec(ctx, "UPDATE business_offers SET available=available-$2 WHERE id=$1", i["id"], i["quantity"]); e != nil {
					return nil, e
				}
			}
			if _, e = tx.Exec(ctx, "DELETE FROM business_cart WHERE customer_id=$1", actor); e != nil {
				return nil, e
			}
			if e = b.event(ctx, tx, "order.created", r.Id); e != nil {
				return nil, e
			}
		}
		o, e := loadOrder(ctx, tx, r.Id)
		if e != nil {
			return nil, e
		}
		result["order"] = o
	case "orders.list":
		if role != "customer" && role != "admin" {
			return nil, forbidden()
		}
		rows, e := records(ctx, tx, "SELECT * FROM business_orders WHERE customer_id=$1 OR $2 ORDER BY created_at DESC LIMIT 200", actor, role == "admin")
		if e != nil {
			return nil, e
		}
		result["orders"] = rows
	case "orders.get", "orders.pay", "orders.cancel":
		if role != "customer" && role != "admin" {
			return nil, forbidden()
		}
		o, e := one(ctx, tx, "SELECT * FROM business_orders WHERE id=$1 FOR UPDATE", r.Id)
		if e != nil {
			return nil, e
		}
		if o["customer_id"] != actor && role != "admin" {
			return nil, forbidden()
		}
		if op != "orders.get" && role != "customer" {
			return nil, forbidden()
		}
		state := o["status"].(string)
		if state == "RESERVED" && !o["expires_at"].(time.Time).After(time.Now()) {
			if e = b.releaseExpired(ctx, tx, r.Id); e != nil {
				return nil, e
			}
			state = "EXPIRED"
			if op != "orders.get" {
				if e = tx.Commit(ctx); e != nil {
					return nil, e
				}
				return nil, problem{409, "RESERVATION_EXPIRED"}
			}
		}
		if op == "orders.pay" {
			pending, e := records(ctx, tx, "SELECT order_id FROM business_refund_attempts WHERE order_id=$1", r.Id)
			if e != nil {
				return nil, e
			}
			if len(pending) > 0 {
				return nil, problem{409, "CANCELLATION_PENDING"}
			}
		}
		if op == "orders.pay" && state != "PAID" {
			if state != "RESERVED" {
				return nil, problem{409, "INVALID_STATE"}
			}
			paymentID, payErr := b.payment(ctx, "payments", r.Id, o["total_cents"].(int64), text(m, "scenario"))
			if payErr != nil {
				e = payErr
				return nil, e
			}
			if _, e = tx.Exec(ctx, "UPDATE business_orders SET status='PAID',payment_id=$2 WHERE id=$1", r.Id, paymentID); e != nil {
				return nil, e
			}
			items, e := records(ctx, tx, "SELECT * FROM business_items WHERE order_id=$1 ORDER BY offer_id", r.Id)
			if e != nil {
				return nil, e
			}
			for _, i := range items {
				for n := int32(0); n < i["quantity"].(int32); n++ {
					if _, e = tx.Exec(ctx, "INSERT INTO business_tickets(code,order_id,offer_id,vendor_id) VALUES($1,$2,$3,$4)", uuid.NewString(), r.Id, i["offer_id"], i["vendor_id"]); e != nil {
						return nil, e
					}
				}
			}
			if e = b.event(ctx, tx, "order.paid", r.Id); e != nil {
				return nil, e
			}
		}
		if op == "orders.cancel" && state != "CANCELLED" {
			if state != "RESERVED" && state != "PAID" {
				return nil, problem{409, "INVALID_STATE"}
			}
			used, e := records(ctx, tx, "SELECT code FROM business_tickets WHERE order_id=$1 AND status='REDEEMED'", r.Id)
			if e != nil {
				return nil, e
			}
			if len(used) > 0 {
				return nil, problem{409, "TICKET_ALREADY_REDEEMED"}
			}
			if state == "RESERVED" {
				if e = b.cancelUncertain(ctx, tx, r.Id); e != nil {
					return nil, e
				}
			}
			if state == "PAID" {
				var refundID string
				var refundErr error
				if legacy, _ := o["legacy_fixture_payment"].(bool); legacy && o["payment_id"] == nil {
					refundID, refundErr = b.legacyRefund(ctx, r.Id, text(m, "scenario"))
				} else {
					refundID, refundErr = b.payment(ctx, "refunds", r.Id, o["total_cents"].(int64), text(m, "scenario"))
				}
				if refundErr != nil {
					var decline problem
					if errors.As(refundErr, &decline) && decline.code == "MOCK_DECLINED" {
						// Only a first definite decline can restore ticket usability. A previous
						// uncertain request may still arrive late, so later declines retain the fence.
						pending, lookupErr := records(ctx, tx, "SELECT attempt_count FROM business_refund_attempts WHERE order_id=$1", r.Id)
						if lookupErr != nil {
							return nil, lookupErr
						}
						if len(pending) == 1 && pending[0]["attempt_count"].(int64) == 1 {
							if _, clearErr := tx.Exec(ctx, "DELETE FROM business_refund_attempts WHERE order_id=$1", r.Id); clearErr != nil {
								return nil, clearErr
							}
							if commitErr := tx.Commit(ctx); commitErr != nil {
								return nil, commitErr
							}
						}
					}
					return nil, refundErr
				}
				if _, e = tx.Exec(ctx, "UPDATE business_orders SET refund_id=$2 WHERE id=$1", r.Id, refundID); e != nil {
					return nil, e
				}
			}
			if _, e = tx.Exec(ctx, "UPDATE business_orders SET status='CANCELLED' WHERE id=$1", r.Id); e != nil {
				return nil, e
			}
			if _, e = tx.Exec(ctx, "UPDATE business_tickets SET status='REVOKED' WHERE order_id=$1", r.Id); e != nil {
				return nil, e
			}
			if _, e = records(ctx, tx, "SELECT o.id FROM business_offers o JOIN business_items i ON i.offer_id=o.id WHERE i.order_id=$1 ORDER BY o.id FOR UPDATE OF o", r.Id); e != nil {
				return nil, e
			}
			if _, e = tx.Exec(ctx, "UPDATE business_offers o SET available=o.available+i.quantity FROM business_items i WHERE i.order_id=$1 AND i.offer_id=o.id", r.Id); e != nil {
				return nil, e
			}
			if e = b.event(ctx, tx, "order.cancelled", r.Id); e != nil {
				return nil, e
			}
		}
		o, e = loadOrder(ctx, tx, r.Id)
		if e != nil {
			return nil, e
		}
		result["order"] = o
	case "tickets.redeem":
		if role != "vendor" {
			return nil, forbidden()
		}
		t, e := one(ctx, tx, "SELECT * FROM business_tickets WHERE code=$1", r.Id)
		if e != nil {
			return nil, e
		}
		if t["vendor_id"] != actor {
			return nil, forbidden()
		}
		o, e := one(ctx, tx, "SELECT * FROM business_orders WHERE id=$1 FOR UPDATE", t["order_id"])
		if e != nil {
			return nil, e
		}
		pending, e := records(ctx, tx, "SELECT order_id FROM business_refund_attempts WHERE order_id=$1", t["order_id"])
		if e != nil {
			return nil, e
		}
		if len(pending) > 0 {
			return nil, problem{409, "REFUND_PENDING"}
		}
		t, e = one(ctx, tx, "SELECT * FROM business_tickets WHERE code=$1 FOR UPDATE", r.Id)
		if e != nil {
			return nil, e
		}
		if t["status"] != "VALID" || o["status"] != "PAID" {
			return nil, problem{409, "INVALID_TICKET_STATE"}
		}
		t, e = one(ctx, tx, "UPDATE business_tickets SET status='REDEEMED' WHERE code=$1 RETURNING *", r.Id)
		if e != nil {
			return nil, e
		}
		if e = b.event(ctx, tx, "ticket.redeemed", t["order_id"].(string)); e != nil {
			return nil, e
		}
		result["ticket"] = t
	case "sales.get":
		if role != "vendor" && role != "admin" {
			return nil, forbidden()
		}
		rows, e := records(ctx, tx, "SELECT i.*,o.created_at FROM business_items i JOIN business_orders o ON o.id=i.order_id WHERE o.status='PAID' AND (i.vendor_id=$1 OR $2) ORDER BY o.created_at DESC LIMIT 500", actor, role == "admin")
		if e != nil {
			return nil, e
		}
		var gross, fee int64
		for _, i := range rows {
			gross += i["total_cents"].(int64)
			fee += i["commission_cents"].(int64)
		}
		result["sales"] = rows
		result["gross_cents"] = gross
		result["commission_cents"] = fee
		result["net_cents"] = gross - fee
	default:
		return nil, problem{404, "UNKNOWN_OPERATION"}
	}
	commitCtx, commitSpan := otel.Tracer("orders-service").Start(ctx, "transaction.commit")
	e = tx.Commit(commitCtx)
	commitSpan.End()
	if e != nil {
		return nil, e
	}
	return result, nil
}
func loadOrder(ctx context.Context, tx pgx.Tx, id string) (map[string]any, error) {
	o, e := one(ctx, tx, "SELECT * FROM business_orders WHERE id=$1", id)
	if e != nil {
		return nil, e
	}
	o["items"], e = records(ctx, tx, "SELECT * FROM business_items WHERE order_id=$1 ORDER BY offer_id", id)
	if e != nil {
		return nil, e
	}
	o["tickets"], e = records(ctx, tx, "SELECT * FROM business_tickets WHERE order_id=$1 ORDER BY code", id)
	return o, e
}
func (b *business) mock(ctx context.Context, path, scenario string) error {
	if scenario == "" {
		scenario = "success"
	}
	switch scenario {
	case "success", "error", "delay", "timeout":
	default:
		return bad("INVALID_SCENARIO")
	}
	data, _ := json.Marshal(map[string]string{"scenario": scenario})
	req, _ := http.NewRequestWithContext(ctx, "POST", b.base+path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
	res, e := b.client.Do(req)
	if e != nil {
		return problem{504, "MOCK_TIMEOUT"}
	}
	defer res.Body.Close()
	var body struct {
		Mock   bool   `json:"mock"`
		Status string `json:"status"`
	}
	if res.StatusCode != 200 || json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&body) != nil || !body.Mock || body.Status != "DEMO_SUCCEEDED" {
		return problem{502, "MOCK_FAILED"}
	}
	return nil
}
func (b *business) event(ctx context.Context, tx pgx.Tx, kind, id string) error {
	ctx, span := otel.Tracer("orders-service").Start(ctx, "outbox.enqueue")
	defer span.End()
	eventID := uuid.NewString()
	data, _ := json.Marshal(map[string]any{"schema_version": 1, "demo": true, "event_id": eventID, "event_type": kind, "order_id": id, "occurred_at": time.Now().UTC().Format(time.RFC3339Nano)})
	headers := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, headers)
	h, _ := json.Marshal(headers)
	_, e := tx.Exec(ctx, "INSERT INTO business_outbox(event_id,payload,headers) VALUES($1,$2,$3)", eventID, data, h)
	return e
}
func (b *business) outbox(ctx context.Context) {
	for ctx.Err() == nil {
		if e := b.expire(ctx); e != nil {
			slog.Error("reservation expiry retry")
		}
		if e := b.flush(ctx); e != nil {
			slog.Error("business outbox retry")
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}
func (b *business) flush(ctx context.Context) error {
	tx, e := b.db.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	rows, e := records(ctx, tx, "SELECT event_id,payload::text,headers::text FROM business_outbox WHERE published_at IS NULL ORDER BY created_at,event_id LIMIT 20 FOR UPDATE SKIP LOCKED")
	if e != nil {
		return e
	}
	for _, r := range rows {
		var h map[string]string
		json.Unmarshal([]byte(r["headers"].(string)), &h)
		headers := []kafka.Header{}
		for k, v := range h {
			headers = append(headers, kafka.Header{Key: k, Value: []byte(v)})
		}
		carrier := propagation.MapCarrier(h)
		sendCtx := otel.GetTextMapPropagator().Extract(ctx, carrier)
		sendCtx, span := otel.Tracer("order-service").Start(sendCtx, "outbox.publish", trace.WithSpanKind(trace.SpanKindProducer))
		e = b.writer.WriteMessages(sendCtx, kafka.Message{Key: []byte(r["event_id"].(string)), Value: []byte(r["payload"].(string)), Headers: headers})
		span.End()
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, "UPDATE business_outbox SET published_at=now() WHERE event_id=$1", r["event_id"]); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	tx, e := pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(81290401)"); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations(version int PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())"); e != nil {
		return e
	}
	for i, sql := range []string{migration, businessMigration, expiryMigration, paymentIntentMigration, refundIntentMigration, legacyPaidMigration} {
		var exists bool
		if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)", i+1).Scan(&exists); e != nil {
			return e
		}
		if !exists {
			if _, e = tx.Exec(ctx, sql); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, "INSERT INTO schema_migrations(version) VALUES($1)", i+1); e != nil {
				return e
			}
		}
	}
	return tx.Commit(ctx)
}
