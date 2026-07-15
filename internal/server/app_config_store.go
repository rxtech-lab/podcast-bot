package server

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// App-level configuration keys. Values are stored as strings and overlaid on
// top of the env defaults by the resolver methods below, so ENV remains the
// default and the admin UI can override without a redeploy.
const (
	appConfigKeyDefaultHostModel = "default_host_model"
	// appConfigKeySTTProvider picks the speech-to-text provider used to
	// transcribe uploaded podcast audio (stt.ProviderGemini / stt.ProviderAzure).
	appConfigKeySTTProvider = "stt_provider"
	// appConfigKeySTTGeminiModel picks the Gemini model used when the STT
	// provider is gemini; empty falls back to the env transcribe model.
	appConfigKeySTTGeminiModel = "stt_gemini_model"
)

// AppConfigStore persists admin-editable, app-level configuration as a simple
// key/value table. It shares the DiscussionStore database handle so it lives in
// the same database as the rest of the app state.
type AppConfigStore struct {
	db *sqlDB
}

// NewAppConfigStore builds the store on the DiscussionStore's shared handle and
// ensures its schema exists.
func NewAppConfigStore(ds *DiscussionStore) (*AppConfigStore, error) {
	if ds == nil || ds.db == nil {
		return nil, errors.New("app config store: nil discussion store")
	}
	s := &AppConfigStore{db: ds.db}
	if err := s.ensureSchema(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *AppConfigStore) ensureSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS app_config (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		updated_at BIGINT NOT NULL
	)`)
	return err
}

// Get returns the stored value for key. The bool is false when no override row
// exists (the caller should fall back to the env default).
func (s *AppConfigStore) Get(ctx context.Context, key string) (string, bool, error) {
	if s == nil {
		return "", false, nil
	}
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM app_config WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

// Set upserts an override value for key.
func (s *AppConfigStore) Set(ctx context.Context, key, value string) error {
	if s == nil {
		return errors.New("app config store: nil")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO app_config (key, value, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, time.Now().UnixMilli())
	return err
}
