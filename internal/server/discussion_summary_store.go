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

// Summary document types stored per discussion.
const (
	SummaryDocTypeSummary = "summary"
	SummaryDocTypePPT     = "ppt"
	// SummaryDocTypeText is the audiobook "text-based content": a readable
	// book rendering of the narration with the generated illustrations inline
	// and a link to the audio at the bottom.
	SummaryDocTypeText = "text"
	// SummaryDocTypeMindmap is the discussion mindmap: a JSON node tree
	// (summarizer.MindmapSpec) stored in the markdown column, editable by the
	// owner after generation.
	SummaryDocTypeMindmap = "mindmap"
)

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

// SummarySearchResult is an owner-scoped summary body with the podcast
// identity the Q&A agent needs for library-wide answers.
type SummarySearchResult struct {
	DiscussionID string
	Title        string
	Markdown     string
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

// DiscussionWithTranscript loads a discussion (unscoped — background workers
// have no requesting user) together with its persisted transcript lines: the
// input rebuild for queued summary/mindmap generation.
func (s *DiscussionStore) DiscussionWithTranscript(ctx context.Context, id string) (*Discussion, error) {
	d, err := s.GetForNotification(ctx, id)
	if err != nil || d == nil {
		return d, err
	}
	lines, err := s.lines(ctx, id)
	if err != nil {
		return nil, err
	}
	d.Lines = lines
	return d, nil
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

// SearchSummaryDocuments returns ready generated summaries owned by owner.
// Supplying discussionID reads that podcast's summary directly. Otherwise the
// query is matched against the podcast title/topic and summary body; a handful
// of low-signal English question words are discarded so natural questions such
// as "what did the episode say about AI safety" still find useful summaries.
func (s *DiscussionStore) SearchSummaryDocuments(ctx context.Context, owner, discussionID, query string, limit int) ([]SummarySearchResult, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 5
	}
	if limit > 10 {
		limit = 10
	}
	where := `d.owner_user_id = ? AND sm.doc_type = ? AND sm.status = ?`
	args := []any{owner, SummaryDocTypeSummary, string(SummaryReadyState)}
	if discussionID = strings.TrimSpace(discussionID); discussionID != "" {
		where += ` AND d.id = ?`
		args = append(args, discussionID)
	} else {
		terms := summarySearchTerms(query)
		if len(terms) == 0 {
			return []SummarySearchResult{}, nil
		}
		clauses := make([]string, 0, len(terms))
		for _, term := range terms {
			pattern := "%" + escapeLike(strings.ToLower(term)) + "%"
			clauses = append(clauses, `(LOWER(d.title) LIKE ? ESCAPE '\' OR LOWER(d.topic) LIKE ? ESCAPE '\' OR LOWER(sm.markdown) LIKE ? ESCAPE '\')`)
			args = append(args, pattern, pattern, pattern)
		}
		where += " AND (" + strings.Join(clauses, " OR ") + ")"
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `SELECT d.id, d.title, sm.markdown
		FROM native_discussion_summaries sm
		JOIN native_discussions d ON d.id = sm.discussion_id
		WHERE `+where+`
		ORDER BY sm.generated_at DESC, d.created_at DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]SummarySearchResult, 0)
	for rows.Next() {
		var result SummarySearchResult
		if err := rows.Scan(&result.DiscussionID, &result.Title, &result.Markdown); err != nil {
			return nil, err
		}
		out = append(out, result)
	}
	return out, rows.Err()
}

func summarySearchTerms(query string) []string {
	stop := map[string]bool{
		"a": true, "about": true, "and": true, "are": true, "did": true,
		"do": true, "episode": true, "for": true, "from": true, "in": true,
		"is": true, "it": true, "my": true, "of": true, "on": true,
		"podcast": true, "say": true, "the": true, "this": true, "to": true,
		"was": true, "what": true, "which": true, "with": true,
	}
	seen := map[string]bool{}
	out := make([]string, 0, 6)
	for _, raw := range strings.Fields(strings.ToLower(strings.TrimSpace(query))) {
		term := strings.Trim(raw, ".,!?;:()[]{}\"'")
		if term == "" || stop[term] || seen[term] {
			continue
		}
		seen[term] = true
		out = append(out, term)
		if len(out) == 6 {
			break
		}
	}
	return out
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
			error = '', attempts = 0, claimed_at = 0, updated_at = excluded.updated_at`,
		discussionID, docType, string(SummaryGenerating), model, now, now)
	return err
}

// ClaimSummaryRun atomically claims one queue-delivered generation attempt
// of a summary-family document (summary, mindmap, exports). The document
// stays `generating` across attempts, so the claim distinguishes a fresh
// attempt (attempts below the delivered attempt) from a crash takeover
// (same attempt, claim older than staleAfter). False means another consumer
// already handled this delivery — or the document reached a terminal state
// — so the caller acks and skips.
func (s *DiscussionStore) ClaimSummaryRun(ctx context.Context, discussionID, docType string, attempt int, staleAfter time.Duration) (bool, error) {
	if s == nil {
		return false, errors.New("discussion store is not configured")
	}
	docType = normalizeDocType(docType)
	now := time.Now().UnixMilli()
	res, err := s.db.ExecContext(ctx, `UPDATE native_discussion_summaries
		SET attempts = ?, claimed_at = ?, updated_at = ?
		WHERE discussion_id = ? AND doc_type = ? AND status = ?
		  AND (attempts < ? OR (attempts = ? AND claimed_at < ?))`,
		attempt, now, now,
		discussionID, docType, string(SummaryGenerating),
		attempt, attempt, now-staleAfter.Milliseconds())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
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

// UpdateSummaryMarkdown replaces only the document body of an existing ready
// row, preserving the model and usage audit columns. Used by user edits (e.g.
// mindmap PUT), which should not look like a fresh generation.
func (s *DiscussionStore) UpdateSummaryMarkdown(ctx context.Context, discussionID, docType, markdown string) error {
	if s == nil {
		return errors.New("discussion store is not configured")
	}
	docType = normalizeDocType(docType)
	res, err := s.db.ExecContext(ctx,
		`UPDATE native_discussion_summaries SET markdown = ?, updated_at = ?
		 WHERE discussion_id = ? AND doc_type = ? AND status = ?`,
		markdown, time.Now().UnixMilli(), discussionID, docType, string(SummaryReadyState))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return errors.New("document is not ready for edits")
	}
	return nil
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
