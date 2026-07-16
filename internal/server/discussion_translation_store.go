package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/summarizer"
)

type DiscussionTranslationStatus string

const (
	DiscussionTranslationGenerating DiscussionTranslationStatus = "generating"
	DiscussionTranslationReady      DiscussionTranslationStatus = "ready"
	DiscussionTranslationFailed     DiscussionTranslationStatus = "failed"
)

// DiscussionTranslationMeta is safe to expose to every viewer who can see the
// source podcast. The translated content remains on the language-aware content
// endpoints and is never included in list rows.
type DiscussionTranslationMeta struct {
	Language    string                      `json:"language"`
	Status      DiscussionTranslationStatus `json:"status"`
	Available   bool                        `json:"available"`
	Pending     bool                        `json:"pending,omitempty"`
	Error       string                      `json:"error,omitempty"`
	GeneratedAt *time.Time                  `json:"generated_at,omitempty"`
}

// DiscussionTranslationBundle is a presentation-only copy. Stable identifiers,
// media URLs, timing, model/voice fields, and source URLs are copied unchanged;
// only human-readable strings are translated by the generator.
type DiscussionTranslationBundle struct {
	Language        string                  `json:"language"`
	Title           string                  `json:"title,omitempty"`
	Topic           string                  `json:"topic,omitempty"`
	Markdown        string                  `json:"markdown,omitempty"`
	Script          *config.DebateTopic     `json:"script,omitempty"`
	Lines           []DiscussionLine        `json:"lines,omitempty"`
	CaptionsVTT     string                  `json:"captions_vtt,omitempty"`
	SummaryMarkdown string                  `json:"summary_markdown,omitempty"`
	TextMarkdown    string                  `json:"text_markdown,omitempty"`
	Mindmap         *summarizer.MindmapSpec `json:"mindmap,omitempty"`
}

type DiscussionTranslation struct {
	DiscussionID string
	Language     string
	Status       DiscussionTranslationStatus
	Bundle       DiscussionTranslationBundle
	Model        string
	Error        string
	Usage        SummaryUsage
	Attempts     int
	ClaimedAt    time.Time
	GeneratedAt  time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (t *DiscussionTranslation) Meta() DiscussionTranslationMeta {
	meta := DiscussionTranslationMeta{
		Language:  t.Language,
		Status:    t.Status,
		Available: t.Status == DiscussionTranslationReady,
		Pending:   t.Status == DiscussionTranslationGenerating,
		Error:     t.Error,
	}
	if !t.GeneratedAt.IsZero() {
		v := t.GeneratedAt
		meta.GeneratedAt = &v
	}
	return meta
}

func normalizeTranslationLanguage(language string) string {
	return strings.TrimSpace(language)
}

func (s *DiscussionStore) BeginTranslation(ctx context.Context, discussionID, language, model string) error {
	language = normalizeTranslationLanguage(language)
	if language == "" {
		return errors.New("translation language is required")
	}
	now := time.Now().UnixMilli()
	_, err := s.exec(ctx, `INSERT INTO native_discussion_translations
		(discussion_id, language, status, bundle_json, model, error, created_at, updated_at)
		VALUES (?, ?, ?, '', ?, '', ?, ?)
		ON CONFLICT(discussion_id, language) DO UPDATE SET
			status = excluded.status, bundle_json = '', model = excluded.model,
			error = '', prompt_tokens = 0, completion_tokens = 0, total_tokens = 0,
			llm_cost_usd = 0, attempts = 0, claimed_at = 0, generated_at = 0,
			updated_at = excluded.updated_at`,
		discussionID, language, string(DiscussionTranslationGenerating), model, now, now)
	return err
}

func (s *DiscussionStore) ClaimTranslationRun(ctx context.Context, discussionID, language string, attempt int, staleAfter time.Duration) (bool, error) {
	now := time.Now().UnixMilli()
	res, err := s.exec(ctx, `UPDATE native_discussion_translations
		SET attempts = ?, claimed_at = ?, updated_at = ?
		WHERE discussion_id = ? AND language = ? AND status = ?
		AND (attempts < ? OR (attempts = ? AND claimed_at < ?))`,
		attempt, now, now, discussionID, normalizeTranslationLanguage(language),
		string(DiscussionTranslationGenerating), attempt, attempt, now-staleAfter.Milliseconds())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *DiscussionStore) SaveTranslation(ctx context.Context, discussionID, language string, bundle DiscussionTranslationBundle, model string, usage SummaryUsage) error {
	bundle.Language = normalizeTranslationLanguage(language)
	raw, err := json.Marshal(bundle)
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	_, err = s.exec(ctx, `UPDATE native_discussion_translations SET
		status = ?, bundle_json = ?, model = ?, error = '', prompt_tokens = ?,
		completion_tokens = ?, total_tokens = ?, llm_cost_usd = ?, generated_at = ?, updated_at = ?
		WHERE discussion_id = ? AND language = ?`,
		string(DiscussionTranslationReady), string(raw), model, usage.PromptTokens,
		usage.CompletionTokens, usage.TotalTokens, usage.LLMCostUSD, now, now,
		discussionID, bundle.Language)
	return err
}

func (s *DiscussionStore) FailTranslation(ctx context.Context, discussionID, language, message string) error {
	_, err := s.exec(ctx, `UPDATE native_discussion_translations
		SET status = ?, error = ?, updated_at = ? WHERE discussion_id = ? AND language = ?`,
		string(DiscussionTranslationFailed), truncateSummaryError(message), time.Now().UnixMilli(),
		discussionID, normalizeTranslationLanguage(language))
	return err
}

func (s *DiscussionStore) TranslationFor(ctx context.Context, discussionID, language string) (*DiscussionTranslation, error) {
	row := s.db.QueryRowContext(ctx, `SELECT discussion_id, language, status, bundle_json, model, error,
		prompt_tokens, completion_tokens, total_tokens, llm_cost_usd, attempts, claimed_at,
		generated_at, created_at, updated_at
		FROM native_discussion_translations WHERE discussion_id = ? AND language = ?`,
		discussionID, normalizeTranslationLanguage(language))
	return scanDiscussionTranslation(row)
}

func (s *DiscussionStore) TranslationForJob(ctx context.Context, jobID, language string) (*DiscussionTranslation, error) {
	row := s.db.QueryRowContext(ctx, `SELECT t.discussion_id, t.language, t.status, t.bundle_json, t.model, t.error,
		t.prompt_tokens, t.completion_tokens, t.total_tokens, t.llm_cost_usd, t.attempts, t.claimed_at,
		t.generated_at, t.created_at, t.updated_at
		FROM native_discussion_translations t JOIN native_discussions d ON d.id = t.discussion_id
		WHERE d.job_id = ? AND t.language = ?`, jobID, normalizeTranslationLanguage(language))
	return scanDiscussionTranslation(row)
}

func (s *DiscussionStore) ListTranslations(ctx context.Context, discussionID string) ([]DiscussionTranslationMeta, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT discussion_id, language, status, bundle_json, model, error,
		prompt_tokens, completion_tokens, total_tokens, llm_cost_usd, attempts, claimed_at,
		generated_at, created_at, updated_at
		FROM native_discussion_translations WHERE discussion_id = ? ORDER BY language`, discussionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DiscussionTranslationMeta
	for rows.Next() {
		t, err := scanDiscussionTranslation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t.Meta())
	}
	return out, rows.Err()
}

type translationScanner interface{ Scan(...any) error }

func scanDiscussionTranslation(row translationScanner) (*DiscussionTranslation, error) {
	var t DiscussionTranslation
	var status, bundleJSON string
	var claimedAt, generatedAt, createdAt, updatedAt int64
	err := row.Scan(&t.DiscussionID, &t.Language, &status, &bundleJSON, &t.Model, &t.Error,
		&t.Usage.PromptTokens, &t.Usage.CompletionTokens, &t.Usage.TotalTokens, &t.Usage.LLMCostUSD,
		&t.Attempts, &claimedAt, &generatedAt, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.Status = DiscussionTranslationStatus(status)
	if strings.TrimSpace(bundleJSON) != "" {
		if err := json.Unmarshal([]byte(bundleJSON), &t.Bundle); err != nil {
			return nil, err
		}
	}
	if claimedAt > 0 {
		t.ClaimedAt = time.UnixMilli(claimedAt)
	}
	if generatedAt > 0 {
		t.GeneratedAt = time.UnixMilli(generatedAt)
	}
	t.CreatedAt = time.UnixMilli(createdAt)
	t.UpdatedAt = time.UnixMilli(updatedAt)
	return &t, nil
}

func applyTranslationBundle(d *Discussion, bundle DiscussionTranslationBundle) {
	if d == nil {
		return
	}
	d.MainLanguage = d.Language
	if bundle.Language != "" {
		d.Language = bundle.Language
	}
	if bundle.Title != "" {
		d.Title = bundle.Title
	}
	if bundle.Topic != "" {
		d.Topic = bundle.Topic
	}
	if bundle.Markdown != "" {
		d.Markdown = bundle.Markdown
	}
	if bundle.Script != nil {
		d.Script = bundle.Script
	}
	if bundle.Lines != nil {
		d.Lines = bundle.Lines
	}
}
