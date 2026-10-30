package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"example.com/netdosuga/gateway/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type shopClient struct {
	api.OrderServiceClient
	request       *api.ShopRequest
	authorization string
	result        *api.ShopResponse
	err           error
}

func (c *shopClient) Shop(ctx context.Context, r *api.ShopRequest, opts ...grpc.CallOption) (*api.ShopResponse, error) {
	c.request = r
	md, _ := metadata.FromOutgoingContext(ctx)
	if values := md.Get("authorization"); len(values) > 0 {
		c.authorization = values[0]
	}
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) < 4*time.Second {
		return nil, status.Error(codes.Internal, "missing transaction deadline")
	}
	return c.result, c.err
}
func TestShopMapping(t *testing.T) {
	client := &shopClient{result: &api.ShopResponse{Status: 201, BodyJson: `{"order":{"id":"order-1"}}`, InstanceId: "order-b"}}
	g := &gateway{client: client, timeout: time.Second}
	mux := http.NewServeMux()
	for route, operation := range shopRoutes {
		mux.HandleFunc(route, g.shop(operation))
	}
	for route, operation := range shopRoutes {
		parts := strings.SplitN(route, " ", 2)
		path := strings.ReplaceAll(parts[1], "{id}", "opaque-123")
		if operation == "catalog.list" {
			path += "?city=Москва&category=sport&actor=admin-demo&operation=evil"
		}
		req := httptest.NewRequest(parts[0], path, strings.NewReader(`{"scenario":"delay"}`))
		req.Header.Set("X-Demo-Actor", "customer-demo")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "key-1")
		req.AddCookie(&http.Cookie{Name: "nd_session", Value: "private-session"})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != 201 || client.request.Operation != operation || client.request.Actor != "" || client.authorization != "Bearer private-session" || client.request.IdempotencyKey != "key-1" {
			t.Fatalf("route %s: %d %+v", route, w.Code, client.request)
		}
		if strings.Contains(route, "{id}") && client.request.Id != "opaque-123" {
			t.Fatalf("lost ID: %+v", client.request)
		}
		if operation == "catalog.list" {
			var body map[string]any
			json.Unmarshal([]byte(client.request.BodyJson), &body)
			if body["city"] != "Москва" || body["category"] != "sport" || body["actor"] != nil || body["operation"] != nil {
				t.Fatalf("query allowlist: %v", body)
			}
		}
		var response map[string]any
		json.Unmarshal(w.Body.Bytes(), &response)
		if response["demo"] != true || response["trace_id"] == nil || w.Header().Get("X-Gateway-Instance") == "" || w.Header().Get("X-Order-Instance") != "order-b" {
			t.Fatalf("envelope %v headers %v", response, w.Header())
		}
	}
	req := httptest.NewRequest("POST", "/api/v1/shop/evil", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 404 {
		t.Fatalf("unknown operation allowed: %d", w.Code)
	}
}
func TestShopErrors(t *testing.T) {
	for _, test := range []struct {
		name, body string
		result     *api.ShopResponse
		err        error
		want       int
	}{
		{name: "domain conflict", body: `{}`, result: &api.ShopResponse{Status: 409, BodyJson: `{"code":"INVALID_STATE","message":"invalid state"}`}, want: 409},
		{name: "bad status", body: `{}`, result: &api.ShopResponse{Status: 999, BodyJson: `{}`}, want: 502},
		{name: "bad response", body: `{}`, result: &api.ShopResponse{Status: 200, BodyJson: `null`}, want: 502},
		{name: "deadline", body: `{}`, err: status.Error(codes.DeadlineExceeded, "timeout"), want: 504},
		{name: "offline", body: `{}`, err: status.Error(codes.Unavailable, "offline"), want: 502},
		{name: "non object", body: `[]`, want: 400},
		{name: "null", body: `null`, want: 400},
		{name: "trailing json", body: `{} {}`, want: 400},
		{name: "oversized", body: strings.Repeat("x", 65537), want: 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &shopClient{result: test.result, err: test.err}
			g := &gateway{client: client}
			w := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/", strings.NewReader(test.body))
			req.Header.Set("Content-Type", "application/json")
			g.shop("orders.create")(w, req)
			if w.Code != test.want {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
			var body map[string]any
			if json.Unmarshal(w.Body.Bytes(), &body) != nil || body["demo"] != true || body["trace_id"] == nil {
				t.Fatalf("invalid error envelope: %s", w.Body.String())
			}
		})
	}
}

func TestEmbeddedUI(t *testing.T) {
	for _, name := range []string{"web/index.html", "web/app.js", "web/style.css"} {
		data, err := webFiles.ReadFile(name)
		if err != nil || len(data) == 0 {
			t.Fatalf("asset %s: %v", name, err)
		}
	}
	script, _ := webFiles.ReadFile("web/app.js")
	if strings.Contains(string(script), "innerHTML") {
		t.Fatal("user-controlled business fields must use textContent")
	}
	if !strings.Contains(string(script), `order.status === "RESERVED"`) {
		t.Fatal("payment must be available for reserved orders")
	}
}
