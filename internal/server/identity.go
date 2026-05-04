package server

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"unicode"
)

// usernameCookie is the cookie carrying each viewer's persistent display
// name. Server-issued on the first request that lacks one (typically the
// initial GET /api/me from the web UI). The cookie is not HttpOnly so the
// frontend could read it directly, but we still expose /api/me so the value
// is available even when SameSite policies suppress the cookie on a fetch.
const usernameCookie = "debate-bot-username"

const (
	// usernameMaxLen caps the user-chosen length (random handles are well under this).
	usernameMaxLen = 32
	// cookieMaxAgeSeconds = ~1 year. Long enough that a viewer keeps their
	// handle across visits without manual sign-in.
	cookieMaxAgeSeconds = 60 * 60 * 24 * 365
)

var usernameAdjectives = []string{
	"Curious", "Witty", "Bold", "Quiet", "Eager", "Calm", "Sharp", "Wise",
	"Brave", "Sly", "Lucky", "Sunny", "Stormy", "Cosmic", "Neon", "Velvet",
	"Iron", "Silver", "Crimson", "Jade", "Amber", "Indigo", "Mystic", "Quantum",
}

var usernameAnimals = []string{
	"Otter", "Fox", "Lynx", "Owl", "Falcon", "Panda", "Heron", "Wolf",
	"Tiger", "Whale", "Raven", "Hawk", "Bear", "Crane", "Lemur", "Koala",
	"Mantis", "Stag", "Seal", "Ibis", "Quokka", "Tapir", "Badger", "Penguin",
}

// randomUsername returns a fresh adjective+animal+two-digit handle. Uses
// crypto/rand because this is the viewer's identity for the session — math/rand's
// default seed would collide if the server starts up multiple viewers at once.
func randomUsername() string {
	pick := func(arr []string) string {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(arr))))
		if err != nil {
			return arr[0]
		}
		return arr[n.Int64()]
	}
	suffix, err := rand.Int(rand.Reader, big.NewInt(90))
	if err != nil {
		suffix = big.NewInt(0)
	}
	return fmt.Sprintf("%s%s%d", pick(usernameAdjectives), pick(usernameAnimals), suffix.Int64()+10)
}

// sanitizeUsername strips disallowed characters and trims to the max length.
// Keeps letters, digits, hyphen, underscore, dot — enough to be friendly in
// chat without opening transcript injection vectors.
func sanitizeUsername(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		}
		if b.Len() >= usernameMaxLen {
			break
		}
	}
	return b.String()
}

// usernameFromRequest reads the viewer's name from the cookie. Returns "" if
// missing or if the cookie value is empty after sanitisation.
func usernameFromRequest(r *http.Request) string {
	c, err := r.Cookie(usernameCookie)
	if err != nil {
		return ""
	}
	return sanitizeUsername(c.Value)
}

// setUsernameCookie writes the cookie. SameSite=Lax + long max-age so it
// persists across visits but isn't sent on cross-site requests.
func setUsernameCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     usernameCookie,
		Value:    name,
		Path:     "/",
		MaxAge:   cookieMaxAgeSeconds,
		SameSite: http.SameSiteLaxMode,
	})
}

// ensureUsername returns the request's username, generating + setting one if
// the cookie is missing. The Set-Cookie response header is added so the
// browser stores the new handle.
func (s *Server) ensureUsername(w http.ResponseWriter, r *http.Request) string {
	if name := usernameFromRequest(r); name != "" {
		return name
	}
	name := randomUsername()
	setUsernameCookie(w, name)
	return name
}

type meResponse struct {
	Username string `json:"username"`
}

type meRequest struct {
	Username string `json:"username"`
}

// handleGetMe returns the viewer's username, issuing one if this is their
// first request. The web UI calls this on mount to populate the chat header.
func (s *Server) handleGetMe(w http.ResponseWriter, r *http.Request) {
	name := s.ensureUsername(w, r)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(meResponse{Username: name})
}

// handlePostMe lets the viewer change their handle. The new value is
// sanitised; an empty / whitespace-only value resets to a fresh random name.
func (s *Server) handlePostMe(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1024))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	var req meRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	name := sanitizeUsername(req.Username)
	if name == "" {
		name = randomUsername()
	}
	setUsernameCookie(w, name)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(meResponse{Username: name})
}
