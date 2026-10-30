package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"io"
	"log/slog"
	"net/http"
	"time"
)

type businessEvent struct {
	SchemaVersion int    `json:"schema_version"`
	Demo          bool   `json:"demo"`
	EventID       string `json:"event_id"`
	EventType     string `json:"event_type"`
	OrderID       string `json:"order_id"`
	OccurredAt    string `json:"occurred_at"`
}

func parseBusiness(data []byte) (businessEvent, error) {
	var e businessEvent
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if len(data) > 16384 || d.Decode(&e) != nil || d.Decode(&struct{}{}) != io.EOF || e.SchemaVersion != 1 || !e.Demo || len(e.EventID) != 36 || len(e.OrderID) < 1 || len(e.OrderID) > 128 {
		return e, fmt.Errorf("invalid business event")
	}
	if _, err := time.Parse(time.RFC3339Nano, e.OccurredAt); err != nil {
		return e, err
	}
	switch e.EventType {
	case "order.created", "order.paid", "order.cancelled", "order.expired", "ticket.redeemed":
		return e, nil
	}
	return e, fmt.Errorf("unknown event")
}
func businessDB(ctx context.Context, url string) (*pgxpool.Pool, error) {
	if url == "" {
		return nil, fmt.Errorf("DATABASE_URL required")
	}
	p, e := pgxpool.New(ctx, url)
	if e != nil {
		return nil, e
	}
	tx, e := p.Begin(ctx)
	if e != nil {
		p.Close()
		return nil, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(81290402)"); e == nil {
		_, e = tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS business_notifications(event_id text PRIMARY KEY,delivered_at timestamptz NOT NULL DEFAULT now()); CREATE TABLE IF NOT EXISTS business_poison(topic text NOT NULL,partition int NOT NULL,message_offset bigint NOT NULL,reason text NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(topic,partition,message_offset))")
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		p.Close()
		return nil, e
	}
	return p, nil
}
func (n *notifier) businessDeliver(ctx context.Context, p *pgxpool.Pool, msg kafka.Message) (resultErr error) {
	carrier := propagation.MapCarrier{}
	for _, h := range msg.Headers {
		carrier[h.Key] = string(h.Value)
	}
	ctx = otel.GetTextMapPropagator().Extract(ctx, carrier)
	ctx, span := otel.Tracer("notification").Start(ctx, "business event consume", trace.WithSpanKind(trace.SpanKindConsumer))
	started := time.Now()
	defer func() {
		outcome := "success"
		if resultErr != nil {
			outcome = "error"
			span.SetStatus(codes.Error, "notification failed")
		}
		notificationOperations.WithLabelValues("business.notify", outcome).Inc()
		notificationDuration.WithLabelValues("business.notify").Observe(time.Since(started).Seconds())
		span.End()
	}()
	vctx, validation := otel.Tracer("notification").Start(ctx, "validate")
	_ = vctx
	e, err := parseBusiness(msg.Value)
	validation.End()
	if err != nil {
		_, err = p.Exec(ctx, "INSERT INTO business_poison(topic,partition,message_offset,reason) VALUES($1,$2,$3,'INVALID_EVENT') ON CONFLICT DO NOTHING", msg.Topic, msg.Partition, msg.Offset)
		if err == nil {
			n.events.WithLabelValues("poison").Inc()
		}
		return err
	}
	tx, err := p.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", e.EventID); err != nil {
		return err
	}
	dctx, dedup := otel.Tracer("notification").Start(ctx, "deduplicate")
	defer dedup.End()
	var exists bool
	if err = tx.QueryRow(dctx, "SELECT EXISTS(SELECT 1 FROM business_notifications WHERE event_id=$1)", e.EventID).Scan(&exists); err != nil {
		return err
	}
	dedup.End()
	if exists {
		n.events.WithLabelValues("duplicate").Inc()
		return tx.Commit(ctx)
	}
	sctx, send := otel.Tracer("notification").Start(ctx, "send")
	defer send.End()
	data, _ := json.Marshal(map[string]any{"mock": true, "event_id": e.EventID, "order_id": e.OrderID})
	req, _ := http.NewRequestWithContext(sctx, "POST", n.base+"/mock/business-emails", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	otel.GetTextMapPropagator().Inject(sctx, propagation.HeaderCarrier(req.Header))
	res, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	if res.StatusCode != 200 {
		return fmt.Errorf("business mail HTTP %d", res.StatusCode)
	}
	send.End()
	cctx, commit := otel.Tracer("notification").Start(ctx, "commit")
	defer commit.End()
	if _, err = tx.Exec(cctx, "INSERT INTO business_notifications(event_id) VALUES($1)", e.EventID); err != nil {
		return err
	}
	if err = tx.Commit(cctx); err == nil {
		n.events.WithLabelValues("success").Inc()
		sc := trace.SpanContextFromContext(ctx)
		slog.InfoContext(ctx, "business notification delivered", "operation", "business.notify", "method", "consume", "route", "business.events", "status", "delivered", "duration_ms", time.Since(started).Milliseconds(), "trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String(), "request", map[string]any{"order_id": e.OrderID, "event_type": e.EventType}, "response", map[string]any{"status": "delivered", "mock": true})
		span.SetAttributes(attribute.String("app.request", fmt.Sprintf(`{"order_id":%q,"event_type":%q}`, e.OrderID, e.EventType)), attribute.String("app.response", `{"status":"delivered","mock":true}`))
	}
	return err
}
func (n *notifier) consumeBusiness(ctx context.Context, p *pgxpool.Pool, r consumer) {
	var lastFetchLog time.Time
	for ctx.Err() == nil {
		msg, e := r.FetchMessage(ctx)
		if e != nil {
			if ctx.Err() == nil && time.Since(lastFetchLog) > 10*time.Second {
				slog.Warn("business consumer fetch retry", "operation", "business.fetch", "status", "retry", "error", e)
				lastFetchLog = time.Now()
			}
			if !pause(ctx) {
				return
			}
			continue
		}
		for ctx.Err() == nil {
			e = n.businessDeliver(ctx, p, msg)
			if e == nil {
				e = r.CommitMessages(ctx, msg)
			}
			if e == nil {
				break
			}
			n.events.WithLabelValues("error").Inc()
			slog.Error("business notification retry", "error", e)
			if !pause(ctx) {
				return
			}
		}
	}
}
