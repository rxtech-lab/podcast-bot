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

const discussionListSelectColumns = `id, owner_user_id, topic, title, status, language, job_id,
	download_url, duration_seconds, prompt_tokens, completion_tokens, total_tokens, llm_cost_usd, llm_cost_known,
	tts_cost_usd, music_cost_usd, points_charged, visibility, published_at, cover_type, cover_image_url,
	cover_image_key, cover_gradient_start, cover_gradient_end, cover_prompt, '' AS script_json, '' AS markdown, '[]' AS sources_json, researched,
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
	// SenderUserID is the authenticated id of the human who sent this line. It is
	// server-owned — set from the request principal in the append wrappers, never
	// trusted from the client — so a client cannot impersonate another participant
	// by spoofing the display name in Speaker. Empty for agent/panelist lines (no
	// human sender) and for legacy rows written before this column existed; clients
	// fall back to name matching only in that empty case.
	SenderUserID string `json:"sender_user_id,omitempty"`
	// AudioURL is a (re-signed, ephemeral) playback URL for a voice message; the
	// agent only ever sees Text, but other participants can replay the audio. It is
	// always derived server-side from AudioKey on read — never trusted from clients.
	AudioURL string `json:"audio_url,omitempty"`
	// AudioKey is the durable storage key behind AudioURL. It is server-internal
	// (never serialized to clients) and is validated against the sender before
	// being persisted, so it can be safely re-signed on read.
	AudioKey string `json:"-"`
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
	ShowUsageSummary bool                 `json:"showUsageSummary"`
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
	// Summary is the content-free descriptor of the podcast's generated summary
	// document. nil when no summary exists yet (e.g. the podcast hasn't finished).
	// The Markdown body is never included here — it is fetched separately from the
	// summary content endpoint when the summary view mounts. Populated lazily on
	// the detail path only.
	Summary             *SummaryMeta `json:"summary,omitempty"`
	AllowSendingMessage bool         `json:"allowSendingMessage"`
	CreatedAt           time.Time    `json:"created_at"`
	UpdatedAt           time.Time    `json:"updated_at"`
}

type DiscussionStore struct {
	db            *sql.DB
	joinVideoJobs bool
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
	// The hosted libsql/Turso connector keeps a long-lived HTTP stream per
	// connection. When that stream sits idle the server tears it down, and the
	// next use fails with "stream is closed: driver: bad connection". Cap the
	// connection lifetime/idle time so database/sql recycles the stream before
	// it goes stale rather than handing back a dead one.
	db.SetConnMaxIdleTime(30 * time.Second)
	db.SetConnMaxLifetime(5 * time.Minute)
	s := &DiscussionStore{db: db}
	if err := s.ensureSchema(context.Background()); err != nil {
		_ = s.Close()
		return nil, err
	}
	s.joinVideoJobs = s.tableExists(context.Background(), "video_jobs")
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

func (s *DiscussionStore) tableExists(ctx context.Context, name string) bool {
	if s == nil || s.db == nil || strings.TrimSpace(name) == "" {
		return false
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&count)
	return err == nil && count > 0
}

func (s *DiscussionStore) videoJobsJoin() string {
	if s != nil && s.joinVideoJobs {
		return ` LEFT JOIN video_jobs j ON j.id = d.job_id`
	}
	return ""
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
			sender_user_id TEXT NOT NULL DEFAULT '',
			audio_url TEXT NOT NULL DEFAULT '',
			audio_key TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			UNIQUE(discussion_id, speaker, role, text, is_user, audio_key),
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
		`CREATE TABLE IF NOT EXISTS native_discussion_shares (
			token TEXT PRIMARY KEY,
			discussion_id TEXT NOT NULL,
			owner_user_id TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			revoked_at INTEGER NOT NULL DEFAULT 0,
			FOREIGN KEY(discussion_id) REFERENCES native_discussions(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS native_discussion_shares_discussion_idx
			ON native_discussion_shares(discussion_id, created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS native_discussion_participants (
			discussion_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			joined_at INTEGER NOT NULL,
			PRIMARY KEY(discussion_id, user_id),
			FOREIGN KEY(discussion_id) REFERENCES native_discussions(id) ON DELETE CASCADE
		)`,
		// Summary documents (Markdown) generated after a podcast finishes. Keyed by
		// (discussion_id, doc_type) so additional document kinds (e.g. "ppt") can be
		// stored per podcast later. Content lives here — never on native_discussions
		// — so it is never returned by the detail/list selects.
		`CREATE TABLE IF NOT EXISTS native_discussion_summaries (
			discussion_id TEXT NOT NULL,
			doc_type TEXT NOT NULL DEFAULT 'summary',
			status TEXT NOT NULL DEFAULT 'generating',
			markdown TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			prompt_tokens INTEGER NOT NULL DEFAULT 0,
			completion_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			llm_cost_usd REAL NOT NULL DEFAULT 0,
			generated_at INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY(discussion_id, doc_type),
			FOREIGN KEY(discussion_id) REFERENCES native_discussions(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS user_push_tokens (
			user_id TEXT NOT NULL,
			token TEXT NOT NULL,
			environment TEXT NOT NULL DEFAULT 'sandbox',
			platform TEXT NOT NULL DEFAULT 'ios',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY(user_id, token, environment)
		)`,
		`CREATE INDEX IF NOT EXISTS user_push_tokens_user_env_idx
			ON user_push_tokens(user_id, environment, updated_at DESC)`,
	}
	for _, stmt := range stmts {
		if _, err := s.exec(ctx, stmt); err != nil {
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
	if _, err := s.exec(ctx, `CREATE INDEX IF NOT EXISTS native_discussions_market_idx
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
		// op_id is a per-append idempotency key so the connection retry in s.exec
		// cannot duplicate a turn (e.g. the "Current plan" history) when libsql
		// applies the insert but the result read fails on a stale stream.
		{"op_id", "op_id TEXT NOT NULL DEFAULT ''"},
	} {
		if err := s.ensureColumn(ctx, "native_discussion_edit_turns", col.name, col.def); err != nil {
			return err
		}
	}
	// Partial index: enforce op_id uniqueness only for real ids, so legacy rows
	// (op_id = '') coexist and an INSERT OR IGNORE retry collapses to a no-op.
	if _, err := s.exec(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS native_discussion_edit_turns_op_idx
		ON native_discussion_edit_turns(op_id) WHERE op_id != ''`); err != nil {
		return err
	}
	// Voice-message columns on transcript lines are newer than the table; backfill
	// them on databases created before audio messages were supported.
	for _, col := range []struct {
		name string
		def  string
	}{
		{"audio_url", "audio_url TEXT NOT NULL DEFAULT ''"},
		{"audio_key", "audio_key TEXT NOT NULL DEFAULT ''"},
		{"sender_user_id", "sender_user_id TEXT NOT NULL DEFAULT ''"},
	} {
		if err := s.ensureColumn(ctx, "native_discussion_lines", col.name, col.def); err != nil {
			return err
		}
	}
	if err := s.migrateLineUniqueness(ctx); err != nil {
		return err
	}
	return nil
}

// migrateLineUniqueness rebuilds native_discussion_lines so its uniqueness key
// includes audio_key. The legacy key — (discussion_id, speaker, role, text,
// is_user) — silently dropped a second voice message whose transcript matched an
// earlier line (e.g. two "yes" notes, or a text line then a voice note with the
// same words), losing its audio. Including audio_key keeps agent/text dedupe
// intact (those rows have an empty key) while letting distinct voice notes (each
// with a unique upload key) coexist. Idempotent: a no-op once migrated.
func (s *DiscussionStore) migrateLineUniqueness(ctx context.Context) error {
	var ddl string
	err := s.db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='native_discussion_lines'`).Scan(&ddl)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if strings.Contains(ddl, "is_user, audio_key") {
		return nil // already migrated
	}
	if !strings.Contains(ddl, "UNIQUE(discussion_id, speaker, role, text, is_user)") {
		return nil // unknown shape; leave it alone
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmts := []string{
		`CREATE TABLE native_discussion_lines_new (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			discussion_id TEXT NOT NULL,
			speaker TEXT NOT NULL,
			role TEXT NOT NULL,
			side TEXT NOT NULL DEFAULT '',
			text TEXT NOT NULL,
			start_ms INTEGER NOT NULL DEFAULT 0,
			is_user INTEGER NOT NULL DEFAULT 0,
			sender_user_id TEXT NOT NULL DEFAULT '',
			audio_url TEXT NOT NULL DEFAULT '',
			audio_key TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			UNIQUE(discussion_id, speaker, role, text, is_user, audio_key),
			FOREIGN KEY(discussion_id) REFERENCES native_discussions(id) ON DELETE CASCADE
		)`,
		`INSERT OR IGNORE INTO native_discussion_lines_new
			(id, discussion_id, speaker, role, side, text, start_ms, is_user, sender_user_id, audio_url, audio_key, created_at)
			SELECT id, discussion_id, speaker, role, side, text, start_ms, is_user, sender_user_id, audio_url, audio_key, created_at
			FROM native_discussion_lines`,
		`DROP TABLE native_discussion_lines`,
		`ALTER TABLE native_discussion_lines_new RENAME TO native_discussion_lines`,
		`CREATE INDEX IF NOT EXISTS native_discussion_lines_discussion_idx
			ON native_discussion_lines(discussion_id, id)`,
	}
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return tx.Commit()
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
	_, err = s.exec(ctx, `INSERT INTO native_discussions
		(id, owner_user_id, topic, title, status, language, script_json, markdown, sources_json, researched, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING`,
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
	_, err := s.exec(ctx, `INSERT INTO native_discussions
		(id, owner_user_id, topic, title, status, language, script_json, markdown, sources_json, researched, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING`,
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
	where := "d.owner_user_id = ?"
	args := []any{owner}
	if visibility != "" {
		where += " AND d.visibility = ?"
		args = append(args, string(visibility))
	}
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, `SELECT `+prefixedDiscussionListSelectColumns("d", s.joinVideoJobs)+`
			FROM native_discussions d`+s.videoJobsJoin()+` WHERE `+where+`
			ORDER BY d.created_at DESC, d.id DESC LIMIT ? OFFSET ?`, args...)
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
	where := "d.owner_user_id = ?"
	args := []any{owner}
	if visibility != "" {
		where += " AND d.visibility = ?"
		args = append(args, string(visibility))
	}
	args = append(args, pattern, pattern, pattern, limit, offset)
	rows, err := s.db.QueryContext(ctx, `SELECT `+prefixedDiscussionListSelectColumns("d", s.joinVideoJobs)+`
			FROM native_discussions d`+s.videoJobsJoin()+`
			WHERE `+where+` AND (
			d.topic LIKE ? ESCAPE '\' OR
			d.title LIKE ? ESCAPE '\' OR
			d.markdown LIKE ? ESCAPE '\')
		ORDER BY d.created_at DESC, d.id DESC LIMIT ? OFFSET ?`, args...)
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
	rows, err := s.db.QueryContext(ctx, `SELECT `+prefixedDiscussionListSelectColumns("d", s.joinVideoJobs)+`,
		(SELECT COUNT(1) FROM native_discussion_likes l WHERE l.discussion_id = d.id) AS like_count,
		EXISTS(SELECT 1 FROM native_discussion_likes l WHERE l.discussion_id = d.id AND l.user_id = ?) AS is_liked,
		d.owner_user_id = ? AS is_owner
		FROM native_discussions d`+s.videoJobsJoin()+where+`
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
	from := ` FROM native_discussions d` + s.videoJobsJoin()
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
	rows, err := s.db.QueryContext(ctx, `SELECT `+prefixedDiscussionListSelectColumns("d", s.joinVideoJobs)+`,
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
	d, _, err := s.LikeWithCreated(ctx, viewer, id)
	return d, err
}

func (s *DiscussionStore) LikeWithCreated(ctx context.Context, viewer, id string) (*Discussion, bool, error) {
	if ok, err := s.isPublic(ctx, id); err != nil || !ok {
		return nil, false, err
	}
	res, err := s.exec(ctx, `INSERT OR IGNORE INTO native_discussion_likes
		(user_id, discussion_id, created_at) VALUES (?, ?, ?)`, viewer, id, time.Now().UnixMilli())
	if err != nil {
		return nil, false, err
	}
	d, err := s.GetVisible(ctx, viewer, id)
	n, _ := res.RowsAffected()
	return d, n > 0, err
}

func (s *DiscussionStore) Unlike(ctx context.Context, viewer, id string) (*Discussion, error) {
	_, err := s.exec(ctx, `DELETE FROM native_discussion_likes WHERE user_id = ? AND discussion_id = ?`, viewer, id)
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
	_, err := s.exec(ctx, `INSERT INTO creator_profiles
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

// exec runs a write statement, retrying on a transient libsql/Turso connection
// error (e.g. "stream is closed: driver: bad connection"). The hosted libsql
// driver surfaces a stale HTTP stream as a plain error rather than the
// driver.ErrBadConn sentinel, so database/sql never retries it for us; the retry
// here re-runs the statement on a fresh connection.
//
// IMPORTANT: the retry re-executes the statement, so callers must only pass
// idempotent writes — UPDATE/DELETE, upserts (ON CONFLICT / INSERT OR IGNORE),
// or inserts keyed so a re-run collapses to a no-op. A blind INSERT with an
// autoincrement id must not use this path (it would duplicate on retry); make it
// idempotent first or run it via s.db.ExecContext without retry.
func (s *DiscussionStore) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	var res sql.Result
	err := retryTransientDBConnection(ctx, func() error {
		var execErr error
		res, execErr = s.db.ExecContext(ctx, query, args...)
		return execErr
	})
	return res, err
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
		_, err := s.exec(ctx, `INSERT OR IGNORE INTO creator_follows
			(follower_user_id, creator_user_id, created_at) VALUES (?, ?, ?)`,
			follower, creatorID, time.Now().UnixMilli())
		if err != nil {
			return nil, err
		}
	}
	return s.CreatorProfile(ctx, follower, creatorID)
}

func (s *DiscussionStore) UnfollowCreator(ctx context.Context, follower, creatorID string) (*CreatorProfile, error) {
	_, err := s.exec(ctx, `DELETE FROM creator_follows WHERE follower_user_id = ? AND creator_user_id = ?`, follower, creatorID)
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
	creatorIDs := make([]string, 0, len(items))
	for i := range items {
		creatorID := strings.TrimSpace(items[i].OwnerUserID)
		if creatorID == "" {
			continue
		}
		creatorIDs = append(creatorIDs, creatorID)
	}
	profiles, err := s.CreatorProfiles(ctx, viewer, creatorIDs)
	if err != nil {
		return err
	}
	for i := range items {
		items[i].Creator = profiles[strings.TrimSpace(items[i].OwnerUserID)]
	}
	return nil
}

func (s *DiscussionStore) CreatorProfiles(ctx context.Context, viewer string, creatorIDs []string) (map[string]*CreatorProfile, error) {
	profiles := map[string]*CreatorProfile{}
	ids := make([]string, 0, len(creatorIDs))
	seen := make(map[string]bool, len(creatorIDs))
	for _, id := range creatorIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return profiles, nil
	}
	values := make([]string, len(ids))
	args := make([]any, 0, len(ids)+4)
	for i, id := range ids {
		values[i] = "(?)"
		args = append(args, id)
	}
	args = append(args, viewer, viewer, viewer, string(DiscussionPublic))
	err := retryTransientDBConnection(ctx, func() error {
		rows, err := s.db.QueryContext(ctx, `WITH ids(user_id) AS (VALUES `+strings.Join(values, ",")+`)
			SELECT
			ids.user_id,
			COALESCE(p.display_name, '') AS display_name,
			COALESCE(p.username, '') AS username,
			COALESCE(p.avatar_url, '') AS avatar_url,
			(SELECT COUNT(1) FROM creator_follows f WHERE f.creator_user_id = ids.user_id) AS follower_count,
			EXISTS(SELECT 1 FROM creator_follows f WHERE f.creator_user_id = ids.user_id AND f.follower_user_id = ?) AS is_followed,
			ids.user_id = ? AS is_self,
			EXISTS(SELECT 1 FROM native_discussions d WHERE d.owner_user_id = ids.user_id AND (d.owner_user_id = ? OR d.visibility = ?)) AS has_visible_discussion
			FROM ids
			LEFT JOIN creator_profiles p ON p.user_id = ids.user_id`, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		nextProfiles := map[string]*CreatorProfile{}
		for rows.Next() {
			profile, visible, err := scanCreatorProfile(rows)
			if err != nil {
				return err
			}
			if !visible && !profile.IsSelf {
				continue
			}
			p := profile
			nextProfiles[p.ID] = &p
		}
		if err := rows.Err(); err != nil {
			return err
		}
		profiles = nextProfiles
		return nil
	})
	return profiles, err
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
	res, err := s.exec(ctx, `UPDATE native_discussions SET
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
	res, err := s.exec(ctx, `UPDATE native_discussions SET
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

func (s *DiscussionStore) GetByJobID(ctx context.Context, jobID string) (*Discussion, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, nil
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+discussionSelectColumns+`
		FROM native_discussions WHERE job_id = ? LIMIT 1`, jobID)
	d, err := scanDiscussion(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	markDiscussionViewer(&d, d.OwnerUserID)
	return &d, nil
}

func (s *DiscussionStore) GetForNotification(ctx context.Context, id string) (*Discussion, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, nil
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+discussionSelectColumns+`
		FROM native_discussions WHERE id = ?`, id)
	d, err := scanDiscussion(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	markDiscussionViewer(&d, d.OwnerUserID)
	return &d, nil
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
	res, err := s.exec(ctx, `UPDATE native_discussions SET
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

// SetSpeakerModel changes the LLM model for a single speaker (the host or a
// discussant, matched by name) in the discussion's plan. Only script_json is
// rewritten so sources/markdown/research survive. Returns nil (→ 404) when the
// discussion has no plan or no speaker matches the given name.
func (s *DiscussionStore) SetSpeakerModel(ctx context.Context, owner, id, speaker, model string) (*Discussion, error) {
	d, err := s.getDiscussion(ctx, owner, id)
	if err != nil || d == nil {
		return nil, err
	}
	if d.Script == nil {
		return nil, nil
	}
	matched := false
	if d.Script.Host.Name == speaker {
		d.Script.Host.Model = model
		matched = true
	}
	for i := range d.Script.Discussants {
		if d.Script.Discussants[i].Name == speaker {
			d.Script.Discussants[i].Model = model
			matched = true
		}
	}
	if !matched {
		return nil, nil
	}
	scriptJSON, err := marshalString(d.Script)
	if err != nil {
		return nil, err
	}
	res, err := s.exec(ctx, `UPDATE native_discussions SET
		script_json = ?, updated_at = ?
		WHERE owner_user_id = ? AND id = ?`,
		scriptJSON, time.Now().UnixMilli(), owner, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil
	}
	return s.Get(ctx, owner, id)
}

func (s *DiscussionStore) SetJob(ctx context.Context, owner, id, jobID string) (*Discussion, error) {
	res, err := s.exec(ctx, `UPDATE native_discussions SET
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
	_, err := s.exec(ctx, `UPDATE native_discussions SET status = ?, download_url = ?, updated_at = ?
		WHERE id = ?`, status, downloadURL, time.Now().UnixMilli(), id)
	return err
}

func (s *DiscussionStore) SetUsage(ctx context.Context, id string, promptTokens, completionTokens, totalTokens int64, costUSD float64, costKnown bool, ttsCostUSD, musicCostUSD float64) error {
	_, err := s.exec(ctx, `UPDATE native_discussions SET
		prompt_tokens = ?, completion_tokens = ?, total_tokens = ?, llm_cost_usd = ?, llm_cost_known = ?,
		tts_cost_usd = ?, music_cost_usd = ?, updated_at = ?
		WHERE id = ?`,
		promptTokens, completionTokens, totalTokens, costUSD, boolInt(costKnown),
		ttsCostUSD, musicCostUSD, time.Now().UnixMilli(), id)
	return err
}

func (s *DiscussionStore) Delete(ctx context.Context, owner, id string) (bool, error) {
	res, err := s.exec(ctx, `DELETE FROM native_discussions WHERE owner_user_id = ? AND id = ?`, owner, id)
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
	// op_id is generated once per call, so the s.exec connection retry reuses it
	// and INSERT OR IGNORE collapses a retried insert into a no-op — while two
	// genuinely separate appends (even with identical text) get distinct ids and
	// both persist.
	opID := newJobID()
	_, err := s.exec(ctx, `INSERT OR IGNORE INTO native_discussion_edit_turns
		(op_id, discussion_id, role, text, script_json, sources_json, markdown, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		opID, id, role, text, scriptJSON, sourcesJSON, markdown, time.Now().UnixMilli())
	return err
}

func (s *DiscussionStore) AppendLine(ctx context.Context, owner, id string, line DiscussionLine) error {
	if ok, err := s.owns(ctx, owner, id); err != nil || !ok {
		return err
	}
	return s.appendLine(ctx, id, stampSender(line, owner))
}

func (s *DiscussionStore) AppendLineVisible(ctx context.Context, viewer, id string, line DiscussionLine) error {
	if err := s.AuthorizeDiscussionParticipation(ctx, viewer, id); err != nil {
		return err
	}
	return s.appendLine(ctx, id, stampSender(line, viewer))
}

// AppendLineVisibleWithToken appends a viewer's line, authorizing via a share
// token when present (a signed-in participant of a valid share may comment on a
// private discussion). An empty token behaves exactly like AppendLineVisible.
func (s *DiscussionStore) AppendLineVisibleWithToken(ctx context.Context, viewer, id, token string, line DiscussionLine) error {
	if err := s.AuthorizeShareParticipation(ctx, viewer, id, token); err != nil {
		return err
	}
	return s.appendLine(ctx, id, stampSender(line, viewer))
}

// stampSender fixes the line's sender identity to the authenticated principal so
// it is server-owned and unspoofable: a user line is attributed to the caller,
// and any client-supplied SenderUserID on a non-user (agent) line is cleared.
func stampSender(line DiscussionLine, userID string) DiscussionLine {
	if line.IsUser {
		line.SenderUserID = strings.TrimSpace(userID)
	} else {
		line.SenderUserID = ""
	}
	return line
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
	if !discussionAllowsSendingMessage(DiscussionStatus(status)) {
		return errDiscussionForbidden
	}
	if owner == viewer {
		return nil
	}
	if visibility == string(DiscussionPublic) && status == string(DiscussionGenerating) {
		return nil
	}
	return errDiscussionForbidden
}

// ReplaceTranscript rewrites the agent lines for a discussion. The whole
// transaction is retried on a transient libsql connection error; the body is
// idempotent (delete-all-agent-lines, then INSERT OR IGNORE, then bump
// updated_at), so re-running it after a stale-stream failure yields the same
// final state and never partially applies (a failed attempt rolls back).
func (s *DiscussionStore) ReplaceTranscript(ctx context.Context, id string, lines []agent.TranscriptLine) error {
	return retryTransientDBConnection(ctx, func() error {
		return s.replaceTranscriptOnce(ctx, id, lines)
	})
}

func (s *DiscussionStore) replaceTranscriptOnce(ctx context.Context, id string, lines []agent.TranscriptLine) error {
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
		// Preserve each line's real speak time as created_at. This batch deletes
		// and re-inserts every agent line on each call, so stamping time.Now()
		// here collapsed all agent lines to the moment of the last rewrite —
		// which then sorted them after any user message sent during generation
		// (they interleave by created_at). Carrying l.At keeps the transcript in
		// true chronological order across reloads; fall back to now if unset.
		createdAt := l.At.UnixMilli()
		if l.At.IsZero() {
			createdAt = time.Now().UnixMilli()
		}
		_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO native_discussion_lines
			(discussion_id, speaker, role, side, text, start_ms, is_user, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, l.Speaker, string(l.Role), string(l.Side), text, 0, 0, createdAt)
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
	// Order by created_at so user messages and agent lines interleave in true
	// chronological order. id alone was unreliable: ReplaceTranscript deletes and
	// re-inserts every agent line, giving them fresh (higher) ids than user
	// messages appended earlier, so id-order clumped all user messages ahead of
	// the agent transcript on reload. id is the stable tiebreak for equal stamps.
	rows, err := s.db.QueryContext(ctx, `SELECT speaker, role, side, text, start_ms, is_user, sender_user_id, audio_url, audio_key
		FROM native_discussion_lines WHERE discussion_id = ? ORDER BY created_at, id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]DiscussionLine, 0)
	for rows.Next() {
		var line DiscussionLine
		var isUser int
		if err := rows.Scan(&line.Speaker, &line.Role, &line.Side, &line.Text, &line.StartMS, &isUser, &line.SenderUserID, &line.AudioURL, &line.AudioKey); err != nil {
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
	_, err := s.exec(ctx, `INSERT OR IGNORE INTO native_discussion_lines
		(discussion_id, speaker, role, side, text, start_ms, is_user, sender_user_id, audio_url, audio_key, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, strings.TrimSpace(line.Speaker), strings.TrimSpace(line.Role), strings.TrimSpace(line.Side),
		text, line.StartMS, boolInt(line.IsUser), strings.TrimSpace(line.SenderUserID),
		strings.TrimSpace(line.AudioURL), strings.TrimSpace(line.AudioKey),
		time.Now().UnixMilli())
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, `UPDATE native_discussions SET updated_at = ? WHERE id = ?`, time.Now().UnixMilli(), id)
	return err
}

type discussionScanner interface {
	Scan(dest ...any) error
}

func prefixedDiscussionSelectColumns(prefix string) string {
	return prefixedDiscussionColumns(prefix, discussionSelectColumns, false)
}

func prefixedDiscussionListSelectColumns(prefix string, joinVideoJobs bool) string {
	return prefixedDiscussionColumns(prefix, discussionListSelectColumns, joinVideoJobs)
}

func prefixedDiscussionColumns(prefix, columns string, joinVideoJobs bool) string {
	cols := strings.Split(columns, ",")
	for i, col := range cols {
		col = strings.TrimSpace(col)
		if strings.HasPrefix(col, "'' AS ") || strings.HasPrefix(col, "'[]' AS ") {
			cols[i] = col
		} else if joinVideoJobs && col == "status" {
			cols[i] = "CASE WHEN j.status = 'done' THEN 'ready' WHEN j.status = 'error' THEN 'failed' ELSE " + prefix + ".status END AS status"
		} else {
			cols[i] = prefix + "." + col
		}
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
	d.ShowUsageSummary = d.IsOwner
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
	d.refreshComputedFields()
	d.CreatedAt = time.UnixMilli(created)
	d.UpdatedAt = time.UnixMilli(updated)
	if published > 0 {
		t := time.UnixMilli(published)
		d.PublishedAt = &t
	}
}

func discussionAllowsSendingMessage(status DiscussionStatus) bool {
	return status == DiscussionGenerating
}

func (d *Discussion) refreshComputedFields() {
	if d == nil {
		return
	}
	d.AllowSendingMessage = discussionAllowsSendingMessage(d.Status)
}

func markDiscussionViewer(d *Discussion, viewer string) {
	if d == nil {
		return
	}
	d.IsOwner = d.OwnerUserID == viewer
	d.ShowUsageSummary = d.IsOwner
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
	// ALTER ADD COLUMN is not idempotent (a retry after a dropped result fails
	// with "duplicate column name"), so it must not go through the retrying
	// s.exec. The existence check above already guards re-runs; this is a
	// startup-only path on a fresh connection.
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
