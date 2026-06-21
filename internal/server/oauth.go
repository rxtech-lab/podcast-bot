package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// oauthValidator authenticates per-user rxlab OAuth access tokens by calling
// the issuer's OIDC userinfo endpoint. RxAuthSwift (the iOS app) holds an
// access token that this engine can't mint or refresh, so rather than verify a
// JWT signature locally (tokens may be opaque) we ask the issuer whether the
// token is good: a 200 from userinfo means valid.
//
// Results are cached by token hash for a short TTL so live streaming — which
// fires many authenticated requests (HLS segments, subtitle polls) per second —
// doesn't make a userinfo round-trip every time.
type oauthValidator struct {
	userInfoURL string
	client      *http.Client
	log         *slog.Logger

	mu    sync.Mutex
	cache map[string]time.Time // token sha256 -> expiry of the cached "valid" verdict
}

const (
	// oauthCacheTTL bounds how long a validated token is trusted without
	// re-checking userinfo. Short enough that a revoked/expired token stops
	// working promptly; long enough to absorb streaming bursts.
	oauthCacheTTL = 60 * time.Second
	// userInfoPath mirrors RxAuthConfiguration's default in RxAuthSwift.
	userInfoPath = "/api/oauth/userinfo"
)

func newOAuthValidator(issuer string, log *slog.Logger) *oauthValidator {
	return &oauthValidator{
		userInfoURL: issuer + userInfoPath,
		client:      &http.Client{Timeout: 8 * time.Second},
		log:         log,
		cache:       make(map[string]time.Time),
	}
}

// valid reports whether the access token is currently accepted by the issuer.
func (v *oauthValidator) valid(ctx context.Context, token string) bool {
	if v == nil || token == "" {
		return false
	}
	sum := sha256.Sum256([]byte(token))
	key := hex.EncodeToString(sum[:])

	now := time.Now()
	v.mu.Lock()
	if exp, ok := v.cache[key]; ok {
		if now.Before(exp) {
			v.mu.Unlock()
			return true
		}
		delete(v.cache, key)
	}
	v.mu.Unlock()

	if !v.introspect(ctx, token) {
		return false
	}

	v.mu.Lock()
	v.cache[key] = now.Add(oauthCacheTTL)
	// Opportunistically evict stale entries so the map can't grow unbounded.
	if len(v.cache) > 1024 {
		for k, exp := range v.cache {
			if now.After(exp) {
				delete(v.cache, k)
			}
		}
	}
	v.mu.Unlock()
	return true
}

// introspect performs the live userinfo round-trip; a 200 means the token is
// valid. Any non-200 or transport error is treated as invalid (fail closed).
func (v *oauthValidator) introspect(ctx context.Context, token string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.userInfoURL, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := v.client.Do(req)
	if err != nil {
		v.logger().Warn("oauth userinfo validation failed",
			"userinfo_url", v.userInfoURL,
			"err", err,
		)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		v.logger().Warn("oauth userinfo rejected bearer",
			"userinfo_url", v.userInfoURL,
			"status", resp.StatusCode,
		)
	}
	return resp.StatusCode == http.StatusOK
}

func (v *oauthValidator) logger() *slog.Logger {
	if v.log != nil {
		return v.log
	}
	return slog.Default()
}
