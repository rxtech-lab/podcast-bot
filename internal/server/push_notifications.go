package server

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/sirily11/debate-bot/internal/config"
)

const (
	apnsSandboxURL    = "https://api.sandbox.push.apple.com"
	apnsProductionURL = "https://api.push.apple.com"
	apnsJWTValidFor   = 45 * time.Minute
)

type PushNotificationKind string

const (
	PushKindPlanReady      PushNotificationKind = "plan_ready"
	PushKindPodcastStarted PushNotificationKind = "podcast_started"
	PushKindPodcastReady   PushNotificationKind = "podcast_ready"
	PushKindSummaryReady   PushNotificationKind = "summary_ready"
	PushKindMarketLike     PushNotificationKind = "market_like"
)

type PushNotification struct {
	Kind         PushNotificationKind
	DiscussionID string
	Title        string
	Body         string
	URL          string
}

type APNSSendResult struct {
	StatusCode int
	APNSID     string
	Reason     string
}

type APNSClient struct {
	env        string
	keyID      string
	teamID     string
	bundleID   string
	privateKey *ecdsa.PrivateKey
	http       *http.Client

	mu        sync.Mutex
	jwt       string
	jwtExpiry time.Time
}

func NewAPNSClient(env *config.Env) (*APNSClient, error) {
	if env == nil {
		return nil, nil
	}
	keyID := strings.TrimSpace(env.APNSKeyID)
	teamID := strings.TrimSpace(env.APNSTeamID)
	bundleID := strings.TrimSpace(env.APNSBundleID)
	keyValue := strings.TrimSpace(env.APNSKeyBase64)
	if keyID == "" || teamID == "" || bundleID == "" || keyValue == "" {
		return nil, nil
	}
	keyBytes, err := loadAPNSPrivateKeyBytes(keyValue)
	if err != nil {
		return nil, err
	}
	privateKey, err := parseAPNSPrivateKey(keyBytes)
	if err != nil {
		return nil, err
	}
	return &APNSClient{
		env:        normalizePushEnvironment(env.APNSEnvironment),
		keyID:      keyID,
		teamID:     teamID,
		bundleID:   bundleID,
		privateKey: privateKey,
		http:       &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func loadAPNSPrivateKeyBytes(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, `\n`, "\n")
	if strings.Contains(value, "-----BEGIN") {
		return []byte(value), nil
	}
	compact := strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\n', '\r', '\t':
			return -1
		default:
			return r
		}
	}, value)
	keyBytes, err := base64.StdEncoding.DecodeString(compact)
	if err != nil {
		return nil, fmt.Errorf("decode APNS_KEY_BASE64 (expected base64 .p8 or raw PEM .p8): %w", err)
	}
	return keyBytes, nil
}

func parseAPNSPrivateKey(raw []byte) (*ecdsa.PrivateKey, error) {
	if block, _ := pem.Decode(raw); block != nil {
		raw = block.Bytes
	}
	key, err := x509.ParsePKCS8PrivateKey(raw)
	if err != nil {
		return nil, fmt.Errorf("parse APNs private key: %w", err)
	}
	ecdsaKey, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("APNs private key is not ECDSA")
	}
	return ecdsaKey, nil
}

func (c *APNSClient) Environment() string {
	if c == nil {
		return PushEnvironmentSandbox
	}
	return c.env
}

func (c *APNSClient) Send(ctx context.Context, token string, n PushNotification) (APNSSendResult, error) {
	if c == nil {
		return APNSSendResult{}, nil
	}
	token = normalizePushToken(token)
	if token == "" {
		return APNSSendResult{}, nil
	}
	payload := map[string]any{
		"aps": map[string]any{
			"alert": map[string]string{
				"title": n.Title,
				"body":  n.Body,
			},
			"sound": "default",
		},
		"kind":          string(n.Kind),
		"discussion_id": n.DiscussionID,
		"url":           n.URL,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return APNSSendResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.endpoint()+"/3/device/"+token, bytes.NewReader(body))
	if err != nil {
		return APNSSendResult{}, err
	}
	jwt, err := c.authorizationJWT()
	if err != nil {
		return APNSSendResult{}, err
	}
	req.Header.Set("authorization", "bearer "+jwt)
	req.Header.Set("apns-topic", c.bundleID)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")
	req.Header.Set("content-type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return APNSSendResult{}, err
	}
	defer resp.Body.Close()
	result := APNSSendResult{
		StatusCode: resp.StatusCode,
		APNSID:     resp.Header.Get("apns-id"),
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return result, nil
	}
	result.Reason = apnsErrorReason(resp.Body)
	if result.Reason == "" {
		result.Reason = resp.Status
	}
	return result, fmt.Errorf("apns status %d: %s", resp.StatusCode, result.Reason)
}

func apnsErrorReason(r io.Reader) string {
	if r == nil {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(r, 4096))
	if err != nil || len(body) == 0 {
		return ""
	}
	var parsed struct {
		Reason string `json:"reason"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.Reason != "" {
		return parsed.Reason
	}
	return strings.TrimSpace(string(body))
}

func (c *APNSClient) endpoint() string {
	if c.env == PushEnvironmentProduction {
		return apnsProductionURL
	}
	return apnsSandboxURL
}

func (c *APNSClient) authorizationJWT() (string, error) {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.jwt != "" && now.Before(c.jwtExpiry) {
		return c.jwt, nil
	}
	header := map[string]string{"alg": "ES256", "kid": c.keyID}
	claims := map[string]any{"iss": c.teamID, "iat": now.Unix()}
	headerJSON, _ := json.Marshal(header)
	claimsJSON, _ := json.Marshal(claims)
	unsigned := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)
	digest := sha256.Sum256([]byte(unsigned))
	r, s, err := ecdsa.Sign(rand.Reader, c.privateKey, digest[:])
	if err != nil {
		return "", err
	}
	sig := append(fixedWidth(r, 32), fixedWidth(s, 32)...)
	c.jwt = unsigned + "." + base64.RawURLEncoding.EncodeToString(sig)
	c.jwtExpiry = now.Add(apnsJWTValidFor)
	return c.jwt, nil
}

func fixedWidth(n *big.Int, width int) []byte {
	b := n.Bytes()
	if len(b) >= width {
		return b[len(b)-width:]
	}
	out := make([]byte, width)
	copy(out[width-len(b):], b)
	return out
}

func (s *Server) notifyUser(ctx context.Context, userID string, n PushNotification) {
	if s == nil {
		return
	}
	SendPushNotification(ctx, s.d.Discussions, s.apns, userID, n, s.logger())
}

func SendPushNotification(ctx context.Context, store *DiscussionStore, apns *APNSClient, userID string, n PushNotification, log *slog.Logger) {
	if log == nil {
		log = slog.Default()
	}
	if apns == nil {
		log.Info("push notification skipped: APNs disabled",
			"kind", n.Kind,
			"discussion", n.DiscussionID,
			"user", userID)
		return
	}
	if store == nil {
		log.Info("push notification skipped: store disabled",
			"kind", n.Kind,
			"discussion", n.DiscussionID,
			"user", userID,
			"environment", apns.Environment())
		return
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		log.Info("push notification skipped: empty user",
			"kind", n.Kind,
			"discussion", n.DiscussionID,
			"environment", apns.Environment())
		return
	}
	tokens, err := store.PushTokensForUser(ctx, userID, apns.Environment())
	if err != nil {
		log.Warn("push token lookup failed",
			"kind", n.Kind,
			"discussion", n.DiscussionID,
			"user", userID,
			"environment", apns.Environment(),
			"err", err)
		return
	}
	log.Info("push notification dispatch",
		"kind", n.Kind,
		"discussion", n.DiscussionID,
		"user", userID,
		"environment", apns.Environment(),
		"token_count", len(tokens),
		"url", n.URL)
	if len(tokens) == 0 {
		return
	}
	for _, tok := range tokens {
		maskedToken := maskPushToken(tok.Token)
		log.Info("push send start",
			"kind", n.Kind,
			"discussion", n.DiscussionID,
			"user", userID,
			"environment", tok.Environment,
			"token", maskedToken)
		result, err := apns.Send(ctx, tok.Token, n)
		if err != nil {
			log.Warn("push send failed",
				"kind", n.Kind,
				"discussion", n.DiscussionID,
				"user", userID,
				"environment", tok.Environment,
				"token", maskedToken,
				"status", result.StatusCode,
				"apns_id", result.APNSID,
				"reason", result.Reason,
				"err", err)
			continue
		}
		log.Info("push send succeeded",
			"kind", n.Kind,
			"discussion", n.DiscussionID,
			"user", userID,
			"environment", tok.Environment,
			"token", maskedToken,
			"status", result.StatusCode,
			"apns_id", result.APNSID)
	}
}

func maskPushToken(token string) string {
	token = normalizePushToken(token)
	if len(token) <= 12 {
		return token
	}
	return token[:6] + "..." + token[len(token)-6:]
}

func (s *Server) discussionDeepLink(id string) string {
	return DiscussionDeepLink(s.d.WebsiteBaseURL, id)
}

func DiscussionDeepLink(websiteBaseURL, id string) string {
	base := strings.TrimRight(websiteBaseURL, "/")
	if base == "" {
		base = "https://podcast.rxlab.app"
	}
	return base + "/d/" + strings.TrimSpace(id)
}

// frontendBaseURL is the public base of the web frontend used for "listen again"
// links. It prefers FRONTEND_PUBLIC_URL (which may point at localhost in dev),
// then falls back to WEBSITE_BASE_URL, then the production default.
func (s *Server) frontendBaseURL() string {
	if s.d.Env != nil {
		if v := strings.TrimRight(strings.TrimSpace(s.d.Env.FrontendPublicURL), "/"); v != "" {
			return v
		}
	}
	base := strings.TrimRight(strings.TrimSpace(s.d.WebsiteBaseURL), "/")
	if base == "" {
		base = "https://podcast.rxlab.app"
	}
	return base
}

// podcastPlayerURL is the public, view-only web player page for a discussion —
// the target of the "listen again" link embedded in exported summaries.
func (s *Server) podcastPlayerURL(id string) string {
	return s.frontendBaseURL() + "/p/" + strings.TrimSpace(id)
}

// summaryMarkdownWithLink appends a "listen again" link to a summary's Markdown
// body so every export surface (Markdown download, PDF, Notion) carries a way
// back to the original podcast. It is injected on read — never stored — so the
// link always reflects the current frontend URL. Idempotent: re-injecting is a
// no-op once the link is present.
func (s *Server) summaryMarkdownWithLink(discussionID, markdown string) string {
	playerURL := s.podcastPlayerURL(discussionID)
	if strings.Contains(markdown, playerURL) {
		return markdown
	}
	link := fmt.Sprintf("🎧 [Listen to the original podcast](%s)", playerURL)
	body := strings.TrimRight(markdown, "\n")
	if body == "" {
		return link + "\n"
	}
	return body + "\n\n---\n\n" + link + "\n"
}
