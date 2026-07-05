package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/llm"
	"github.com/sirily11/debate-bot/internal/planner"
)

// PlanningConversationStatus tracks the lifecycle of a conversational planning
// thread. A thread is "awaiting_answer" while a question is pending the user's
// reply; the agent resumes (back to "active") once the answer arrives.
type PlanningConversationStatus string

const (
	PlanningConversationActive         PlanningConversationStatus = "active"
	PlanningConversationAwaitingAnswer PlanningConversationStatus = "awaiting_answer"
	PlanningConversationCompleted      PlanningConversationStatus = "completed"
	PlanningConversationFailed         PlanningConversationStatus = "failed"
)

// PlanningConversation is one conversational planning thread, one-to-one with a
// discussion. The conversation's turns (planning_turns) reconstruct both the LLM
// message history (for resume) and the client-facing chat (cards + bubbles).
type PlanningConversation struct {
	ID            string                     `json:"id"`
	DiscussionID  string                     `json:"discussion_id"`
	OwnerUserID   string                     `json:"-"`
	Status        PlanningConversationStatus `json:"status"`
	PointsCharged int64                      `json:"points_charged"`
	FlatCharged   bool                       `json:"-"`
	CreatedAt     time.Time                  `json:"created_at"`
	UpdatedAt     time.Time                  `json:"updated_at"`
}

// planningTurnRow is the persisted shape of one turn. role ∈
// user|assistant|tool|question. Assistant turns carry tool_calls; tool turns
// carry a tool_call_id + result (and, for write/update_plan, a plan snapshot);
// question turns carry the question payload + (once answered) the answers.
type planningTurnRow struct {
	ID              int64
	Seq             int64
	Role            string
	Text            string
	AttachmentsJSON string
	ReferencesJSON  string
	ToolCallsJSON   string
	ToolCallID      string
	ToolName        string
	ResultText      string
	IsError         bool
	ScriptJSON      string
	SourcesJSON     string
	Markdown        string
	QuestionID      string
	QuestionsJSON   string
	AnswersJSON     string
	QuestionStatus  string
	CreatedAt       int64
}

// planningTurnInput is what callers append. OpID is an idempotency key; when
// empty AppendTurn generates one so a connection retry can never duplicate a
// turn (mirrors native_discussion_edit_turns.op_id).
type planningTurnInput struct {
	Role           string
	Text           string
	Attachments    []planner.Attachment
	References     []planner.PodcastReference
	ToolCalls      []llm.ToolCall
	ToolCallID     string
	ToolName       string
	ResultText     string
	IsError        bool
	Script         *config.DebateTopic
	Sources        []config.Source
	Markdown       string
	QuestionID     string
	QuestionsJSON  string
	AnswersJSON    string
	QuestionStatus string
	OpID           string
}

// PlanningPart is one client-facing item in the conversation: either a text
// bubble or a tool card. The server flattens assistant tool_calls + their result
// turns into one card each so the iOS client renders a simple ordered list
// (matching the linda-assistant conversation design).
type PlanningPart struct {
	Kind        string                     `json:"kind"` // "text" | "tool"
	ID          string                     `json:"id"`
	Role        string                     `json:"role,omitempty"` // text parts: "user" | "assistant"
	Text        string                     `json:"text,omitempty"`
	Attachments []planner.Attachment       `json:"attachments,omitempty"`
	References  []planner.PodcastReference `json:"references,omitempty"`

	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	Status     string          `json:"status,omitempty"` // running|completed|failed|pending_question|rejected
	Input      json.RawMessage `json:"input,omitempty"`
	ResultText string          `json:"result_text,omitempty"`

	// Plan snapshot (show_plan card).
	Script   *config.DebateTopic `json:"script,omitempty"`
	Sources  []config.Source     `json:"sources,omitempty"`
	Markdown string              `json:"markdown,omitempty"`

	// Question card (ask_question).
	QuestionID string          `json:"question_id,omitempty"`
	Questions  json.RawMessage `json:"questions,omitempty"`
	Answers    json.RawMessage `json:"answers,omitempty"`
}

// PlanningConversationView is the full client payload: the conversation metadata
// plus its ordered display parts.
type PlanningConversationView struct {
	Conversation *PlanningConversation `json:"conversation"`
	Parts        []PlanningPart        `json:"parts"`
	NeedsRun     bool                  `json:"needs_run"`
	IsRunning    bool                  `json:"is_running,omitempty"`
	ActiveStream string                `json:"active_stream_id,omitempty"`
}

// PlanningStore owns the conversational planning tables. It shares the
// discussion store's *sql.DB (single connection) so a turn write and a points
// debit serialize against the same handle — constructed like NewPointsStore.
type PlanningStore struct {
	db *sqlDB
}

// NewPlanningStore builds a PlanningStore over the discussion store's database.
func NewPlanningStore(ds *DiscussionStore) (*PlanningStore, error) {
	if ds == nil || ds.db == nil {
		return nil, errors.New("planning store requires a discussion store")
	}
	s := &PlanningStore{db: ds.db}
	if err := s.ensureSchema(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *PlanningStore) ensureSchema(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS planning_conversations (
			id TEXT PRIMARY KEY,
			discussion_id TEXT NOT NULL,
			owner_user_id TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active',
			points_charged INTEGER NOT NULL DEFAULT 0,
			flat_charged INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			FOREIGN KEY(discussion_id) REFERENCES native_discussions(id) ON DELETE CASCADE
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS planning_conversations_discussion_idx
			ON planning_conversations(discussion_id)`,
		`CREATE TABLE IF NOT EXISTS planning_turns (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			op_id TEXT NOT NULL DEFAULT '',
			conversation_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			role TEXT NOT NULL,
			text TEXT NOT NULL DEFAULT '',
			attachments_json TEXT NOT NULL DEFAULT '',
			references_json TEXT NOT NULL DEFAULT '',
			tool_calls_json TEXT NOT NULL DEFAULT '',
			tool_call_id TEXT NOT NULL DEFAULT '',
			tool_name TEXT NOT NULL DEFAULT '',
			result_text TEXT NOT NULL DEFAULT '',
			is_error INTEGER NOT NULL DEFAULT 0,
			script_json TEXT NOT NULL DEFAULT '',
			sources_json TEXT NOT NULL DEFAULT '',
			markdown TEXT NOT NULL DEFAULT '',
			question_id TEXT NOT NULL DEFAULT '',
			questions_json TEXT NOT NULL DEFAULT '',
			answers_json TEXT NOT NULL DEFAULT '',
			question_status TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			FOREIGN KEY(conversation_id) REFERENCES planning_conversations(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS planning_turns_conversation_idx
			ON planning_turns(conversation_id, seq)`,
		// Partial unique index: enforce op_id uniqueness only for real ids so an
		// ON CONFLICT retry collapses to a no-op.
		`CREATE UNIQUE INDEX IF NOT EXISTS planning_turns_op_idx
			ON planning_turns(op_id) WHERE op_id != ''`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	if err := s.ensureColumn(ctx, "planning_turns", "attachments_json", "attachments_json TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "planning_turns", "references_json", "references_json TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	return nil
}

func (s *PlanningStore) ensureColumn(ctx context.Context, table, column, definition string) error {
	return s.db.ensureColumn(ctx, table, column, definition)
}

func (s *PlanningStore) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	var res sql.Result
	err := retryTransientDBConnection(ctx, func() error {
		var execErr error
		res, execErr = s.db.ExecContext(ctx, query, args...)
		return execErr
	})
	return res, err
}

// EnsureConversation returns the discussion's planning conversation, creating it
// on first use. One conversation per discussion (UNIQUE discussion_id).
func (s *PlanningStore) EnsureConversation(ctx context.Context, owner, discussionID string) (*PlanningConversation, error) {
	if s == nil {
		return nil, errors.New("planning store is not configured")
	}
	discussionID = strings.TrimSpace(discussionID)
	if discussionID == "" {
		return nil, errors.New("discussion id is required")
	}
	now := time.Now().UnixMilli()
	id := newJobID()
	if _, err := s.exec(ctx, `INSERT INTO planning_conversations
		(id, discussion_id, owner_user_id, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(discussion_id) DO NOTHING`,
		id, discussionID, owner, string(PlanningConversationActive), now, now); err != nil {
		return nil, err
	}
	return s.ConversationByDiscussion(ctx, owner, discussionID)
}

// ConversationByDiscussion loads the conversation owned by `owner` for the given
// discussion. Returns nil when none exists.
func (s *PlanningStore) ConversationByDiscussion(ctx context.Context, owner, discussionID string) (*PlanningConversation, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, discussion_id, owner_user_id, status, points_charged, flat_charged, created_at, updated_at
		FROM planning_conversations WHERE discussion_id = ? AND owner_user_id = ?`, discussionID, owner)
	return scanPlanningConversation(row)
}

// ConversationWithTurnsByDiscussion loads the discussion ownership check,
// planning conversation, and ordered turns in one round trip. The returned
// exists flag is true when the owner can see the discussion, even when no
// planning conversation has been created yet.
func (s *PlanningStore) ConversationWithTurnsByDiscussion(ctx context.Context, owner, discussionID string) (bool, *PlanningConversation, []planningTurnRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT d.id,
		c.id, COALESCE(c.discussion_id, ''), COALESCE(c.owner_user_id, ''), COALESCE(c.status, ''),
		COALESCE(c.points_charged, 0), COALESCE(c.flat_charged, 0), COALESCE(c.created_at, 0), COALESCE(c.updated_at, 0),
		t.id, COALESCE(t.seq, 0), COALESCE(t.role, ''), COALESCE(t.text, ''), COALESCE(t.attachments_json, ''),
		COALESCE(t.references_json, ''), COALESCE(t.tool_calls_json, ''), COALESCE(t.tool_call_id, ''),
		COALESCE(t.tool_name, ''), COALESCE(t.result_text, ''), COALESCE(t.is_error, 0),
		COALESCE(t.script_json, ''), COALESCE(t.sources_json, ''), COALESCE(t.markdown, ''),
		COALESCE(t.question_id, ''), COALESCE(t.questions_json, ''), COALESCE(t.answers_json, ''),
		COALESCE(t.question_status, ''), COALESCE(t.created_at, 0)
		FROM native_discussions d
		LEFT JOIN planning_conversations c
			ON c.discussion_id = d.id AND c.owner_user_id = d.owner_user_id
		LEFT JOIN planning_turns t ON t.conversation_id = c.id
		WHERE d.owner_user_id = ? AND d.id = ?
		ORDER BY t.seq ASC`, owner, discussionID)
	if err != nil {
		return false, nil, nil, err
	}
	defer rows.Close()

	exists := false
	var conv *PlanningConversation
	turns := make([]planningTurnRow, 0)
	for rows.Next() {
		exists = true
		var (
			discussionRowID string
			convID          sql.NullString
			convDiscussion  string
			convOwner       string
			convStatus      string
			pointsCharged   int64
			flatCharged     int64
			convCreated     int64
			convUpdated     int64
			turnID          sql.NullInt64
			turn            planningTurnRow
			turnIsError     int64
		)
		if err := rows.Scan(&discussionRowID,
			&convID, &convDiscussion, &convOwner, &convStatus, &pointsCharged, &flatCharged, &convCreated, &convUpdated,
			&turnID, &turn.Seq, &turn.Role, &turn.Text, &turn.AttachmentsJSON, &turn.ReferencesJSON, &turn.ToolCallsJSON,
			&turn.ToolCallID, &turn.ToolName, &turn.ResultText, &turnIsError, &turn.ScriptJSON, &turn.SourcesJSON,
			&turn.Markdown, &turn.QuestionID, &turn.QuestionsJSON, &turn.AnswersJSON, &turn.QuestionStatus, &turn.CreatedAt); err != nil {
			return false, nil, nil, err
		}
		if convID.Valid && conv == nil {
			conv = &PlanningConversation{
				ID:            convID.String,
				DiscussionID:  convDiscussion,
				OwnerUserID:   convOwner,
				Status:        PlanningConversationStatus(convStatus),
				PointsCharged: pointsCharged,
				FlatCharged:   flatCharged != 0,
				CreatedAt:     time.UnixMilli(convCreated),
				UpdatedAt:     time.UnixMilli(convUpdated),
			}
		}
		if turnID.Valid {
			turn.ID = turnID.Int64
			turn.IsError = turnIsError != 0
			turns = append(turns, turn)
		}
	}
	if err := rows.Err(); err != nil {
		return false, nil, nil, err
	}
	return exists, conv, turns, nil
}

func scanPlanningConversation(row interface{ Scan(...any) error }) (*PlanningConversation, error) {
	var (
		c          PlanningConversation
		flat       int64
		created    int64
		updated    int64
		status     string
		discussion string
	)
	if err := row.Scan(&c.ID, &discussion, &c.OwnerUserID, &status, &c.PointsCharged, &flat, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	c.DiscussionID = discussion
	c.Status = PlanningConversationStatus(status)
	c.FlatCharged = flat != 0
	c.CreatedAt = time.UnixMilli(created)
	c.UpdatedAt = time.UnixMilli(updated)
	return &c, nil
}

// AppendTurn persists one turn, assigning the next seq. Idempotent on OpID.
func (s *PlanningStore) AppendTurn(ctx context.Context, conversationID string, in planningTurnInput) error {
	if s == nil {
		return errors.New("planning store is not configured")
	}
	opID := strings.TrimSpace(in.OpID)
	if opID == "" {
		opID = newJobID()
	}
	attachmentsJSON := ""
	if len(in.Attachments) > 0 {
		b, err := json.Marshal(in.Attachments)
		if err != nil {
			return err
		}
		attachmentsJSON = string(b)
	}
	referencesJSON := ""
	if len(in.References) > 0 {
		b, err := json.Marshal(publicPlanningReferences(in.References))
		if err != nil {
			return err
		}
		referencesJSON = string(b)
	}
	toolCallsJSON := ""
	if len(in.ToolCalls) > 0 {
		b, err := json.Marshal(in.ToolCalls)
		if err != nil {
			return err
		}
		toolCallsJSON = string(b)
	}
	scriptJSON := ""
	if in.Script != nil {
		b, err := json.Marshal(in.Script)
		if err != nil {
			return err
		}
		scriptJSON = string(b)
	}
	sourcesJSON := ""
	if in.Sources != nil {
		b, err := json.Marshal(in.Sources)
		if err != nil {
			return err
		}
		sourcesJSON = string(b)
	}
	now := time.Now().UnixMilli()
	_, err := s.exec(ctx, `INSERT INTO planning_turns
		(op_id, conversation_id, seq, role, text, attachments_json, references_json, tool_calls_json, tool_call_id, tool_name, result_text, is_error,
		 script_json, sources_json, markdown, question_id, questions_json, answers_json, question_status, created_at)
		VALUES (?, ?, (SELECT COALESCE(MAX(seq), 0) + 1 FROM planning_turns WHERE conversation_id = ?), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		opID, conversationID, conversationID, in.Role, in.Text, attachmentsJSON, referencesJSON, toolCallsJSON, in.ToolCallID, in.ToolName, in.ResultText, boolInt(in.IsError),
		scriptJSON, sourcesJSON, in.Markdown, in.QuestionID, in.QuestionsJSON, in.AnswersJSON, in.QuestionStatus, now)
	if err != nil {
		return err
	}
	_, _ = s.exec(ctx, `UPDATE planning_conversations SET updated_at = ? WHERE id = ?`, now, conversationID)
	return nil
}

// Turns returns every turn for a conversation, oldest first.
func (s *PlanningStore) Turns(ctx context.Context, conversationID string) ([]planningTurnRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, seq, role, text, attachments_json, references_json, tool_calls_json, tool_call_id, tool_name, result_text,
		is_error, script_json, sources_json, markdown, question_id, questions_json, answers_json, question_status, created_at
		FROM planning_turns WHERE conversation_id = ? ORDER BY seq ASC`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]planningTurnRow, 0)
	for rows.Next() {
		var r planningTurnRow
		var isErr int64
		if err := rows.Scan(&r.ID, &r.Seq, &r.Role, &r.Text, &r.AttachmentsJSON, &r.ReferencesJSON, &r.ToolCallsJSON, &r.ToolCallID, &r.ToolName, &r.ResultText,
			&isErr, &r.ScriptJSON, &r.SourcesJSON, &r.Markdown, &r.QuestionID, &r.QuestionsJSON, &r.AnswersJSON, &r.QuestionStatus, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.IsError = isErr != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// PendingQuestion returns the pending question turn matching questionID, or nil.
func (s *PlanningStore) PendingQuestion(ctx context.Context, conversationID, questionID string) (*planningTurnRow, error) {
	turns, err := s.Turns(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	for i := range turns {
		t := turns[i]
		if t.Role == "question" && t.QuestionID == questionID && t.QuestionStatus == "pending" {
			return &t, nil
		}
	}
	return nil, nil
}

// RecordAnswer marks a pending question turn answered (or rejected) and stores
// the answer payload.
func (s *PlanningStore) RecordAnswer(ctx context.Context, conversationID, questionID, answersJSON, status string) error {
	_, err := s.exec(ctx, `UPDATE planning_turns SET answers_json = ?, question_status = ?
		WHERE conversation_id = ? AND question_id = ? AND role = 'question' AND question_status = 'pending'`,
		answersJSON, status, conversationID, questionID)
	return err
}

// SetStatus updates the conversation lifecycle status.
func (s *PlanningStore) SetStatus(ctx context.Context, conversationID string, status PlanningConversationStatus) error {
	_, err := s.exec(ctx, `UPDATE planning_conversations SET status = ?, updated_at = ? WHERE id = ?`,
		string(status), time.Now().UnixMilli(), conversationID)
	return err
}

// MarkFlatCharged records that the one-time per-conversation point floor has been
// applied, so later turns settle on pure metered usage.
func (s *PlanningStore) MarkFlatCharged(ctx context.Context, conversationID string) error {
	_, err := s.exec(ctx, `UPDATE planning_conversations SET flat_charged = 1, updated_at = ? WHERE id = ?`,
		time.Now().UnixMilli(), conversationID)
	return err
}

// messagesForLLM rebuilds the OpenAI message history from persisted turns.
// Question turns are UI-only — the model sees the answer through the synthetic
// tool result turn written when the user answers. User turns that carry image
// attachments are rebuilt as multimodal messages; refreshURL (optional) maps a
// stored S3 key to a fresh presigned URL, since the URL persisted at upload
// time expires after uploadPresignTTL.
func planningMessagesForLLM(rows []planningTurnRow, refreshURL func(key string) string) []llm.Message {
	out := make([]llm.Message, 0, len(rows))
	for _, r := range rows {
		switch r.Role {
		case "user":
			out = append(out, planner.UserTurnMessage(r.Text, refreshedAttachments(planningTurnAttachments(r), refreshURL)))
		case "assistant":
			msg := llm.Message{Role: llm.RoleAssistant, Content: r.Text}
			if strings.TrimSpace(r.ToolCallsJSON) != "" {
				_ = json.Unmarshal([]byte(r.ToolCallsJSON), &msg.ToolCalls)
			}
			out = append(out, msg)
		case "tool":
			out = append(out, llm.Message{Role: llm.RoleTool, Content: r.ResultText, ToolCallID: r.ToolCallID})
		}
	}
	return out
}

// refreshedAttachments swaps in a freshly signed URL for any attachment whose
// S3 key is known, since the URL persisted at upload time expires. Attachments
// without a key keep their stored URL (best effort — it may have expired).
func refreshedAttachments(atts []planner.Attachment, refreshURL func(key string) string) []planner.Attachment {
	if refreshURL == nil {
		return atts
	}
	for i := range atts {
		if key := strings.TrimSpace(atts[i].Key); key != "" {
			if fresh := refreshURL(key); fresh != "" {
				atts[i].URL = fresh
			}
		}
	}
	return atts
}

// planningConversationParts flattens turns into the ordered client display list.
func planningConversationParts(rows []planningTurnRow) []PlanningPart {
	resultByCall := map[string]planningTurnRow{}
	questionByCall := map[string]planningTurnRow{}
	for _, r := range rows {
		switch r.Role {
		case "tool":
			if r.ToolCallID != "" {
				resultByCall[r.ToolCallID] = r
			}
		case "question":
			if r.ToolCallID != "" {
				questionByCall[r.ToolCallID] = r
			}
		}
	}
	parts := make([]PlanningPart, 0, len(rows))
	for _, r := range rows {
		switch r.Role {
		case "user":
			parts = append(parts, PlanningPart{
				Kind:        "text",
				ID:          turnPartID(r),
				Role:        "user",
				Text:        planningUserDisplayText(r.Text),
				Attachments: planningTurnAttachments(r),
				References:  planningTurnReferences(r),
			})
		case "assistant":
			if strings.TrimSpace(r.Text) != "" {
				parts = append(parts, PlanningPart{Kind: "text", ID: turnPartID(r), Role: "assistant", Text: r.Text})
			}
			var calls []llm.ToolCall
			if strings.TrimSpace(r.ToolCallsJSON) != "" {
				_ = json.Unmarshal([]byte(r.ToolCallsJSON), &calls)
			}
			for _, c := range calls {
				parts = append(parts, planningToolPart(c, resultByCall, questionByCall))
			}
		}
	}
	return keepOnlyLatestVisiblePlan(parts)
}

func planningTurnAttachments(r planningTurnRow) []planner.Attachment {
	if strings.TrimSpace(r.AttachmentsJSON) == "" {
		return nil
	}
	var attachments []planner.Attachment
	if err := json.Unmarshal([]byte(r.AttachmentsJSON), &attachments); err != nil {
		return nil
	}
	return attachments
}

func planningTurnReferences(r planningTurnRow) []planner.PodcastReference {
	if strings.TrimSpace(r.ReferencesJSON) == "" {
		return nil
	}
	var refs []planner.PodcastReference
	if err := json.Unmarshal([]byte(r.ReferencesJSON), &refs); err != nil {
		return nil
	}
	return refs
}

func publicPlanningReferences(refs []planner.PodcastReference) []planner.PodcastReference {
	out := make([]planner.PodcastReference, 0, len(refs))
	for _, ref := range refs {
		ref.Context = ""
		if strings.TrimSpace(ref.ID) == "" {
			continue
		}
		out = append(out, ref)
	}
	return out
}

func planningUserDisplayText(text string) string {
	const topicPrefix = "Topic:"
	trimmed := strings.TrimSpace(text)
	if idx := strings.Index(trimmed, "\n\nCurrent plan settings:"); idx >= 0 {
		return strings.TrimSpace(trimmed[:idx])
	}
	if idx := strings.Index(trimmed, "\n\nReferenced podcast context:"); idx >= 0 {
		trimmed = strings.TrimSpace(trimmed[:idx])
	}
	if idx := strings.Index(trimmed, "\n\nThe user uploaded these reference documents;"); idx >= 0 {
		trimmed = strings.TrimSpace(trimmed[:idx])
	}
	if !strings.Contains(trimmed, "Plan settings:") {
		return trimmed
	}
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, topicPrefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, topicPrefix))
		}
	}
	return trimmed
}

func planningToolPart(c llm.ToolCall, resultByCall, questionByCall map[string]planningTurnRow) PlanningPart {
	part := PlanningPart{Kind: "tool", ID: "tc-" + c.ID, ToolCallID: c.ID, ToolName: c.Name}
	if json.Valid([]byte(c.Arguments)) {
		part.Input = json.RawMessage(c.Arguments)
	}
	if q, ok := questionByCall[c.ID]; ok {
		part.Status = questionStatusToClient(q.QuestionStatus)
		part.QuestionID = q.QuestionID
		if json.Valid([]byte(q.QuestionsJSON)) {
			part.Questions = json.RawMessage(q.QuestionsJSON)
		}
		if strings.TrimSpace(q.AnswersJSON) != "" && json.Valid([]byte(q.AnswersJSON)) {
			part.Answers = json.RawMessage(q.AnswersJSON)
		}
		return part
	}
	if res, ok := resultByCall[c.ID]; ok {
		part.Status = "completed"
		if res.IsError {
			part.Status = "failed"
		}
		part.ResultText = res.ResultText
		if c.Name == "show_plan" && strings.TrimSpace(res.ScriptJSON) != "" {
			var topic config.DebateTopic
			if json.Unmarshal([]byte(res.ScriptJSON), &topic) == nil {
				part.Script = &topic
			}
			if strings.TrimSpace(res.SourcesJSON) != "" {
				_ = json.Unmarshal([]byte(res.SourcesJSON), &part.Sources)
			}
			part.Markdown = res.Markdown
		}
		return part
	}
	part.Status = "running"
	return part
}

func keepOnlyLatestVisiblePlan(parts []PlanningPart) []PlanningPart {
	lastPlanID := ""
	for _, p := range parts {
		if p.Kind == "tool" && p.ToolName == "show_plan" && p.Script != nil {
			lastPlanID = p.ID
		}
	}
	if lastPlanID == "" {
		return parts
	}
	out := parts[:0]
	for _, p := range parts {
		if p.Kind == "tool" && p.ToolName == "show_plan" && p.Script != nil && p.ID != lastPlanID {
			continue
		}
		out = append(out, p)
	}
	return out
}

func planningConversationNeedsRun(rows []planningTurnRow) bool {
	for i := len(rows) - 1; i >= 0; i-- {
		switch rows[i].Role {
		case "user":
			return true
		case "assistant", "tool", "question":
			return false
		}
	}
	return false
}

func planningConversationShouldAutoRun(conv *PlanningConversation, rows []planningTurnRow) bool {
	if conv != nil && conv.Status == PlanningConversationFailed {
		return false
	}
	return planningConversationNeedsRun(rows)
}

func questionStatusToClient(status string) string {
	switch status {
	case "answered":
		return "completed"
	case "rejected":
		return "rejected"
	default:
		return "pending_question"
	}
}

func turnPartID(r planningTurnRow) string {
	return "turn-" + strconv.FormatInt(r.ID, 10)
}
