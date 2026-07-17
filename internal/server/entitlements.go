package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// entitlementsTTL bounds how stale a user's cached resolved permissions may get.
// The request calls for a fast 60s cache so the endpoint and UI-action gating
// stay cheap; admin edits bust it early via the epoch (see BumpEpoch).
const entitlementsTTL = 60 * time.Second

const (
	entitlementsEpochKey  = "debate-bot:entitlements:epoch"
	entitlementsKeyPrefix = "debate-bot:entitlements:"
)

// EntitlementsStore caches per-user resolved Permissions in Redis. The cache key
// embeds a global epoch; bumping the epoch on any admin write to the
// subscription-permission table invalidates every user's cached value at once
// (old keys simply expire via TTL). It is nil-safe: with Redis unconfigured
// every method is a no-op / cache miss and the resolver recomputes each call.
type EntitlementsStore struct {
	client *redis.Client
	log    *slog.Logger
}

// NewEntitlementsStore builds the cache from a REDIS_URL. Returns nil when no
// URL is configured or it can't be parsed, so the server runs without caching.
func NewEntitlementsStore(redisURL string, log *slog.Logger) *EntitlementsStore {
	redisURL = strings.TrimSpace(redisURL)
	if redisURL == "" {
		return nil
	}
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		if log != nil {
			log.Warn("invalid REDIS_URL; entitlements cache disabled", "err", err)
		}
		return nil
	}
	return &EntitlementsStore{client: redis.NewClient(opts), log: log}
}

// Epoch returns the current cache epoch (0 when unset or Redis is unavailable).
func (s *EntitlementsStore) Epoch(ctx context.Context) int64 {
	if s == nil || s.client == nil {
		return 0
	}
	raw, err := s.client.Get(ctx, entitlementsEpochKey).Result()
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	return n
}

// BumpEpoch invalidates all cached entitlements by advancing the epoch.
// Best-effort; failures are logged only.
func (s *EntitlementsStore) BumpEpoch(ctx context.Context) {
	if s == nil || s.client == nil {
		return
	}
	if err := s.client.Incr(ctx, entitlementsEpochKey).Err(); err != nil && s.log != nil {
		s.log.Warn("bump entitlements epoch", "err", err)
	}
}

func entitlementsKey(epoch int64, userID string) string {
	return entitlementsKeyPrefix + strconv.FormatInt(epoch, 10) + ":" + userID
}

// Get returns the cached permissions for a user at the given epoch and true on a
// cache hit.
func (s *EntitlementsStore) Get(ctx context.Context, epoch int64, userID string) (Permissions, bool) {
	if s == nil || s.client == nil {
		return Permissions{}, false
	}
	raw, err := s.client.Get(ctx, entitlementsKey(epoch, userID)).Bytes()
	if err != nil {
		return Permissions{}, false
	}
	var perms Permissions
	if err := json.Unmarshal(raw, &perms); err != nil {
		return Permissions{}, false
	}
	return perms, true
}

// Set caches the permissions for a user at the given epoch with a 60s TTL.
// Best-effort; failures are logged only.
func (s *EntitlementsStore) Set(ctx context.Context, epoch int64, userID string, perms Permissions) {
	if s == nil || s.client == nil {
		return
	}
	raw, err := json.Marshal(perms)
	if err != nil {
		return
	}
	if err := s.client.Set(ctx, entitlementsKey(epoch, userID), raw, entitlementsTTL).Err(); err != nil && s.log != nil {
		s.log.Warn("cache entitlements", "err", err)
	}
}

// invalidateEntitlementsCache advances the epoch so subsequent resolves miss the
// cache. Called after any admin write to the subscription-permission table.
func (s *Server) invalidateEntitlementsCache(ctx context.Context) {
	if s.d.Entitlements != nil {
		s.d.Entitlements.BumpEpoch(ctx)
	}
}

// resolveEntitlements returns the resolved Permissions for a user, using the 60s
// Redis cache when available.
func (s *Server) resolveEntitlements(ctx context.Context, userID string) (Permissions, error) {
	userID = strings.TrimSpace(userID)
	var epoch int64
	if s.d.Entitlements != nil {
		epoch = s.d.Entitlements.Epoch(ctx)
		if perms, ok := s.d.Entitlements.Get(ctx, epoch, userID); ok {
			return perms, nil
		}
	}
	perms, err := s.computeEntitlements(ctx, userID)
	if err != nil {
		return Permissions{}, err
	}
	if s.d.Entitlements != nil {
		s.d.Entitlements.Set(ctx, epoch, userID, perms)
	}
	return perms, nil
}

// computeEntitlements resolves a user's permissions from the ground truth: their
// active subscription's class permissions, else the free class, else the hard
// default (nothing allowed).
func (s *Server) computeEntitlements(ctx context.Context, userID string) (Permissions, error) {
	if s.d.SubscriptionPermissions == nil {
		return DefaultPermissions(), nil
	}
	if s.d.Points != nil {
		sub, err := s.d.Points.Subscription(ctx, userID)
		if err != nil {
			return Permissions{}, err
		}
		if sub != nil && sub.Active(time.Now().UnixMilli()) {
			row, err := s.d.SubscriptionPermissions.GetForClass(ctx, sub.ProductID, sub.StoreEnvironment)
			if err != nil {
				return Permissions{}, err
			}
			if row != nil {
				return row.Permissions, nil
			}
		}
	}
	free, err := s.d.SubscriptionPermissions.GetFree(ctx)
	if err != nil {
		return Permissions{}, err
	}
	if free != nil {
		return free.Permissions, nil
	}
	return DefaultPermissions(), nil
}

// uploadAudioAllowedForUser reports whether the caller may use the
// upload-own-audio feature: their subscription tier must grant
// CanUploadOwnAudio (admin-managed per class in Subscription Permissions).
func (s *Server) uploadAudioAllowedForUser(ctx context.Context, userID string) bool {
	perms, err := s.resolveEntitlements(ctx, userID)
	if err != nil {
		return false
	}
	return perms.Features.CanUploadOwnAudio
}

// chatAllowedForUser reports whether the caller's subscription class grants
// global library chat and per-podcast Q&A.
func (s *Server) chatAllowedForUser(ctx context.Context, userID string) (bool, error) {
	perms, err := s.resolveEntitlements(ctx, userID)
	if err != nil {
		return false, err
	}
	return perms.Features.CanUseChat, nil
}

// uploadAudioCapBytes returns the caller's effective per-file audio upload cap
// in bytes: the tier's MaxUploadAudioMB when set, bounded by the server-wide
// MAX_PODCAST_AUDIO_UPLOAD_MB ceiling.
func (s *Server) uploadAudioCapBytes(ctx context.Context, userID string) int64 {
	var envMB int64 = 500
	if s.d.Env != nil && s.d.Env.MaxPodcastAudioUploadMB > 0 {
		envMB = s.d.Env.MaxPodcastAudioUploadMB
	}
	capMB := envMB
	if perms, err := s.resolveEntitlements(ctx, userID); err == nil {
		if tier := perms.Limits.MaxUploadAudioMB; tier > 0 && tier < capMB {
			capMB = tier
		}
	}
	return capMB << 20
}

// handleEntitlements serves the caller's resolved permissions. Auth is enforced
// upstream by withAuth, so requestUser().ID is a validated identity here.
func (s *Server) handleEntitlements(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	perms, err := s.resolveEntitlements(r.Context(), user.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, perms)
}
