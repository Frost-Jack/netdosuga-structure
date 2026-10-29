package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"example.com/netdosuga/order-service/api"
	"example.com/netdosuga/order-service/internal/observe"
	"github.com/segmentio/kafka-go"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

func TestGRPCPaymentAndEvent(t *testing.T) {
	previousProvider := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		provider.Shutdown(context.Background())
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previousPropagator)
	})
	var paymentCalls atomic.Int32
	var inserts atomic.Int32
	var publishes atomic.Int32
	var storeFailure atomic.Bool
	var publishFailure atomic.Bool
	payment := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paymentCalls.Add(1)
		if r.URL.Path != "/mock/payments" || r.Method != "POST" {
			t.Errorf("wrong payment request: %s %s", r.Method, r.URL.Path)
		}
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		if !trace.SpanContextFromContext(ctx).IsValid() {
			t.Error("payment lost trace context")
		}
		var input struct {
			Scenario string `json:"scenario"`
		}
		json.NewDecoder(r.Body).Decode(&input)
		switch input.Scenario {
		case "error":
			http.Error(w, "synthetic failure", 503)
			return
		case "timeout":
			select {
			case <-r.Context().Done():
				return
			case <-time.After(time.Second):
			}
		case "delay":
			time.Sleep(10 * time.Millisecond)
		}
		json.NewEncoder(w).Encode(map[string]any{"mock": true, "payment_id": "demo-payment-001", "status": "DEMO_SUCCEEDED"})
	}))
	defer payment.Close()
	service := &orderServer{paymentURL: payment.URL, httpClient: &http.Client{Timeout: 50 * time.Millisecond, Transport: otelhttp.NewTransport(http.DefaultTransport)}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	service.insert = func(ctx context.Context, id, scenario string) error {
		inserts.Add(1)
		if storeFailure.Load() {
			return errors.New("database unavailable")
		}
		return nil
	}
	service.publish = func(ctx context.Context, payload []byte, headers []kafka.Header) error {
		publishes.Add(1)
		if publishFailure.Load() {
			return errors.New("broker unavailable")
		}
		var event demoEvent
		if err := json.Unmarshal(payload, &event); err != nil {
			t.Error(err)
		}
		if event.EventID == "" || event.EventType != "demo.order.created" || !event.Mock || event.OrderID != "demo-order-001" || event.SchemaVersion != 1 {
			t.Errorf("bad event %s", payload)
		}
		carrier := propagation.MapCarrier{}
		for _, h := range headers {
			carrier[h.Key] = string(h.Value)
		}
		extracted := otel.GetTextMapPropagator().Extract(context.Background(), carrier)
		if trace.SpanContextFromContext(extracted).TraceID() != trace.SpanContextFromContext(ctx).TraceID() {
			t.Error("Kafka lost trace")
		}
		return nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.StatsHandler(otelgrpc.NewServerHandler()))
	api.RegisterOrderServiceServer(server, service)
	go server.Serve(listener)
	defer server.Stop()
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithStatsHandler(otelgrpc.NewClientHandler()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := api.NewOrderServiceClient(conn)
	for _, test := range []struct {
		scenario   string
		code       codes.Code
		operations int32
	}{{"success", codes.OK, 1}, {"delay", codes.OK, 1}, {"error", codes.Unavailable, 0}, {"timeout", codes.DeadlineExceeded, 0}, {"invalid", codes.InvalidArgument, 0}} {
		t.Run(test.scenario, func(t *testing.T) {
			beforeInsert, beforePublish := inserts.Load(), publishes.Load()
			ctx, span := otel.Tracer("test").Start(context.Background(), "request")
			defer span.End()
			deadline, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			result, err := client.CreateDemoOrder(deadline, &api.CreateDemoOrderRequest{Scenario: test.scenario})
			if status.Code(err) != test.code {
				t.Fatalf("want %v got %v", test.code, err)
			}
			if inserts.Load()-beforeInsert != test.operations || publishes.Load()-beforePublish != test.operations {
				t.Fatalf("unexpected effects insert=%d publish=%d", inserts.Load()-beforeInsert, publishes.Load()-beforePublish)
			}
			if err == nil && (result.TraceId != span.SpanContext().TraceID().String() || result.EventId == "" || !result.Mock) {
				t.Fatalf("bad response %v", result)
			}
		})
	}
	storeFailure.Store(true)
	before := publishes.Load()
	_, err = client.CreateDemoOrder(context.Background(), &api.CreateDemoOrderRequest{Scenario: "success"})
	if status.Code(err) != codes.Unavailable || publishes.Load() != before {
		t.Fatal("database failure must prevent publish", err)
	}
	storeFailure.Store(false)
	publishFailure.Store(true)
	_, err = client.CreateDemoOrder(context.Background(), &api.CreateDemoOrderRequest{Scenario: "success"})
	if status.Code(err) != codes.Unavailable {
		t.Fatal("publish failure must not succeed", err)
	}
	publishFailure.Store(false)
	payment.Close()
	before = inserts.Load()
	_, err = client.CreateDemoOrder(context.Background(), &api.CreateDemoOrderRequest{Scenario: "success"})
	if status.Code(err) != codes.Unavailable || inserts.Load() != before {
		t.Fatal("offline payment created effects", err)
	}
	if paymentCalls.Load() != 6 {
		t.Fatalf("expected six payment requests, got %d", paymentCalls.Load())
	}
}
func TestHealthDependencyFailure(t *testing.T) {
	h := &healthServer{ready: func(context.Context) error { return errors.New("offline") }}
	out, err := h.Check(context.Background(), &grpc_health_v1.HealthCheckRequest{})
	if err != nil || out.Status != grpc_health_v1.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("unready dependency: %v %v", out, err)
	}
}
func TestFailClosed(t *testing.T) {
	for _, v := range []struct{ env, mock string }{{"", ""}, {"production", "true"}, {"local", "false"}} {
		t.Setenv("APP_ENV", v.env)
		t.Setenv("MOCK_MODE", v.mock)
		if err := run(); err == nil {
			t.Fatalf("unsafe mode accepted: %+v", v)
		}
	}
	t.Setenv("APP_ENV", "local")
	t.Setenv("MOCK_MODE", "true")
	if err := observe.DemoOnly(); err != nil {
		t.Fatal(err)
	}
}
