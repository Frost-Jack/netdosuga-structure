package main

import (
	"context"
	"encoding/json"
	"regexp"

	"example.com/netdosuga/order-service/api"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var digitPattern = regexp.MustCompile(`^\d+$`)

func digits(value string, min, max int) bool {
	return len(value) >= min && len(value) <= max && digitPattern.MatchString(value)
}

func (b *business) catalog(ctx context.Context, tx pgx.Tx, r *api.ShopRequest, m map[string]json.RawMessage, op, actor, role string, result map[string]any) error {
	switch op {
	case "catalog.list":
		rows, e := records(ctx, tx, "SELECT * FROM business_offers WHERE status='PUBLISHED' AND ($1='' OR city=$1) AND ($2='' OR category=$2) ORDER BY id LIMIT 200", text(m, "city"), text(m, "category"))
		if e != nil {
			return e
		}
		result["offers"] = rows
	case "applications.list", "offers.list":
		if role != "vendor" && role != "admin" {
			return forbidden()
		}
		table, key := "business_applications", "applications"
		if op == "offers.list" {
			table, key = "business_offers", "offers"
		}
		rows, e := records(ctx, tx, "SELECT * FROM "+table+" WHERE vendor_id=$1 OR $2 ORDER BY id LIMIT 200", actor, role == "admin")
		if e != nil {
			return e
		}
		result[key] = rows
	case "applications.create":
		if role != "vendor" {
			return forbidden()
		}
		for _, k := range []string{"organization", "title", "city", "category", "address", "contact_person"} {
			if !valid(text(m, k)) {
				return bad("REQUIRED_FIELDS")
			}
		}
		if !digits(text(m, "inn"), 10, 12) {
			return bad("INVALID_INN")
		}
		if !digits(text(m, "ogrn"), 13, 15) {
			return bad("INVALID_OGRN")
		}
		if !digits(text(m, "bik"), 9, 9) {
			return bad("INVALID_BIK")
		}
		if !digits(text(m, "account"), 20, 20) {
			return bad("INVALID_ACCOUNT")
		}
		a, e := one(ctx, tx, "INSERT INTO business_applications(id,vendor_id,organization,title,city,category,inn,ogrn,address,contact_person,bik,account) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING *", uuid.NewString(), actor, text(m, "organization"), text(m, "title"), text(m, "city"), text(m, "category"), text(m, "inn"), text(m, "ogrn"), text(m, "address"), text(m, "contact_person"), text(m, "bik"), text(m, "account"))
		if e != nil {
			return e
		}
		result["application"] = a
	case "applications.approve", "applications.reject":
		if role != "admin" {
			return forbidden()
		}
		a, e := one(ctx, tx, "SELECT * FROM business_applications WHERE id=$1 FOR UPDATE", r.Id)
		if e != nil {
			return e
		}
		if a["status"] != "PENDING" {
			return problem{409, "INVALID_STATE"}
		}
		state := "APPROVED"
		if op == "applications.reject" {
			state = "REJECTED"
			if !valid(text(m, "reason")) {
				return bad("REASON_REQUIRED")
			}
		}
		a, e = one(ctx, tx, "UPDATE business_applications SET status=$2,reason=$3 WHERE id=$1 RETURNING *", r.Id, state, text(m, "reason"))
		if e != nil {
			return e
		}
		result["application"] = a
		if state == "APPROVED" {
			o, e := one(ctx, tx, "INSERT INTO business_offers(id,vendor_id,title,city,category) VALUES($1,$2,$3,$4,$5) RETURNING *", uuid.NewString(), a["vendor_id"], a["title"], a["city"], a["category"])
			if e != nil {
				return e
			}
			result["offer"] = o
		}
	case "offers.update", "offers.publish":
		if role != "vendor" {
			return forbidden()
		}
		o, e := one(ctx, tx, "SELECT * FROM business_offers WHERE id=$1 FOR UPDATE", r.Id)
		if e != nil {
			return e
		}
		if o["vendor_id"] != actor {
			return forbidden()
		}
		if op == "offers.publish" {
			if o["price_cents"].(int64) <= 0 || o["capacity"].(int32) <= 0 || !valid(o["description"].(string)) {
				return bad("INCOMPLETE_OFFER")
			}
			o, e = one(ctx, tx, "UPDATE business_offers SET status='PUBLISHED' WHERE id=$1 RETURNING *", r.Id)
		} else {
			for _, k := range []string{"title", "description", "city", "category"} {
				if !valid(text(m, k)) {
					return bad("REQUIRED_FIELDS")
				}
			}
			schedule := text(m, "schedule")
			if len(schedule) > 1000 {
				return bad("INVALID_SCHEDULE")
			}
			price, cap := number(m, "price_cents"), number(m, "capacity")
			used := int64(o["capacity"].(int32) - o["available"].(int32))
			if price < 1 || price > 100000000 || cap < used || cap > 100000 {
				return bad("INVALID_AMOUNT_OR_CAPACITY")
			}
			o, e = one(ctx, tx, "UPDATE business_offers SET title=$2,description=$3,city=$4,category=$5,price_cents=$6,capacity=$7,available=$8,schedule=$9 WHERE id=$1 RETURNING *", r.Id, text(m, "title"), text(m, "description"), text(m, "city"), text(m, "category"), price, cap, cap-used, schedule)
		}
		if e != nil {
			return e
		}
		result["offer"] = o
	case "sales.get":
		if role != "vendor" && role != "admin" {
			return forbidden()
		}
		rows, e := records(ctx, tx, "SELECT i.*,o.created_at FROM business_items i JOIN business_orders o ON o.id=i.order_id WHERE o.status='PAID' AND (i.vendor_id=$1 OR $2) ORDER BY o.created_at DESC LIMIT 500", actor, role == "admin")
		if e != nil {
			return e
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
	}
	return nil
}
