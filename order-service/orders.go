package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"example.com/netdosuga/order-service/api"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (b *business) orders(ctx context.Context, tx pgx.Tx, r *api.ShopRequest, m map[string]json.RawMessage, op, actor, role string, result map[string]any) error {
	switch op {
	case "cart.get", "cart.add", "cart.remove":
		if role != "customer" {
			return forbidden()
		}
		if op == "cart.add" {
			q := number(m, "quantity")
			if q < 1 || q > 20 {
				return bad("INVALID_QUANTITY")
			}
			o, e := one(ctx, tx, "SELECT * FROM business_offers WHERE id=$1 AND status='PUBLISHED'", text(m, "offer_id"))
			if e != nil {
				return e
			}
			if int64(o["available"].(int32)) < q {
				return problem{409, "UNAVAILABLE_CAPACITY"}
			}
			if _, e = tx.Exec(ctx, "INSERT INTO business_cart(customer_id,offer_id,quantity) VALUES($1,$2,$3) ON CONFLICT(customer_id,offer_id) DO UPDATE SET quantity=excluded.quantity", actor, text(m, "offer_id"), q); e != nil {
				return e
			}
		}
		if op == "cart.remove" {
			if _, e := tx.Exec(ctx, "DELETE FROM business_cart WHERE customer_id=$1 AND offer_id=$2", actor, r.Id); e != nil {
				return e
			}
		}
		items, e := records(ctx, tx, "SELECT o.*,c.offer_id,c.quantity,o.price_cents*c.quantity AS total_cents FROM business_cart c JOIN business_offers o ON o.id=c.offer_id WHERE c.customer_id=$1 ORDER BY o.id", actor)
		if e != nil {
			return e
		}
		var total int64
		for _, i := range items {
			total += i["total_cents"].(int64)
		}
		result["cart"] = map[string]any{"items": items, "total_cents": total}
	case "orders.create":
		if role != "customer" {
			return forbidden()
		}
		if !valid(r.IdempotencyKey) || len(r.IdempotencyKey) > 128 {
			return bad("IDEMPOTENCY_KEY_REQUIRED")
		}
		old, e := records(ctx, tx, "SELECT id FROM business_orders WHERE customer_id=$1 AND idempotency_key=$2", actor, r.IdempotencyKey)
		if e != nil {
			return e
		}
		if len(old) > 0 {
			r.Id = old[0]["id"].(string)
		} else {
			items, e := records(ctx, tx, "SELECT o.*,c.quantity FROM business_cart c JOIN business_offers o ON o.id=c.offer_id WHERE c.customer_id=$1 ORDER BY o.id FOR UPDATE OF o", actor)
			if e != nil {
				return e
			}
			if len(items) == 0 {
				return bad("EMPTY_CART")
			}
			var total, commission int64
			for _, i := range items {
				q := int64(i["quantity"].(int32))
				if i["status"] != "PUBLISHED" || int64(i["available"].(int32)) < q {
					return problem{409, "UNAVAILABLE_CAPACITY"}
				}
				v := q * i["price_cents"].(int64)
				total += v
				commission += v / 10
			}
			r.Id = uuid.NewString()
			if _, e = tx.Exec(ctx, "INSERT INTO business_orders(id,customer_id,status,total_cents,commission_cents,idempotency_key) VALUES($1,$2,'RESERVED',$3,$4,$5)", r.Id, actor, total, commission, r.IdempotencyKey); e != nil {
				return e
			}
			for _, i := range items {
				v := int64(i["quantity"].(int32)) * i["price_cents"].(int64)
				if _, e = tx.Exec(ctx, "INSERT INTO business_items VALUES($1,$2,$3,$4,$5,$6,$7,$8)", r.Id, i["id"], i["vendor_id"], i["title"], i["quantity"], i["price_cents"], v, v/10); e != nil {
					return e
				}
				if _, e = tx.Exec(ctx, "UPDATE business_offers SET available=available-$2 WHERE id=$1", i["id"], i["quantity"]); e != nil {
					return e
				}
			}
			if _, e = tx.Exec(ctx, "DELETE FROM business_cart WHERE customer_id=$1", actor); e != nil {
				return e
			}
			if e = b.event(ctx, tx, "order.created", r.Id); e != nil {
				return e
			}
		}
		o, e := loadOrder(ctx, tx, r.Id)
		if e != nil {
			return e
		}
		result["order"] = o
	case "orders.list":
		if role != "customer" && role != "admin" {
			return forbidden()
		}
		rows, e := records(ctx, tx, "SELECT * FROM business_orders WHERE customer_id=$1 OR $2 ORDER BY created_at DESC LIMIT 200", actor, role == "admin")
		if e != nil {
			return e
		}
		result["orders"] = rows
	case "orders.get", "orders.pay", "orders.cancel":
		if role != "customer" && role != "admin" {
			return forbidden()
		}
		o, e := one(ctx, tx, "SELECT * FROM business_orders WHERE id=$1 FOR UPDATE", r.Id)
		if e != nil {
			return e
		}
		if o["customer_id"] != actor && role != "admin" {
			return forbidden()
		}
		if op != "orders.get" && role != "customer" {
			return forbidden()
		}
		state := o["status"].(string)
		if state == "RESERVED" && !o["expires_at"].(time.Time).After(time.Now()) {
			if e = b.releaseExpired(ctx, tx, r.Id); e != nil {
				return e
			}
			state = "EXPIRED"
			if op != "orders.get" {
				if e = tx.Commit(ctx); e != nil {
					return e
				}
				return problem{409, "RESERVATION_EXPIRED"}
			}
		}
		if op == "orders.pay" {
			pending, e := records(ctx, tx, "SELECT order_id FROM business_refund_attempts WHERE order_id=$1", r.Id)
			if e != nil {
				return e
			}
			if len(pending) > 0 {
				return problem{409, "CANCELLATION_PENDING"}
			}
		}
		if op == "orders.pay" && state != "PAID" {
			if state != "RESERVED" {
				return problem{409, "INVALID_STATE"}
			}
			paymentID, payErr := b.payment(ctx, "payments", r.Id, o["total_cents"].(int64), text(m, "scenario"))
			if payErr != nil {
				return payErr
			}
			if _, e = tx.Exec(ctx, "UPDATE business_orders SET status='PAID',payment_id=$2 WHERE id=$1", r.Id, paymentID); e != nil {
				return e
			}
			items, e := records(ctx, tx, "SELECT * FROM business_items WHERE order_id=$1 ORDER BY offer_id", r.Id)
			if e != nil {
				return e
			}
			for _, i := range items {
				for n := int32(0); n < i["quantity"].(int32); n++ {
					if _, e = tx.Exec(ctx, "INSERT INTO business_tickets(code,order_id,offer_id,vendor_id) VALUES($1,$2,$3,$4)", uuid.NewString(), r.Id, i["offer_id"], i["vendor_id"]); e != nil {
						return e
					}
				}
			}
			if e = b.event(ctx, tx, "order.paid", r.Id); e != nil {
				return e
			}
		}
		if op == "orders.cancel" && state != "CANCELLED" {
			if state != "RESERVED" && state != "PAID" {
				return problem{409, "INVALID_STATE"}
			}
			used, e := records(ctx, tx, "SELECT code FROM business_tickets WHERE order_id=$1 AND status='REDEEMED'", r.Id)
			if e != nil {
				return e
			}
			if len(used) > 0 {
				return problem{409, "TICKET_ALREADY_REDEEMED"}
			}
			if state == "RESERVED" {
				if e = b.cancelUncertain(ctx, tx, r.Id); e != nil {
					return e
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
							return lookupErr
						}
						if len(pending) == 1 && pending[0]["attempt_count"].(int64) == 1 {
							if _, clearErr := tx.Exec(ctx, "DELETE FROM business_refund_attempts WHERE order_id=$1", r.Id); clearErr != nil {
								return clearErr
							}
							if commitErr := tx.Commit(ctx); commitErr != nil {
								return commitErr
							}
						}
					}
					return refundErr
				}
				if _, e = tx.Exec(ctx, "UPDATE business_orders SET refund_id=$2 WHERE id=$1", r.Id, refundID); e != nil {
					return e
				}
			}
			if _, e = tx.Exec(ctx, "UPDATE business_orders SET status='CANCELLED' WHERE id=$1", r.Id); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, "UPDATE business_tickets SET status='REVOKED' WHERE order_id=$1", r.Id); e != nil {
				return e
			}
			if _, e = records(ctx, tx, "SELECT o.id FROM business_offers o JOIN business_items i ON i.offer_id=o.id WHERE i.order_id=$1 ORDER BY o.id FOR UPDATE OF o", r.Id); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, "UPDATE business_offers o SET available=o.available+i.quantity FROM business_items i WHERE i.order_id=$1 AND i.offer_id=o.id", r.Id); e != nil {
				return e
			}
			if e = b.event(ctx, tx, "order.cancelled", r.Id); e != nil {
				return e
			}
		}
		o, e = loadOrder(ctx, tx, r.Id)
		if e != nil {
			return e
		}
		result["order"] = o
	case "tickets.redeem":
		if role != "vendor" {
			return forbidden()
		}
		t, e := one(ctx, tx, "SELECT * FROM business_tickets WHERE code=$1", r.Id)
		if e != nil {
			return e
		}
		if t["vendor_id"] != actor {
			return forbidden()
		}
		o, e := one(ctx, tx, "SELECT * FROM business_orders WHERE id=$1 FOR UPDATE", t["order_id"])
		if e != nil {
			return e
		}
		pending, e := records(ctx, tx, "SELECT order_id FROM business_refund_attempts WHERE order_id=$1", t["order_id"])
		if e != nil {
			return e
		}
		if len(pending) > 0 {
			return problem{409, "REFUND_PENDING"}
		}
		t, e = one(ctx, tx, "SELECT * FROM business_tickets WHERE code=$1 FOR UPDATE", r.Id)
		if e != nil {
			return e
		}
		if t["status"] != "VALID" || o["status"] != "PAID" {
			return problem{409, "INVALID_TICKET_STATE"}
		}
		t, e = one(ctx, tx, "UPDATE business_tickets SET status='REDEEMED' WHERE code=$1 RETURNING *", r.Id)
		if e != nil {
			return e
		}
		if e = b.event(ctx, tx, "ticket.redeemed", t["order_id"].(string)); e != nil {
			return e
		}
		result["ticket"] = t
	}
	return nil
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
