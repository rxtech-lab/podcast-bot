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

const planningStreamTTL = time.Hour

type PlanningActiveStream struct {
	RunID          string    `json:"run_id"`
	ConversationID string    `json:"conversation_id"`
	DiscussionID   string    `json:"discussion_id"`
	OwnerUserID    string    `json:"owner_user_id"`
	StartedAt      time.Time `json:"started_at"`
}

type PlanningStreamFrame struct {
	ID      string
	Event   string
	Payload json.RawMessage
}

type PlanningStreamStore struct {
	client *redis.Client
	log    *slog.Logger
	err    error
}

func NewPlanningStreamStore(redisURL string, log *slog.Logger) *PlanningStreamStore {
	redisURL = strings.TrimSpace(redisURL)
	if redisURL == "" {
		return nil
	}
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		if log != nil {
			log.Warn("invalid REDIS_URL; planning stream recovery disabled", "err", err)
		}
		return &PlanningStreamStore{log: log, err: err}
	}
	return &PlanningStreamStore{client: redis.NewClient(opts), log: log}
}

func (s *PlanningStreamStore) Ping(ctx context.Context) error {
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

func (s *PlanningStreamStore) Enabled() bool {
	return s != nil && s.client != nil
}

func (s *PlanningStreamStore) SetActive(ctx context.Context, active PlanningActiveStream) error {
	if !s.Enabled() || strings.TrimSpace(active.ConversationID) == "" || strings.TrimSpace(active.RunID) == "" {
		return nil
	}
	active.StartedAt = active.StartedAt.UTC()
	raw, err := json.Marshal(active)
	if err != nil {
		return err
	}
	if err := s.client.Set(ctx, planningActiveStreamKey(active.ConversationID), raw, planningStreamTTL).Err(); err != nil {
		if s.log != nil {
			s.log.Warn("set planning active stream", "conversation", active.ConversationID, "err", err)
		}
		return err
	}
	return nil
}

func (s *PlanningStreamStore) Active(ctx context.Context, conversationID string) (*PlanningActiveStream, bool) {
	if !s.Enabled() || strings.TrimSpace(conversationID) == "" {
		return nil, false
	}
	raw, err := s.client.Get(ctx, planningActiveStreamKey(conversationID)).Bytes()
	if err != nil {
		return nil, false
	}
	var active PlanningActiveStream
	if err := json.Unmarshal(raw, &active); err != nil {
		return nil, false
	}
	if active.RunID == "" {
		return nil, false
	}
	return &active, true
}

func (s *PlanningStreamStore) ClearActive(ctx context.Context, conversationID, runID string) {
	if !s.Enabled() || strings.TrimSpace(conversationID) == "" {
		return
	}
	active, ok := s.Active(ctx, conversationID)
	if ok && runID != "" && active.RunID != runID {
		return
	}
	if err := s.client.Del(ctx, planningActiveStreamKey(conversationID)).Err(); err != nil && s.log != nil {
		s.log.Warn("clear planning active stream", "conversation", conversationID, "err", err)
	}
}

func (s *PlanningStreamStore) Append(ctx context.Context, conversationID, runID, event string, payload any) (string, error) {
	if !s.Enabled() || strings.TrimSpace(runID) == "" || strings.TrimSpace(event) == "" {
		return "", nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	key := planningRunStreamKey(runID)
	id, err := s.client.XAdd(ctx, &redis.XAddArgs{
		Stream: key,
		Values: map[string]any{
			"event":   event,
			"payload": string(raw),
		},
	}).Result()
	if err != nil {
		if s.log != nil {
			s.log.Warn("append planning stream frame", "run", runID, "event", event, "err", err)
		}
		return "", err
	}
	pipe := s.client.Pipeline()
	pipe.Expire(ctx, key, planningStreamTTL)
	if conversationID != "" {
		pipe.Expire(ctx, planningActiveStreamKey(conversationID), planningStreamTTL)
	}
	_, _ = pipe.Exec(ctx)
	return id, nil
}

func (s *PlanningStreamStore) Read(ctx context.Context, runID, afterID string, block time.Duration, count int64) ([]PlanningStreamFrame, error) {
	if !s.Enabled() || strings.TrimSpace(runID) == "" {
		return nil, redis.Nil
	}
	if strings.TrimSpace(afterID) == "" {
		afterID = "0"
	}
	if count <= 0 {
		count = 32
	}
	streams, err := s.client.XRead(ctx, &redis.XReadArgs{
		Streams: []string{planningRunStreamKey(runID), afterID},
		Block:   block,
		Count:   count,
	}).Result()
	if err != nil {
		return nil, err
	}
	var out []PlanningStreamFrame
	for _, stream := range streams {
		for _, msg := range stream.Messages {
			event, _ := msg.Values["event"].(string)
			payloadString, _ := msg.Values["payload"].(string)
			if event == "" || payloadString == "" {
				continue
			}
			out = append(out, PlanningStreamFrame{
				ID:      msg.ID,
				Event:   event,
				Payload: json.RawMessage(payloadString),
			})
		}
	}
	return out, nil
}

func planningActiveStreamKey(conversationID string) string {
	return "debate-bot:planning:active:" + conversationID
}

func planningRunStreamKey(runID string) string {
	return "debate-bot:planning:stream:" + runID
}
