package observe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func Env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func DemoOnly() error {
	if os.Getenv("APP_ENV") != "local" || os.Getenv("MOCK_MODE") != "true" {
		return fmt.Errorf("demo requires APP_ENV=local and MOCK_MODE=true")
	}
	return nil
}
func Duration(key string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	d, e := time.ParseDuration(v)
	if e != nil || d <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", key)
	}
	return d, nil
}
func Logger(service string) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey {
			a.Key = "timestamp"
		}
		if a.Key == slog.MessageKey {
			a.Key = "message"
		}
		return a
	}})).With("service", service, "instance", Env("HOSTNAME", "local"))
}
func Log(ctx context.Context, logger *slog.Logger, message string, args ...any) {
	sc := trace.SpanContextFromContext(ctx)
	args = append(args, "trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String())
	logger.InfoContext(ctx, message, args...)
}
func Error(ctx context.Context, err error) {
	s := trace.SpanFromContext(ctx)
	s.RecordError(errors.New("operation failed"))
	s.SetStatus(codes.Error, "operation failed")
}
func Tracing(ctx context.Context, service string) (func(context.Context) error, error) {
	exporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpoint(Env("OTEL_EXPORTER_OTLP_ENDPOINT", "jaeger:4317")), otlptracegrpc.WithInsecure())
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", Env("OTEL_SERVICE_NAME", service)))))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	return tp.Shutdown, nil
}

type HTTPMetrics struct {
	Requests *prometheus.CounterVec
	Duration *prometheus.HistogramVec
}

func NewHTTPMetrics(service string) *HTTPMetrics {
	m := &HTTPMetrics{
		prometheus.NewCounterVec(prometheus.CounterOpts{Name: "netdosuga_http_requests_total", Help: "HTTP requests", ConstLabels: prometheus.Labels{"service": service}}, []string{"method", "route", "status"}),
		prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "netdosuga_http_request_duration_seconds", Help: "HTTP request duration", ConstLabels: prometheus.Labels{"service": service}}, []string{"method", "route"})}
	prometheus.MustRegister(m.Requests, m.Duration)
	return m
}

type response struct {
	http.ResponseWriter
	status int
}

func (w *response) WriteHeader(status int) { w.status = status; w.ResponseWriter.WriteHeader(status) }
func (m *HTTPMetrics) Wrap(route string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		rw := &response{w, 200}
		h.ServeHTTP(rw, r)
		method := r.Method
		if method != "GET" && method != "POST" {
			method = "OTHER"
		}
		m.Requests.WithLabelValues(method, route, strconv.Itoa(rw.status)).Inc()
		m.Duration.WithLabelValues(method, route).Observe(time.Since(started).Seconds())
	})
}
func Endpoints(mux *http.ServeMux, metrics *HTTPMetrics, ready func(context.Context) error) {
	mux.Handle("GET /healthz", metrics.Wrap("/healthz", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })))
	mux.Handle("GET /readyz", metrics.Wrap("/readyz", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if ready(ctx) != nil {
			http.Error(w, "dependencies unavailable", 503)
			return
		}
		w.Write([]byte("ready\n"))
	})))
	mux.Handle("GET /metrics", promhttp.Handler())
}
