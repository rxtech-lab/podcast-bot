package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
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

// withAuth wraps the mux so every /api/* route requires authorization.
// Exceptions (always reachable so the login screen can render and submit):
//   - POST /api/login           — the credential exchange itself
//   - GET  /api/config          — tells the SPA that auth is required
//   - any non-/api/ path        — the embedded SPA shell + JS/CSS bundle
//
// A request is authorized if it carries a valid password cookie (human SPA
// users) OR a valid `Authorization: Bearer <ServiceToken>` header (the
// dashboard backend). Either mechanism alone is sufficient.
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/login" || r.URL.Path == "/api/config" ||
			r.URL.Path == "/api/revenuecat/webhook" ||
			isPublicShareResolve(r) ||
			!strings.HasPrefix(r.URL.Path, "/api/") {
			// The RevenueCat webhook carries no user/service credential; it is
			// authenticated by the shared secret the handler verifies itself.
			next.ServeHTTP(w, r)
			return
		}
		decision := s.authorizeRequest(r)
		if !decision.authorized {
			s.logAuthDenied(r, decision)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isPublicShareResolve(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	path := strings.Trim(r.URL.Path, "/")
	parts := strings.Split(path, "/")
	return len(parts) == 3 && parts[0] == "api" && parts[1] == "share" && parts[2] != ""
}

type authDecision struct {
	authorized bool
	method     string
	reason     string
	hasBearer  bool
	hasCookie  bool
}

// requestAuthorized reports whether the request may reach a protected route,
// via the password cookie or the service-token bearer header.
func (s *Server) requestAuthorized(r *http.Request) bool {
	return s.authorizeRequest(r).authorized
}

func (s *Server) authorizeRequest(r *http.Request) authDecision {
	tok := bearerToken(r)
	decision := authDecision{
		reason:    "no accepted credentials",
		hasBearer: tok != "",
		hasCookie: s.authEnabled() && s.requestAuthed(r),
	}
	if s.authEnabled() && s.requestAuthed(r) {
		decision.authorized = true
		decision.method = "cookie"
		decision.reason = "password cookie accepted"
		return decision
	}
	if s.d.ServiceToken != "" {
		if tok := bearerToken(r); tok != "" &&
			subtle.ConstantTimeCompare([]byte(tok), []byte(s.d.ServiceToken)) == 1 {
			decision.authorized = true
			decision.method = "service_token"
			decision.reason = "service token accepted"
			return decision
		}
		if tok != "" {
			decision.reason = "bearer did not match service token"
		}
	}
	// Per-user rxlab OAuth: validate the access token against the issuer's
	// userinfo endpoint (native iOS clients). Cached so a burst of streaming
	// requests doesn't hammer the auth server.
	if s.oauth != nil {
		if tok := bearerToken(r); tok != "" && s.oauth.valid(r.Context(), tok) {
			decision.authorized = true
			decision.method = "oauth"
			decision.reason = "oauth bearer accepted"
			return decision
		}
		if tok == "" {
			decision.reason = "missing bearer token for oauth"
		} else {
			decision.reason = "oauth bearer rejected by userinfo"
		}
	} else if tok != "" {
		decision.reason = "bearer present but oauth issuer not configured"
	} else if s.authEnabled() {
		decision.reason = "missing or invalid password cookie"
	} else if s.d.ServiceToken != "" {
		decision.reason = "missing bearer service token"
	}
	return decision
}

func (s *Server) logAuthDenied(r *http.Request, decision authDecision) {
	s.logger().Warn("request denied by auth middleware",
		"method", r.Method,
		"path", r.URL.Path,
		"mode", s.d.Mode,
		"reason", decision.reason,
		"has_bearer", decision.hasBearer,
		"has_valid_cookie", decision.hasCookie,
		"password_auth_enabled", s.authEnabled(),
		"service_token_enabled", s.d.ServiceToken != "",
		"oauth_enabled", s.oauth != nil,
		"auth_issuer", s.d.AuthIssuer,
	)
}

func (s *Server) logAuthStartup(enabled bool) {
	s.logger().Info("auth middleware configured",
		"enabled", enabled,
		"mode", s.d.Mode,
		"password_auth_enabled", s.authTok != "",
		"service_token_enabled", s.d.ServiceToken != "",
		"oauth_enabled", s.oauth != nil,
		"auth_issuer", s.d.AuthIssuer,
	)
}

func (s *Server) logger() *slog.Logger {
	if s.d.Log != nil {
		return s.d.Log
	}
	return slog.Default()
}

// bearerToken extracts the token from an `Authorization: Bearer <token>`
// header, or "" when absent/malformed.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

type requestUser struct {
	ID        string
	Name      string
	Email     string
	AvatarURL string
}

func (s *Server) requestUser(r *http.Request) requestUser {
	if tok := bearerToken(r); tok != "" {
		sum := sha256.Sum256([]byte(tok))
		fallback := "bearer:" + hex.EncodeToString(sum[:])
		if s.oauth != nil {
			if user, ok := s.oauth.user(r.Context(), tok); ok {
				id := strings.TrimSpace(user.Subject)
				if id == "" {
					id = fallback
				}
				return requestUser{ID: "oauth:" + id, Name: user.Name, Email: user.Email, AvatarURL: user.Picture}
			}
		}
		if s.d.ServiceToken != "" &&
			subtle.ConstantTimeCompare([]byte(tok), []byte(s.d.ServiceToken)) == 1 {
			return requestUser{ID: "service:dashboard", Name: "Dashboard"}
		}
		return requestUser{ID: fallback}
	}
	if name := usernameFromRequest(r); name != "" {
		return requestUser{ID: "cookie:" + name, Name: name}
	}
	return requestUser{ID: "anonymous", Name: "viewer"}
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
