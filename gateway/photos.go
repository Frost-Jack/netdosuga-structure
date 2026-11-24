package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"example.com/netdosuga/gateway/api"
	"example.com/netdosuga/gateway/internal/observe"
	"fmt"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/metadata"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxPhotoBytes = 2 << 20

func (g *gateway) uploadPhoto(w http.ResponseWriter, r *http.Request) {
	ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
	ctx, span := otel.Tracer("gateway").Start(ctx, r.Pattern, trace.WithSpanKind(trace.SpanKindServer))
	defer span.End()
	traceID := span.SpanContext().TraceID().String()
	w.Header().Set("Content-Type", "application/json")
	fail := func(code int, message string) {
		observe.Error(ctx, errors.New(message))
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]any{"demo": true, "code": message, "message": message, "trace_id": traceID})
	}
	if !mutationGuard(r) {
		fail(http.StatusForbidden, "FORBIDDEN")
		return
	}

	id := r.PathValue("id")
	if id == "" {
		fail(http.StatusBadRequest, "INVALID_ARGUMENT")
		return
	}
	if !g.canUploadPhoto(ctx, r, id) {
		fail(http.StatusForbidden, "FORBIDDEN")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxPhotoBytes+64<<10)
	if err := r.ParseMultipartForm(maxPhotoBytes); err != nil {
		fail(http.StatusBadRequest, "INVALID_PHOTO")
		return
	}
	file, header, err := r.FormFile("photo")
	if err != nil {
		fail(http.StatusBadRequest, "INVALID_PHOTO")
		return
	}
	defer file.Close()
	if header.Size > maxPhotoBytes {
		fail(http.StatusBadRequest, "PHOTO_TOO_LARGE")
		return
	}

	prefix := make([]byte, 8)
	if _, err := io.ReadFull(file, prefix); err != nil {
		fail(http.StatusBadRequest, "INVALID_PHOTO")
		return
	}
	ext := ""
	switch {
	case prefix[0] == 0xFF && prefix[1] == 0xD8 && prefix[2] == 0xFF:
		ext = ".jpg"
	case bytes.Equal(prefix[:8], []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		ext = ".png"
	default:
		fail(http.StatusBadRequest, "INVALID_PHOTO")
		return
	}

	photoDir := observe.Env("PHOTO_DIR", "./photos")
	if err := os.MkdirAll(photoDir, 0o755); err != nil {
		fail(http.StatusInternalServerError, "PHOTO_STORAGE_UNAVAILABLE")
		return
	}
	safeID := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, id)
	filename := fmt.Sprintf("%s-%d%s", safeID, time.Now().UnixNano(), ext)
	target := filepath.Join(photoDir, filename)
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		fail(http.StatusInternalServerError, "PHOTO_STORAGE_UNAVAILABLE")
		return
	}
	if _, err := io.Copy(output, io.MultiReader(bytes.NewReader(prefix), file)); err != nil {
		output.Close()
		os.Remove(target)
		fail(http.StatusInternalServerError, "PHOTO_STORAGE_UNAVAILABLE")
		return
	}
	if err := output.Close(); err != nil {
		os.Remove(target)
		fail(http.StatusInternalServerError, "PHOTO_STORAGE_UNAVAILABLE")
		return
	}

	photoPath := "/photos/" + filename
	if !g.bindPhoto(ctx, id, photoPath) {
		os.Remove(target)
		fail(http.StatusBadGateway, "DEPENDENCY_ERROR")
		return
	}
	observe.Log(ctx, g.logger, "offer photo uploaded", "offer_id", id, "photo_path", photoPath)
	json.NewEncoder(w).Encode(map[string]any{"demo": true, "photo_path": photoPath, "trace_id": traceID})
}

func (g *gateway) canUploadPhoto(ctx context.Context, r *http.Request, offerID string) bool {
	callCtx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	if cookie, err := r.Cookie("nd_session"); err == nil {
		callCtx = metadataAppend(callCtx, "Bearer "+cookie.Value)
	}
	if response, err := g.client.Shop(callCtx, &api.ShopRequest{Operation: "admin.vendors"}); err == nil && response.Status < 400 {
		return true
	}
	response, err := g.client.Shop(callCtx, &api.ShopRequest{Operation: "offers.list"})
	if err != nil || response.Status >= 400 {
		return false
	}
	var body struct {
		Offers []struct {
			ID string `json:"id"`
		} `json:"offers"`
	}
	if json.Unmarshal([]byte(response.BodyJson), &body) != nil {
		return false
	}
	for _, offer := range body.Offers {
		if offer.ID == offerID {
			return true
		}
	}
	return false
}

func (g *gateway) bindPhoto(ctx context.Context, offerID, photoPath string) bool {
	token := observe.Env("COMMERCE_INTERNAL_TOKEN", "")
	if token == "" {
		return false
	}
	base := strings.TrimRight(observe.Env("COMMERCE_BASE_URL", "http://commerce-service:8087"), "/")
	body, _ := json.Marshal(map[string]string{"photo_path": photoPath})
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, base+"/internal/offers/"+url.PathEscape(offerID)+"/photo", bytes.NewReader(body))
	if err != nil {
		return false
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Internal-Token", token)
	response, err := g.httpClient.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusNoContent
}

func metadataAppend(ctx context.Context, authorization string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", authorization)
}
