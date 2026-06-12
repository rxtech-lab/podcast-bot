package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sirily11/debate-bot/internal/eventbus"
)

// newAuthServer stands up a minimal password-protected server. No channels /
// orchestrators are needed — these tests only exercise the auth middleware
// and the login/config endpoints.
func newAuthServer(t *testing.T, password string) *httptest.Server {
	t.Helper()
	bus := eventbus.New(nil)
	srv := New(Deps{
		Bus:      bus,
		Sessions: NewSessionRegistry(),
		Log:      slog.Default(),
		Password: password,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		bus.Close()
	})
	return ts
}

// TestAuthBlocksProtectedRoutes — with a password set, an unauthenticated
// /api/* request is rejected with 401, but /api/config stays reachable so the
// SPA can learn auth is required.
func TestAuthBlocksProtectedRoutes(t *testing.T) {
	ts := newAuthServer(t, "s3cret")

	resp, err := http.Get(ts.URL + "/api/topics")
	if err != nil {
		t.Fatalf("get topics: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("topics status = %d, want 401", resp.StatusCode)
	}

	cresp, err := http.Get(ts.URL + "/api/config")
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	defer cresp.Body.Close()
	if cresp.StatusCode != http.StatusOK {
		t.Fatalf("config status = %d, want 200", cresp.StatusCode)
	}
	var cfg struct {
		AuthRequired bool `json:"auth_required"`
		Authed       bool `json:"authed"`
	}
	if err := json.NewDecoder(cresp.Body).Decode(&cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if !cfg.AuthRequired || cfg.Authed {
		t.Fatalf("config = %+v, want auth_required=true authed=false", cfg)
	}
}

// TestAuthLoginFlow — a correct password yields a cookie that unlocks the
// protected routes; a wrong password is rejected.
func TestAuthLoginFlow(t *testing.T) {
	ts := newAuthServer(t, "s3cret")

	// Wrong password → 401, no cookie.
	bad, err := http.Post(ts.URL+"/api/login", "application/json",
		strings.NewReader(`{"password":"nope"}`))
	if err != nil {
		t.Fatalf("login bad: %v", err)
	}
	bad.Body.Close()
	if bad.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad login status = %d, want 401", bad.StatusCode)
	}

	// Correct password → 200 + Set-Cookie. Use a cookie jar so the cookie
	// rides along on the follow-up protected request.
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	client := &http.Client{Jar: jar}
	ok, err := client.Post(ts.URL+"/api/login", "application/json",
		strings.NewReader(`{"password":"s3cret"}`))
	if err != nil {
		t.Fatalf("login ok: %v", err)
	}
	ok.Body.Close()
	if ok.StatusCode != http.StatusOK {
		t.Fatalf("good login status = %d, want 200", ok.StatusCode)
	}

	// Now the protected route is reachable with the cookie.
	resp, err := client.Get(ts.URL + "/api/topics")
	if err != nil {
		t.Fatalf("get topics authed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authed topics status = %d, want 200", resp.StatusCode)
	}
}

// TestNoPasswordLeavesRoutesOpen — without a password configured the middleware
// is absent and every route is reachable.
func TestNoPasswordLeavesRoutesOpen(t *testing.T) {
	ts := newAuthServer(t, "")

	resp, err := http.Get(ts.URL + "/api/topics")
	if err != nil {
		t.Fatalf("get topics: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("topics status = %d, want 200 (auth disabled)", resp.StatusCode)
	}
}
