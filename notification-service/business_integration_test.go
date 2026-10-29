package main

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/segmentio/kafka-go"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestBusinessDurableDelivery(t *testing.T) {
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
	schema := fmt.Sprintf("%q", "notify_"+uuid.NewString())
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
	if _, e = p.Exec(ctx, "CREATE TABLE business_notifications(event_id text PRIMARY KEY,delivered_at timestamptz DEFAULT now());CREATE TABLE business_poison(topic text,partition int,message_offset bigint,reason text,PRIMARY KEY(topic,partition,message_offset))"); e != nil {
		t.Fatal(e)
	}
	var sends atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends.Add(1)
		fmt.Fprint(w, `{"mock":true,"status":"DEMO_SENT"}`)
	}))
	defer server.Close()
	n := &notifier{base: server.URL, client: server.Client(), events: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_events"}, []string{"outcome"})}
	msg := kafka.Message{Topic: "test", Value: []byte(`{"schema_version":1,"demo":true,"event_id":"11111111-1111-1111-1111-111111111111","event_type":"order.paid","order_id":"opaque-order","occurred_at":"2026-10-29T12:00:00Z"}`)}
	if e = n.businessDeliver(ctx, p, msg); e != nil {
		t.Fatal(e)
	}
	restarted := &notifier{base: n.base, client: n.client, events: n.events}
	if e = restarted.businessDeliver(ctx, p, msg); e != nil {
		t.Fatal(e)
	}
	if sends.Load() != 1 {
		t.Fatal("replay sent twice")
	}
	msg.Offset = 1
	msg.Value = []byte(`{"poison":true}`)
	if e = n.businessDeliver(ctx, p, msg); e != nil {
		t.Fatal(e)
	}
	var count int
	if e = p.QueryRow(ctx, "SELECT count(*) FROM business_poison").Scan(&count); e != nil || count != 1 {
		t.Fatal("poison not quarantined", e)
	}
	consumeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	good := kafka.Message{Topic: "test", Offset: 3, Value: []byte(`{"schema_version":1,"demo":true,"event_id":"22222222-2222-2222-2222-222222222222","event_type":"order.created","order_id":"next-order","occurred_at":"2026-10-29T12:00:01Z"}`)}
	msg.Offset = 2
	r := &businessQueue{messages: []kafka.Message{msg, good}, cancel: cancel}
	n.consumeBusiness(consumeCtx, p, r)
	if r.commits != 2 || sends.Load() != 2 {
		t.Fatalf("poison did not advance: commits=%d sends=%d", r.commits, sends.Load())
	}
}

type businessQueue struct {
	messages      []kafka.Message
	next, commits int
	cancel        context.CancelFunc
}

func (q *businessQueue) FetchMessage(ctx context.Context) (kafka.Message, error) {
	if q.next >= len(q.messages) {
		<-ctx.Done()
		return kafka.Message{}, ctx.Err()
	}
	m := q.messages[q.next]
	q.next++
	return m, nil
}
func (q *businessQueue) CommitMessages(context.Context, ...kafka.Message) error {
	q.commits++
	if q.commits == len(q.messages) {
		q.cancel()
	}
	return nil
}
