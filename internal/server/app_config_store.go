package server

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// App-level configuration keys. Model values are stored only here; process
// environment variables never provide model ids or fallbacks.
const (
	appConfigKeyDefaultHostModel  = "default_host_model"
	appConfigKeyScenePlannerModel = "scene_planner_model"
	appConfigKeyCompressionModel  = "compression_model"
	appConfigKeySummaryModel      = "podcast_summary_model"
	appConfigKeyTranslationModel  = "translation_model"
	appConfigKeyJudgementModel    = "judgement_model"
	appConfigKeySummaryPPTModel   = "podcast_summary_ppt_model"
	// appConfigKeySTTProvider picks the speech-to-text provider used to
	// transcribe uploaded podcast audio (stt.ProviderGemini / stt.ProviderAzure).
	appConfigKeySTTProvider = "stt_provider"
	// appConfigKeySTTGeminiModel picks the Gemini model used when the STT
	// provider is gemini.
	appConfigKeySTTGeminiModel = "stt_gemini_model"
	// appConfigKeyQAModel picks the LLM behind the podcast Q&A / global chat
	// agent.
	appConfigKeyQAModel = "qa_model"
	// appConfigKeyEmbeddingModel picks the embedding model used to vectorize
	// podcast content and search queries. Switching it marks previously indexed
	// chunks stale (they are keyed by model) and the precheck backfill re-indexes
	// them.
	appConfigKeyEmbeddingModel = "embedding_model"
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

// Get returns the stored value for key. The bool is false when no row exists.
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

// Set upserts an admin-owned value for key.
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

// SeedE2EModels writes deterministic model assignments into the same
// admin-owned table used in production. It is only called by the hermetic E2E
// bootstrap and never overwrites a value, so E2E exercises the real ownership
// path without model environment variables.
func (s *AppConfigStore) SeedE2EModels(ctx context.Context) error {
	values := map[string]string{
		appConfigKeyDefaultHostModel:  "e2e-fake-model",
		appConfigKeyScenePlannerModel: "e2e-fake-model",
		appConfigKeyCompressionModel:  "e2e-fake-model",
		appConfigKeySummaryModel:      "e2e-fake-model",
		appConfigKeyTranslationModel:  "e2e-fake-model",
		appConfigKeyJudgementModel:    "e2e-fake-model",
		appConfigKeySummaryPPTModel:   "e2e-fake-model",
		appConfigKeyQAModel:           "e2e-fake-model",
		appConfigKeyEmbeddingModel:    "e2e-fake-embedding",
		appConfigKeySTTGeminiModel:    "e2e-fake-model",
	}
	for key, value := range values {
		if _, ok, err := s.Get(ctx, key); err != nil {
			return err
		} else if ok {
			continue
		}
		if err := s.Set(ctx, key, value); err != nil {
			return err
		}
	}
	return nil
}
