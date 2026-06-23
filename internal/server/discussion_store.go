package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	libsql "github.com/tursodatabase/libsql-client-go/libsql"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
)

type DiscussionStatus string

const (
	DiscussionPlanning   DiscussionStatus = "planning"
	DiscussionGenerating DiscussionStatus = "generating"
	DiscussionReady      DiscussionStatus = "ready"
	DiscussionFailed     DiscussionStatus = "failed"
)

// Pagination bounds for listing discussions.
const (
	defaultDiscussionPageSize = 20
	maxDiscussionPageSize     = 100
)

type DiscussionLine struct {
	Speaker string `json:"speaker"`
	Role    string `json:"role"`
	Side    string `json:"side,omitempty"`
	Text    string `json:"text"`
	StartMS int64  `json:"start_ms,omitempty"`
	IsUser  bool   `json:"is_user"`
}

type DiscussionEditTurn struct {
	ID   int64  `json:"id,omitempty"`
	Role string `json:"role"`
	Text string `json:"text,omitempty"`
	// Script/Sources/Markdown snapshot the plan as it stood at this turn so the
	// client can faithfully rebuild the chat history (every plan card), not just
	// the latest plan. Only set for "plan" turns.
	Script    *config.DebateTopic `json:"script,omitempty"`
	Sources   []config.Source     `json:"sources,omitempty"`
	Markdown  string              `json:"markdown,omitempty"`
	CreatedAt time.Time           `json:"created_at"`
}

type Discussion struct {
	ID               string               `json:"id"`
	OwnerUserID      string               `json:"-"`
	Topic            string               `json:"topic"`
	Title            string               `json:"title"`
	Status           DiscussionStatus     `json:"status"`
	Language         string               `json:"language"`
	JobID            string               `json:"job_id,omitempty"`
	DownloadURL      string               `json:"download_url,omitempty"`
	DurationSeconds  float64              `json:"duration_seconds,omitempty"`
	PromptTokens     int64                `json:"prompt_tokens,omitempty"`
	CompletionTokens int64                `json:"completion_tokens,omitempty"`
	TotalTokens      int64                `json:"total_tokens,omitempty"`
	LLMCostUSD       float64              `json:"llm_cost_usd,omitempty"`
	LLMCostKnown     bool                 `json:"llm_cost_known,omitempty"`
	TTSCostUSD       float64              `json:"tts_cost_usd,omitempty"`
	MusicCostUSD     float64              `json:"music_cost_usd,omitempty"`
	// PointsCharged is the running total of points charged across this
	// discussion's whole lifecycle (planning + generation). It is the only
	// usage figure shown to end users; the token/cost fields above are hidden
	// from clients (zeroed) once the points economy is enabled.
	PointsCharged    int64                `json:"points_charged"`
	Script           *config.DebateTopic  `json:"script,omitempty"`
	Markdown         string               `json:"markdown,omitempty"`
	Sources          []config.Source      `json:"sources,omitempty"`
	Researched       bool                 `json:"researched"`
	Lines            []DiscussionLine     `json:"lines,omitempty"`
	EditTurns        []DiscussionEditTurn `json:"edit_turns,omitempty"`
	EditTurnsHasMore bool                 `json:"edit_turns_has_more,omitempty"`
	EditTurnsBefore  int64                `json:"edit_turns_before,omitempty"`
	Progress         *DiscussionProgress  `json:"progress,omitempty"`
	CreatedAt        time.Time            `json:"created_at"`
	UpdatedAt        time.Time            `json:"updated_at"`
}

type DiscussionStore struct {
	db *sql.DB
}

func NewDiscussionStore(dbPath, primaryURL, authToken string) (*DiscussionStore, error) {
	var (
		db  *sql.DB
		err error
	)
	if primaryURL != "" {
		var opts []libsql.Option
		if authToken != "" {
			opts = append(opts, libsql.WithAuthToken(authToken))
		}
		c, err := libsql.NewConnector(primaryURL, opts...)
		if err != nil {
			return nil, err
		}
		db = sql.OpenDB(c)
	} else {
		if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
			return nil, err
		}
		db, err = sql.Open("sqlite3", sqliteDSN(dbPath))
		if err != nil {
			return nil, err
		}
	}
	db.SetMaxOpenConns(1)
	s := &DiscussionStore{db: db}
	if err := s.ensureSchema(context.Background()); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func (s *DiscussionStore) Close() error {
	if s == nil {
		return nil
	}
	var err error
	if s.db != nil {
		err = s.db.Close()
	}
	return err
}

func (s *DiscussionStore) ensureSchema(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS native_discussions (
			id TEXT PRIMARY KEY,
			owner_user_id TEXT NOT NULL,
			topic TEXT NOT NULL,
			title TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL,
			language TEXT NOT NULL DEFAULT 'en-US',
			job_id TEXT NOT NULL DEFAULT '',
			download_url TEXT NOT NULL DEFAULT '',
			duration_seconds REAL NOT NULL DEFAULT 0,
			prompt_tokens INTEGER NOT NULL DEFAULT 0,
			completion_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			llm_cost_usd REAL NOT NULL DEFAULT 0,
			llm_cost_known INTEGER NOT NULL DEFAULT 0,
			script_json TEXT NOT NULL DEFAULT '',
			markdown TEXT NOT NULL DEFAULT '',
			sources_json TEXT NOT NULL DEFAULT '[]',
			researched INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS native_discussions_owner_updated_idx
			ON native_discussions(owner_user_id, updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS native_discussions_owner_created_idx
			ON native_discussions(owner_user_id, created_at DESC, id DESC)`,
		`CREATE TABLE IF NOT EXISTS native_discussion_lines (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			discussion_id TEXT NOT NULL,
			speaker TEXT NOT NULL,
			role TEXT NOT NULL,
			side TEXT NOT NULL DEFAULT '',
			text TEXT NOT NULL,
			start_ms INTEGER NOT NULL DEFAULT 0,
			is_user INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			UNIQUE(discussion_id, speaker, role, text, is_user),
			FOREIGN KEY(discussion_id) REFERENCES native_discussions(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS native_discussion_lines_discussion_idx
			ON native_discussion_lines(discussion_id, id)`,
		`CREATE TABLE IF NOT EXISTS native_discussion_edit_turns (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			discussion_id TEXT NOT NULL,
			role TEXT NOT NULL,
			text TEXT NOT NULL DEFAULT '',
			script_json TEXT NOT NULL DEFAULT '',
			sources_json TEXT NOT NULL DEFAULT '',
			markdown TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			FOREIGN KEY(discussion_id) REFERENCES native_discussions(id) ON DELETE CASCADE
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	for _, col := range []struct {
		name string
		def  string
	}{
		{"prompt_tokens", "prompt_tokens INTEGER NOT NULL DEFAULT 0"},
		{"completion_tokens", "completion_tokens INTEGER NOT NULL DEFAULT 0"},
		{"total_tokens", "total_tokens INTEGER NOT NULL DEFAULT 0"},
		{"llm_cost_usd", "llm_cost_usd REAL NOT NULL DEFAULT 0"},
		{"llm_cost_known", "llm_cost_known INTEGER NOT NULL DEFAULT 0"},
		{"tts_cost_usd", "tts_cost_usd REAL NOT NULL DEFAULT 0"},
		{"music_cost_usd", "music_cost_usd REAL NOT NULL DEFAULT 0"},
		{"points_charged", "points_charged INTEGER NOT NULL DEFAULT 0"},
		{"points_reserved", "points_reserved INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := s.ensureColumn(ctx, "native_discussions", col.name, col.def); err != nil {
			return err
		}
	}
	// Plan-snapshot columns on edit turns are newer than the table; backfill
	// them on databases created before snapshots were stored.
	for _, col := range []struct {
		name string
		def  string
	}{
		{"script_json", "script_json TEXT NOT NULL DEFAULT ''"},
		{"sources_json", "sources_json TEXT NOT NULL DEFAULT ''"},
		{"markdown", "markdown TEXT NOT NULL DEFAULT ''"},
	} {
		if err := s.ensureColumn(ctx, "native_discussion_edit_turns", col.name, col.def); err != nil {
			return err
		}
	}
	return nil
}

func (s *DiscussionStore) Create(ctx context.Context, owner, topic string, resp planResponse) (*Discussion, error) {
	if s == nil {
		return nil, errors.New("discussion store is not configured")
	}
	id := newJobID()
	now := time.Now()
	title := ""
	language := "en-US"
	if resp.Script != nil {
		title = resp.Script.Title
		language = resp.Script.Language
	}
	scriptJSON, err := marshalString(resp.Script)
	if err != nil {
		return nil, err
	}
	sourcesJSON, err := marshalString(resp.Sources)
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO native_discussions
		(id, owner_user_id, topic, title, status, language, script_json, markdown, sources_json, researched, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, owner, topic, title, DiscussionPlanning, language, scriptJSON, resp.Markdown, sourcesJSON, boolInt(resp.Researched),
		now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return nil, err
	}
	_ = s.AppendPlanTurn(ctx, owner, id, "Current plan", resp)
	return s.Get(ctx, owner, id)
}

// CreatePlaceholder inserts an empty discussion in the planning state so the
// client gets an id immediately and can stream the plan into it. The plan body
// (script/sources/markdown) is filled in later via UpdatePlan once the planner
// finishes. No plan turn is appended yet — the first turn is written when the
// stream completes.
func (s *DiscussionStore) CreatePlaceholder(ctx context.Context, owner, topic, language string) (*Discussion, error) {
	if s == nil {
		return nil, errors.New("discussion store is not configured")
	}
	if language == "" {
		language = "en-US"
	}
	id := newJobID()
	now := time.Now()
	_, err := s.db.ExecContext(ctx, `INSERT INTO native_discussions
		(id, owner_user_id, topic, title, status, language, script_json, markdown, sources_json, researched, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, owner, topic, "", DiscussionPlanning, language, "", "", "", 0,
		now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, owner, id)
}

// List returns discussions for an owner sorted by creation time, newest first.
// limit/offset paginate the result; a non-positive limit falls back to the
// default page size and offsets below zero are clamped to zero.
func (s *DiscussionStore) List(ctx context.Context, owner string, limit, offset int) ([]Discussion, error) {
	if limit <= 0 {
		limit = defaultDiscussionPageSize
	}
	if limit > maxDiscussionPageSize {
		limit = maxDiscussionPageSize
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, owner_user_id, topic, title, status, language, job_id,
		download_url, duration_seconds, prompt_tokens, completion_tokens, total_tokens, llm_cost_usd, llm_cost_known,
		tts_cost_usd, music_cost_usd, points_charged,
		script_json, markdown, sources_json, researched, created_at, updated_at
		FROM native_discussions WHERE owner_user_id = ?
		ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, owner, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Discussion, 0)
	for rows.Next() {
		d, err := scanDiscussion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Search returns the owner's discussions whose topic, title, or markdown body
// contains the query (case-insensitive substring), newest first. It mirrors
// List's column set, scanning, and limit/offset clamping; an empty query is the
// caller's responsibility (handlers fall back to List in that case).
func (s *DiscussionStore) Search(ctx context.Context, owner, query string, limit, offset int) ([]Discussion, error) {
	if limit <= 0 {
		limit = defaultDiscussionPageSize
	}
	if limit > maxDiscussionPageSize {
		limit = maxDiscussionPageSize
	}
	if offset < 0 {
		offset = 0
	}
	pattern := "%" + escapeLike(query) + "%"
	rows, err := s.db.QueryContext(ctx, `SELECT id, owner_user_id, topic, title, status, language, job_id,
		download_url, duration_seconds, prompt_tokens, completion_tokens, total_tokens, llm_cost_usd, llm_cost_known,
		tts_cost_usd, music_cost_usd, points_charged,
		script_json, markdown, sources_json, researched, created_at, updated_at
		FROM native_discussions
		WHERE owner_user_id = ? AND (
			topic LIKE ? ESCAPE '\' OR
			title LIKE ? ESCAPE '\' OR
			markdown LIKE ? ESCAPE '\')
		ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, owner, pattern, pattern, pattern, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Discussion, 0)
	for rows.Next() {
		d, err := scanDiscussion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// escapeLike escapes the SQL LIKE wildcards (% and _) and the escape character
// itself (\) so user-supplied search text is matched literally.
func escapeLike(s string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(s)
}

func (s *DiscussionStore) Get(ctx context.Context, owner, id string) (*Discussion, error) {
	d, err := s.getDiscussion(ctx, owner, id)
	if err != nil || d == nil {
		return d, err
	}
	lines, err := s.Lines(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	d.Lines = lines
	turns, err := s.EditTurns(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	d.EditTurns = turns
	return d, nil
}

func (s *DiscussionStore) getDiscussion(ctx context.Context, owner, id string) (*Discussion, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, owner_user_id, topic, title, status, language, job_id,
		download_url, duration_seconds, prompt_tokens, completion_tokens, total_tokens, llm_cost_usd, llm_cost_known,
		tts_cost_usd, music_cost_usd, points_charged,
		script_json, markdown, sources_json, researched, created_at, updated_at
		FROM native_discussions WHERE owner_user_id = ? AND id = ?`, owner, id)
	d, err := scanDiscussion(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &d, nil
}

func (s *DiscussionStore) GetWithEditTurnPage(ctx context.Context, owner, id string, limit int, beforeID int64) (*Discussion, error) {
	d, err := s.getDiscussion(ctx, owner, id)
	if err != nil || d == nil {
		return d, err
	}
	lines, err := s.Lines(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	d.Lines = lines
	turns, hasMore, err := s.EditTurnsPage(ctx, owner, id, limit, beforeID)
	if err != nil {
		return nil, err
	}
	d.EditTurns = turns
	d.EditTurnsHasMore = hasMore
	if len(turns) > 0 {
		d.EditTurnsBefore = turns[0].ID
	}
	return d, nil
}

func (s *DiscussionStore) UpdatePlan(ctx context.Context, owner, id string, resp planResponse) (*Discussion, error) {
	title := ""
	language := "en-US"
	if resp.Script != nil {
		title = resp.Script.Title
		language = resp.Script.Language
	}
	scriptJSON, err := marshalString(resp.Script)
	if err != nil {
		return nil, err
	}
	sourcesJSON, err := marshalString(resp.Sources)
	if err != nil {
		return nil, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE native_discussions SET
		title = ?, language = ?, script_json = ?, markdown = ?, sources_json = ?, researched = ?, updated_at = ?
		WHERE owner_user_id = ? AND id = ?`,
		title, language, scriptJSON, resp.Markdown, sourcesJSON, boolInt(resp.Researched), time.Now().UnixMilli(), owner, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil
	}
	return s.Get(ctx, owner, id)
}

func (s *DiscussionStore) SetJob(ctx context.Context, owner, id, jobID string) (*Discussion, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE native_discussions SET
		status = ?, job_id = ?, prompt_tokens = 0, completion_tokens = 0, total_tokens = 0,
		llm_cost_usd = 0, llm_cost_known = 0, tts_cost_usd = 0, music_cost_usd = 0, updated_at = ?
		WHERE owner_user_id = ? AND id = ?`,
		DiscussionGenerating, jobID, time.Now().UnixMilli(), owner, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil
	}
	return s.Get(ctx, owner, id)
}

func (s *DiscussionStore) SetJobResult(ctx context.Context, id string, status DiscussionStatus, downloadURL string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE native_discussions SET status = ?, download_url = ?, updated_at = ?
		WHERE id = ?`, status, downloadURL, time.Now().UnixMilli(), id)
	return err
}

func (s *DiscussionStore) SetUsage(ctx context.Context, id string, promptTokens, completionTokens, totalTokens int64, costUSD float64, costKnown bool, ttsCostUSD, musicCostUSD float64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE native_discussions SET
		prompt_tokens = ?, completion_tokens = ?, total_tokens = ?, llm_cost_usd = ?, llm_cost_known = ?,
		tts_cost_usd = ?, music_cost_usd = ?, updated_at = ?
		WHERE id = ?`,
		promptTokens, completionTokens, totalTokens, costUSD, boolInt(costKnown),
		ttsCostUSD, musicCostUSD, time.Now().UnixMilli(), id)
	return err
}

func (s *DiscussionStore) Delete(ctx context.Context, owner, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM native_discussions WHERE owner_user_id = ? AND id = ?`, owner, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// AppendEditTurn records a text-only turn (e.g. a "user" instruction).
func (s *DiscussionStore) AppendEditTurn(ctx context.Context, owner, id, role, text string) error {
	return s.appendTurn(ctx, owner, id, role, text, "", "", "")
}

// AppendPlanTurn records a "plan" turn together with a full snapshot of the
// plan at that moment (script, sources, markdown) so the chat history can be
// rebuilt card-for-card, not just collapsed to the latest plan.
func (s *DiscussionStore) AppendPlanTurn(ctx context.Context, owner, id, label string, resp planResponse) error {
	scriptJSON, err := marshalString(resp.Script)
	if err != nil {
		return err
	}
	sourcesJSON, err := marshalString(resp.Sources)
	if err != nil {
		return err
	}
	return s.appendTurn(ctx, owner, id, "plan", label, scriptJSON, sourcesJSON, resp.Markdown)
}

func (s *DiscussionStore) appendTurn(ctx context.Context, owner, id, role, text, scriptJSON, sourcesJSON, markdown string) error {
	if ok, err := s.owns(ctx, owner, id); err != nil || !ok {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO native_discussion_edit_turns
		(discussion_id, role, text, script_json, sources_json, markdown, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, role, text, scriptJSON, sourcesJSON, markdown, time.Now().UnixMilli())
	return err
}

func (s *DiscussionStore) AppendLine(ctx context.Context, owner, id string, line DiscussionLine) error {
	if ok, err := s.owns(ctx, owner, id); err != nil || !ok {
		return err
	}
	return s.appendLine(ctx, id, line)
}

func (s *DiscussionStore) ReplaceTranscript(ctx context.Context, id string, lines []agent.TranscriptLine) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM native_discussion_lines WHERE discussion_id = ? AND is_user = 0`, id); err != nil {
		return err
	}
	for _, l := range lines {
		text := strings.TrimSpace(l.Text)
		if text == "" {
			continue
		}
		_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO native_discussion_lines
			(discussion_id, speaker, role, side, text, start_ms, is_user, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, l.Speaker, string(l.Role), string(l.Side), text, 0, 0, time.Now().UnixMilli())
		if err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE native_discussions SET updated_at = ? WHERE id = ?`, time.Now().UnixMilli(), id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *DiscussionStore) Lines(ctx context.Context, owner, id string) ([]DiscussionLine, error) {
	if ok, err := s.owns(ctx, owner, id); err != nil || !ok {
		return nil, err
	}
	return s.lines(ctx, id)
}

func (s *DiscussionStore) LinesByJob(ctx context.Context, jobID string) ([]DiscussionLine, error) {
	jobID = strings.TrimSpace(jobID)
	if s == nil || jobID == "" {
		return nil, nil
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM native_discussions WHERE job_id = ?`, jobID).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return s.lines(ctx, id)
}

func (s *DiscussionStore) lines(ctx context.Context, id string) ([]DiscussionLine, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT speaker, role, side, text, start_ms, is_user
		FROM native_discussion_lines WHERE discussion_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]DiscussionLine, 0)
	for rows.Next() {
		var line DiscussionLine
		var isUser int
		if err := rows.Scan(&line.Speaker, &line.Role, &line.Side, &line.Text, &line.StartMS, &isUser); err != nil {
			return nil, err
		}
		line.IsUser = isUser != 0
		out = append(out, line)
	}
	return out, rows.Err()
}

func (s *DiscussionStore) EditTurns(ctx context.Context, owner, id string) ([]DiscussionEditTurn, error) {
	if ok, err := s.owns(ctx, owner, id); err != nil || !ok {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, role, text, script_json, sources_json, markdown, created_at
		FROM native_discussion_edit_turns WHERE discussion_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEditTurns(rows)
}

func (s *DiscussionStore) EditTurnsPage(ctx context.Context, owner, id string, limit int, beforeID int64) ([]DiscussionEditTurn, bool, error) {
	if ok, err := s.owns(ctx, owner, id); err != nil || !ok {
		return nil, false, err
	}
	if limit <= 0 {
		limit = defaultDiscussionPageSize
	}
	if limit > maxDiscussionPageSize {
		limit = maxDiscussionPageSize
	}
	args := []any{id}
	where := "discussion_id = ?"
	if beforeID > 0 {
		where += " AND id < ?"
		args = append(args, beforeID)
	}
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, `SELECT id, role, text, script_json, sources_json, markdown, created_at
		FROM native_discussion_edit_turns WHERE `+where+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out, err := scanEditTurns(rows)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, hasMore, nil
}

func scanEditTurns(rows *sql.Rows) ([]DiscussionEditTurn, error) {
	out := make([]DiscussionEditTurn, 0)
	for rows.Next() {
		var t DiscussionEditTurn
		var scriptJSON, sourcesJSON string
		var created int64
		if err := rows.Scan(&t.ID, &t.Role, &t.Text, &scriptJSON, &sourcesJSON, &t.Markdown, &created); err != nil {
			return nil, err
		}
		if strings.TrimSpace(scriptJSON) != "" {
			var script config.DebateTopic
			if err := json.Unmarshal([]byte(scriptJSON), &script); err == nil {
				t.Script = &script
			}
		}
		if strings.TrimSpace(sourcesJSON) != "" {
			_ = json.Unmarshal([]byte(sourcesJSON), &t.Sources)
		}
		t.CreatedAt = time.UnixMilli(created)
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *DiscussionStore) owns(ctx context.Context, owner, id string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM native_discussions WHERE owner_user_id = ? AND id = ?`, owner, id).Scan(&n)
	return n > 0, err
}

func (s *DiscussionStore) appendLine(ctx context.Context, id string, line DiscussionLine) error {
	text := strings.TrimSpace(line.Text)
	if text == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO native_discussion_lines
		(discussion_id, speaker, role, side, text, start_ms, is_user, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, strings.TrimSpace(line.Speaker), strings.TrimSpace(line.Role), strings.TrimSpace(line.Side),
		text, line.StartMS, boolInt(line.IsUser), time.Now().UnixMilli())
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE native_discussions SET updated_at = ? WHERE id = ?`, time.Now().UnixMilli(), id)
	return err
}

type discussionScanner interface {
	Scan(dest ...any) error
}

func scanDiscussion(row discussionScanner) (Discussion, error) {
	var d Discussion
	var scriptJSON, sourcesJSON string
	var researched int
	var created, updated int64
	var costKnown int
	err := row.Scan(&d.ID, &d.OwnerUserID, &d.Topic, &d.Title, &d.Status, &d.Language, &d.JobID,
		&d.DownloadURL, &d.DurationSeconds, &d.PromptTokens, &d.CompletionTokens, &d.TotalTokens, &d.LLMCostUSD, &costKnown,
		&d.TTSCostUSD, &d.MusicCostUSD, &d.PointsCharged,
		&scriptJSON, &d.Markdown, &sourcesJSON, &researched,
		&created, &updated)
	if err != nil {
		return d, err
	}
	if strings.TrimSpace(scriptJSON) != "" {
		var script config.DebateTopic
		if err := json.Unmarshal([]byte(scriptJSON), &script); err != nil {
			return d, err
		}
		d.Script = &script
	}
	if strings.TrimSpace(sourcesJSON) != "" {
		if err := json.Unmarshal([]byte(sourcesJSON), &d.Sources); err != nil {
			return d, err
		}
	}
	d.LLMCostKnown = costKnown != 0
	d.Researched = researched != 0
	d.CreatedAt = time.UnixMilli(created)
	d.UpdatedAt = time.UnixMilli(updated)
	return d, nil
}

func (s *DiscussionStore) ensureColumn(ctx context.Context, table, column, definition string) error {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid        int
			name       string
			typ        string
			notNull    int
			defaultVal any
			pk         int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultVal, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s`, table, definition))
	return err
}

func marshalString(v any) (string, error) {
	if v == nil {
		return "", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func discussionNotFound(id string) error {
	return fmt.Errorf("discussion %s not found", id)
}
