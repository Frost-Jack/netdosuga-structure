package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"example.com/netdosuga/identity-service/internal/observe"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"golang.org/x/crypto/bcrypt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

type user struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Role  string `json:"role"`
}
type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
	Role     string `json:"role"`
}
type limitEntry struct {
	count int
	until time.Time
}
type server struct {
	db       *pgxpool.Pool
	mu       sync.Mutex
	attempts map[string]limitEntry
}

const schema = `CREATE SCHEMA IF NOT EXISTS identity;
CREATE TABLE IF NOT EXISTS users(id text PRIMARY KEY,email text UNIQUE NOT NULL,name text NOT NULL,role text NOT NULL CHECK(role IN ('customer','vendor','admin')),password_hash text NOT NULL);
CREATE TABLE IF NOT EXISTS sessions(token_hash text PRIMARY KEY,user_id text NOT NULL REFERENCES users(id),expires_at timestamptz NOT NULL);
CREATE INDEX IF NOT EXISTS sessions_expiry ON sessions(expires_at);`

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
func (s *server) allowed(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, v := range s.attempts {
		if now.After(v.until) {
			delete(s.attempts, k)
		}
	}
	if len(s.attempts) >= 10000 {
		if _, exists := s.attempts[key]; !exists {
			return false
		}
	}
	v := s.attempts[key]
	if v.until.IsZero() {
		v.until = now.Add(time.Minute)
	}
	v.count++
	s.attempts[key] = v
	return v.count <= 20
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		json.NewEncoder(w).Encode(v)
	}
}
func failure(w http.ResponseWriter, status int, code string) {
	write(w, status, map[string]string{"code": code, "message": code})
}
func decode(w http.ResponseWriter, r *http.Request, c *credentials) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(c) != nil || d.Decode(&struct{}{}) != io.EOF {
		failure(w, 400, "INVALID_JSON")
		return false
	}
	return true
}
func validate(c *credentials, register bool) bool {
	c.Email = strings.ToLower(strings.TrimSpace(c.Email))
	a, e := mail.ParseAddress(c.Email)
	if e != nil || a.Address != c.Email || len(c.Email) > 254 || len(c.Password) < 8 || len(c.Password) > 72 {
		return false
	}
	if register && (strings.TrimSpace(c.Name) == "" || len(c.Name) > 128 || c.Role != "customer" && c.Role != "vendor") {
		return false
	}
	return true
}
func (s *server) session(ctx context.Context, u user) (map[string]any, error) {
	ctx, span := otel.Tracer("identity-service").Start(ctx, "session.create")
	defer span.End()
	tokenBytes := make([]byte, 32)
	if _, e := rand.Read(tokenBytes); e != nil {
		return nil, e
	}
	token := hex.EncodeToString(tokenBytes)
	expires := time.Now().UTC().Add(24 * time.Hour)
	_, e := s.db.Exec(ctx, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,$3)", hashToken(token), u.ID, expires)
	return map[string]any{"user": u, "session_token": token, "expires_at": expires}, e
}
func (s *server) auth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var c credentials
	if !decode(w, r, &c) {
		return
	}
	register := r.URL.Path == "/v1/register"
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	throttleKey := "login:" + strings.ToLower(strings.TrimSpace(c.Email))
	if register {
		throttleKey = "register:" + host
	}
	if !s.allowed(throttleKey) {
		failure(w, 429, "RATE_LIMITED")
		return
	}
	if !validate(&c, register) {
		failure(w, 400, "INVALID_CREDENTIALS")
		return
	}
	u := user{Email: c.Email, Name: strings.TrimSpace(c.Name), Role: c.Role}
	var err error
	if register {
		u.ID = uuid.NewString()
		hashCtx, span := otel.Tracer("identity-service").Start(ctx, "password.hash")
		_ = hashCtx
		hashed, e := bcrypt.GenerateFromPassword([]byte(c.Password), bcrypt.DefaultCost)
		span.End()
		if e != nil {
			failure(w, 500, "UNAVAILABLE")
			return
		}
		_, err = s.db.Exec(ctx, "INSERT INTO users(id,email,name,role,password_hash) VALUES($1,$2,$3,$4,$5)", u.ID, u.Email, u.Name, u.Role, string(hashed))
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23505" {
			failure(w, 409, "EMAIL_EXISTS")
			return
		}
	} else {
		var hashed string
		err = s.db.QueryRow(ctx, "SELECT id,email,name,role,password_hash FROM users WHERE email=$1", c.Email).Scan(&u.ID, &u.Email, &u.Name, &u.Role, &hashed)
		if errors.Is(err, pgx.ErrNoRows) {
			hashed = "$2a$10$C6UzMDM.H6dfI/f/IKcEe.5J2t7.8e6gOSM1P7LkWcFKgLtSCKQHe"
		}
		_, span := otel.Tracer("identity-service").Start(ctx, "password.verify")
		match := bcrypt.CompareHashAndPassword([]byte(hashed), []byte(c.Password))
		span.End()
		if errors.Is(err, pgx.ErrNoRows) || match != nil {
			failure(w, 401, "INVALID_CREDENTIALS")
			return
		}
	}
	if err != nil {
		failure(w, 503, "UNAVAILABLE")
		return
	}
	if !register {
		s.mu.Lock()
		delete(s.attempts, throttleKey)
		s.mu.Unlock()
	}
	result, err := s.session(ctx, u)
	if err != nil {
		failure(w, 503, "UNAVAILABLE")
		return
	}
	code := 200
	if register {
		code = 201
	}
	write(w, code, result)
}
func bearer(r *http.Request) string {
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || len(parts[1]) != 64 {
		return ""
	}
	if _, e := hex.DecodeString(parts[1]); e != nil {
		return ""
	}
	return parts[1]
}
func (s *server) resolve(w http.ResponseWriter, r *http.Request) {
	token := bearer(r)
	if token == "" {
		failure(w, 401, "UNAUTHENTICATED")
		return
	}
	ctx, span := otel.Tracer("identity-service").Start(r.Context(), "session.lookup")
	defer span.End()
	var u user
	var expires time.Time
	err := s.db.QueryRow(ctx, "SELECT u.id,u.email,u.name,u.role,s.expires_at FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.expires_at>now()", hashToken(token)).Scan(&u.ID, &u.Email, &u.Name, &u.Role, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		failure(w, 401, "UNAUTHENTICATED")
		return
	}
	if err != nil {
		failure(w, 503, "UNAVAILABLE")
		return
	}
	if r.URL.Path == "/v1/logout" {
		ctx, sp := otel.Tracer("identity-service").Start(ctx, "session.revoke")
		_, err = s.db.Exec(ctx, "DELETE FROM sessions WHERE token_hash=$1", hashToken(token))
		sp.End()
		if err != nil {
			failure(w, 503, "UNAVAILABLE")
			return
		}
		write(w, 204, nil)
		return
	}
	write(w, 200, map[string]any{"user": u, "expires_at": expires})
}
func seed(ctx context.Context, db *pgxpool.Pool) error {
	password := observe.Env("MVP_SEED_PASSWORD", "demo12345")
	if len(password) < 8 || len(password) > 72 {
		return fmt.Errorf("MVP_SEED_PASSWORD must be 8..72 bytes")
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if e != nil {
		return e
	}
	for _, u := range []user{{"customer-demo", "buyer@netdosuga.local", "Покупатель", "customer"}, {"customer-two", "buyer2@netdosuga.local", "Покупатель 2", "customer"}, {"vendor-demo", "vendor@netdosuga.local", "Организатор", "vendor"}, {"vendor-two", "vendor2@netdosuga.local", "Организатор 2", "vendor"}, {"admin-demo", "admin@netdosuga.local", "Модератор", "admin"}} {
		if _, e = db.Exec(ctx, "INSERT INTO users(id,email,name,role,password_hash) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING", u.ID, u.Email, u.Name, u.Role, string(hash)); e != nil {
			return e
		}
	}
	return nil
}
func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	stop, e := observe.Tracing(ctx, "identity-service")
	if e != nil {
		return e
	}
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stop(shutdown)
	}()
	if os.Getenv("DATABASE_URL") == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	config, e := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if e != nil {
		return fmt.Errorf("invalid database configuration")
	}
	config.ConnConfig.Tracer = observe.QueryTracer{}
	config.ConnConfig.ValidateConnect = pgconn.ValidateConnectTargetSessionAttrsReadWrite
	config.ConnConfig.RuntimeParams["search_path"] = "identity"
	db, e := pgxpool.NewWithConfig(ctx, config)
	if e != nil {
		return e
	}
	defer db.Close()
	startup, c := context.WithTimeout(ctx, 20*time.Second)
	defer c()
	if _, e = db.Exec(startup, schema); e != nil {
		return e
	}
	if e = seed(startup, db); e != nil {
		return e
	}
	s := &server{db: db, attempts: map[string]limitEntry{}}
	mux := http.NewServeMux()
	metrics := observe.NewHTTPMetrics("identity-service")
	observe.Endpoints(mux, metrics, func(c context.Context) error { return observe.Writable(c, db) })
	for _, route := range []string{"POST /v1/register", "POST /v1/login", "GET /v1/session", "POST /v1/logout"} {
		path := strings.Split(route, " ")[1]
		handler := http.HandlerFunc(s.auth)
		if path == "/v1/session" || path == "/v1/logout" {
			handler = s.resolve
		}
		mux.Handle(route, metrics.Wrap(path, observe.AuthDiagnostic(path, handler)))
	}
	srv := &http.Server{Addr: observe.Env("HTTP_ADDR", ":8084"), Handler: otelhttp.NewHandler(mux, "identity.http"), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		c, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		srv.Shutdown(c)
	}()
	e = srv.ListenAndServe()
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
func main() {
	if e := run(); e != nil {
		observe.Logger("identity-service").Error("service failed", "error", "startup or runtime failure")
		os.Exit(1)
	}
}
