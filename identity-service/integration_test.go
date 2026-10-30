package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestIdentityDatabase(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, e := pgxpool.New(ctx, url)
	if e != nil {
		t.Fatal(e)
	}
	defer admin.Close()
	name := "identity_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, e = admin.Exec(ctx, "CREATE SCHEMA "+name); e != nil {
		t.Fatal(e)
	}
	defer admin.Exec(ctx, "DROP SCHEMA "+name+" CASCADE")
	cfg, e := pgxpool.ParseConfig(url)
	if e != nil {
		t.Fatal(e)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = name
	db, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if _, e = db.Exec(ctx, strings.TrimPrefix(schema, "CREATE SCHEMA IF NOT EXISTS identity;")); e != nil {
		t.Fatal(e)
	}
	if e = seed(ctx, db); e != nil {
		t.Fatal(e)
	}
	s := &server{db: db, attempts: map[string]limitEntry{}}
	call := func(path, body, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:1"
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		if path == "/v1/session" || path == "/v1/logout" {
			s.resolve(w, r)
		} else {
			s.auth(w, r)
		}
		return w
	}
	var token string
	for _, role := range []string{"customer", "vendor"} {
		body := fmt.Sprintf(`{"email":"%s@example.local","password":"demo12345","name":"Demo","role":"%s"}`, role, role)
		w := call("/v1/register", body, "")
		if w.Code != 201 {
			t.Fatal(w.Code, w.Body.String())
		}
		var out struct {
			Token string `json:"session_token"`
		}
		json.Unmarshal(w.Body.Bytes(), &out)
		token = out.Token
		if token == "" {
			t.Fatal("missing token")
		}
	}
	if w := call("/v1/login", `{"email":"vendor@example.local","password":"incorrect"}`, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := call("/v1/session", "", token); w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	var rawStored int
	if e = db.QueryRow(ctx, "SELECT count(*) FROM sessions WHERE token_hash=$1", token).Scan(&rawStored); e != nil || rawStored != 0 {
		t.Fatal("raw token persisted", e)
	}
	if w := call("/v1/logout", "", token); w.Code != 204 {
		t.Fatal(w.Code)
	}
	if w := call("/v1/session", "", token); w.Code != 401 {
		t.Fatal("logout did not revoke", w.Code)
	}
	w := call("/v1/login", `{"email":"buyer@netdosuga.local","password":"demo12345"}`, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var out struct {
		Token string `json:"session_token"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	if _, e = db.Exec(ctx, "UPDATE sessions SET expires_at=now()-interval '1 second' WHERE token_hash=$1", hashToken(out.Token)); e != nil {
		t.Fatal(e)
	}
	if w := call("/v1/session", "", out.Token); w.Code != 401 {
		t.Fatal("expired session accepted")
	}
}
