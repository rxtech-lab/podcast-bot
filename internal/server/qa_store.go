package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/llm"
	"github.com/sirily11/debate-bot/internal/qa"
)

// QAConversationStatus tracks a Q&A thread's lifecycle.
type QAConversationStatus string

const (
	QAConversationActive QAConversationStatus = "active"
	QAConversationFailed QAConversationStatus = "failed"
)

// QAConversation is one Q&A chat thread: bound to a discussion (podcast Q&A)
// or, with an empty DiscussionID, the user's single global chat.
type QAConversation struct {
	ID            string               `json:"id"`
	DiscussionID  string               `json:"discussion_id,omitempty"`
	OwnerUserID   string               `json:"-"`
	Status        QAConversationStatus `json:"status"`
	PointsCharged int64                `json:"points_charged"`
	FlatCharged   bool                 `json:"-"`
	CreatedAt     time.Time            `json:"created_at"`
	UpdatedAt     time.Time            `json:"updated_at"`
}

// qaTurnRow is the persisted shape of one turn. role ∈
// user|assistant|tool|summary. summary rows are compaction summaries: the
// model sees them as a user message replacing the compacted prefix; the
// client renders them as a subtle "history summarized" chip.
type qaTurnRow struct {
	ID            int64
	Seq           int64
	Role          string
	Text          string
	ToolCallsJSON string
	ToolCallID    string
	ToolName      string
	ResultText    string
	PayloadJSON   string
	IsError       bool
	IsCompacted   bool
	CreatedAt     int64
}

// qaTurnInput is what callers append. OpID is the idempotency key (generated
// when empty), mirroring planning_turns.op_id.
type qaTurnInput struct {
	Role        string
	Text        string
	ToolCalls   []llm.ToolCall
	ToolCallID  string
	ToolName    string
	ResultText  string
	PayloadJSON string
	IsError     bool
	OpID        string
}

// QAPart is one client-facing item: a text bubble, a generic tool card, or a
// dedicated card (podcast/transcript/sources). It reuses the planning part
// field names so the iOS Codable layer is shared with the plan chat.
type QAPart struct {
	Kind string `json:"kind"` // "text" | "tool" | "summary"
	ID   string `json:"id"`
	Role string `json:"role,omitempty"`
	Text string `json:"text,omitempty"`

	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	Status     string          `json:"status,omitempty"` // running|completed|failed
	Input      json.RawMessage `json:"input,omitempty"`
	ResultText string          `json:"result_text,omitempty"`

	// Card is the dedicated-view payload behind show_podcast /
	// show_transcript / show_sources tool calls.
	Card json.RawMessage `json:"card,omitempty"`
}

// QAConversationView is the full client payload for one Q&A thread.
type QAConversationView struct {
	Conversation *QAConversation `json:"conversation"`
	Parts        []QAPart        `json:"parts"`
	NeedsRun     bool            `json:"needs_run"`
	IsRunning    bool            `json:"is_running,omitempty"`
	ActiveStream string          `json:"active_stream_id,omitempty"`
}

// QAStore owns the Q&A conversation tables on the shared database handle.
type QAStore struct {
	db *sqlDB
}

// NewQAStore builds a QAStore over the discussion store's database.
func NewQAStore(ds *DiscussionStore) (*QAStore, error) {
	if ds == nil || ds.db == nil {
		return nil, errors.New("qa store requires a discussion store")
	}
	s := &QAStore{db: ds.db}
	if err := s.ensureSchema(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *QAStore) ensureSchema(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS qa_conversations (
			id TEXT PRIMARY KEY,
			discussion_id TEXT NOT NULL DEFAULT '',
			owner_user_id TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active',
			points_charged INTEGER NOT NULL DEFAULT 0,
			flat_charged INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		// One podcast conversation per (discussion, owner); one global
		// conversation (discussion_id = '') per owner. Partial indexes work on
		// SQLite, libSQL, and Postgres alike.
		`CREATE UNIQUE INDEX IF NOT EXISTS qa_conversations_discussion_idx
			ON qa_conversations(discussion_id, owner_user_id) WHERE discussion_id != ''`,
		`CREATE UNIQUE INDEX IF NOT EXISTS qa_conversations_global_idx
			ON qa_conversations(owner_user_id) WHERE discussion_id = ''`,
		`CREATE TABLE IF NOT EXISTS qa_turns (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			op_id TEXT NOT NULL DEFAULT '',
			conversation_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			role TEXT NOT NULL,
			text TEXT NOT NULL DEFAULT '',
			tool_calls_json TEXT NOT NULL DEFAULT '',
			tool_call_id TEXT NOT NULL DEFAULT '',
			tool_name TEXT NOT NULL DEFAULT '',
			result_text TEXT NOT NULL DEFAULT '',
			payload_json TEXT NOT NULL DEFAULT '',
			is_error INTEGER NOT NULL DEFAULT 0,
			is_compacted INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			FOREIGN KEY(conversation_id) REFERENCES qa_conversations(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS qa_turns_conversation_idx
			ON qa_turns(conversation_id, seq)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS qa_turns_op_idx
			ON qa_turns(op_id) WHERE op_id != ''`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *QAStore) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	var res sql.Result
	err := retryTransientDBConnection(ctx, func() error {
		var execErr error
		res, execErr = s.db.ExecContext(ctx, query, args...)
		return execErr
	})
	return res, err
}

// EnsureConversation returns the scope's conversation, creating it on first
// use. discussionID == "" is the owner's single global chat.
func (s *QAStore) EnsureConversation(ctx context.Context, owner, discussionID string) (*QAConversation, error) {
	if s == nil {
		return nil, errors.New("qa store is not configured")
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, errors.New("owner is required")
	}
	discussionID = strings.TrimSpace(discussionID)
	now := time.Now().UnixMilli()
	// ON CONFLICT DO NOTHING without a target works across both partial
	// unique indexes on every dialect.
	if _, err := s.exec(ctx, `INSERT INTO qa_conversations
		(id, discussion_id, owner_user_id, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING`,
		newJobID(), discussionID, owner, string(QAConversationActive), now, now); err != nil {
		return nil, err
	}
	return s.Conversation(ctx, owner, discussionID)
}

// Conversation loads the owner's conversation for a scope, or nil.
func (s *QAStore) Conversation(ctx context.Context, owner, discussionID string) (*QAConversation, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, discussion_id, owner_user_id, status, points_charged, flat_charged, created_at, updated_at
		FROM qa_conversations WHERE owner_user_id = ? AND discussion_id = ?`, owner, strings.TrimSpace(discussionID))
	return scanQAConversation(row)
}

// ConversationByID loads a conversation by id scoped to its owner, or nil.
func (s *QAStore) ConversationByID(ctx context.Context, owner, conversationID string) (*QAConversation, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, discussion_id, owner_user_id, status, points_charged, flat_charged, created_at, updated_at
		FROM qa_conversations WHERE owner_user_id = ? AND id = ?`, owner, conversationID)
	return scanQAConversation(row)
}

func scanQAConversation(row interface{ Scan(...any) error }) (*QAConversation, error) {
	var (
		c       QAConversation
		flat    int64
		created int64
		updated int64
		status  string
	)
	if err := row.Scan(&c.ID, &c.DiscussionID, &c.OwnerUserID, &status, &c.PointsCharged, &flat, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	c.Status = QAConversationStatus(status)
	c.FlatCharged = flat != 0
	c.CreatedAt = time.UnixMilli(created)
	c.UpdatedAt = time.UnixMilli(updated)
	return &c, nil
}

// AppendTurn persists one turn at the next seq. Idempotent on OpID.
func (s *QAStore) AppendTurn(ctx context.Context, conversationID string, in qaTurnInput) error {
	if s == nil {
		return errors.New("qa store is not configured")
	}
	opID := strings.TrimSpace(in.OpID)
	if opID == "" {
		opID = newJobID()
	}
	toolCallsJSON := ""
	if len(in.ToolCalls) > 0 {
		b, err := json.Marshal(in.ToolCalls)
		if err != nil {
			return err
		}
		toolCallsJSON = string(b)
	}
	now := time.Now().UnixMilli()
	_, err := s.exec(ctx, `INSERT INTO qa_turns
		(op_id, conversation_id, seq, role, text, tool_calls_json, tool_call_id, tool_name, result_text, payload_json, is_error, created_at)
		VALUES (?, ?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM qa_turns WHERE conversation_id = ?), ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		opID, conversationID, conversationID, in.Role, in.Text, toolCallsJSON, in.ToolCallID, in.ToolName, in.ResultText, in.PayloadJSON, boolInt(in.IsError), now)
	if err != nil {
		return err
	}
	_, _ = s.exec(ctx, `UPDATE qa_conversations SET updated_at = ? WHERE id = ?`, now, conversationID)
	return nil
}

// Turns returns every turn (client view), oldest first.
func (s *QAStore) Turns(ctx context.Context, conversationID string) ([]qaTurnRow, error) {
	return s.turns(ctx, conversationID, false)
}

// ClearTurns removes the visible and model-view history for one owner-scoped
// conversation while preserving the conversation row and its billing fields.
func (s *QAStore) ClearTurns(ctx context.Context, owner, discussionID string) error {
	if s == nil {
		return errors.New("qa store is not configured")
	}
	owner = strings.TrimSpace(owner)
	discussionID = strings.TrimSpace(discussionID)
	conv, err := s.Conversation(ctx, owner, discussionID)
	if err != nil || conv == nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM qa_turns WHERE conversation_id = ?`, conv.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE qa_conversations
		SET status = ?, updated_at = ? WHERE id = ? AND owner_user_id = ?`,
		string(QAConversationActive), time.Now().UnixMilli(), conv.ID, owner); err != nil {
		return err
	}
	return tx.Commit()
}

// ModelTurns returns only non-compacted turns (the model's rolling window),
// oldest first. Compaction summary rows are included (role = summary).
func (s *QAStore) ModelTurns(ctx context.Context, conversationID string) ([]qaTurnRow, error) {
	return s.turns(ctx, conversationID, true)
}

func (s *QAStore) turns(ctx context.Context, conversationID string, modelView bool) ([]qaTurnRow, error) {
	query := `SELECT id, seq, role, text, tool_calls_json, tool_call_id, tool_name, result_text, payload_json, is_error, is_compacted, created_at
		FROM qa_turns WHERE conversation_id = ?`
	if modelView {
		query += ` AND is_compacted = 0`
	}
	query += ` ORDER BY seq ASC, id ASC`
	rows, err := s.db.QueryContext(ctx, query, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]qaTurnRow, 0)
	for rows.Next() {
		var r qaTurnRow
		var isErr, isCompacted int64
		if err := rows.Scan(&r.ID, &r.Seq, &r.Role, &r.Text, &r.ToolCallsJSON, &r.ToolCallID, &r.ToolName, &r.ResultText, &r.PayloadJSON, &isErr, &isCompacted, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.IsError = isErr != 0
		r.IsCompacted = isCompacted != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// Compact marks every turn with seq < keepFromSeq compacted and inserts the
// summary as a role=summary turn positioned just before the kept suffix
// (same seq as the boundary predecessor, higher id — order is (seq, id)).
// Rows are never deleted: the full history stays visible to the user.
func (s *QAStore) Compact(ctx context.Context, conversationID string, keepFromSeq int64, summary string) error {
	if s == nil {
		return errors.New("qa store is not configured")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE qa_turns SET is_compacted = 1
		WHERE conversation_id = ? AND seq < ? AND is_compacted = 0 AND role != 'summary'`,
		conversationID, keepFromSeq); err != nil {
		return err
	}
	// Prior summaries are superseded by the new one (its text folds them in).
	if _, err := tx.ExecContext(ctx, `UPDATE qa_turns SET is_compacted = 1
		WHERE conversation_id = ? AND role = 'summary' AND is_compacted = 0 AND seq < ?`,
		conversationID, keepFromSeq); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	summarySeq := keepFromSeq - 1
	if summarySeq < 0 {
		summarySeq = 0
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO qa_turns
		(op_id, conversation_id, seq, role, text, created_at)
		VALUES (?, ?, ?, 'summary', ?, ?)`,
		newJobID(), conversationID, summarySeq, summary, now); err != nil {
		return err
	}
	return tx.Commit()
}

// SetStatus updates the conversation lifecycle status.
func (s *QAStore) SetStatus(ctx context.Context, conversationID string, status QAConversationStatus) error {
	_, err := s.exec(ctx, `UPDATE qa_conversations SET status = ?, updated_at = ? WHERE id = ?`,
		string(status), time.Now().UnixMilli(), conversationID)
	return err
}

// MarkFlatCharged records that the one-time per-conversation point floor has
// been applied.
func (s *QAStore) MarkFlatCharged(ctx context.Context, conversationID string) error {
	_, err := s.exec(ctx, `UPDATE qa_conversations SET flat_charged = 1, updated_at = ? WHERE id = ?`,
		time.Now().UnixMilli(), conversationID)
	return err
}

// qaMessagesForLLM rebuilds the OpenAI message history from model-view turns.
// Summary rows become the user-role summary message.
func qaMessagesForLLM(rows []qaTurnRow) []llm.Message {
	out := make([]llm.Message, 0, len(rows))
	for _, r := range rows {
		switch r.Role {
		case "user":
			out = append(out, llm.Message{Role: llm.RoleUser, Content: r.Text})
		case "assistant":
			msg := llm.Message{Role: llm.RoleAssistant, Content: r.Text}
			if strings.TrimSpace(r.ToolCallsJSON) != "" {
				_ = json.Unmarshal([]byte(r.ToolCallsJSON), &msg.ToolCalls)
			}
			out = append(out, msg)
		case "tool":
			out = append(out, llm.Message{Role: llm.RoleTool, Content: r.ResultText, ToolCallID: r.ToolCallID})
		case "summary":
			out = append(out, qa.SummaryMessage(r.Text))
		}
	}
	return out
}

// qaConversationParts flattens turns into the ordered client display list,
// pairing each assistant tool call with its result/card turn.
func qaConversationParts(rows []qaTurnRow) []QAPart {
	resultByCall := map[string]qaTurnRow{}
	for _, r := range rows {
		if r.Role == "tool" && r.ToolCallID != "" {
			resultByCall[r.ToolCallID] = r
		}
	}
	parts := make([]QAPart, 0, len(rows))
	for _, r := range rows {
		switch r.Role {
		case "user":
			parts = append(parts, QAPart{Kind: "text", ID: qaTurnPartID(r), Role: "user", Text: r.Text})
		case "summary":
			parts = append(parts, QAPart{Kind: "summary", ID: qaTurnPartID(r), Text: r.Text})
		case "assistant":
			if strings.TrimSpace(r.Text) != "" {
				parts = append(parts, QAPart{Kind: "text", ID: qaTurnPartID(r), Role: "assistant", Text: r.Text})
			}
			var calls []llm.ToolCall
			if strings.TrimSpace(r.ToolCallsJSON) != "" {
				_ = json.Unmarshal([]byte(r.ToolCallsJSON), &calls)
			}
			for _, c := range calls {
				parts = append(parts, qaToolPart(c, resultByCall))
			}
		}
	}
	return parts
}

func qaToolPart(c llm.ToolCall, resultByCall map[string]qaTurnRow) QAPart {
	part := QAPart{Kind: "tool", ID: "tc-" + c.ID, ToolCallID: c.ID, ToolName: c.Name}
	if json.Valid([]byte(c.Arguments)) {
		part.Input = json.RawMessage(c.Arguments)
	}
	res, ok := resultByCall[c.ID]
	if !ok {
		part.Status = "running"
		return part
	}
	part.Status = "completed"
	if res.IsError {
		part.Status = "failed"
	}
	part.ResultText = res.ResultText
	if strings.TrimSpace(res.PayloadJSON) != "" && json.Valid([]byte(res.PayloadJSON)) {
		part.Card = json.RawMessage(res.PayloadJSON)
	}
	return part
}

func qaConversationNeedsRun(rows []qaTurnRow) bool {
	for i := len(rows) - 1; i >= 0; i-- {
		switch rows[i].Role {
		case "user":
			return true
		case "assistant", "tool":
			return false
		}
	}
	return false
}

func qaTurnPartID(r qaTurnRow) string {
	return "turn-" + strconv.FormatInt(r.ID, 10)
}
