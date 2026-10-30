package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

type event struct {
	SchemaVersion int    `json:"schema_version"`
	Mock          bool   `json:"mock"`
	EventID       string `json:"event_id"`
	EventType     string `json:"event_type"`
	OrderID       string `json:"order_id"`
	Scenario      string `json:"scenario"`
}
type consumer interface {
	FetchMessage(context.Context) (kafka.Message, error)
	CommitMessages(context.Context, ...kafka.Message) error
}
type notifier struct {
	client *http.Client
	base   string
	events *prometheus.CounterVec
}

func (n *notifier) deliver(ctx context.Context, msg kafka.Message) error {
	carrier := propagation.MapCarrier{}
	for _, h := range msg.Headers {
		carrier[h.Key] = string(h.Value)
	}
	ctx = otel.GetTextMapPropagator().Extract(ctx, carrier)
	ctx, span := otel.Tracer("notification").Start(ctx, "demo.order.created consume", trace.WithSpanKind(trace.SpanKindConsumer))
	defer span.End()
	err := n.send(ctx, msg.Value)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		n.events.WithLabelValues("error").Inc()
		sc := trace.SpanContextFromContext(ctx)
		slog.Error("mock notification failed", "trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String(), "error", err)
		return err
	}
	n.events.WithLabelValues("success").Inc()
	logOperation(ctx, "mock notification delivered")
	return nil
}
func (n *notifier) send(ctx context.Context, data []byte) error {
	var e event
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&e) != nil || d.Decode(&struct{}{}) != io.EOF || e.SchemaVersion != 1 || !e.Mock || e.EventID == "" || len(e.EventID) > 128 || e.EventType != "demo.order.created" || e.OrderID != "demo-order-001" || (e.Scenario != "success" && e.Scenario != "delay") {
		return fmt.Errorf("invalid demo event")
	}
	body, _ := json.Marshal(map[string]any{"mock": true, "event_id": e.EventID, "order_id": e.OrderID})
	ctx, span := otel.Tracer("notification").Start(ctx, "POST /mock/emails", trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()
	req, _ := http.NewRequestWithContext(ctx, "POST", n.base+"/mock/emails", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
	resp, err := n.client.Do(req)
	if err == nil {
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		if resp.StatusCode != 200 {
			err = fmt.Errorf("mock email HTTP %d", resp.StatusCode)
		}
	}
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return err
}

// A failed delivery retries the same record; committing a later record would skip it.
func (n *notifier) consume(ctx context.Context, r consumer) {
	for ctx.Err() == nil {
		msg, err := r.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() == nil {
				slog.Error("Kafka fetch failed", "error", err)
				if !pause(ctx) {
					return
				}
			}
			continue
		}
		for ctx.Err() == nil {
			if err = n.deliver(ctx, msg); err == nil {
				err = r.CommitMessages(ctx, msg)
				if err == nil {
					break
				}
				slog.Error("offset commit failed", "error", err)
			}
			if !pause(ctx) {
				return
			}
		}
	}
}
func pause(ctx context.Context) bool {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
func readiness(ctx context.Context, broker, topic, base string, client *http.Client) error {
	dialer := &kafka.Dialer{Timeout: time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", broker)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	parts, err := conn.ReadPartitions(topic)
	if err != nil || len(parts) == 0 {
		return fmt.Errorf("Kafka topic unavailable")
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/readyz", nil)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("mock unavailable")
	}
	return nil
}
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	tp, err := configure(ctx, "notification-worker")
	if err != nil {
		return err
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = tp.Shutdown(c)
	}()
	startupCtx, startupCancel := context.WithTimeout(ctx, 30*time.Second)
	db, err := businessDB(startupCtx, os.Getenv("DATABASE_URL"))
	startupCancel()
	if err != nil {
		return fmt.Errorf("business notification migration failed: %w", err)
	}
	defer db.Close()
	brokers := strings.Split(env("KAFKA_BROKERS", "kafka:9092"), ",")
	topic := env("KAFKA_TOPIC", "netdosuga.demo.orders.v1")
	businessTopic := env("BUSINESS_KAFKA_TOPIC", "netdosuga.business.events.v1")
	topicCtx, topicCancel := context.WithTimeout(ctx, 45*time.Second)
	err = ensureTopics(topicCtx, &kafka.Client{Addr: kafka.TCP(brokers...), Timeout: 3 * time.Second}, topic, businessTopic)
	topicCancel()
	if err != nil {
		return err
	}
	base := strings.TrimRight(env("MOCK_BASE_URL", "http://integration-mock:8083"), "/")
	t := newTelemetry("notification-worker")
	events := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "netdosuga_events_total", ConstLabels: prometheus.Labels{"service": "notification-worker"}}, []string{"outcome"})
	t.registry.MustRegister(events)
	n := &notifier{client: &http.Client{Timeout: time.Second}, base: base, events: events}
	reader := kafka.NewReader(kafka.ReaderConfig{Brokers: brokers, Topic: topic, GroupID: env("KAFKA_GROUP_ID", "netdosuga-demo-notification-v1"), StartOffset: kafka.FirstOffset, CommitInterval: 0, WatchPartitionChanges: true, PartitionWatchInterval: time.Second, MaxWait: 500 * time.Millisecond, Dialer: &kafka.Dialer{Timeout: time.Second}})
	businessReader := kafka.NewReader(kafka.ReaderConfig{Brokers: brokers, Topic: businessTopic, GroupID: "netdosuga-business-notification-v1", StartOffset: kafka.FirstOffset, CommitInterval: 0, WatchPartitionChanges: true, PartitionWatchInterval: time.Second, MaxWait: 500 * time.Millisecond})
	defer businessReader.Close()
	go n.consumeBusiness(ctx, db, businessReader)
	done := make(chan struct{})
	go func() { defer close(done); n.consume(ctx, reader) }()
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", t.wrap("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	mux.HandleFunc("/readyz", t.wrap("/readyz", func(w http.ResponseWriter, r *http.Request) {
		c, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if writableDB(c, db) != nil || readiness(c, brokers[0], topic, base, n.client) != nil || readiness(c, brokers[0], businessTopic, base, n.client) != nil {
			http.Error(w, "dependencies unavailable", 503)
			return
		}
		w.WriteHeader(200)
	}))
	mux.Handle("/metrics", t.metrics())
	mux.HandleFunc("/", t.wrap("unmatched", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }))
	s := &http.Server{Addr: env("HTTP_ADDR", ":8082"), Handler: mux, ReadHeaderTimeout: 3 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	errs := make(chan error, 1)
	go func() { errs <- s.ListenAndServe() }()
	slog.Info("Kafka demo consumer started")
	select {
	case <-ctx.Done():
	case err = <-errs:
		stop()
	}
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.Shutdown(c)
	go reader.Close()
	select {
	case <-done:
	case <-c.Done():
		return c.Err()
	}
	if err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		slog.Error("service stopped", "error", err)
		os.Exit(1)
	}
}
