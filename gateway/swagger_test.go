package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestSwaggerRoutes(t *testing.T) {
	mux := http.NewServeMux()
	registerSwagger(mux)
	for _, tc := range []struct{ path, contentType string }{
		{"/swagger/", "text/html; charset=utf-8"},
		{"/swagger/swagger-ui-bundle.js", "text/javascript; charset=utf-8"},
		{"/swagger/swagger-ui.css", "text/css; charset=utf-8"},
		{"/openapi.yaml", "application/json"},
		{"/swagger/LICENSE", "text/plain; charset=utf-8"},
		{"/swagger/NOTICE", "text/plain; charset=utf-8"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if w.Code != http.StatusOK || w.Header().Get("Content-Type") != tc.contentType || w.Body.Len() == 0 {
				t.Fatalf("GET: status=%d content-type=%q bytes=%d", w.Code, w.Header().Get("Content-Type"), w.Body.Len())
			}
			if tc.path == "/openapi.yaml" {
				canonical, err := os.ReadFile("api/openapi.yaml")
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(w.Body.Bytes(), canonical) {
					t.Fatal("served contract differs from canonical source")
				}
			}
			w = httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, tc.path, nil))
			if w.Code != http.StatusMethodNotAllowed {
				t.Fatalf("POST: status=%d", w.Code)
			}
		})
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/swagger", nil))
	if w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != "/swagger/" {
		t.Fatalf("redirect: status=%d location=%q", w.Code, w.Header().Get("Location"))
	}
	for _, path := range []string{"/swagger/missing", "/swagger/vendor/LICENSE", "/api/v1/unknown"} {
		w = httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s: status=%d", path, w.Code)
		}
	}
}

func TestSwaggerLocalConfiguration(t *testing.T) {
	page, err := swaggerFiles.ReadFile("swagger/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{`url: "/openapi.yaml"`, "validatorUrl: null", "persistAuthorization: false", "withCredentials: true", `src="/swagger/swagger-ui-bundle.js"`, `href="/swagger/swagger-ui.css"`} {
		if !strings.Contains(string(page), required) {
			t.Errorf("missing configuration %q", required)
		}
	}
	for _, external := range []string{"https://", "http://", "requestInterceptor", "localStorage"} {
		if strings.Contains(string(page), external) {
			t.Errorf("unexpected external dependency or credential handling: %q", external)
		}
	}
}
