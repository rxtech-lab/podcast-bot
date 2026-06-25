package server

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

const (
	PushEnvironmentSandbox    = "sandbox"
	PushEnvironmentProduction = "production"
)

type PushToken struct {
	UserID      string
	Token       string
	Environment string
	Platform    string
}

func normalizePushEnvironment(env string) string {
	switch strings.ToLower(strings.TrimSpace(env)) {
	case PushEnvironmentProduction, "prod":
		return PushEnvironmentProduction
	default:
		return PushEnvironmentSandbox
	}
}

func normalizePushToken(token string) string {
	token = strings.TrimSpace(token)
	token = strings.ReplaceAll(token, " ", "")
	token = strings.ReplaceAll(token, "<", "")
	token = strings.ReplaceAll(token, ">", "")
	return strings.ToLower(token)
}

func (s *DiscussionStore) UpsertPushToken(ctx context.Context, userID, token, environment, platform string) error {
	userID = strings.TrimSpace(userID)
	token = normalizePushToken(token)
	environment = normalizePushEnvironment(environment)
	platform = strings.TrimSpace(platform)
	if platform == "" {
		platform = "ios"
	}
	if userID == "" || token == "" {
		return nil
	}
	now := time.Now().UnixMilli()
	_, err := s.exec(ctx, `INSERT INTO user_push_tokens
		(user_id, token, environment, platform, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, token, environment) DO UPDATE SET
			platform = excluded.platform,
			updated_at = excluded.updated_at`,
		userID, token, environment, platform, now, now)
	return err
}

func (s *DiscussionStore) DeletePushToken(ctx context.Context, userID, token, environment string) error {
	userID = strings.TrimSpace(userID)
	token = normalizePushToken(token)
	environment = normalizePushEnvironment(environment)
	if userID == "" || token == "" {
		return nil
	}
	_, err := s.exec(ctx, `DELETE FROM user_push_tokens
		WHERE user_id = ? AND token = ? AND environment = ?`, userID, token, environment)
	return err
}

func (s *DiscussionStore) PushTokensForUser(ctx context.Context, userID, environment string) ([]PushToken, error) {
	userID = strings.TrimSpace(userID)
	environment = normalizePushEnvironment(environment)
	if userID == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT user_id, token, environment, platform
		FROM user_push_tokens
		WHERE user_id = ? AND environment = ?
		ORDER BY updated_at DESC`, userID, environment)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()
	out := make([]PushToken, 0)
	for rows.Next() {
		var tok PushToken
		if err := rows.Scan(&tok.UserID, &tok.Token, &tok.Environment, &tok.Platform); err != nil {
			return nil, err
		}
		out = append(out, tok)
	}
	return out, rows.Err()
}
