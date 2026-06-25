package server

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// SummaryStatus is the lifecycle of a generated summary document.
type SummaryStatus string

const (
	SummaryGenerating SummaryStatus = "generating"
	SummaryReadyState SummaryStatus = "ready"
	SummaryFailed     SummaryStatus = "failed"
)

// SummaryDocTypeSummary is the default document type — a Markdown summary. Future
// kinds (e.g. a slide deck) get their own doc_type and reuse this table.
const SummaryDocTypeSummary = "summary"

// SummaryMeta is the content-free descriptor returned on the discussion detail
// payload. Markdown is intentionally excluded — clients fetch the body separately
// from the summary content endpoint only when the summary view mounts.
type SummaryMeta struct {
	DocType     string        `json:"doc_type"`
	Status      SummaryStatus `json:"status,omitempty"`
	Available   bool          `json:"available"`
	Pending     bool          `json:"pending,omitempty"`
	Generation  bool          `json:"generation,omitempty"`
	GeneratedAt *time.Time    `json:"generated_at,omitempty"`
}

// SummaryDocument is the full summary payload served by the content endpoint.
type SummaryDocument struct {
	DocType     string        `json:"doc_type"`
	Status      SummaryStatus `json:"status"`
	Markdown    string        `json:"markdown"`
	GeneratedAt *time.Time    `json:"generated_at,omitempty"`
}

// SummaryUsage carries the metered LLM usage of a summary run so it is persisted
// alongside the document (kept out of client payloads, like other usage figures).
type SummaryUsage struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	LLMCostUSD       float64
}

func normalizeDocType(docType string) string {
	docType = strings.TrimSpace(docType)
	if docType == "" {
		return SummaryDocTypeSummary
	}
	return docType
}

// OwnerOf returns the owner user id of a discussion, or "" when it does not
// exist. Used by background billing where only the discussion id is in scope.
func (s *DiscussionStore) OwnerOf(ctx context.Context, discussionID string) (string, error) {
	if s == nil {
		return "", errors.New("discussion store is not configured")
	}
	var owner string
	err := s.db.QueryRowContext(ctx, `SELECT owner_user_id FROM native_discussions WHERE id = ?`, discussionID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return owner, err
}

// SummaryMetaFor returns the content-free summary descriptor for a discussion, or
// nil when no summary row exists yet (so the detail payload omits `summary`).
func (s *DiscussionStore) SummaryMetaFor(ctx context.Context, discussionID, docType string) (*SummaryMeta, error) {
	if s == nil {
		return nil, nil
	}
	docType = normalizeDocType(docType)
	var status string
	var generatedAt int64
	err := s.db.QueryRowContext(ctx,
		`SELECT status, generated_at FROM native_discussion_summaries WHERE discussion_id = ? AND doc_type = ?`,
		discussionID, docType).Scan(&status, &generatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	meta := &SummaryMeta{
		DocType:   docType,
		Status:    SummaryStatus(status),
		Available: SummaryStatus(status) == SummaryReadyState,
		Pending:   SummaryStatus(status) == SummaryGenerating,
	}
	if generatedAt > 0 {
		t := time.UnixMilli(generatedAt)
		meta.GeneratedAt = &t
	}
	return meta, nil
}

// SummaryDocumentFor returns the full summary (including Markdown) for a
// discussion, or nil when no summary row exists.
func (s *DiscussionStore) SummaryDocumentFor(ctx context.Context, discussionID, docType string) (*SummaryDocument, error) {
	if s == nil {
		return nil, nil
	}
	docType = normalizeDocType(docType)
	var status, markdown string
	var generatedAt int64
	err := s.db.QueryRowContext(ctx,
		`SELECT status, markdown, generated_at FROM native_discussion_summaries WHERE discussion_id = ? AND doc_type = ?`,
		discussionID, docType).Scan(&status, &markdown, &generatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	doc := &SummaryDocument{
		DocType:  docType,
		Status:   SummaryStatus(status),
		Markdown: markdown,
	}
	if generatedAt > 0 {
		t := time.UnixMilli(generatedAt)
		doc.GeneratedAt = &t
	}
	return doc, nil
}

// SummaryStatusFor returns the current status of a summary document and whether a
// row exists, so the trigger path can avoid regenerating an existing summary.
func (s *DiscussionStore) SummaryStatusFor(ctx context.Context, discussionID, docType string) (SummaryStatus, bool, error) {
	if s == nil {
		return "", false, nil
	}
	docType = normalizeDocType(docType)
	var status string
	err := s.db.QueryRowContext(ctx,
		`SELECT status FROM native_discussion_summaries WHERE discussion_id = ? AND doc_type = ?`,
		discussionID, docType).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return SummaryStatus(status), true, nil
}

// BeginSummary upserts a summary row in the generating state, clearing any prior
// error/markdown, so the detail payload reflects that a summary is on the way.
func (s *DiscussionStore) BeginSummary(ctx context.Context, discussionID, docType, model string) error {
	if s == nil {
		return errors.New("discussion store is not configured")
	}
	docType = normalizeDocType(docType)
	now := time.Now().UnixMilli()
	_, err := s.db.ExecContext(ctx, `INSERT INTO native_discussion_summaries
		(discussion_id, doc_type, status, markdown, model, error, generated_at, created_at, updated_at)
		VALUES (?, ?, ?, '', ?, '', 0, ?, ?)
		ON CONFLICT(discussion_id, doc_type) DO UPDATE SET
			status = excluded.status, markdown = '', model = excluded.model,
			error = '', updated_at = excluded.updated_at`,
		discussionID, docType, string(SummaryGenerating), model, now, now)
	return err
}

// SaveSummary stores the finished Markdown and marks the document ready, stamping
// generated_at and recording the run's metered usage.
func (s *DiscussionStore) SaveSummary(ctx context.Context, discussionID, docType, markdown, model string, usage SummaryUsage) error {
	if s == nil {
		return errors.New("discussion store is not configured")
	}
	docType = normalizeDocType(docType)
	now := time.Now().UnixMilli()
	_, err := s.db.ExecContext(ctx, `INSERT INTO native_discussion_summaries
		(discussion_id, doc_type, status, markdown, model, error,
		 prompt_tokens, completion_tokens, total_tokens, llm_cost_usd, generated_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(discussion_id, doc_type) DO UPDATE SET
			status = excluded.status, markdown = excluded.markdown, model = excluded.model, error = '',
			prompt_tokens = excluded.prompt_tokens, completion_tokens = excluded.completion_tokens,
			total_tokens = excluded.total_tokens, llm_cost_usd = excluded.llm_cost_usd,
			generated_at = excluded.generated_at, updated_at = excluded.updated_at`,
		discussionID, docType, string(SummaryReadyState), markdown, model,
		usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, usage.LLMCostUSD, now, now, now)
	return err
}

// FailSummary marks a summary row failed with a short error message so a client
// can show why it is unavailable (and a future retry can overwrite it).
func (s *DiscussionStore) FailSummary(ctx context.Context, discussionID, docType, message string) error {
	if s == nil {
		return errors.New("discussion store is not configured")
	}
	docType = normalizeDocType(docType)
	now := time.Now().UnixMilli()
	_, err := s.db.ExecContext(ctx, `INSERT INTO native_discussion_summaries
		(discussion_id, doc_type, status, markdown, model, error, generated_at, created_at, updated_at)
		VALUES (?, ?, ?, '', '', ?, 0, ?, ?)
		ON CONFLICT(discussion_id, doc_type) DO UPDATE SET
			status = excluded.status, error = excluded.error, updated_at = excluded.updated_at`,
		discussionID, docType, string(SummaryFailed), truncateSummaryError(message), now, now)
	return err
}

func truncateSummaryError(message string) string {
	message = strings.TrimSpace(message)
	const max = 500
	if len(message) > max {
		return message[:max]
	}
	return message
}
