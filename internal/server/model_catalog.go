package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/sirily11/debate-bot/internal/config"
)

// modelCatalogTTL bounds how stale the cached gateway model roster may get. The
// list rarely changes, so a day between upstream /models hits is plenty.
const modelCatalogTTL = 24 * time.Hour

// v2: entries now carry the gateway model type (language/embedding/...); v1
// rows lack it, and a typeless cache would empty the type-filtered pickers
// for up to a full TTL, so the old key is simply abandoned.
const modelCatalogKey = "debate-bot:models:catalog:v2"

// ModelCatalogStore caches the gateway's advertised model roster in Redis so we
// don't hit the upstream /models endpoint on every dashboard/app load. It is
// nil-safe: when Redis is unconfigured every method is a no-op / cache miss and
// the caller falls back to a live fetch.
type ModelCatalogStore struct {
	client *redis.Client
	log    *slog.Logger
}

// NewModelCatalogStore builds the cache from a REDIS_URL. Returns nil when no
// URL is configured or it can't be parsed, so the server runs without caching.
func NewModelCatalogStore(redisURL string, log *slog.Logger) *ModelCatalogStore {
	redisURL = strings.TrimSpace(redisURL)
	if redisURL == "" {
		return nil
	}
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		if log != nil {
			log.Warn("invalid REDIS_URL; model catalog cache disabled", "err", err)
		}
		return nil
	}
	return &ModelCatalogStore{client: redis.NewClient(opts), log: log}
}

// Get returns the cached models and true on a cache hit.
func (s *ModelCatalogStore) Get(ctx context.Context) ([]config.ModelInfo, bool) {
	if s == nil || s.client == nil {
		return nil, false
	}
	raw, err := s.client.Get(ctx, modelCatalogKey).Bytes()
	if err != nil {
		return nil, false
	}
	var models []config.ModelInfo
	if err := json.Unmarshal(raw, &models); err != nil {
		return nil, false
	}
	return models, true
}

// Set caches the models with a 24h TTL. Best-effort; failures are logged only.
func (s *ModelCatalogStore) Set(ctx context.Context, models []config.ModelInfo) {
	if s == nil || s.client == nil {
		return
	}
	raw, err := json.Marshal(models)
	if err != nil {
		return
	}
	if err := s.client.Set(ctx, modelCatalogKey, raw, modelCatalogTTL).Err(); err != nil && s.log != nil {
		s.log.Warn("cache model catalog", "err", err)
	}
}
