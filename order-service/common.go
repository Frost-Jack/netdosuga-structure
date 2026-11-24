package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"example.com/netdosuga/order-service/api"
	"example.com/netdosuga/order-service/internal/observe"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
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
	case "catalog.list", "applications.list", "applications.create", "applications.approve", "applications.reject", "offers.list", "offers.update", "offers.publish", "sales.get":
		e = b.catalog(ctx, tx, r, m, op, actor, role, result)
	case "cart.get", "cart.add", "cart.remove", "orders.create", "orders.list", "orders.get", "orders.pay", "orders.cancel", "tickets.redeem":
		e = b.orders(ctx, tx, r, m, op, actor, role, result)
	default:
		return nil, problem{404, "UNKNOWN_OPERATION"}
	}
	if e != nil {
		return nil, e
	}
	commitCtx, commitSpan := otel.Tracer("orders-service").Start(ctx, "transaction.commit")
	e = tx.Commit(commitCtx)
	commitSpan.End()
	if e != nil {
		return nil, e
	}
	return result, nil
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
		carrier := propagation.MapCarrier(h)
		sendCtx := otel.GetTextMapPropagator().Extract(ctx, carrier)
		sendCtx, span := otel.Tracer("order-service").Start(sendCtx, "outbox.publish", trace.WithSpanKind(trace.SpanKindProducer), trace.WithAttributes(
			attribute.String("messaging.system", "kafka"), attribute.String("messaging.destination.name", b.writer.Topic), attribute.String("messaging.operation.name", "publish")))
		var event struct {
			Type string `json:"event_type"`
		}
		if json.Unmarshal([]byte(r["payload"].(string)), &event) == nil {
			switch event.Type {
			case "order.created", "order.paid", "order.cancelled", "order.expired", "ticket.redeemed":
				span.SetAttributes(attribute.String("messaging.message.type", event.Type))
			}
		}
		e = b.writer.WriteMessages(sendCtx, kafka.Message{Key: []byte(r["event_id"].(string)), Value: []byte(r["payload"].(string)), Headers: outboxTraceHeaders(sendCtx)})
		if e != nil {
			span.SetStatus(codes.Error, "Kafka publish failed")
		}
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

// Inject after starting outbox.publish so the consumer is its descendant.
func outboxTraceHeaders(ctx context.Context) []kafka.Header {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	headers := []kafka.Header{}
	for k, v := range carrier {
		headers = append(headers, kafka.Header{Key: k, Value: []byte(v)})
	}
	return headers
}

//go:embed migrations/*.sql
var migrationsFS embed.FS

type migrationFile struct {
	version int
	name    string
}

func migrationFiles() ([]migrationFile, error) {
	entries, e := migrationsFS.ReadDir("migrations")
	if e != nil {
		return nil, e
	}
	files := []migrationFile{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		parts := strings.SplitN(entry.Name(), "_", 2)
		if len(parts) != 2 {
			return nil, errors.New("invalid migration name")
		}
		version, e := strconv.Atoi(parts[0])
		if e != nil {
			return nil, errors.New("invalid migration version")
		}
		files = append(files, migrationFile{version: version, name: entry.Name()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].version < files[j].version })
	return files, nil
}

func migrationSQL(name string) (string, error) {
	data, e := migrationsFS.ReadFile("migrations/" + name)
	return string(data), e
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
	files, e := migrationFiles()
	if e != nil {
		return e
	}
	for _, file := range files {
		var exists bool
		if e = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)", file.version).Scan(&exists); e != nil {
			return e
		}
		if !exists {
			sql, e := migrationSQL(file.name)
			if e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, sql); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, "INSERT INTO schema_migrations(version) VALUES($1)", file.version); e != nil {
				return e
			}
		}
	}
	return tx.Commit(ctx)
}
