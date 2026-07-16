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
	// Cover is the language-dedicated cover art, present only when one has been
	// set; viewers fall back to the podcast's default cover otherwise.
	Cover *DiscussionCover `json:"cover,omitempty"`
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
	// Cover is stored in dedicated columns, not in bundle_json, so re-running a
	// translation (which rewrites the bundle) can never clobber the cover art.
	Cover       DiscussionCover
	Model       string
	Error       string
	Usage       SummaryUsage
	Attempts    int
	ClaimedAt   time.Time
	GeneratedAt time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
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
	if t.Cover.Valid() {
		cover := t.Cover
		meta.Cover = &cover
	}
	return meta
}

func normalizeTranslationLanguage(language string) string {
	return strings.TrimSpace(language)
}

// BeginTranslation resets the row for a fresh run. The cover_* columns are
// deliberately left untouched: re-translating a language keeps its cover art.
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

// SetTranslationCover persists (or, given an empty cover, clears) the
// language-dedicated cover art on an existing translation row of an owned
// discussion. It returns (nil, nil) when the translation does not exist or the
// caller does not own the discussion.
func (s *DiscussionStore) SetTranslationCover(ctx context.Context, owner, discussionID, language string, cover DiscussionCover) (*DiscussionTranslation, error) {
	language = normalizeTranslationLanguage(language)
	if language == "" {
		return nil, errors.New("translation language is required")
	}
	res, err := s.exec(ctx, `UPDATE native_discussion_translations SET
		cover_type = ?, cover_image_url = ?, cover_image_key = ?,
		cover_gradient_start = ?, cover_gradient_end = ?, cover_prompt = ?, updated_at = ?
		WHERE discussion_id = ? AND language = ?
		AND EXISTS (SELECT 1 FROM native_discussions WHERE id = ? AND owner_user_id = ?)`,
		strings.TrimSpace(cover.Type), storedCoverImageURL(cover), strings.TrimSpace(cover.ImageKey),
		strings.TrimSpace(cover.GradientStart), strings.TrimSpace(cover.GradientEnd), strings.TrimSpace(cover.Prompt),
		time.Now().UnixMilli(), discussionID, language, discussionID, owner)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil
	}
	return s.TranslationFor(ctx, discussionID, language)
}

func (s *DiscussionStore) TranslationFor(ctx context.Context, discussionID, language string) (*DiscussionTranslation, error) {
	row := s.db.QueryRowContext(ctx, `SELECT discussion_id, language, status, bundle_json, model, error,
		prompt_tokens, completion_tokens, total_tokens, llm_cost_usd, attempts, claimed_at,
		generated_at, created_at, updated_at,
		cover_type, cover_image_url, cover_image_key, cover_gradient_start, cover_gradient_end, cover_prompt
		FROM native_discussion_translations WHERE discussion_id = ? AND language = ?`,
		discussionID, normalizeTranslationLanguage(language))
	return scanDiscussionTranslation(row)
}

// ReadyTranslation pairs a ready translation's presentation bundle with its
// language-dedicated cover art (zero-valued when the language has none).
type ReadyTranslation struct {
	Bundle DiscussionTranslationBundle
	Cover  DiscussionCover
}

// ReadyTranslationBundles returns the ready presentation bundles (and their
// language covers) for one language across a page of discussions. List
// endpoints use this instead of issuing one translation query per row.
func (s *DiscussionStore) ReadyTranslationBundles(ctx context.Context, discussionIDs []string, language string) (map[string]ReadyTranslation, error) {
	out := make(map[string]ReadyTranslation)
	language = normalizeTranslationLanguage(language)
	if s == nil || language == "" {
		return out, nil
	}

	ids := make([]string, 0, len(discussionIDs))
	seen := make(map[string]bool, len(discussionIDs))
	for _, id := range discussionIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return out, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+2)
	args = append(args, language, string(DiscussionTranslationReady))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT discussion_id, bundle_json,
		cover_type, cover_image_url, cover_image_key, cover_gradient_start, cover_gradient_end, cover_prompt
		FROM native_discussion_translations
		WHERE language = ? AND status = ? AND discussion_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var discussionID, bundleJSON string
		var rt ReadyTranslation
		if err := rows.Scan(&discussionID, &bundleJSON,
			&rt.Cover.Type, &rt.Cover.ImageURL, &rt.Cover.ImageKey,
			&rt.Cover.GradientStart, &rt.Cover.GradientEnd, &rt.Cover.Prompt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(bundleJSON), &rt.Bundle); err != nil {
			return nil, err
		}
		out[discussionID] = rt
	}
	return out, rows.Err()
}

func (s *DiscussionStore) TranslationForJob(ctx context.Context, jobID, language string) (*DiscussionTranslation, error) {
	row := s.db.QueryRowContext(ctx, `SELECT t.discussion_id, t.language, t.status, t.bundle_json, t.model, t.error,
		t.prompt_tokens, t.completion_tokens, t.total_tokens, t.llm_cost_usd, t.attempts, t.claimed_at,
		t.generated_at, t.created_at, t.updated_at,
		t.cover_type, t.cover_image_url, t.cover_image_key, t.cover_gradient_start, t.cover_gradient_end, t.cover_prompt
		FROM native_discussion_translations t JOIN native_discussions d ON d.id = t.discussion_id
		WHERE d.job_id = ? AND t.language = ?`, jobID, normalizeTranslationLanguage(language))
	return scanDiscussionTranslation(row)
}

func (s *DiscussionStore) ListTranslations(ctx context.Context, discussionID string) ([]DiscussionTranslationMeta, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT discussion_id, language, status, bundle_json, model, error,
		prompt_tokens, completion_tokens, total_tokens, llm_cost_usd, attempts, claimed_at,
		generated_at, created_at, updated_at,
		cover_type, cover_image_url, cover_image_key, cover_gradient_start, cover_gradient_end, cover_prompt
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
		&t.Attempts, &claimedAt, &generatedAt, &createdAt, &updatedAt,
		&t.Cover.Type, &t.Cover.ImageURL, &t.Cover.ImageKey,
		&t.Cover.GradientStart, &t.Cover.GradientEnd, &t.Cover.Prompt)
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
