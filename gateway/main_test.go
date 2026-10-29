package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"example.com/netdosuga/gateway/api"
	"example.com/netdosuga/gateway/internal/observe"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

type testOrder struct {
	api.UnimplementedOrderServiceServer
	traceID chan string
}

func (s *testOrder) CreateDemoOrder(ctx context.Context, in *api.CreateDemoOrderRequest) (*api.CreateDemoOrderResponse, error) {
	s.traceID <- trace.SpanContextFromContext(ctx).TraceID().String()
	switch in.Scenario {
	case "error":
		return nil, status.Error(codes.Unavailable, "simulated dependency error")
	case "timeout":
		<-ctx.Done()
		return nil, status.Error(codes.DeadlineExceeded, "simulated timeout")
	case "delay":
		time.Sleep(15 * time.Millisecond)
	}
	return &api.CreateDemoOrderResponse{Mock: true, OrderId: "demo-order-001", Status: "DEMO_ACCEPTED", EventId: "bda42096-25c5-42d0-bd20-e273fc8b65bb"}, nil
}
func (s *testOrder) Shop(ctx context.Context, in *api.ShopRequest) (*api.ShopResponse, error) {
	s.traceID <- trace.SpanContextFromContext(ctx).TraceID().String()
	return &api.ShopResponse{Status: 200, BodyJson: `{"offers":[]}`, InstanceId: "order-test"}, nil
}
func TestHTTPToGRPC(t *testing.T) {
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
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.StatsHandler(otelgrpc.NewServerHandler()))
	service := &testOrder{traceID: make(chan string, 10)}
	api.RegisterOrderServiceServer(server, service)
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithStatsHandler(otelgrpc.NewClientHandler()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	g := &gateway{api.NewOrderServiceClient(conn), 80 * time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil))}
	httpServer := httptest.NewServer(http.HandlerFunc(g.create))
	defer httpServer.Close()
	for _, test := range []struct {
		scenario string
		want     int
		code     string
	}{{"success", 200, ""}, {"delay", 200, ""}, {"error", 502, "DEPENDENCY_ERROR"}, {"timeout", 504, "DEADLINE_EXCEEDED"}, {"unknown", 400, "INVALID_ARGUMENT"}} {
		t.Run(test.scenario, func(t *testing.T) {
			req, _ := http.NewRequest("POST", httpServer.URL, strings.NewReader(`{"scenario":"`+test.scenario+`"}`))
			req.Header.Set("traceparent", "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			var body map[string]any
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != test.want || body["mock"] != true || body["trace_id"] != "0123456789abcdef0123456789abcdef" {
				t.Fatalf("status %d body %v", response.StatusCode, body)
			}
			if test.code != "" && body["code"] != test.code {
				t.Fatalf("code: %v", body)
			}
			if test.scenario != "unknown" {
				select {
				case got := <-service.traceID:
					if got != body["trace_id"] {
						t.Fatalf("gRPC lost trace: %s", got)
					}
				case <-time.After(time.Second):
					t.Fatal("gRPC not called")
				}
			}
		})
	}
	for _, input := range []string{`{}`, `{"scenario":"success","extra":1}`, `{"scenario":"success"} {}`, `null`, `garbage`} {
		response, err := http.Post(httpServer.URL, "application/json", strings.NewReader(input))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 400 {
			t.Fatalf("input %s returned %d", input, response.StatusCode)
		}
	}
	shopReq := httptest.NewRequest("GET", "/api/v1/shop/catalog", nil)
	shopReq.Header.Set("traceparent", "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
	shopResponse := httptest.NewRecorder()
	g.shop("catalog.list")(shopResponse, shopReq)
	if shopResponse.Code != 200 || shopResponse.Header().Get("X-Order-Instance") != "order-test" {
		t.Fatalf("shop RPC: %d %s", shopResponse.Code, shopResponse.Body.String())
	}
	select {
	case got := <-service.traceID:
		if got != "0123456789abcdef0123456789abcdef" {
			t.Fatalf("Shop lost trace: %s", got)
		}
	case <-time.After(time.Second):
		t.Fatal("Shop not called")
	}
	server.Stop()
	response, err := http.Post(httpServer.URL, "application/json", strings.NewReader(`{"scenario":"success"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 502 {
		t.Fatalf("offline gRPC returned %d", response.StatusCode)
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
