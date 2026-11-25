package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestN01RegistrationBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, email, displayName, password string
		accepted                           bool
	}{
		{"empty name", "user@example.test", "  ", "password", false},
		{"name 129", "user@example.test", strings.Repeat("n", 129), "password", false},
		{"name 128", "user@example.test", strings.Repeat("n", 128), "password", true},
		{"bad email", "bad-email", "User", "password", false},
		{"email over 254", strings.Repeat("u", 64) + "@" + strings.Repeat("d", 63) + "." + strings.Repeat("d", 63) + "." + strings.Repeat("d", 60) + ".test", "User", "password", false},
		{"password 7", "user@example.test", "User", strings.Repeat("p", 7), false},
		{"password 8", "user@example.test", "User", strings.Repeat("p", 8), true},
		{"password 72 bytes", "user@example.test", "User", strings.Repeat("я", 36), true},
		{"password 73 bytes", "user@example.test", "User", strings.Repeat("я", 36) + "p", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := credentials{Email: tc.email, Name: tc.displayName, Password: tc.password, Role: "customer"}
			if tc.accepted {
				if !validate(&input, true) {
					t.Fatal("valid boundary was rejected")
				}
				return
			}
			// A nil database ensures rejected input cannot enter a write path.
			s := server{attempts: map[string]limitEntry{}}
			body, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "/v1/register", strings.NewReader(string(body)))
			w := httptest.NewRecorder()
			s.auth(w, r)
			var failure map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusBadRequest || failure["code"] != "INVALID_CREDENTIALS" {
				t.Fatalf("registration status=%d code=%s", w.Code, failure["code"])
			}
		})
	}
}

func TestN04RegistrationRateLimitPerIP(t *testing.T) {
	s := server{attempts: map[string]limitEntry{}}
	attempt := func(ip, email string) int {
		body, _ := json.Marshal(credentials{Email: email, Password: "short", Name: "User", Role: "customer"})
		r := httptest.NewRequest("POST", "/v1/register", strings.NewReader(string(body)))
		r.RemoteAddr = ip
		w := httptest.NewRecorder()
		s.auth(w, r)
		return w.Code
	}
	for range 20 {
		if status := attempt("192.0.2.1:1200", "first@example.test"); status != 400 {
			t.Fatalf("validation status=%d", status)
		}
	}
	if status := attempt("192.0.2.1:1201", "second@example.test"); status != 429 {
		t.Fatalf("the same IP with a new port and email returned %d", status)
	}
	if status := attempt("192.0.2.2:1200", "first@example.test"); status != 400 {
		t.Fatalf("another IP was limited: %d", status)
	}
	s.attempts["register:192.0.2.1"] = limitEntry{count: 21, until: time.Now().Add(-time.Second)}
	if status := attempt("192.0.2.1:1200", "first@example.test"); status != 400 {
		t.Fatalf("expired registration window returned %d", status)
	}
}

func TestRegistrationValidation(t *testing.T) {
	for _, c := range []credentials{{Email: "a@b.local", Password: "demo12345", Name: "A", Role: "admin"}, {Email: "bad", Password: "demo12345", Name: "A", Role: "customer"}, {Email: "a@b.local", Password: "short", Name: "A", Role: "customer"}} {
		if validate(&c, true) {
			t.Fatal("accepted invalid credentials")
		}
	}
	c := credentials{Email: "A@b.local", Password: "demo12345", Name: "A", Role: "vendor"}
	if !validate(&c, true) || c.Email != "a@b.local" {
		t.Fatal(c)
	}
}
func TestTokenHashAndBearer(t *testing.T) {
	token := strings.Repeat("a", 64)
	if hashToken(token) == token || len(hashToken(token)) != 64 {
		t.Fatal("not hashed")
	}
	r := httptest.NewRequest("GET", "/v1/session", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	if bearer(r) != token {
		t.Fatal("valid bearer rejected")
	}
	r.Header.Set("Authorization", "Bearer short")
	if bearer(r) != "" {
		t.Fatal("invalid bearer accepted")
	}
}
func TestBoundedAndTrailingJSON(t *testing.T) {
	for _, body := range []string{`{} {}`, `{"password":"` + strings.Repeat("a", 5000) + `"}`, `{"unknown":"field"}`} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/v1/login", strings.NewReader(body))
		var c credentials
		if decode(w, r, &c) {
			t.Fatal("invalid body accepted")
		}
	}
}
func TestLoginThrottleWindow(t *testing.T) {
	s := &server{attempts: map[string]limitEntry{}}
	for n := 0; n < 20; n++ {
		if !s.allowed("ip") {
			t.Fatal(n)
		}
	}
	if s.allowed("ip") {
		t.Fatal("unbounded login attempts")
	}
	s.attempts["ip"] = limitEntry{count: 20, until: time.Now().Add(-time.Second)}
	if !s.allowed("ip") {
		t.Fatal("window did not reset")
	}
}
