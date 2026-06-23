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

type DiscussionVisibility string

const (
	DiscussionPrivate DiscussionVisibility = "private"
	DiscussionPublic  DiscussionVisibility = "public"
)

type DiscussionCover struct {
	Type          string `json:"type,omitempty"`
	ImageURL      string `json:"image_url,omitempty"`
	ImageKey      string `json:"image_key,omitempty"`
	GradientStart string `json:"gradient_start,omitempty"`
	GradientEnd   string `json:"gradient_end,omitempty"`
	Prompt        string `json:"prompt,omitempty"`
}

type CreatorProfile struct {
	ID            string `json:"id"`
	DisplayName   string `json:"display_name"`
	Username      string `json:"username,omitempty"`
	AvatarURL     string `json:"avatar_url,omitempty"`
	FollowerCount int64  `json:"follower_count"`
	IsFollowed    bool   `json:"is_followed"`
	IsSelf        bool   `json:"is_self"`
}

type MarketProfile struct {
	Profile   CreatorProfile   `json:"profile"`
	Stations  []Discussion     `json:"stations"`
	Following []CreatorProfile `json:"following"`
}

// Pagination bounds for listing discussions.
const (
	defaultDiscussionPageSize = 20
	maxDiscussionPageSize     = 100
)

const discussionSelectColumns = `id, owner_user_id, topic, title, status, language, job_id,
	download_url, duration_seconds, prompt_tokens, completion_tokens, total_tokens, llm_cost_usd, llm_cost_known,
	tts_cost_usd, music_cost_usd, points_charged, visibility, published_at, cover_type, cover_image_url,
	cover_image_key, cover_gradient_start, cover_gradient_end, cover_prompt, script_json, markdown, sources_json, researched,
	created_at, updated_at`

var (
	errDiscussionNotVisible = errors.New("discussion is not visible")
	errDiscussionForbidden  = errors.New("discussion access forbidden")
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
	ID               string           `json:"id"`
	OwnerUserID      string           `json:"-"`
	Topic            string           `json:"topic"`
	Title            string           `json:"title"`
	Status           DiscussionStatus `json:"status"`
	Language         string           `json:"language"`
	JobID            string           `json:"job_id,omitempty"`
	DownloadURL      string           `json:"download_url,omitempty"`
	DurationSeconds  float64          `json:"duration_seconds,omitempty"`
	PromptTokens     int64            `json:"prompt_tokens,omitempty"`
	CompletionTokens int64            `json:"completion_tokens,omitempty"`
	TotalTokens      int64            `json:"total_tokens,omitempty"`
	LLMCostUSD       float64          `json:"llm_cost_usd,omitempty"`
	LLMCostKnown     bool             `json:"llm_cost_known,omitempty"`
	TTSCostUSD       float64          `json:"tts_cost_usd,omitempty"`
	MusicCostUSD     float64          `json:"music_cost_usd,omitempty"`
	// PointsCharged is the running total of points charged across this
	// discussion's whole lifecycle (planning + generation). It is the only
	// usage figure shown to end users; the token/cost fields above are hidden
	// from clients (zeroed) once the points economy is enabled.
	PointsCharged    int64                `json:"points_charged"`
	Visibility       DiscussionVisibility `json:"visibility"`
	Cover            DiscussionCover      `json:"cover,omitempty"`
	Creator          *CreatorProfile      `json:"creator,omitempty"`
	LikeCount        int64                `json:"like_count"`
	IsLiked          bool                 `json:"is_liked"`
	IsOwner          bool                 `json:"is_owner"`
	PublishedAt      *time.Time           `json:"published_at,omitempty"`
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

func (s *DiscussionStore) Ping(ctx context.Context) error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.PingContext(ctx)
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
		`CREATE TABLE IF NOT EXISTS native_discussion_likes (
			user_id TEXT NOT NULL,
			discussion_id TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			PRIMARY KEY(user_id, discussion_id),
			FOREIGN KEY(discussion_id) REFERENCES native_discussions(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS native_discussion_likes_user_created_idx
			ON native_discussion_likes(user_id, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS native_discussion_likes_discussion_idx
			ON native_discussion_likes(discussion_id)`,
		`CREATE TABLE IF NOT EXISTS creator_profiles (
			user_id TEXT PRIMARY KEY,
			display_name TEXT NOT NULL DEFAULT '',
			username TEXT NOT NULL DEFAULT '',
			avatar_url TEXT NOT NULL DEFAULT '',
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS creator_follows (
			follower_user_id TEXT NOT NULL,
			creator_user_id TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			PRIMARY KEY(follower_user_id, creator_user_id)
		)`,
		`CREATE INDEX IF NOT EXISTS creator_follows_creator_idx
			ON creator_follows(creator_user_id)`,
		`CREATE INDEX IF NOT EXISTS creator_follows_follower_created_idx
			ON creator_follows(follower_user_id, created_at DESC)`,
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
		{"visibility", "visibility TEXT NOT NULL DEFAULT 'private'"},
		{"published_at", "published_at INTEGER NOT NULL DEFAULT 0"},
		{"cover_type", "cover_type TEXT NOT NULL DEFAULT ''"},
		{"cover_image_url", "cover_image_url TEXT NOT NULL DEFAULT ''"},
		{"cover_image_key", "cover_image_key TEXT NOT NULL DEFAULT ''"},
		{"cover_gradient_start", "cover_gradient_start TEXT NOT NULL DEFAULT ''"},
		{"cover_gradient_end", "cover_gradient_end TEXT NOT NULL DEFAULT ''"},
		{"cover_prompt", "cover_prompt TEXT NOT NULL DEFAULT ''"},
	} {
		if err := s.ensureColumn(ctx, "native_discussions", col.name, col.def); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS native_discussions_market_idx
		ON native_discussions(visibility, published_at DESC, created_at DESC, id DESC)`); err != nil {
		return err
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

// CreateFromVisiblePlan copies the current plan from a discussion the requester
// can see into a new private planning discussion owned by owner.
func (s *DiscussionStore) CreateFromVisiblePlan(ctx context.Context, owner, sourceID string) (*Discussion, error) {
	if s == nil {
		return nil, errors.New("discussion store is not configured")
	}
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return nil, errors.New("plan id is required")
	}
	source, err := s.GetVisible(ctx, owner, sourceID)
	if err != nil || source == nil {
		return source, err
	}
	if source.Script == nil {
		return nil, errors.New("source plan is not available")
	}
	return s.Create(ctx, owner, source.Topic, planResponse{
		Script:     source.Script,
		Markdown:   source.Markdown,
		Sources:    source.Sources,
		Researched: source.Researched,
	})
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
	return s.list(ctx, owner, "", limit, offset)
}

// ListByVisibility returns an owner's public or private discussions, newest first.
func (s *DiscussionStore) ListByVisibility(ctx context.Context, owner string, visibility DiscussionVisibility, limit, offset int) ([]Discussion, error) {
	return s.list(ctx, owner, visibility, limit, offset)
}

func (s *DiscussionStore) list(ctx context.Context, owner string, visibility DiscussionVisibility, limit, offset int) ([]Discussion, error) {
	if limit <= 0 {
		limit = defaultDiscussionPageSize
	}
	if limit > maxDiscussionPageSize {
		limit = maxDiscussionPageSize
	}
	if offset < 0 {
		offset = 0
	}
	where := "owner_user_id = ?"
	args := []any{owner}
	if visibility != "" {
		where += " AND visibility = ?"
		args = append(args, string(visibility))
	}
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, `SELECT `+discussionSelectColumns+`
		FROM native_discussions WHERE `+where+`
		ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, args...)
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
		markDiscussionViewer(&d, owner)
		out = append(out, d)
	}
	return out, rows.Err()
}

// Search returns the owner's discussions whose topic, title, or markdown body
// contains the query (case-insensitive substring), newest first. It mirrors
// List's column set, scanning, and limit/offset clamping; an empty query is the
// caller's responsibility (handlers fall back to List in that case).
func (s *DiscussionStore) Search(ctx context.Context, owner, query string, limit, offset int) ([]Discussion, error) {
	return s.search(ctx, owner, query, "", limit, offset)
}

// SearchByVisibility returns matching owner discussions filtered to public or private visibility.
func (s *DiscussionStore) SearchByVisibility(ctx context.Context, owner, query string, visibility DiscussionVisibility, limit, offset int) ([]Discussion, error) {
	return s.search(ctx, owner, query, visibility, limit, offset)
}

func (s *DiscussionStore) search(ctx context.Context, owner, query string, visibility DiscussionVisibility, limit, offset int) ([]Discussion, error) {
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
	where := "owner_user_id = ?"
	args := []any{owner}
	if visibility != "" {
		where += " AND visibility = ?"
		args = append(args, string(visibility))
	}
	args = append(args, pattern, pattern, pattern, limit, offset)
	rows, err := s.db.QueryContext(ctx, `SELECT `+discussionSelectColumns+`
		FROM native_discussions
		WHERE `+where+` AND (
			topic LIKE ? ESCAPE '\' OR
			title LIKE ? ESCAPE '\' OR
			markdown LIKE ? ESCAPE '\')
		ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, args...)
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
		markDiscussionViewer(&d, owner)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *DiscussionStore) ListPublic(ctx context.Context, viewer, query string, limit, offset int) ([]Discussion, error) {
	return s.listMarket(ctx, viewer, query, limit, offset, false)
}

func (s *DiscussionStore) ListLiked(ctx context.Context, viewer, query string, limit, offset int) ([]Discussion, error) {
	return s.listMarket(ctx, viewer, query, limit, offset, true)
}

func (s *DiscussionStore) ListByCreator(ctx context.Context, viewer, creatorID, query string, limit, offset int) ([]Discussion, error) {
	if limit <= 0 {
		limit = defaultDiscussionPageSize
	}
	if limit > maxDiscussionPageSize {
		limit = maxDiscussionPageSize
	}
	if offset < 0 {
		offset = 0
	}
	query = strings.TrimSpace(query)
	args := []any{viewer, viewer, creatorID, string(DiscussionPublic), string(DiscussionGenerating), string(DiscussionReady)}
	where := ` WHERE d.owner_user_id = ? AND d.visibility = ? AND d.status IN (?, ?)`
	if query != "" {
		pattern := "%" + escapeLike(query) + "%"
		where += ` AND (d.topic LIKE ? ESCAPE '\' OR d.title LIKE ? ESCAPE '\' OR d.markdown LIKE ? ESCAPE '\')`
		args = append(args, pattern, pattern, pattern)
	}
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, `SELECT `+prefixedDiscussionSelectColumns("d")+`,
		(SELECT COUNT(1) FROM native_discussion_likes l WHERE l.discussion_id = d.id) AS like_count,
		EXISTS(SELECT 1 FROM native_discussion_likes l WHERE l.discussion_id = d.id AND l.user_id = ?) AS is_liked,
		d.owner_user_id = ? AS is_owner
		FROM native_discussions d`+where+`
		ORDER BY d.published_at DESC, d.created_at DESC, d.id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Discussion, 0)
	for rows.Next() {
		d, err := scanDiscussionWithMarket(rows)
		if err != nil {
			return nil, err
		}
		markDiscussionViewer(&d, viewer)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.AttachCreatorProfiles(ctx, viewer, out)
}

func (s *DiscussionStore) listMarket(ctx context.Context, viewer, query string, limit, offset int, likedOnly bool) ([]Discussion, error) {
	if limit <= 0 {
		limit = defaultDiscussionPageSize
	}
	if limit > maxDiscussionPageSize {
		limit = maxDiscussionPageSize
	}
	if offset < 0 {
		offset = 0
	}
	query = strings.TrimSpace(query)
	args := []any{viewer, viewer}
	from := ` FROM native_discussions d`
	where := ` WHERE d.visibility = ? AND d.status IN (?, ?)`
	if likedOnly {
		from += ` JOIN native_discussion_likes mine ON mine.discussion_id = d.id AND mine.user_id = ?`
		args = append(args, viewer)
	}
	args = append(args, string(DiscussionPublic), string(DiscussionGenerating), string(DiscussionReady))
	if query != "" {
		pattern := "%" + escapeLike(query) + "%"
		where += ` AND (d.topic LIKE ? ESCAPE '\' OR d.title LIKE ? ESCAPE '\' OR d.markdown LIKE ? ESCAPE '\')`
		args = append(args, pattern, pattern, pattern)
	}
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, `SELECT `+prefixedDiscussionSelectColumns("d")+`,
		(SELECT COUNT(1) FROM native_discussion_likes l WHERE l.discussion_id = d.id) AS like_count,
		EXISTS(SELECT 1 FROM native_discussion_likes l WHERE l.discussion_id = d.id AND l.user_id = ?) AS is_liked,
		d.owner_user_id = ? AS is_owner`+from+where+`
		ORDER BY d.published_at DESC, d.created_at DESC, d.id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Discussion, 0)
	for rows.Next() {
		d, err := scanDiscussionWithMarket(rows)
		if err != nil {
			return nil, err
		}
		markDiscussionViewer(&d, viewer)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, s.AttachCreatorProfiles(ctx, viewer, out)
}

func (s *DiscussionStore) GetVisible(ctx context.Context, viewer, id string) (*Discussion, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+prefixedDiscussionSelectColumns("d")+`,
		(SELECT COUNT(1) FROM native_discussion_likes l WHERE l.discussion_id = d.id) AS like_count,
		EXISTS(SELECT 1 FROM native_discussion_likes l WHERE l.discussion_id = d.id AND l.user_id = ?) AS is_liked,
		d.owner_user_id = ? AS is_owner
		FROM native_discussions d
		WHERE d.id = ? AND (d.owner_user_id = ? OR d.visibility = ?)`,
		viewer, viewer, id, viewer, string(DiscussionPublic))
	d, err := scanDiscussionWithMarket(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	markDiscussionViewer(&d, viewer)
	lines, err := s.lines(ctx, id)
	if err != nil {
		return nil, err
	}
	d.Lines = lines
	if d.IsOwner {
		turns, err := s.EditTurns(ctx, viewer, id)
		if err != nil {
			return nil, err
		}
		d.EditTurns = turns
	}
	if profile, err := s.CreatorProfile(ctx, viewer, d.OwnerUserID); err == nil {
		d.Creator = profile
	} else {
		return nil, err
	}
	return &d, nil
}

func (s *DiscussionStore) Like(ctx context.Context, viewer, id string) (*Discussion, error) {
	if ok, err := s.isPublic(ctx, id); err != nil || !ok {
		return nil, err
	}
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO native_discussion_likes
		(user_id, discussion_id, created_at) VALUES (?, ?, ?)`, viewer, id, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	return s.GetVisible(ctx, viewer, id)
}

func (s *DiscussionStore) Unlike(ctx context.Context, viewer, id string) (*Discussion, error) {
	_, err := s.db.ExecContext(ctx, `DELETE FROM native_discussion_likes WHERE user_id = ? AND discussion_id = ?`, viewer, id)
	if err != nil {
		return nil, err
	}
	return s.GetVisible(ctx, viewer, id)
}

func (s *DiscussionStore) UpsertCreatorProfile(ctx context.Context, profile CreatorProfile) error {
	if s == nil {
		return errors.New("discussion store is not configured")
	}
	profile.ID = strings.TrimSpace(profile.ID)
	if profile.ID == "" || profile.ID == "anonymous" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO creator_profiles
		(user_id, display_name, username, avatar_url, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			display_name = excluded.display_name,
			username = excluded.username,
			avatar_url = excluded.avatar_url,
			updated_at = excluded.updated_at`,
		profile.ID, strings.TrimSpace(profile.DisplayName), strings.TrimSpace(profile.Username),
		strings.TrimSpace(profile.AvatarURL), time.Now().UnixMilli())
	return err
}

func (s *DiscussionStore) CreatorProfile(ctx context.Context, viewer, creatorID string) (*CreatorProfile, error) {
	var profile *CreatorProfile
	err := retryTransientDBConnection(ctx, func() error {
		var err error
		profile, err = s.creatorProfileOnce(ctx, viewer, creatorID)
		return err
	})
	return profile, err
}

func (s *DiscussionStore) creatorProfileOnce(ctx context.Context, viewer, creatorID string) (*CreatorProfile, error) {
	creatorID = strings.TrimSpace(creatorID)
	if creatorID == "" {
		return nil, nil
	}
	row := s.db.QueryRowContext(ctx, `SELECT
		? AS user_id,
		COALESCE(p.display_name, '') AS display_name,
		COALESCE(p.username, '') AS username,
		COALESCE(p.avatar_url, '') AS avatar_url,
		(SELECT COUNT(1) FROM creator_follows f WHERE f.creator_user_id = ?) AS follower_count,
		EXISTS(SELECT 1 FROM creator_follows f WHERE f.creator_user_id = ? AND f.follower_user_id = ?) AS is_followed,
		? = ? AS is_self,
		EXISTS(SELECT 1 FROM native_discussions d WHERE d.owner_user_id = ? AND (d.owner_user_id = ? OR d.visibility = ?)) AS has_visible_discussion
		FROM (SELECT 1) seed
		LEFT JOIN creator_profiles p ON p.user_id = ?`,
		creatorID, creatorID, creatorID, viewer, creatorID, viewer, creatorID, viewer, string(DiscussionPublic), creatorID)
	profile, visible, err := scanCreatorProfile(row)
	if err != nil {
		return nil, err
	}
	if !visible && !profile.IsSelf {
		return nil, nil
	}
	return &profile, nil
}

func retryTransientDBConnection(ctx context.Context, op func() error) error {
	err := op()
	if err == nil || !isTransientDBConnectionError(err) {
		return err
	}
	for i := 0; i < 3; i++ {
		if ctx.Err() != nil {
			return err
		}
		time.Sleep(time.Duration(i+1) * 100 * time.Millisecond)
		if retryErr := op(); retryErr != nil {
			err = retryErr
			if !isTransientDBConnectionError(err) {
				return err
			}
			continue
		}
		return nil
	}
	return err
}

func (s *DiscussionStore) FollowCreator(ctx context.Context, follower, creatorID string) (*CreatorProfile, error) {
	follower = strings.TrimSpace(follower)
	creatorID = strings.TrimSpace(creatorID)
	if follower == "" || creatorID == "" {
		return nil, nil
	}
	target, err := s.CreatorProfile(ctx, follower, creatorID)
	if err != nil || target == nil {
		return target, err
	}
	if follower != creatorID {
		_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO creator_follows
			(follower_user_id, creator_user_id, created_at) VALUES (?, ?, ?)`,
			follower, creatorID, time.Now().UnixMilli())
		if err != nil {
			return nil, err
		}
	}
	return s.CreatorProfile(ctx, follower, creatorID)
}

func (s *DiscussionStore) UnfollowCreator(ctx context.Context, follower, creatorID string) (*CreatorProfile, error) {
	_, err := s.db.ExecContext(ctx, `DELETE FROM creator_follows WHERE follower_user_id = ? AND creator_user_id = ?`, follower, creatorID)
	if err != nil {
		return nil, err
	}
	return s.CreatorProfile(ctx, follower, creatorID)
}

func (s *DiscussionStore) ListFollowing(ctx context.Context, viewer string, limit, offset int) ([]CreatorProfile, error) {
	if limit <= 0 {
		limit = defaultDiscussionPageSize
	}
	if limit > maxDiscussionPageSize {
		limit = maxDiscussionPageSize
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx, `SELECT
		f.creator_user_id,
		COALESCE(p.display_name, '') AS display_name,
		COALESCE(p.username, '') AS username,
		COALESCE(p.avatar_url, '') AS avatar_url,
		(SELECT COUNT(1) FROM creator_follows ff WHERE ff.creator_user_id = f.creator_user_id) AS follower_count,
		1 AS is_followed,
		f.creator_user_id = ? AS is_self,
		1 AS has_visible_discussion
		FROM creator_follows f
		LEFT JOIN creator_profiles p ON p.user_id = f.creator_user_id
		WHERE f.follower_user_id = ?
		ORDER BY f.created_at DESC LIMIT ? OFFSET ?`, viewer, viewer, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]CreatorProfile, 0)
	for rows.Next() {
		p, _, err := scanCreatorProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *DiscussionStore) AttachCreatorProfiles(ctx context.Context, viewer string, items []Discussion) error {
	if len(items) == 0 {
		return nil
	}
	cache := map[string]*CreatorProfile{}
	for i := range items {
		creatorID := strings.TrimSpace(items[i].OwnerUserID)
		if creatorID == "" {
			continue
		}
		profile, ok := cache[creatorID]
		if !ok {
			var err error
			profile, err = s.CreatorProfile(ctx, viewer, creatorID)
			if err != nil {
				return err
			}
			cache[creatorID] = profile
		}
		items[i].Creator = profile
	}
	return nil
}

func (s *DiscussionStore) SetVisibility(ctx context.Context, owner, id string, visibility DiscussionVisibility, cover DiscussionCover) (*Discussion, error) {
	if visibility == DiscussionPublic && !cover.Valid() {
		return nil, errors.New("cover is required to publish")
	}
	now := time.Now().UnixMilli()
	publishedAt := int64(0)
	if visibility == DiscussionPublic {
		publishedAt = now
	}
	res, err := s.db.ExecContext(ctx, `UPDATE native_discussions SET
		visibility = ?, published_at = ?, cover_type = ?, cover_image_url = ?, cover_image_key = ?,
		cover_gradient_start = ?, cover_gradient_end = ?, cover_prompt = ?, updated_at = ?
		WHERE owner_user_id = ? AND id = ?`,
		string(visibility), publishedAt, strings.TrimSpace(cover.Type), storedCoverImageURL(cover), strings.TrimSpace(cover.ImageKey),
		strings.TrimSpace(cover.GradientStart), strings.TrimSpace(cover.GradientEnd), strings.TrimSpace(cover.Prompt),
		now, owner, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil
	}
	return s.Get(ctx, owner, id)
}

// SetCover updates only the cover columns for an owned discussion, leaving
// visibility untouched. This lets any discussion carry cover art — not just
// published ones — set from the new-discussion sheet or the player's cover
// editor. Passing an empty cover clears the existing art.
func (s *DiscussionStore) SetCover(ctx context.Context, owner, id string, cover DiscussionCover) (*Discussion, error) {
	now := time.Now().UnixMilli()
	res, err := s.db.ExecContext(ctx, `UPDATE native_discussions SET
		cover_type = ?, cover_image_url = ?, cover_image_key = ?,
		cover_gradient_start = ?, cover_gradient_end = ?, cover_prompt = ?, updated_at = ?
		WHERE owner_user_id = ? AND id = ?`,
		strings.TrimSpace(cover.Type), storedCoverImageURL(cover), strings.TrimSpace(cover.ImageKey),
		strings.TrimSpace(cover.GradientStart), strings.TrimSpace(cover.GradientEnd), strings.TrimSpace(cover.Prompt),
		now, owner, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil
	}
	return s.Get(ctx, owner, id)
}

func storedCoverImageURL(cover DiscussionCover) string {
	if strings.TrimSpace(cover.ImageKey) != "" {
		return ""
	}
	return strings.TrimSpace(cover.ImageURL)
}

func (c DiscussionCover) Valid() bool {
	switch strings.TrimSpace(c.Type) {
	case "image", "ai":
		return strings.TrimSpace(c.ImageURL) != "" || strings.TrimSpace(c.ImageKey) != ""
	case "gradient":
		return strings.TrimSpace(c.GradientStart) != "" && strings.TrimSpace(c.GradientEnd) != ""
	default:
		return false
	}
}

func (s *DiscussionStore) isPublic(ctx context.Context, id string) (bool, error) {
	var visibility string
	err := s.db.QueryRowContext(ctx, `SELECT visibility FROM native_discussions WHERE id = ?`, id).Scan(&visibility)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return visibility == string(DiscussionPublic), err
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
	row := s.db.QueryRowContext(ctx, `SELECT `+discussionSelectColumns+`
		FROM native_discussions WHERE owner_user_id = ? AND id = ?`, owner, id)
	d, err := scanDiscussion(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	markDiscussionViewer(&d, owner)
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

func (s *DiscussionStore) AppendLineVisible(ctx context.Context, viewer, id string, line DiscussionLine) error {
	if err := s.AuthorizeDiscussionParticipation(ctx, viewer, id); err != nil {
		return err
	}
	return s.appendLine(ctx, id, line)
}

func (s *DiscussionStore) AuthorizeDiscussionParticipation(ctx context.Context, viewer, id string) error {
	return s.authorizeParticipation(ctx, viewer, id, "")
}

func (s *DiscussionStore) AuthorizeJobParticipation(ctx context.Context, viewer, id, jobID string) error {
	if strings.TrimSpace(jobID) == "" {
		return errDiscussionNotVisible
	}
	return s.authorizeParticipation(ctx, viewer, id, jobID)
}

func (s *DiscussionStore) AuthorizeJobOwner(ctx context.Context, viewer, jobID string) error {
	if strings.TrimSpace(jobID) == "" {
		return errDiscussionNotVisible
	}
	var owner string
	err := s.db.QueryRowContext(ctx, `SELECT owner_user_id FROM native_discussions WHERE job_id = ?`, jobID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return errDiscussionNotVisible
	}
	if err != nil {
		return err
	}
	if owner != viewer {
		return errDiscussionForbidden
	}
	return nil
}

func (s *DiscussionStore) authorizeParticipation(ctx context.Context, viewer, id, jobID string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errDiscussionNotVisible
	}
	args := []any{id}
	where := `id = ?`
	if strings.TrimSpace(jobID) != "" {
		where += ` AND job_id = ?`
		args = append(args, jobID)
	}
	var owner, visibility, status string
	err := s.db.QueryRowContext(ctx, `SELECT owner_user_id, visibility, status FROM native_discussions WHERE `+where, args...).Scan(&owner, &visibility, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return errDiscussionNotVisible
	}
	if err != nil {
		return err
	}
	if owner == viewer {
		return nil
	}
	if visibility == string(DiscussionPublic) && status == string(DiscussionGenerating) {
		return nil
	}
	return errDiscussionForbidden
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

func prefixedDiscussionSelectColumns(prefix string) string {
	cols := strings.Split(discussionSelectColumns, ",")
	for i, col := range cols {
		cols[i] = prefix + "." + strings.TrimSpace(col)
	}
	return strings.Join(cols, ", ")
}

func scanDiscussion(row discussionScanner) (Discussion, error) {
	var d Discussion
	var scriptJSON, sourcesJSON string
	var researched int
	var created, updated, published int64
	var costKnown int
	err := row.Scan(&d.ID, &d.OwnerUserID, &d.Topic, &d.Title, &d.Status, &d.Language, &d.JobID,
		&d.DownloadURL, &d.DurationSeconds, &d.PromptTokens, &d.CompletionTokens, &d.TotalTokens, &d.LLMCostUSD, &costKnown,
		&d.TTSCostUSD, &d.MusicCostUSD, &d.PointsCharged, &d.Visibility, &published, &d.Cover.Type, &d.Cover.ImageURL,
		&d.Cover.ImageKey, &d.Cover.GradientStart, &d.Cover.GradientEnd, &d.Cover.Prompt,
		&scriptJSON, &d.Markdown, &sourcesJSON, &researched,
		&created, &updated)
	if err != nil {
		return d, err
	}
	finalizeScannedDiscussion(&d, scriptJSON, sourcesJSON, researched, costKnown, published, created, updated)
	return d, nil
}

func scanDiscussionWithMarket(row discussionScanner) (Discussion, error) {
	var d Discussion
	var scriptJSON, sourcesJSON string
	var researched int
	var created, updated, published int64
	var costKnown, liked, owner int
	err := row.Scan(&d.ID, &d.OwnerUserID, &d.Topic, &d.Title, &d.Status, &d.Language, &d.JobID,
		&d.DownloadURL, &d.DurationSeconds, &d.PromptTokens, &d.CompletionTokens, &d.TotalTokens, &d.LLMCostUSD, &costKnown,
		&d.TTSCostUSD, &d.MusicCostUSD, &d.PointsCharged, &d.Visibility, &published, &d.Cover.Type, &d.Cover.ImageURL,
		&d.Cover.ImageKey, &d.Cover.GradientStart, &d.Cover.GradientEnd, &d.Cover.Prompt,
		&scriptJSON, &d.Markdown, &sourcesJSON, &researched,
		&created, &updated, &d.LikeCount, &liked, &owner)
	if err != nil {
		return d, err
	}
	finalizeScannedDiscussion(&d, scriptJSON, sourcesJSON, researched, costKnown, published, created, updated)
	d.IsLiked = liked != 0
	d.IsOwner = owner != 0
	return d, nil
}

func scanCreatorProfile(row discussionScanner) (CreatorProfile, bool, error) {
	var p CreatorProfile
	var followed, self, visible int
	err := row.Scan(&p.ID, &p.DisplayName, &p.Username, &p.AvatarURL, &p.FollowerCount, &followed, &self, &visible)
	if err != nil {
		return p, false, err
	}
	if strings.TrimSpace(p.DisplayName) == "" {
		p.DisplayName = "Creator"
	}
	p.IsFollowed = followed != 0
	p.IsSelf = self != 0
	if p.IsSelf {
		p.IsFollowed = false
	}
	return p, visible != 0, nil
}

func finalizeScannedDiscussion(d *Discussion, scriptJSON, sourcesJSON string, researched, costKnown int, published, created, updated int64) {
	if strings.TrimSpace(scriptJSON) != "" {
		var script config.DebateTopic
		if err := json.Unmarshal([]byte(scriptJSON), &script); err != nil {
			return
		}
		d.Script = &script
	}
	if strings.TrimSpace(sourcesJSON) != "" {
		_ = json.Unmarshal([]byte(sourcesJSON), &d.Sources)
	}
	if d.Visibility == "" {
		d.Visibility = DiscussionPrivate
	}
	d.LLMCostKnown = costKnown != 0
	d.Researched = researched != 0
	d.CreatedAt = time.UnixMilli(created)
	d.UpdatedAt = time.UnixMilli(updated)
	if published > 0 {
		t := time.UnixMilli(published)
		d.PublishedAt = &t
	}
}

func markDiscussionViewer(d *Discussion, viewer string) {
	if d == nil {
		return
	}
	d.IsOwner = d.OwnerUserID == viewer
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
