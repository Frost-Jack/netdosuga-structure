package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"example.com/netdosuga/gateway/internal/observe"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// mutationGuard rejects explicit cross-site browser requests. Requests without an
// Origin remain available to local CLI clients, which do not attach browser cookies.
func mutationGuard(r *http.Request) bool {
	if r.Method == http.MethodGet {
		return true
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		if err != nil || u.Host != r.Host || u.Scheme != scheme || u.Path != "" || u.RawQuery != "" {
			return false
		}
	}
	if r.ContentLength == 0 {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json"
}

func (g *gateway) auth(operation string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := otel.Tracer("api-gateway").Start(ctx, "auth."+operation, trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()
		started := time.Now()
		responseStatus := 200
		defer func() {
			if responseStatus >= 400 {
				observe.Error(ctx, errors.New("authentication request rejected"))
			}
			g.recordShop(ctx, "auth."+operation, r.Method, "/api/v1/auth/"+operation, responseStatus, started, nil, nil)
		}()
		hostname, _ := os.Hostname()
		w.Header().Set("X-Gateway-Instance", hostname)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		fail := func(status int, message string) {
			responseStatus = status
			w.WriteHeader(status)
			json.NewEncoder(w).Encode(map[string]any{"message": message, "trace_id": span.SpanContext().TraceID().String()})
		}
		if !mutationGuard(r) {
			fail(403, "Запрос с другого сайта запрещён")
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8192))
		if err != nil {
			fail(400, "Слишком большой запрос")
			return
		}
		target := strings.TrimRight(observe.Env("IDENTITY_BASE_URL", "http://identity-service:8084"), "/") + "/v1/" + operation
		request, err := http.NewRequestWithContext(ctx, r.Method, target, bytes.NewReader(raw))
		if err != nil {
			fail(502, "Сервис входа недоступен")
			return
		}
		request.Header.Set("Content-Type", "application/json")
		if cookie, err := r.Cookie("nd_session"); err == nil {
			request.Header.Set("Authorization", "Bearer "+cookie.Value)
		}
		otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(request.Header))
		response, err := (&http.Client{Timeout: 4 * time.Second}).Do(request)
		if err != nil {
			fail(502, "Сервис входа временно недоступен")
			return
		}
		defer response.Body.Close()
		if operation == "logout" {
			http.SetCookie(w, &http.Cookie{Name: "nd_session", Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1, Secure: observe.Env("COOKIE_SECURE", "false") == "true"})
		}
		responseStatus = response.StatusCode
		if response.StatusCode == 204 {
			w.WriteHeader(204)
			return
		}
		var data map[string]any
		if json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&data) != nil || data == nil {
			fail(502, "Некорректный ответ сервиса входа")
			return
		}
		if response.StatusCode < 300 && (operation == "login" || operation == "register") {
			token, _ := data["session_token"].(string)
			expires, _ := data["expires_at"].(string)
			expiry, _ := time.Parse(time.RFC3339, expires)
			if token == "" || expiry.IsZero() {
				fail(502, "Сервис не создал сессию")
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "nd_session", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: max(1, int(time.Until(expiry).Seconds())), Expires: expiry, Secure: observe.Env("COOKIE_SECURE", "false") == "true"})
		}
		delete(data, "session_token")
		data["trace_id"] = span.SpanContext().TraceID().String()
		w.WriteHeader(response.StatusCode)
		json.NewEncoder(w).Encode(data)
	}
}
