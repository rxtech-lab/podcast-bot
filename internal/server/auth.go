package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// authCookie carries the proof-of-password token. HttpOnly so the SPA can't
// read it (and can't accidentally leak it) — the browser still sends it
// automatically on every fetch, EventSource, and <audio>/HLS request, which
// is exactly what lets the streaming endpoints stay authenticated without any
// per-request header plumbing.
const authCookie = "debate-bot-auth"

// authToken derives the opaque cookie value from the configured password.
// Storing the hash (not the password) in the cookie means a stolen cookie
// can't be reversed into the password, and a constant-time compare avoids
// leaking match length via timing.
func authToken(password string) string {
	sum := sha256.Sum256([]byte("debate-bot:" + password))
	return hex.EncodeToString(sum[:])
}

// authEnabled reports whether the server was started with a password.
func (s *Server) authEnabled() bool { return s.d.Password != "" }

// requestAuthed reports whether the request carries a valid auth cookie.
// Always true when no password is configured.
func (s *Server) requestAuthed(r *http.Request) bool {
	if !s.authEnabled() {
		return true
	}
	c, err := r.Cookie(authCookie)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.authTok)) == 1
}

// withAuth wraps the mux so every /api/* route requires a valid auth cookie.
// Exceptions (always reachable so the login screen can render and submit):
//   - POST /api/login           — the credential exchange itself
//   - GET  /api/config          — tells the SPA that auth is required
//   - any non-/api/ path        — the embedded SPA shell + JS/CSS bundle
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/login" || r.URL.Path == "/api/config" ||
			!strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		if !s.requestAuthed(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type loginRequest struct {
	Password string `json:"password"`
}

// handleLogin verifies the submitted password and, on success, installs the
// auth cookie. Returns 401 on mismatch. When no password is configured the
// endpoint is a no-op success so the SPA's optimistic login flow still works.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	if !s.authEnabled() {
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	var req loginRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if subtle.ConstantTimeCompare([]byte(authToken(req.Password)), []byte(s.authTok)) != 1 {
		http.Error(w, "invalid password", http.StatusUnauthorized)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     authCookie,
		Value:    s.authTok,
		Path:     "/",
		MaxAge:   cookieMaxAgeSeconds,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, map[string]bool{"ok": true})
}

// handleLogout clears the auth cookie.
func (s *Server) handleLogout(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     authCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, map[string]bool{"ok": true})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
