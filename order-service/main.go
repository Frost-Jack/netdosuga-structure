package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
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

	"example.com/netdosuga/order-service/api"
	"example.com/netdosuga/order-service/internal/observe"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/segmentio/kafka-go"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

//go:embed migrations/001_demo_receipts.sql
var migration string

//go:embed migrations/002_business.sql
var businessMigration string

type orderServer struct {
	api.UnimplementedOrderServiceServer
	business   *business
	paymentURL string
	httpClient *http.Client
	insert     func(context.Context, string, string) error
	publish    func(context.Context, []byte, []kafka.Header) error
	logger     *slog.Logger
}
type demoEvent struct {
	SchemaVersion int    `json:"schema_version"`
	Mock          bool   `json:"mock"`
	EventID       string `json:"event_id"`
	EventType     string `json:"event_type"`
	OrderID       string `json:"order_id"`
	Scenario      string `json:"scenario"`
}

func (s *orderServer) CreateDemoOrder(ctx context.Context, in *api.CreateDemoOrderRequest) (*api.CreateDemoOrderResponse, error) {
	switch in.Scenario {
	case "success", "error", "delay", "timeout":
	default:
		return nil, status.Error(codes.InvalidArgument, "unknown scenario")
	}
	fail := func(code codes.Code, message string, err error) (*api.CreateDemoOrderResponse, error) {
		observe.Error(ctx, err)
		sc := trace.SpanContextFromContext(ctx)
		s.logger.ErrorContext(ctx, "demo operation failed", "code", code.String(), "trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String())
		return nil, status.Error(code, message)
	}
	body, _ := json.Marshal(map[string]string{"scenario": in.Scenario})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.paymentURL+"/mock/payments", bytes.NewReader(body))
	if err != nil {
		return fail(codes.Internal, "payment request failed", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := s.httpClient.Do(request)
	if err != nil {
		code := codes.Unavailable
		var networkError net.Error
		if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &networkError) && networkError.Timeout() {
			code = codes.DeadlineExceeded
		}
		return fail(code, "mock payment unavailable", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fail(codes.Unavailable, "mock payment failed", fmt.Errorf("mock payment status %d", response.StatusCode))
	}
	var payment struct {
		Mock      bool   `json:"mock"`
		PaymentID string `json:"payment_id"`
		Status    string `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&payment); err != nil {
		return fail(codes.Unavailable, "invalid mock payment response", err)
	}
	if !payment.Mock || payment.PaymentID != "demo-payment-001" || payment.Status != "DEMO_SUCCEEDED" {
		return fail(codes.Unavailable, "invalid mock payment response", fmt.Errorf("unexpected payment fixture"))
	}
	eventID := uuid.NewString()
	dbCtx, dbSpan := otel.Tracer("order-service").Start(ctx, "INSERT demo_receipts", trace.WithSpanKind(trace.SpanKindClient))
	err = s.insert(dbCtx, eventID, in.Scenario)
	if err != nil {
		observe.Error(dbCtx, err)
	}
	dbSpan.End()
	if err != nil {
		return fail(codes.Unavailable, "demo receipt unavailable", err)
	}
	payload, _ := json.Marshal(demoEvent{1, true, eventID, "demo.order.created", "demo-order-001", in.Scenario})
	sendCtx, sendSpan := otel.Tracer("order-service").Start(ctx, "publish demo.order.created", trace.WithSpanKind(trace.SpanKindProducer))
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(sendCtx, carrier)
	headers := make([]kafka.Header, 0, len(carrier))
	for key, value := range carrier {
		headers = append(headers, kafka.Header{Key: key, Value: []byte(value)})
	}
	err = s.publish(sendCtx, payload, headers)
	if err != nil {
		observe.Error(sendCtx, err)
	}
	sendSpan.End()
	if err != nil {
		return fail(codes.Unavailable, "demo event unavailable", err)
	}
	observe.Log(ctx, s.logger, "demo receipt and event created", "event_id", eventID, "scenario", in.Scenario)
	return &api.CreateDemoOrderResponse{Mock: true, OrderId: "demo-order-001", Status: "DEMO_ACCEPTED", EventId: eventID, TraceId: trace.SpanContextFromContext(ctx).TraceID().String()}, nil
}

type healthServer struct {
	grpc_health_v1.UnimplementedHealthServer
	ready func(context.Context) error
}

func (h *healthServer) Check(ctx context.Context, in *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	if in.Service != "" && in.Service != api.OrderService_ServiceDesc.ServiceName {
		return nil, status.Error(codes.NotFound, "unknown service")
	}
	state := grpc_health_v1.HealthCheckResponse_SERVING
	if h.ready(ctx) != nil {
		state = grpc_health_v1.HealthCheckResponse_NOT_SERVING
	}
	return &grpc_health_v1.HealthCheckResponse{Status: state}, nil
}
func grpcMetrics() grpc.UnaryServerInterceptor {
	requests := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "netdosuga_grpc_requests_total", Help: "gRPC requests", ConstLabels: prometheus.Labels{"service": "order-service"}}, []string{"method", "code"})
	duration := prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "netdosuga_grpc_request_duration_seconds", Help: "gRPC duration", ConstLabels: prometheus.Labels{"service": "order-service"}}, []string{"method"})
	prometheus.MustRegister(requests, duration)
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		started := time.Now()
		result, err := handler(ctx, req)
		method := info.FullMethod
		if method != api.OrderService_CreateDemoOrder_FullMethodName && method != api.OrderService_Shop_FullMethodName && method != "/grpc.health.v1.Health/Check" {
			method = "other"
		}
		requests.WithLabelValues(method, status.Code(err).String()).Inc()
		duration.WithLabelValues(method).Observe(time.Since(started).Seconds())
		return result, err
	}
}
func run() error {
	logger := observe.Logger("order-service")
	if err := observe.DemoOnly(); err != nil {
		return err
	}
	timeout, err := observe.Duration("MOCK_HTTP_TIMEOUT", time.Second)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	shutdown, err := observe.Tracing(ctx, "order-service")
	if err != nil {
		return err
	}
	defer func() { c, stop := context.WithTimeout(context.Background(), 5*time.Second); defer stop(); shutdown(c) }()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("database configuration invalid")
	}
	defer pool.Close()
	startupCtx, stopStartup := context.WithTimeout(ctx, 10*time.Second)
	defer stopStartup()
	if err = migrate(startupCtx, pool); err != nil {
		return fmt.Errorf("demo migration failed: %w", err)
	}
	brokers := strings.Split(observe.Env("KAFKA_BROKERS", "kafka:9092"), ",")
	topic := observe.Env("KAFKA_TOPIC", "netdosuga.demo.orders.v1")
	mockURL := strings.TrimRight(observe.Env("MOCK_BASE_URL", "http://integration-mock:8083"), "/")
	writer := &kafka.Writer{Addr: kafka.TCP(brokers...), Topic: topic, Balancer: &kafka.LeastBytes{}, RequiredAcks: kafka.RequireAll, Async: false, WriteTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second, MaxAttempts: 1}
	defer writer.Close()
	client := &http.Client{Timeout: timeout, Transport: otelhttp.NewTransport(http.DefaultTransport)}
	ready := func(c context.Context) error {
		if err := pool.Ping(c); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(c, http.MethodGet, mockURL+"/readyz", nil)
		if err != nil {
			return err
		}
		res, err := client.Do(req)
		if err != nil {
			return err
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			return fmt.Errorf("mock not ready")
		}
		return nil
	}
	service := &orderServer{paymentURL: mockURL, httpClient: client, logger: logger, insert: func(c context.Context, id, scenario string) error {
		_, e := pool.Exec(c, "INSERT INTO demo_receipts(event_id,scenario) VALUES($1,$2)", id, scenario)
		return e
	}, publish: func(c context.Context, value []byte, headers []kafka.Header) error {
		return writer.WriteMessages(c, kafka.Message{Value: value, Headers: headers})
	}}
	businessWriter := &kafka.Writer{Addr: kafka.TCP(brokers...), Topic: observe.Env("BUSINESS_KAFKA_TOPIC", "netdosuga.business.events.v1"), RequiredAcks: kafka.RequireAll, WriteTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second, MaxAttempts: 1}
	defer businessWriter.Close()
	service.business = &business{db: pool, client: client, base: mockURL, writer: businessWriter}
	go service.business.outbox(ctx)
	rpc := grpc.NewServer(grpc.StatsHandler(otelgrpc.NewServerHandler()), grpc.UnaryInterceptor(grpcMetrics()))
	api.RegisterOrderServiceServer(rpc, service)
	grpc_health_v1.RegisterHealthServer(rpc, &healthServer{ready: ready})
	listener, err := net.Listen("tcp", observe.Env("GRPC_ADDR", ":50051"))
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	observe.Endpoints(mux, observe.NewHTTPMetrics("order-service"), ready)
	server := &http.Server{Addr: observe.Env("HTTP_ADDR", ":8081"), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	failures := make(chan error, 2)
	go func() { failures <- rpc.Serve(listener) }()
	go func() { failures <- server.ListenAndServe() }()
	logger.Info("listening", "grpc_address", listener.Addr().String(), "http_address", server.Addr)
	var serveError error
	select {
	case serveError = <-failures:
	case <-ctx.Done():
	}
	stopCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	done := make(chan struct{})
	go func() { rpc.GracefulStop(); close(done) }()
	select {
	case <-done:
	case <-stopCtx.Done():
		rpc.Stop()
	}
	server.Shutdown(stopCtx)
	if serveError != nil && !errors.Is(serveError, http.ErrServerClosed) && !errors.Is(serveError, grpc.ErrServerStopped) {
		return serveError
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		observe.Logger("order-service").Error("startup or runtime failed", "error", err.Error())
		os.Exit(1)
	}
}
