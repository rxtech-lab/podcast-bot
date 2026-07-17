package server

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// AgentDocument is a Markdown document written by the Q&A agent. A nil
// DiscussionID makes it a global document; otherwise it belongs to exactly one
// owned podcast.
type AgentDocument struct {
	ID                   string    `json:"id"`
	Title                string    `json:"title"`
	Markdown             string    `json:"markdown,omitempty"`
	DiscussionID         *string   `json:"discussion_id,omitempty"`
	PodcastTitle         string    `json:"podcast_title,omitempty"`
	SourceConversationID string    `json:"-"`
	SourceToolCallID     string    `json:"-"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// AgentDocumentStore owns persistent chat-authored documents on the shared
// application database.
type AgentDocumentStore struct {
	db *sqlDB
}

const (
	defaultAgentDocumentPageSize = 20
	maxAgentDocumentPageSize     = 100
)

func NewAgentDocumentStore(ds *DiscussionStore) (*AgentDocumentStore, error) {
	if ds == nil || ds.db == nil {
		return nil, errors.New("agent document store requires a discussion store")
	}
	s := &AgentDocumentStore{db: ds.db}
	if err := s.ensureSchema(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *AgentDocumentStore) ensureSchema(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS agent_documents (
			id TEXT PRIMARY KEY,
			owner_user_id TEXT NOT NULL,
			discussion_id TEXT,
			source_conversation_id TEXT NOT NULL DEFAULT '',
			source_tool_call_id TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL,
			markdown TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			FOREIGN KEY(discussion_id) REFERENCES native_discussions(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS agent_documents_owner_global_idx
			ON agent_documents(owner_user_id, updated_at DESC) WHERE discussion_id IS NULL`,
		`CREATE INDEX IF NOT EXISTS agent_documents_owner_discussion_idx
			ON agent_documents(owner_user_id, discussion_id, updated_at DESC) WHERE discussion_id IS NOT NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS agent_documents_tool_call_idx
			ON agent_documents(source_conversation_id, source_tool_call_id)
			WHERE source_conversation_id != '' AND source_tool_call_id != ''`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// Create persists a document once per conversation/tool-call pair. Replayed QA
// tasks return the original row rather than creating duplicate documents.
func (s *AgentDocumentStore) Create(ctx context.Context, owner string, discussionID *string,
	conversationID, toolCallID, title, markdown string) (*AgentDocument, error) {
	if s == nil {
		return nil, errors.New("agent document store is not configured")
	}
	owner = strings.TrimSpace(owner)
	title = strings.TrimSpace(title)
	markdown = strings.TrimSpace(markdown)
	conversationID = strings.TrimSpace(conversationID)
	toolCallID = strings.TrimSpace(toolCallID)
	if owner == "" || title == "" || markdown == "" {
		return nil, errors.New("owner, title, and markdown are required")
	}
	var linked any
	if discussionID != nil && strings.TrimSpace(*discussionID) != "" {
		id := strings.TrimSpace(*discussionID)
		linked = id
		discussionID = &id
	}
	now := time.Now().UnixMilli()
	_, err := s.db.ExecContext(ctx, `INSERT INTO agent_documents
		(id, owner_user_id, discussion_id, source_conversation_id, source_tool_call_id,
		 title, markdown, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING`, newJobID(), owner, linked, conversationID, toolCallID,
		title, markdown, now, now)
	if err != nil {
		return nil, err
	}
	if conversationID != "" && toolCallID != "" {
		return s.byToolCall(ctx, owner, conversationID, toolCallID)
	}
	var id string
	err = s.db.QueryRowContext(ctx, `SELECT id FROM agent_documents
		WHERE owner_user_id = ? AND created_at = ? AND title = ?
		ORDER BY id DESC LIMIT 1`, owner, now, title).Scan(&id)
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, owner, id)
}

func (s *AgentDocumentStore) byToolCall(ctx context.Context, owner, conversationID, toolCallID string) (*AgentDocument, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM agent_documents
		WHERE owner_user_id = ? AND source_conversation_id = ? AND source_tool_call_id = ?`,
		owner, conversationID, toolCallID).Scan(&id)
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, owner, id)
}

// List returns content-free rows for one owner. A nil discussionID selects
// global-only documents; a non-nil id selects documents linked to that podcast.
func (s *AgentDocumentStore) List(ctx context.Context, owner string, discussionID *string) ([]AgentDocument, error) {
	if s == nil {
		return []AgentDocument{}, nil
	}
	where := "ad.owner_user_id = ? AND ad.discussion_id IS NULL"
	args := []any{owner}
	if discussionID != nil {
		where = "ad.owner_user_id = ? AND ad.discussion_id = ?"
		args = append(args, strings.TrimSpace(*discussionID))
	}
	rows, err := s.db.QueryContext(ctx, `SELECT ad.id, ad.title, ad.discussion_id,
		COALESCE(d.title, ''), ad.created_at, ad.updated_at
		FROM agent_documents ad
		LEFT JOIN native_discussions d ON d.id = ad.discussion_id
		WHERE `+where+` ORDER BY ad.updated_at DESC, ad.id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AgentDocument, 0)
	for rows.Next() {
		doc, err := scanAgentDocument(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, *doc)
	}
	return out, rows.Err()
}

// ListAllPage returns one owner's global and podcast-linked documents in a
// stable grouped order: global documents first, then one section per podcast.
// Fetching one extra row lets the caller expose pagination without a separate
// count query.
func (s *AgentDocumentStore) ListAllPage(ctx context.Context, owner, query string, limit, offset int) ([]AgentDocument, bool, error) {
	if s == nil {
		return []AgentDocument{}, false, nil
	}
	if limit <= 0 {
		limit = defaultAgentDocumentPageSize
	}
	if limit > maxAgentDocumentPageSize {
		limit = maxAgentDocumentPageSize
	}
	if offset < 0 {
		offset = 0
	}
	where := "ad.owner_user_id = ?"
	args := []any{owner}
	if query = strings.TrimSpace(query); query != "" {
		pattern := "%" + escapeLike(strings.ToLower(query)) + "%"
		where += ` AND (
			LOWER(ad.title) LIKE ? ESCAPE '\' OR
			LOWER(ad.markdown) LIKE ? ESCAPE '\' OR
			LOWER(COALESCE(d.title, '')) LIKE ? ESCAPE '\')`
		args = append(args, pattern, pattern, pattern)
	}
	args = append(args, limit+1, offset)
	rows, err := s.db.QueryContext(ctx, `SELECT ad.id, ad.title, ad.discussion_id,
		COALESCE(d.title, ''), ad.created_at, ad.updated_at
		FROM agent_documents ad
		LEFT JOIN native_discussions d ON d.id = ad.discussion_id
		WHERE `+where+`
		ORDER BY CASE WHEN ad.discussion_id IS NULL THEN 0 ELSE 1 END,
			LOWER(COALESCE(d.title, '')), ad.discussion_id,
			ad.updated_at DESC, ad.id DESC
		LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := make([]AgentDocument, 0, limit+1)
	for rows.Next() {
		doc, err := scanAgentDocument(rows, false)
		if err != nil {
			return nil, false, err
		}
		out = append(out, *doc)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return out, hasMore, nil
}

// Delete removes one document only when it belongs to owner.
func (s *AgentDocumentStore) Delete(ctx context.Context, owner, id string) (bool, error) {
	if s == nil {
		return false, nil
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM agent_documents
		WHERE owner_user_id = ? AND id = ?`, strings.TrimSpace(owner), strings.TrimSpace(id))
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

func (s *AgentDocumentStore) CountForDiscussion(ctx context.Context, owner, discussionID string) (int, error) {
	if s == nil {
		return 0, nil
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_documents
		WHERE owner_user_id = ? AND discussion_id = ?`, owner, discussionID).Scan(&count)
	return count, err
}

func (s *AgentDocumentStore) Get(ctx context.Context, owner, id string) (*AgentDocument, error) {
	if s == nil {
		return nil, nil
	}
	row := s.db.QueryRowContext(ctx, `SELECT ad.id, ad.title, ad.markdown,
		ad.discussion_id, COALESCE(d.title, ''), ad.source_conversation_id,
		ad.source_tool_call_id, ad.created_at, ad.updated_at
		FROM agent_documents ad
		LEFT JOIN native_discussions d ON d.id = ad.discussion_id
		WHERE ad.owner_user_id = ? AND ad.id = ?`, owner, id)
	doc, err := scanAgentDocument(row, true)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return doc, err
}

func scanAgentDocument(row interface{ Scan(...any) error }, includesMarkdown bool) (*AgentDocument, error) {
	var (
		doc       AgentDocument
		linkedID  sql.NullString
		createdAt int64
		updatedAt int64
	)
	var err error
	if includesMarkdown {
		err = row.Scan(&doc.ID, &doc.Title, &doc.Markdown, &linkedID, &doc.PodcastTitle,
			&doc.SourceConversationID, &doc.SourceToolCallID, &createdAt, &updatedAt)
	} else {
		err = row.Scan(&doc.ID, &doc.Title, &linkedID, &doc.PodcastTitle, &createdAt, &updatedAt)
	}
	if err != nil {
		return nil, err
	}
	if linkedID.Valid {
		id := linkedID.String
		doc.DiscussionID = &id
	}
	doc.CreatedAt = time.UnixMilli(createdAt)
	doc.UpdatedAt = time.UnixMilli(updatedAt)
	return &doc, nil
}
