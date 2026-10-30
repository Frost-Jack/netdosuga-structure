package main

import (
	"embed"
	"net/http"
)

//go:embed api/openapi.yaml swagger/index.html swagger/vendor/swagger-ui-bundle.js swagger/vendor/swagger-ui.css swagger/vendor/LICENSE swagger/vendor/NOTICE
var swaggerFiles embed.FS

func registerSwagger(mux *http.ServeMux) {
	mux.HandleFunc("GET /swagger", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/swagger/", http.StatusMovedPermanently)
	})
	for route, asset := range map[string]struct{ path, contentType string }{
		"GET /swagger/{$}":                  {"swagger/index.html", "text/html; charset=utf-8"},
		"GET /swagger/swagger-ui-bundle.js": {"swagger/vendor/swagger-ui-bundle.js", "text/javascript; charset=utf-8"},
		"GET /swagger/swagger-ui.css":       {"swagger/vendor/swagger-ui.css", "text/css; charset=utf-8"},
		"GET /openapi.yaml":                 {"api/openapi.yaml", "application/json"},
		"GET /swagger/LICENSE":              {"swagger/vendor/LICENSE", "text/plain; charset=utf-8"},
		"GET /swagger/NOTICE":               {"swagger/vendor/NOTICE", "text/plain; charset=utf-8"},
	} {
		mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", asset.contentType)
			data, _ := swaggerFiles.ReadFile(asset.path)
			w.Write(data)
		})
	}
}
