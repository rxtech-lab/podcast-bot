package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/sirily11/debate-bot/internal/tts"
)

// voiceCatalogTTL bounds how stale the cached Azure voice roster may get. The
// list rarely changes, so a day between upstream voices/list hits is plenty.
const voiceCatalogTTL = 24 * time.Hour

const voiceCatalogKey = "debate-bot:voices:catalog"

// VoiceCatalogStore caches the Azure TTS voice roster in Redis so we don't hit
// the upstream voices/list endpoint on every picker load. It is nil-safe: when
// Redis is unconfigured every method is a no-op / cache miss and the caller
// falls back to a live fetch.
type VoiceCatalogStore struct {
	client *redis.Client
	log    *slog.Logger
}

// NewVoiceCatalogStore builds the cache from a REDIS_URL. Returns nil when no
// URL is configured or it can't be parsed, so the server runs without caching.
func NewVoiceCatalogStore(redisURL string, log *slog.Logger) *VoiceCatalogStore {
	redisURL = strings.TrimSpace(redisURL)
	if redisURL == "" {
		return nil
	}
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		if log != nil {
			log.Warn("invalid REDIS_URL; voice catalog cache disabled", "err", err)
		}
		return nil
	}
	return &VoiceCatalogStore{client: redis.NewClient(opts), log: log}
}

// Get returns the cached voices and true on a cache hit.
func (s *VoiceCatalogStore) Get(ctx context.Context) ([]tts.Voice, bool) {
	if s == nil || s.client == nil {
		return nil, false
	}
	raw, err := s.client.Get(ctx, voiceCatalogKey).Bytes()
	if err != nil {
		return nil, false
	}
	var voices []tts.Voice
	if err := json.Unmarshal(raw, &voices); err != nil {
		return nil, false
	}
	return voices, true
}

// Set caches the voices with a 24h TTL. Best-effort; failures are logged only.
func (s *VoiceCatalogStore) Set(ctx context.Context, voices []tts.Voice) {
	if s == nil || s.client == nil {
		return
	}
	raw, err := json.Marshal(voices)
	if err != nil {
		return
	}
	if err := s.client.Set(ctx, voiceCatalogKey, raw, voiceCatalogTTL).Err(); err != nil && s.log != nil {
		s.log.Warn("cache voice catalog", "err", err)
	}
}
