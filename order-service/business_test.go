package main

import (
	"context"
	"encoding/json"
	"example.com/netdosuga/order-service/api"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestShopErrorMarksSpan(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	defer provider.Shutdown(context.Background())
	ctx, span := provider.Tracer("test").Start(context.Background(), "Shop")
	s := &orderServer{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	reply, err := s.Shop(ctx, &api.ShopRequest{Actor: "unsupported", Operation: "orders.list"})
	span.End()
	if err != nil || reply.Status != 401 {
		t.Fatal(reply, err)
	}
	ended := recorder.Ended()
	if len(ended) != 1 || ended[0].Status().Code != codes.Error {
		t.Fatal("HTTP business error did not mark gRPC span")
	}
}

func TestBusinessIdentityAllowlist(t *testing.T) {
	s := &orderServer{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, actor := range []string{"root", "vendor-other", "customer", "admin"} {
		r, e := s.Shop(context.Background(), &api.ShopRequest{Actor: actor, Operation: "orders.list"})
		if e != nil || r.Status != 401 {
			t.Fatalf("%s: %v %v", actor, r, e)
		}
	}
	for _, actor := range []string{"customer-demo", "customer-two", "vendor-demo", "vendor-two", "admin-demo"} {
		if actorRole(actor) == "" {
			t.Fatal(actor)
		}
	}
}
func TestBusinessMalformedBody(t *testing.T) {
	for _, raw := range []string{"[]", "null", "{", "{} {}"} {
		if _, e := parseBody(raw); e == nil {
			t.Fatal(raw)
		}
	}
	m, e := parseBody(`{"quantity":1.5}`)
	if e != nil || number(m, "quantity") != -1 {
		t.Fatal("fraction accepted")
	}
}
func TestPublicCatalogPassesIdentity(t *testing.T) {
	s := &orderServer{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	r, _ := s.Shop(context.Background(), &api.ShopRequest{Operation: "catalog.list"})
	if r.Status == 403 {
		t.Fatal("public catalog requires identity")
	}
	var body map[string]any
	if json.Unmarshal([]byte(r.BodyJson), &body) != nil || body["demo"] != true {
		t.Fatal(r)
	}
}
func TestMockFailureScenarios(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer server.Close()
	b := &business{base: server.URL, client: server.Client()}
	for _, p := range []string{"/mock/payments", "/mock/refunds"} {
		if e := b.mock(context.Background(), p, "error"); e == nil {
			t.Fatal("failed mock accepted")
		}
	}
	if e := b.mock(context.Background(), "/mock/payments", "unknown"); e == nil {
		t.Fatal("unknown scenario accepted")
	}
}
