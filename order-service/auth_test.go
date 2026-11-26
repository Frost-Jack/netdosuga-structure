package main

import (
	"context"
	"encoding/json"
	"example.com/netdosuga/order-service/api"
	"google.golang.org/grpc/metadata"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestActorSpoofingRequiresSession(t *testing.T) {
	s := &orderServer{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, actor := range []string{"admin-demo", "customer-demo", "vendor-demo"} {
		r, _ := s.Shop(context.Background(), &api.ShopRequest{Operation: "orders.list", Actor: actor})
		if r.Status != 401 {
			t.Fatalf("spoof %s: %d", actor, r.Status)
		}
	}
}

func TestResolveFailsClosedOnIdentityErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   problem
	}{
		{"revoked session", 401, "", problem{401, "UNAUTHENTICATED"}},
		{"upstream unavailable", 503, "", problem{503, "IDENTITY_UNAVAILABLE"}},
		{"malformed response", 200, `{"user":`, problem{401, "UNAUTHENTICATED"}},
		{"expired session", 200, `{"user":{"id":"buyer","role":"customer"},"expires_at":"2000-01-01T00:00:00Z"}`, problem{401, "UNAUTHENTICATED"}},
		{"unknown role", 200, `{"user":{"id":"buyer","role":"unknown"},"expires_at":"2100-01-01T00:00:00Z"}`, problem{403, "FORBIDDEN"}},
		{"invalid URL", 0, "", problem{503, "IDENTITY_UNAVAILABLE"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			base := server.URL
			if tc.status == 0 {
				base = "://invalid"
			}
			s := &orderServer{identityURL: base}
			ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer fixture"))
			id, role, err := s.resolveActor(ctx, &api.ShopRequest{})
			if id != "" || role != "" || err != tc.want {
				t.Fatalf("unexpected identity result: id=%q role=%q err=%v", id, role, err)
			}
		})
	}
}
func TestResolveUsesIdentityRoleAndActor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer opaque" {
			t.Error("missing bearer")
		}
		json.NewEncoder(w).Encode(map[string]any{"user": map[string]string{"id": "new-customer", "role": "customer"}, "expires_at": time.Now().Add(time.Hour)})
	}))
	defer server.Close()
	s := &orderServer{identityURL: server.URL, httpClient: server.Client()}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer opaque"))
	actor, role, e := s.resolveActor(ctx, &api.ShopRequest{Actor: "admin-demo"})
	if e != nil || actor != "new-customer" || role != "customer" {
		t.Fatal(actor, role, e)
	}
}
func TestPaymentUsesStableProviderKey(t *testing.T) {
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if r.Header.Get("Idempotency-Key") != "payment:order-1" {
			t.Error("unstable key")
		}
		json.NewEncoder(w).Encode(map[string]any{"mock": true, "payment_id": "p-1", "order_id": "order-1", "amount_cents": 123, "status": "SUCCEEDED"})
	}))
	defer server.Close()
	b := &business{base: server.URL, client: server.Client()}
	for n := 0; n < 2; n++ {
		id, e := b.payment(context.Background(), "payments", "order-1", 123, "success")
		if e != nil || id != "p-1" {
			t.Fatal(id, e)
		}
	}
	if count != 2 {
		t.Fatal(count)
	}
}
func TestRefundFailureCertainty(t *testing.T) {
	for _, code := range []string{"MOCK_PROVIDER_ERROR", "DATABASE_UNAVAILABLE"} {
		t.Run(code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(503)
				json.NewEncoder(w).Encode(map[string]string{"code": code})
			}))
			defer server.Close()
			b := &business{base: server.URL, client: server.Client()}
			_, err := b.payment(context.Background(), "refunds", "order-1", 123, "error")
			p, ok := err.(problem)
			if !ok {
				t.Fatal(err)
			}
			if (p.code == "MOCK_DECLINED") != (code == "MOCK_PROVIDER_ERROR") {
				t.Fatal("ambiguous failure classified definite", p)
			}
		})
	}
}
