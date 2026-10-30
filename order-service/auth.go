package main

import (
	"context"
	"encoding/json"
	"example.com/netdosuga/order-service/api"
	"example.com/netdosuga/order-service/internal/observe"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"google.golang.org/grpc/metadata"
	"io"
	"net/http"
	"strings"
	"time"
)

func (s *orderServer) resolveActor(ctx context.Context, r *api.ShopRequest) (string, string, error) {
	ctx, span := otel.Tracer("orders-service").Start(ctx, "authentication.resolve")
	defer span.End()
	md, _ := metadata.FromIncomingContext(ctx)
	values := md.Get("authorization")
	if len(values) != 1 || len(values[0]) > 256 || !strings.HasPrefix(values[0], "Bearer ") {
		return "", "", problem{401, "UNAUTHENTICATED"}
	}
	url := strings.TrimRight(s.identityURL, "/")
	if !strings.HasSuffix(url, "/v1/session") {
		url += "/v1/session"
	}
	request, e := http.NewRequestWithContext(ctx, "GET", url, nil)
	if e != nil {
		return "", "", problem{503, "IDENTITY_UNAVAILABLE"}
	}
	request.Header.Set("Authorization", values[0])
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(request.Header))
	client := s.httpClient
	if client == nil {
		client = &http.Client{Timeout: time.Second}
	}
	res, e := client.Do(request)
	if e != nil {
		return "", "", problem{503, "IDENTITY_UNAVAILABLE"}
	}
	defer res.Body.Close()
	if res.StatusCode == 401 {
		return "", "", problem{401, "UNAUTHENTICATED"}
	}
	if res.StatusCode != 200 {
		return "", "", problem{503, "IDENTITY_UNAVAILABLE"}
	}
	var body struct {
		User struct {
			ID   string `json:"id"`
			Role string `json:"role"`
		}
		Expires time.Time `json:"expires_at"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&body) != nil || body.User.ID == "" || !body.Expires.After(time.Now()) {
		return "", "", problem{401, "UNAUTHENTICATED"}
	}
	switch body.User.Role {
	case "customer", "vendor", "admin":
		return body.User.ID, body.User.Role, nil
	}
	return "", "", problem{403, "FORBIDDEN"}
}

var knownOperations = map[string]bool{"catalog.list": true, "applications.list": true, "applications.create": true, "applications.approve": true, "applications.reject": true, "offers.list": true, "offers.update": true, "offers.publish": true, "cart.get": true, "cart.add": true, "cart.remove": true, "orders.create": true, "orders.list": true, "orders.get": true, "orders.pay": true, "orders.cancel": true, "tickets.redeem": true, "sales.get": true}

func fixedOperation(op string) string {
	if knownOperations[op] {
		return op
	}
	return "unknown"
}
func diagnosticRequest(r *api.ShopRequest) any {
	var body map[string]any
	if len(r.BodyJson) <= 16384 {
		json.Unmarshal([]byte(r.BodyJson), &body)
	}
	if body == nil {
		body = map[string]any{}
	}
	body["operation"] = fixedOperation(r.Operation)
	return observe.Safe(body)
}
