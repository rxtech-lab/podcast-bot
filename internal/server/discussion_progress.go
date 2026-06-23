package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const discussionProgressTTL = time.Hour

type DiscussionProgress struct {
	Active    bool      `json:"active"`
	Operation string    `json:"operation,omitempty"`
	Phase     string    `json:"phase,omitempty"`
	Text      string    `json:"text,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type DiscussionProgressStore struct {
	client *redis.Client
	log    *slog.Logger
	err    error
}

func NewDiscussionProgressStore(redisURL string, log *slog.Logger) *DiscussionProgressStore {
	redisURL = strings.TrimSpace(redisURL)
	if redisURL == "" {
		return nil
	}
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		if log != nil {
			log.Warn("invalid REDIS_URL; discussion stream recovery disabled", "err", err)
		}
		return &DiscussionProgressStore{log: log, err: err}
	}
	return &DiscussionProgressStore{client: redis.NewClient(opts), log: log}
}

func (s *DiscussionProgressStore) Ping(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if s.err != nil {
		return s.err
	}
	if s.client == nil {
		return errors.New("redis client is not configured")
	}
	return s.client.Ping(ctx).Err()
}

func (s *DiscussionProgressStore) Set(ctx context.Context, id string, p DiscussionProgress) {
	if s == nil || s.client == nil || strings.TrimSpace(id) == "" {
		return
	}
	p.UpdatedAt = time.Now().UTC()
	raw, err := json.Marshal(p)
	if err != nil {
		return
	}
	if err := s.client.Set(ctx, discussionProgressKey(id), raw, discussionProgressTTL).Err(); err != nil && s.log != nil {
		s.log.Warn("persist discussion progress", "discussion", id, "err", err)
	}
}

func (s *DiscussionProgressStore) Get(ctx context.Context, id string) *DiscussionProgress {
	if s == nil || s.client == nil || strings.TrimSpace(id) == "" {
		return nil
	}
	raw, err := s.client.Get(ctx, discussionProgressKey(id)).Bytes()
	if err != nil {
		return nil
	}
	var p DiscussionProgress
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil
	}
	return &p
}

func (s *DiscussionProgressStore) GetMany(ctx context.Context, ids []string) map[string]*DiscussionProgress {
	out := make(map[string]*DiscussionProgress)
	if s == nil || s.client == nil || len(ids) == 0 {
		return out
	}
	keys := make([]string, 0, len(ids))
	orderedIDs := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		orderedIDs = append(orderedIDs, id)
		keys = append(keys, discussionProgressKey(id))
	}
	if len(keys) == 0 {
		return out
	}
	values, err := s.client.MGet(ctx, keys...).Result()
	if err != nil {
		if s.log != nil {
			s.log.Warn("fetch discussion progress batch", "err", err)
		}
		return out
	}
	for i, value := range values {
		if value == nil {
			continue
		}
		var raw []byte
		switch v := value.(type) {
		case string:
			raw = []byte(v)
		case []byte:
			raw = v
		default:
			continue
		}
		var p DiscussionProgress
		if err := json.Unmarshal(raw, &p); err != nil {
			continue
		}
		out[orderedIDs[i]] = &p
	}
	return out
}

func (s *DiscussionProgressStore) Clear(ctx context.Context, id string) {
	if s == nil || s.client == nil || strings.TrimSpace(id) == "" {
		return
	}
	if err := s.client.Del(ctx, discussionProgressKey(id)).Err(); err != nil && s.log != nil {
		s.log.Warn("clear discussion progress", "discussion", id, "err", err)
	}
}

func discussionProgressKey(id string) string {
	return "debate-bot:discussion:progress:" + id
}
