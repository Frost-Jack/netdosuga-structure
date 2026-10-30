package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	}})).With("service", service, "instance", env("HOSTNAME", "mail-mock")))
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
	r.MustRegister(t.requests, t.duration, prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	return t
}

type statusWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.body.Len() < 4096 {
		remaining := 4096 - w.body.Len()
		if remaining > len(p) {
			remaining = len(p)
		}
		w.body.Write(p[:remaining])
	}
	return w.ResponseWriter.Write(p)
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
		var payload any
		if r.Body != nil && r.Method == "POST" {
			raw, _ := io.ReadAll(io.LimitReader(r.Body, 4097))
			r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(raw), r.Body))
			if len(raw) <= 4096 {
				json.Unmarshal(raw, &payload)
			}
		}
		sw := &statusWriter{ResponseWriter: w, status: 200}
		h(sw, r.WithContext(ctx))
		var response any
		json.Unmarshal(sw.body.Bytes(), &response)
		requestJSON, _ := diagnosticPayload(payload)
		responseJSON, _ := diagnosticPayload(response)
		span.SetAttributes(attribute.String("app.request", string(requestJSON)), attribute.String("app.response", string(responseJSON)))
		sc := trace.SpanContextFromContext(ctx)
		slog.InfoContext(ctx, "sanitized exchange", "operation", route, "method", method, "route", route, "status", sw.status, "duration_ms", time.Since(start).Milliseconds(), "request", json.RawMessage(requestJSON), "response", json.RawMessage(responseJSON), "trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String())
		if sw.status >= 400 {
			span.SetStatus(codes.Error, http.StatusText(sw.status))
		}
		t.requests.WithLabelValues(method, route, strconv.Itoa(sw.status)).Inc()
		t.duration.WithLabelValues(method, route).Observe(time.Since(start).Seconds())
		if sw.status >= 500 {
			sc := trace.SpanContextFromContext(ctx)
			slog.ErrorContext(ctx, "http request failed", "status", sw.status, "trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String())
		} else {
			sc := trace.SpanContextFromContext(ctx)
			slog.InfoContext(ctx, "http request completed", "operation", route, "method", method, "route", route, "status", sw.status, "duration_ms", time.Since(start).Milliseconds(), "trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String())
		}
	}
}
func (t *telemetry) metrics() http.Handler {
	return promhttp.HandlerFor(t.registry, promhttp.HandlerOpts{})
}

// Only fixed machine fields survive; free text, headers and credentials never enter diagnostics.
func sanitize(value any, depth int) any {
	if depth > 4 {
		return nil
	}
	switch v := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, item := range v {
			switch k {
			case "mock", "demo", "scenario", "order_id", "offer_id", "event_type", "quantity", "amount_cents", "status", "count", "deliveries", "items":
				out[k] = sanitize(item, depth+1)
			}
		}
		return out
	case []any:
		out := []any{}
		for i, item := range v {
			if i == 3 {
				break
			}
			out = append(out, sanitize(item, depth+1))
		}
		return out
	case string:
		if len(v) > 128 {
			return v[:128]
		}
		return v
	case float64, bool:
		return v
	}
	return nil
}

func diagnosticPayload(value any) ([]byte, error) {
	payload, err := json.Marshal(sanitize(value, 0))
	if len(payload) > 2048 {
		return []byte(`{"truncated":true}`), nil
	}
	return payload, err
}
