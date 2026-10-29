package main

import (
	"context"
	"encoding/json"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/trace"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestDeliveryAndTrace(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	tp := trace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	defer tp.Shutdown(context.Background())
	var got string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("traceparent")
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["event_id"] != "event-test" || body["mock"] != true {
			t.Error("invalid email contract")
		}
		w.WriteHeader(200)
	}))
	defer s.Close()
	n := &notifier{client: s.Client(), base: s.URL, events: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_events"}, []string{"outcome"})}
	msg := kafka.Message{Value: []byte(`{"schema_version":1,"mock":true,"event_id":"event-test","event_type":"demo.order.created","order_id":"demo-order-001","scenario":"success"}`), Headers: []kafka.Header{{Key: "traceparent", Value: []byte("00-11111111111111111111111111111111-2222222222222222-01")}}}
	if err := n.deliver(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if len(got) < 35 || got[3:35] != "11111111111111111111111111111111" {
		t.Fatal("trace lost", got)
	}
	s.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) })
	if n.deliver(context.Background(), msg) == nil {
		t.Fatal("HTTP failure accepted")
	}
	if n.send(context.Background(), []byte(`{"mock":false}`)) == nil {
		t.Fatal("invalid event accepted")
	}
}

type fakeReader struct {
	msg     kafka.Message
	cancel  context.CancelFunc
	commits atomic.Int32
}

func (f *fakeReader) FetchMessage(context.Context) (kafka.Message, error) { return f.msg, nil }
func (f *fakeReader) CommitMessages(context.Context, ...kafka.Message) error {
	f.commits.Add(1)
	f.cancel()
	return nil
}
func TestCommitOnlyAfterDelivery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var attempts atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
	}))
	defer s.Close()
	n := &notifier{client: s.Client(), base: s.URL, events: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "retry_events"}, []string{"outcome"})}
	r := &fakeReader{msg: kafka.Message{Value: []byte(`{"schema_version":1,"mock":true,"event_id":"event-test","event_type":"demo.order.created","order_id":"demo-order-001","scenario":"success"}`)}, cancel: cancel}
	n.consume(ctx, r)
	if attempts.Load() != 2 || r.commits.Load() != 1 {
		t.Fatal("failed delivery committed or retry lost")
	}
}
