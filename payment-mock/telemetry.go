package main

import (
	"context"
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

func env(key, fallback string) string {
	if s := os.Getenv(key); s != "" {
		return s
	}
	return fallback
}
func configure(ctx context.Context, service string) (*sdktrace.TracerProvider, error) {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey {
			a.Key = "timestamp"
		}
		if a.Key == slog.MessageKey {
			a.Key = "message"
		}
		return a
	}})).With("service", service, "instance", env("HOSTNAME", "payment-mock")))
	if os.Getenv("APP_ENV") != "local" || os.Getenv("MOCK_MODE") != "true" {
		return nil, fmt.Errorf("APP_ENV=local and MOCK_MODE=true are required")
	}
	exporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpoint(env("OTEL_EXPORTER_OTLP_ENDPOINT", "jaeger:4317")), otlptracegrpc.WithInsecure())
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(resource.NewWithAttributes("", attribute.String("service.name", env("OTEL_SERVICE_NAME", service)))))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return tp, nil
}
func logOperation(ctx context.Context, message string) {
	sc := trace.SpanContextFromContext(ctx)
	slog.Info(message, "trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String())
}

type telemetry struct {
	registry *prometheus.Registry
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

func newTelemetry(service string) *telemetry {
	r := prometheus.NewRegistry()
	t := &telemetry{registry: r, requests: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "netdosuga_http_requests_total", ConstLabels: prometheus.Labels{"service": service}}, []string{"method", "route", "status"}), duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "netdosuga_http_request_duration_seconds", ConstLabels: prometheus.Labels{"service": service}}, []string{"method", "route"})}
	r.MustRegister(businessOperations, businessDuration, t.requests, t.duration, prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	return t
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(s int) { w.status = s; w.ResponseWriter.WriteHeader(s) }
func (t *telemetry) wrap(route string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		method := r.Method
		if method != "GET" && method != "POST" {
			method = "OTHER"
		}
		start := time.Now()
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := otel.Tracer("http").Start(ctx, r.Method+" "+route, trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()
		sw := &statusWriter{ResponseWriter: w, status: 200}
		h(sw, r.WithContext(ctx))
		if sw.status >= 400 {
			span.SetStatus(codes.Error, http.StatusText(sw.status))
		}
		t.requests.WithLabelValues(method, route, strconv.Itoa(sw.status)).Inc()
		t.duration.WithLabelValues(method, route).Observe(time.Since(start).Seconds())
		if sw.status >= 500 {
			sc := trace.SpanContextFromContext(ctx)
			slog.ErrorContext(ctx, "http request failed", "status", sw.status, "trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String())
		} else {
			logOperation(ctx, "http request completed")
		}
	}
}
func (t *telemetry) metrics() http.Handler {
	return promhttp.HandlerFor(t.registry, promhttp.HandlerOpts{})
}
