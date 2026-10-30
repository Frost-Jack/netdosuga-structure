package main

import (
	"context"
	"encoding/json"
	"errors"
	"example.com/netdosuga/gateway/api"
	"example.com/netdosuga/gateway/internal/observe"
	"fmt"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type gateway struct {
	client  api.OrderServiceClient
	timeout time.Duration
	logger  *slog.Logger
}

func (g *gateway) create(w http.ResponseWriter, r *http.Request) {
	ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
	ctx, span := otel.Tracer("gateway").Start(ctx, "POST /api/v1/demo/orders", trace.WithSpanKind(trace.SpanKindServer))
	defer span.End()
	traceID := span.SpanContext().TraceID().String()
	w.Header().Set("Content-Type", "application/json")
	fail := func(httpCode int, code, message string) {
		observe.Error(ctx, errors.New(message))
		sc := trace.SpanContextFromContext(ctx)
		g.logger.Log(ctx, slog.LevelError, "demo request failed", "code", code, "trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String())
		w.WriteHeader(httpCode)
		json.NewEncoder(w).Encode(map[string]any{"mock": true, "code": code, "message": message, "trace_id": traceID})
	}
	var input struct {
		Scenario string `json:"scenario"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&input); err != nil {
		fail(400, "INVALID_ARGUMENT", "expected JSON with a valid scenario")
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		fail(400, "INVALID_ARGUMENT", "expected one JSON object")
		return
	}
	switch input.Scenario {
	case "success", "error", "delay", "timeout":
	default:
		fail(400, "INVALID_ARGUMENT", "unknown scenario")
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	result, err := g.client.CreateDemoOrder(callCtx, &api.CreateDemoOrderRequest{Scenario: input.Scenario})
	if err != nil {
		code := status.Code(err)
		switch code {
		case codes.InvalidArgument:
			fail(400, "INVALID_ARGUMENT", "unknown scenario")
		case codes.DeadlineExceeded:
			fail(504, "DEADLINE_EXCEEDED", "demo dependency timed out")
		default:
			fail(502, "DEPENDENCY_ERROR", "demo dependency failed")
		}
		return
	}
	observe.Log(ctx, g.logger, "demo order accepted", "event_id", result.EventId, "scenario", input.Scenario)
	json.NewEncoder(w).Encode(map[string]any{"mock": result.Mock, "order_id": result.OrderId, "status": result.Status, "event_id": result.EventId, "trace_id": traceID})
}
func run() error {
	logger := observe.Logger("api-gateway")
	if err := observe.DemoOnly(); err != nil {
		return err
	}
	timeout, err := observe.Duration("ORDER_GRPC_TIMEOUT", 3*time.Second)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	shutdown, err := observe.Tracing(ctx, "api-gateway")
	if err != nil {
		return err
	}
	defer func() { c, stop := context.WithTimeout(context.Background(), 5*time.Second); defer stop(); shutdown(c) }()
	conn, err := grpc.NewClient(observe.Env("ORDER_GRPC_ADDR", "order-service:50051"), grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithStatsHandler(otelgrpc.NewClientHandler()))
	if err != nil {
		return err
	}
	defer conn.Close()
	handler := &gateway{api.NewOrderServiceClient(conn), timeout, logger}
	mux := http.NewServeMux()
	metrics := observe.NewHTTPMetrics("api-gateway")
	mux.Handle("POST /api/v1/demo/orders", metrics.Wrap("/api/v1/demo/orders", http.HandlerFunc(handler.create)))
	handler.registerShop(mux, metrics)
	registerSwagger(mux)
	observe.Endpoints(mux, metrics, func(c context.Context) error {
		out, e := grpc_health_v1.NewHealthClient(conn).Check(c, &grpc_health_v1.HealthCheckRequest{})
		if e != nil {
			return e
		}
		if out.Status != grpc_health_v1.HealthCheckResponse_SERVING {
			return fmt.Errorf("order service not ready")
		}
		return nil
	})
	server := &http.Server{Addr: observe.Env("HTTP_ADDR", ":8080"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	failures := make(chan error, 1)
	go func() { failures <- server.ListenAndServe() }()
	logger.Info("listening", "address", server.Addr)
	select {
	case err := <-failures:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	stopCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	return server.Shutdown(stopCtx)
}
func main() {
	if err := run(); err != nil {
		observe.Logger("api-gateway").Error("startup or runtime failed", "error", err.Error())
		os.Exit(1)
	}
}
