package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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
