package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"example.com/netdosuga/gateway/api"
	"example.com/netdosuga/gateway/internal/observe"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

//go:embed web/*
var webFiles embed.FS

var shopRoutes = map[string]string{
	"GET /api/v1/shop/catalog":                    "catalog.list",
	"GET /api/v1/shop/applications":               "applications.list",
	"POST /api/v1/shop/applications":              "applications.create",
	"POST /api/v1/shop/applications/{id}/approve": "applications.approve",
	"POST /api/v1/shop/applications/{id}/reject":  "applications.reject",
	"GET /api/v1/shop/offers":                     "offers.list",
	"PATCH /api/v1/shop/offers/{id}":              "offers.update",
	"POST /api/v1/shop/offers/{id}/publish":       "offers.publish",
	"GET /api/v1/shop/cart":                       "cart.get",
	"POST /api/v1/shop/cart/items":                "cart.add",
	"DELETE /api/v1/shop/cart/items/{id}":         "cart.remove",
	"POST /api/v1/shop/orders":                    "orders.create",
	"GET /api/v1/shop/orders":                     "orders.list",
	"GET /api/v1/shop/orders/{id}":                "orders.get",
	"POST /api/v1/shop/orders/{id}/pay":           "orders.pay",
	"POST /api/v1/shop/orders/{id}/cancel":        "orders.cancel",
	"POST /api/v1/shop/tickets/{id}/redeem":       "tickets.redeem",
	"GET /api/v1/shop/sales":                      "sales.get",
}

func (g *gateway) shop(operation string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := otel.Tracer("gateway").Start(ctx, r.Pattern, trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()
		traceID := span.SpanContext().TraceID().String()
		w.Header().Set("Content-Type", "application/json")
		hostname, _ := os.Hostname()
		w.Header().Set("X-Gateway-Instance", hostname)
		fail := func(httpCode int, code, message string) {
			observe.Error(ctx, errors.New(message))
			w.WriteHeader(httpCode)
			json.NewEncoder(w).Encode(map[string]any{"demo": true, "code": code, "message": message, "trace_id": traceID})
		}
		body := map[string]any{}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 65536))
		if err != nil {
			fail(400, "INVALID_ARGUMENT", "request body exceeds limit")
			return
		}
		if len(strings.TrimSpace(string(raw))) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil || body == nil {
				fail(400, "INVALID_ARGUMENT", "expected one JSON object")
				return
			}
		}
		if operation == "catalog.list" {
			for _, key := range []string{"city", "category"} {
				if value := r.URL.Query().Get(key); value != "" {
					body[key] = value
				}
			}
		}
		encoded, _ := json.Marshal(body)
		// Business transactions include the payment mock's own bounded deadline.
		deadline := g.timeout
		if deadline < 5*time.Second {
			deadline = 5 * time.Second
		}
		callCtx, cancel := context.WithTimeout(ctx, deadline)
		defer cancel()
		result, err := g.client.Shop(callCtx, &api.ShopRequest{Operation: operation, Actor: r.Header.Get("X-Demo-Actor"), Id: r.PathValue("id"), BodyJson: string(encoded), IdempotencyKey: r.Header.Get("Idempotency-Key")})
		if err != nil {
			if status.Code(err) == codes.DeadlineExceeded {
				fail(504, "DEADLINE_EXCEEDED", "business dependency timed out")
			} else {
				fail(502, "DEPENDENCY_ERROR", "business dependency failed")
			}
			return
		}
		var response map[string]any
		if result.Status < 200 || result.Status > 599 || json.Unmarshal([]byte(result.BodyJson), &response) != nil || response == nil {
			fail(502, "DEPENDENCY_ERROR", "invalid business response")
			return
		}
		response["demo"], response["trace_id"] = true, traceID
		w.Header().Set("X-Order-Instance", result.InstanceId)
		if result.Status >= 400 {
			observe.Error(ctx, errors.New("business operation rejected"))
		}
		w.WriteHeader(int(result.Status))
		json.NewEncoder(w).Encode(response)
	}
}

func (g *gateway) registerShop(mux *http.ServeMux, metrics *observe.HTTPMetrics) {
	for route, operation := range shopRoutes {
		mux.Handle(route, metrics.Wrap(strings.SplitN(route, " ", 2)[1], g.shop(operation)))
	}
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		data, _ := webFiles.ReadFile("web/index.html")
		w.Write(data)
	})
	for _, name := range []string{"app.js", "style.css"} {
		mux.HandleFunc("GET /"+name, func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(name, ".js") {
				w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			} else {
				w.Header().Set("Content-Type", "text/css; charset=utf-8")
			}
			data, _ := webFiles.ReadFile("web/" + name)
			w.Write(data)
		})
	}
}
