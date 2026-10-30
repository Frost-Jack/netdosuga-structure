package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

type request struct {
	OrderID  string `json:"order_id"`
	Amount   int64  `json:"amount_cents"`
	Currency string `json:"currency,omitempty"`
	Scenario string `json:"scenario,omitempty"`
}
type result struct {
	Mock      bool   `json:"mock"`
	PaymentID string `json:"payment_id,omitempty"`
	RefundID  string `json:"refund_id,omitempty"`
	OrderID   string `json:"order_id"`
	Amount    int64  `json:"amount_cents"`
	Status    string `json:"status"`
	Cancelled bool   `json:"cancelled,omitempty"`
}
type server struct{ db *pgxpool.Pool }

func database(ctx context.Context, url string) (*pgxpool.Pool, error) {
	p, e := pgxpool.New(ctx, url)
	if e != nil {
		return nil, e
	}
	_, e = p.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS payment_mock; CREATE TABLE IF NOT EXISTS payment_mock.effects(kind text NOT NULL,idempotency_key text NOT NULL,order_id text NOT NULL,amount_cents bigint NOT NULL,result jsonb NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(kind,idempotency_key),UNIQUE(kind,order_id))`)
	if e != nil {
		p.Close()
		return nil, e
	}
	return p, nil
}
func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, code string) {
	respond(w, status, map[string]any{"mock": true, "code": code, "message": code})
}
func (s *server) handle(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, span := otel.Tracer("payment-mock").Start(r.Context(), "operation."+kind)
		defer span.End()
		status := 200
		start := time.Now()
		var in request
		var out any
		defer func() {
			if status >= 400 {
				span.SetStatus(codes.Error, "mock operation failed")
			}
			req, _ := json.Marshal(in)
			res, _ := json.Marshal(out)
			span.SetAttributes(attribute.String("app.request", string(req)), attribute.String("app.response", string(res)))
			sc := trace.SpanContextFromContext(ctx)
			slog.InfoContext(ctx, "mock payment operation", "trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String(), "operation", kind, "method", r.Method, "route", r.URL.Path, "status", status, "duration_ms", time.Since(start).Milliseconds(), "request", in, "response", out)
			businessOperations.WithLabelValues(kind, outcome(status)).Inc()
			businessDuration.WithLabelValues(kind).Observe(time.Since(start).Seconds())
		}()
		reject := func(st int, code string) {
			status = st
			out = map[string]any{"mock": true, "code": code}
			fail(w, st, code)
		}
		vctx, vspan := otel.Tracer("payment-mock").Start(ctx, "validate")
		_ = vctx
		if r.Method != "POST" {
			vspan.End()
			reject(405, "METHOD_NOT_ALLOWED")
			return
		}
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		d.DisallowUnknownFields()
		if d.Decode(&in) != nil || d.Decode(&struct{}{}) != io.EOF {
			in = request{}
			vspan.End()
			reject(400, "INVALID_REQUEST")
			return
		}
		if in.Scenario == "" {
			in.Scenario = "success"
		}
		key := r.Header.Get("Idempotency-Key")
		validScenario := in.Scenario == "success" || in.Scenario == "error" || in.Scenario == "delay" || in.Scenario == "timeout"
		if len(in.OrderID) < 1 || len(in.OrderID) > 128 || in.Amount <= 0 || !validScenario || (kind == "payment" && in.Currency != "RUB") || (kind == "refund" && in.Currency != "" && in.Currency != "RUB") || key == "" {
			in = request{}
			vspan.End()
			reject(400, "INVALID_REQUEST")
			return
		}
		vspan.End()
		if key != kind+":"+in.OrderID {
			reject(409, "IDEMPOTENCY_CONFLICT")
			return
		}
		tx, e := s.db.Begin(ctx)
		if e != nil {
			reject(503, "DATABASE_UNAVAILABLE")
			return
		}
		defer tx.Rollback(ctx)
		lctx, lspan := otel.Tracer("payment-mock").Start(ctx, "idempotency.lookup")
		_, e = tx.Exec(lctx, "SELECT pg_advisory_xact_lock(hashtext($1))", in.OrderID)
		if e == nil && kind == "payment" {
			var cancelled bool
			e = tx.QueryRow(lctx, "SELECT EXISTS(SELECT 1 FROM payment_mock.effects WHERE kind='cancellation' AND order_id=$1)", in.OrderID).Scan(&cancelled)
			if e == nil && cancelled {
				lspan.End()
				reject(409, "PAYMENT_CANCELLED")
				return
			}
		}
		var saved []byte
		var order string
		var amount int64
		if e == nil {
			e = tx.QueryRow(lctx, "SELECT order_id,amount_cents,result FROM payment_mock.effects WHERE kind=$1 AND (idempotency_key=$2 OR order_id=$3)", kind, key, in.OrderID).Scan(&order, &amount, &saved)
		}
		lspan.End()
		if e == nil {
			if order != in.OrderID || amount != in.Amount {
				reject(409, "IDEMPOTENCY_CONFLICT")
				return
			}
			json.Unmarshal(saved, &out)
			respond(w, 200, out)
			return
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			reject(503, "DATABASE_UNAVAILABLE")
			return
		}
		if kind == "refund" {
			var paid int64
			e = tx.QueryRow(ctx, "SELECT amount_cents FROM payment_mock.effects WHERE kind='payment' AND order_id=$1", in.OrderID).Scan(&paid)
			if e != nil || paid != in.Amount {
				reject(409, "PAYMENT_NOT_FOUND_OR_AMOUNT_MISMATCH")
				return
			}
		}
		simctx, simspan := otel.Tracer("payment-mock").Start(ctx, "provider.simulate")
		switch in.Scenario {
		case "error":
			simspan.End()
			reject(503, "MOCK_PROVIDER_ERROR")
			return
		case "delay", "timeout":
			delay := 300 * time.Millisecond
			if in.Scenario == "timeout" {
				delay = 10 * time.Second
			}
			select {
			case <-simctx.Done():
				simspan.End()
				status = 504
				return
			case <-time.After(delay):
			}
			if in.Scenario == "timeout" {
				simspan.End()
				reject(504, "MOCK_PROVIDER_TIMEOUT")
				return
			}
		}
		simspan.End()
		value := result{Mock: true, OrderID: in.OrderID, Amount: in.Amount, Status: "SUCCEEDED"}
		if kind == "payment" {
			value.PaymentID = "mock_payment_" + uuid.NewString()
		} else {
			value.RefundID = "mock_refund_" + uuid.NewString()
		}
		raw, _ := json.Marshal(value)
		pctx, pspan := otel.Tracer("payment-mock").Start(ctx, "persist")
		_, e = tx.Exec(pctx, "INSERT INTO payment_mock.effects(kind,idempotency_key,order_id,amount_cents,result) VALUES($1,$2,$3,$4,$5)", kind, key, in.OrderID, in.Amount, raw)
		if e == nil {
			e = tx.Commit(pctx)
		}
		pspan.End()
		if e != nil {
			reject(503, "DATABASE_UNAVAILABLE")
			return
		}
		out = value
		respond(w, 200, value)
	}
}

// Lookup shares the order lock with authorize/refund so a caller never observes
// a false absence while an earlier authorization is still being committed.
func (s *server) lookup(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/cancel") {
		s.cancel(w, r)
		return
	}
	if r.Method != "GET" {
		fail(w, 405, "METHOD_NOT_ALLOWED")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/payments/")
	if id == "" || len(id) > 128 || strings.Contains(id, "/") {
		fail(w, 400, "INVALID_REQUEST")
		return
	}
	ctx, span := otel.Tracer("payment-mock").Start(r.Context(), "idempotency.lookup")
	defer span.End()
	tx, e := s.db.Begin(ctx)
	if e != nil {
		fail(w, 503, "DATABASE_UNAVAILABLE")
		return
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", id)
	if e != nil {
		fail(w, 503, "DATABASE_UNAVAILABLE")
		return
	}
	var raw []byte
	e = tx.QueryRow(ctx, "SELECT result FROM payment_mock.effects WHERE kind='payment' AND order_id=$1", id).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		fail(w, 404, "PAYMENT_NOT_FOUND")
		return
	}
	if e != nil {
		fail(w, 503, "DATABASE_UNAVAILABLE")
		return
	}
	var value result
	json.Unmarshal(raw, &value)
	span.SetAttributes(attribute.String("app.request", fmt.Sprintf(`{"order_id":%q}`, id)), attribute.String("app.response", string(raw)))
	respond(w, 200, value)
}

// Cancellation is a terminal provider fence: even a delayed authorization cannot
// recreate an effect after inventory has been released by the order service.
func (s *server) cancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		fail(w, 405, "METHOD_NOT_ALLOWED")
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/payments/"), "/cancel")
	var in struct {
		Amount int64 `json:"amount_cents"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if id == "" || len(id) > 128 || strings.Contains(id, "/") || d.Decode(&in) != nil || d.Decode(&struct{}{}) != io.EOF || in.Amount <= 0 {
		fail(w, 400, "INVALID_REQUEST")
		return
	}
	if r.Header.Get("Idempotency-Key") != "refund:"+id {
		fail(w, 409, "IDEMPOTENCY_CONFLICT")
		return
	}
	ctx, span := otel.Tracer("payment-mock").Start(r.Context(), "operation.cancel")
	defer span.End()
	tx, e := s.db.Begin(ctx)
	if e != nil {
		fail(w, 503, "DATABASE_UNAVAILABLE")
		return
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", id)
	if e != nil {
		fail(w, 503, "DATABASE_UNAVAILABLE")
		return
	}
	var raw []byte
	var amount int64
	e = tx.QueryRow(ctx, "SELECT amount_cents,result FROM payment_mock.effects WHERE kind='cancellation' AND order_id=$1", id).Scan(&amount, &raw)
	if e == nil {
		if amount != in.Amount {
			fail(w, 409, "IDEMPOTENCY_CONFLICT")
			return
		}
		var out result
		json.Unmarshal(raw, &out)
		respond(w, 200, out)
		return
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		fail(w, 503, "DATABASE_UNAVAILABLE")
		return
	}
	out := result{Mock: true, OrderID: id, Amount: in.Amount, Status: "SUCCEEDED", Cancelled: true}
	e = tx.QueryRow(ctx, "SELECT amount_cents FROM payment_mock.effects WHERE kind='payment' AND order_id=$1", id).Scan(&amount)
	if e == nil {
		if amount != in.Amount {
			fail(w, 409, "IDEMPOTENCY_CONFLICT")
			return
		}
		var existing []byte
		e = tx.QueryRow(ctx, "SELECT result FROM payment_mock.effects WHERE kind='refund' AND order_id=$1", id).Scan(&existing)
		if e == nil {
			var refund result
			json.Unmarshal(existing, &refund)
			out.RefundID = refund.RefundID
		} else if errors.Is(e, pgx.ErrNoRows) {
			out.RefundID = "mock_refund_" + uuid.NewString()
			refund := out
			refund.Cancelled = false
			raw, _ = json.Marshal(refund)
			_, e = tx.Exec(ctx, "INSERT INTO payment_mock.effects(kind,idempotency_key,order_id,amount_cents,result) VALUES('refund',$1,$2,$3,$4)", "refund:"+id, id, in.Amount, raw)
		}
	} else if errors.Is(e, pgx.ErrNoRows) {
		e = nil
	}
	if e != nil {
		fail(w, 503, "DATABASE_UNAVAILABLE")
		return
	}
	raw, _ = json.Marshal(out)
	_, e = tx.Exec(ctx, "INSERT INTO payment_mock.effects(kind,idempotency_key,order_id,amount_cents,result) VALUES('cancellation',$1,$2,$3,$4)", "refund:"+id, id, in.Amount, raw)
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		fail(w, 503, "DATABASE_UNAVAILABLE")
		return
	}
	span.SetAttributes(attribute.String("app.request", fmt.Sprintf(`{"order_id":%q,"amount_cents":%d}`, id, in.Amount)), attribute.String("app.response", string(raw)))
	respond(w, 200, out)
}
func outcome(status int) string {
	if status >= 400 {
		return "error"
	}
	return "success"
}
func main() {
	ctx := context.Background()
	tp, e := configure(ctx, "payment-mock")
	if e != nil {
		panic(e)
	}
	defer tp.Shutdown(ctx)
	p, e := database(ctx, os.Getenv("DATABASE_URL"))
	if e != nil {
		panic(e)
	}
	defer p.Close()
	s := &server{db: p}
	t := newTelemetry("payment-mock")
	mux := http.NewServeMux()
	mux.Handle("/metrics", t.metrics())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if writableDB(ctx, p) != nil {
			http.Error(w, "database unavailable", 503)
			return
		}
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("/v1/payments/", t.wrap("/v1/payments/{order_id}", s.lookup))
	mux.HandleFunc("/v1/payments", t.wrap("/v1/payments", s.handle("payment")))
	mux.HandleFunc("/v1/refunds", t.wrap("/v1/refunds", s.handle("refund")))
	httpServer := &http.Server{Addr: ":8085", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	slog.Info("payment mock listening", "port", 8085, "mock", true)
	if e = httpServer.ListenAndServe(); e != nil {
		panic(e)
	}
}
