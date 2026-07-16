package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

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

type NotionConnection struct {
	UserID        string    `json:"-"`
	AccessToken   string    `json:"-"`
	RefreshToken  string    `json:"-"`
	BotID         string    `json:"bot_id,omitempty"`
	WorkspaceID   string    `json:"workspace_id,omitempty"`
	WorkspaceName string    `json:"workspace_name,omitempty"`
	WorkspaceIcon string    `json:"workspace_icon,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Pagination bounds for listing discussions.
const (
	defaultDiscussionPageSize = 20
	maxDiscussionPageSize     = 100
)

const discussionSelectColumns = `id, owner_user_id, topic, title, status, language, job_id,
	download_url, duration_seconds, prompt_tokens, completion_tokens, total_tokens, llm_cost_usd, llm_cost_known,
	tts_cost_usd, music_cost_usd, points_charged, points_reserved, visibility, published_at, cover_type, cover_image_url,
	cover_image_key, cover_gradient_start, cover_gradient_end, cover_prompt, script_json, markdown, sources_json, researched,
	reference_discussion_id, plan_template, created_at, updated_at, album_id`

const discussionListSelectColumns = `id, owner_user_id, topic, title, status, language, job_id,
	download_url, duration_seconds, prompt_tokens, completion_tokens, total_tokens, llm_cost_usd, llm_cost_known,
	tts_cost_usd, music_cost_usd, points_charged, points_reserved, visibility, published_at, cover_type, cover_image_url,
	cover_image_key, cover_gradient_start, cover_gradient_end, cover_prompt, '' AS script_json, '' AS markdown, '[]' AS sources_json, researched,
	reference_discussion_id, plan_template, created_at, updated_at, album_id`

const joinedJobSelectColumns = `j.id, j.status, j.s3_key, j.audio_s3_key, j.audio_only,
	j.prompt_tokens, j.completion_tokens, j.total_tokens, j.llm_cost_usd, j.llm_cost_known,
	j.tts_cost_usd, j.music_cost_usd`

const discussionSummaryListSelectColumns = `sm.status AS summary_status, sm.generated_at AS summary_generated_at`

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
	// ImageURL is an inline illustration (audiobook content) rendered as its own
	// bubble. Such a line carries no spoken Text; it survives reload because it is
	// persisted like any other line. Empty for ordinary spoken/user lines.
	ImageURL         string                   `json:"image_url,omitempty"`
	Sources          []agent.TranscriptSource `json:"sources,omitempty"`
	JudgementComment string                   `json:"judgement_comment,omitempty"`
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
	PointsReserved   int64                `json:"-"`
	ShowUsageSummary bool                 `json:"showUsageSummary"`
	Visibility       DiscussionVisibility `json:"visibility"`
	// ShareURL is the canonical public web link for this podcast — the same
	// /p/{id} player page embedded as the summary's "listen again" link. The
	// server builds it (clients never construct share links themselves) so the
	// shared link and the markdown link always stay in lockstep. Populated by
	// applyDiscussionShareURL on the read paths.
	ShareURL              string              `json:"share_url,omitempty"`
	Cover                 DiscussionCover     `json:"cover,omitempty"`
	Creator               *CreatorProfile     `json:"creator,omitempty"`
	LikeCount             int64               `json:"like_count"`
	IsLiked               bool                `json:"is_liked"`
	IsOwner               bool                `json:"is_owner"`
	PublishedAt           *time.Time          `json:"published_at,omitempty"`
	Script                *config.DebateTopic `json:"script,omitempty"`
	Markdown              string              `json:"markdown,omitempty"`
	Sources               []config.Source     `json:"sources,omitempty"`
	Researched            bool                `json:"researched"`
	ReferenceDiscussionID string              `json:"reference_discussion_id,omitempty"`
	// AlbumID links this discussion into a native_albums group; empty when
	// ungrouped. Album is the attached summary (title/cover/episode count),
	// populated on list rows so the home screen can render album groups.
	AlbumID          string               `json:"album_id,omitempty"`
	Album            *AlbumSummary        `json:"album,omitempty"`
	Template         string               `json:"template,omitempty"`
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
	Summary *SummaryMeta `json:"summary,omitempty"`
	// Mindmap is the content-free descriptor of the discussion's generated
	// mindmap document. Only present for discussion-type podcasts; the JSON
	// tree body is fetched separately from the mindmap endpoint. Populated
	// lazily on the detail path only.
	Mindmap *SummaryMeta `json:"mindmap,omitempty"`
	// MainLanguage is the immutable source language. Language becomes the
	// selected presentation language when a ready translation is applied.
	MainLanguage        string                      `json:"main_language,omitempty"`
	Translations        []DiscussionTranslationMeta `json:"translations,omitempty"`
	summaryMetaLoaded   bool
	joinedJob           *Job
	AllowSendingMessage bool      `json:"allowSendingMessage"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type DiscussionStore struct {
	db            *sqlDB
	joinVideoJobs bool
}

func NewDiscussionStore(dbPath, primaryURL, authToken string) (*DiscussionStore, error) {
	db, err := openSQLDatabase(dbPath, primaryURL, authToken)
	if err != nil {
		return nil, err
	}
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
	return s.db.tableExists(ctx, name)
}

func (s *DiscussionStore) videoJobsJoin() string {
	if s != nil && s.joinVideoJobs {
		return ` LEFT JOIN video_jobs j ON j.id = d.job_id`
	}
	return ""
}

func summaryMetaJoin() string {
	return " LEFT JOIN native_discussion_summaries sm ON sm.discussion_id = d.id AND sm.doc_type = '" + SummaryDocTypeSummary + "'"
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
			image_url TEXT NOT NULL DEFAULT '',
			sources_json TEXT NOT NULL DEFAULT '',
			judgement_comment TEXT NOT NULL DEFAULT '',
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
		`CREATE TABLE IF NOT EXISTS native_discussion_speaker_models (
			discussion_id TEXT NOT NULL,
			speaker_name TEXT NOT NULL,
			model TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY(discussion_id, speaker_name),
			FOREIGN KEY(discussion_id) REFERENCES native_discussions(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS native_discussion_speaker_models_discussion_idx
			ON native_discussion_speaker_models(discussion_id)`,
		`CREATE TABLE IF NOT EXISTS native_discussion_speaker_voices (
			discussion_id TEXT NOT NULL,
			speaker_name TEXT NOT NULL,
			voice TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY(discussion_id, speaker_name),
			FOREIGN KEY(discussion_id) REFERENCES native_discussions(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS native_discussion_speaker_voices_discussion_idx
			ON native_discussion_speaker_voices(discussion_id)`,
		`CREATE TABLE IF NOT EXISTS voice_previews (
			voice TEXT NOT NULL,
			language TEXT NOT NULL,
			s3_key TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			PRIMARY KEY(voice, language)
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
		`CREATE TABLE IF NOT EXISTS notion_connections (
			user_id TEXT PRIMARY KEY,
			access_token TEXT NOT NULL,
			refresh_token TEXT NOT NULL DEFAULT '',
			bot_id TEXT NOT NULL DEFAULT '',
			workspace_id TEXT NOT NULL DEFAULT '',
			workspace_name TEXT NOT NULL DEFAULT '',
			workspace_icon TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
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
		`CREATE TABLE IF NOT EXISTS native_discussion_translations (
			discussion_id TEXT NOT NULL,
			language TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'generating',
			bundle_json TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			prompt_tokens INTEGER NOT NULL DEFAULT 0,
			completion_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			llm_cost_usd REAL NOT NULL DEFAULT 0,
			attempts INTEGER NOT NULL DEFAULT 0,
			claimed_at INTEGER NOT NULL DEFAULT 0,
			generated_at INTEGER NOT NULL DEFAULT 0,
			cover_type TEXT NOT NULL DEFAULT '',
			cover_image_url TEXT NOT NULL DEFAULT '',
			cover_image_key TEXT NOT NULL DEFAULT '',
			cover_gradient_start TEXT NOT NULL DEFAULT '',
			cover_gradient_end TEXT NOT NULL DEFAULT '',
			cover_prompt TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY(discussion_id, language),
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
		// Albums group linked podcasts (audiobook chapter batches and follow-up
		// podcasts) into one home-list entry. kind 'auto' albums are created
		// implicitly around a root discussion when its first follow-up appears;
		// kind 'manual' albums are user-created groupings.
		`CREATE TABLE IF NOT EXISTS native_albums (
			id TEXT PRIMARY KEY,
			owner_user_id TEXT NOT NULL,
			title TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL DEFAULT 'manual',
			root_discussion_id TEXT NOT NULL DEFAULT '',
			cover_type TEXT NOT NULL DEFAULT '',
			cover_image_url TEXT NOT NULL DEFAULT '',
			cover_image_key TEXT NOT NULL DEFAULT '',
			cover_gradient_start TEXT NOT NULL DEFAULT '',
			cover_gradient_end TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS native_albums_owner_idx
			ON native_albums(owner_user_id, updated_at DESC)`,
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
		{"reference_discussion_id", "reference_discussion_id TEXT NOT NULL DEFAULT ''"},
		{"plan_template", "plan_template TEXT NOT NULL DEFAULT 'default'"},
		// video_key is the object-storage key of an audiobook's rendered 1080p
		// video; the playback URL is presigned on demand. Empty until the
		// post-audio render finishes (or when video isn't produced).
		{"video_key", "video_key TEXT NOT NULL DEFAULT ''"},
		// album_id groups this discussion into a native_albums row; empty means
		// ungrouped. album_position orders episodes within the album (audiobook
		// batches use 1000 + first chapter index; 0 falls back to created_at).
		{"album_id", "album_id TEXT NOT NULL DEFAULT ''"},
		{"album_position", "album_position INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := s.ensureColumn(ctx, "native_discussions", col.name, col.def); err != nil {
			return err
		}
	}
	if _, err := s.exec(ctx, `CREATE INDEX IF NOT EXISTS native_discussions_market_idx
		ON native_discussions(visibility, published_at DESC, created_at DESC, id DESC)`); err != nil {
		return err
	}
	if _, err := s.exec(ctx, `CREATE INDEX IF NOT EXISTS native_discussions_album_idx
		ON native_discussions(album_id, album_position, created_at)`); err != nil {
		return err
	}
	if _, err := s.exec(ctx, `CREATE INDEX IF NOT EXISTS native_discussions_reference_idx
		ON native_discussions(reference_discussion_id)`); err != nil {
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
	// (op_id = '') coexist and an ON CONFLICT retry collapses to a no-op.
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
		{"image_url", "image_url TEXT NOT NULL DEFAULT ''"},
		{"sender_user_id", "sender_user_id TEXT NOT NULL DEFAULT ''"},
		{"sources_json", "sources_json TEXT NOT NULL DEFAULT ''"},
		{"judgement_comment", "judgement_comment TEXT NOT NULL DEFAULT ''"},
	} {
		if err := s.ensureColumn(ctx, "native_discussion_lines", col.name, col.def); err != nil {
			return err
		}
	}
	// Queue-retry bookkeeping on summary documents: attempts counts claimed
	// generation attempts (reset by BeginSummary); claimed_at (unix ms) lets a
	// redelivered attempt take over after the consumer that held it died.
	for _, col := range []struct {
		name string
		def  string
	}{
		{"attempts", "attempts INTEGER NOT NULL DEFAULT 0"},
		{"claimed_at", "claimed_at INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := s.ensureColumn(ctx, "native_discussion_summaries", col.name, col.def); err != nil {
			return err
		}
	}
	// Per-language cover art on translations is newer than the table; backfill
	// the cover columns on databases created before language covers existed.
	for _, col := range []struct {
		name string
		def  string
	}{
		{"cover_type", "cover_type TEXT NOT NULL DEFAULT ''"},
		{"cover_image_url", "cover_image_url TEXT NOT NULL DEFAULT ''"},
		{"cover_image_key", "cover_image_key TEXT NOT NULL DEFAULT ''"},
		{"cover_gradient_start", "cover_gradient_start TEXT NOT NULL DEFAULT ''"},
		{"cover_gradient_end", "cover_gradient_end TEXT NOT NULL DEFAULT ''"},
		{"cover_prompt", "cover_prompt TEXT NOT NULL DEFAULT ''"},
	} {
		if err := s.ensureColumn(ctx, "native_discussion_translations", col.name, col.def); err != nil {
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
	if s.db.kind == databasePostgres {
		return nil
	}
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
			sources_json TEXT NOT NULL DEFAULT '',
			judgement_comment TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			UNIQUE(discussion_id, speaker, role, text, is_user, audio_key),
			FOREIGN KEY(discussion_id) REFERENCES native_discussions(id) ON DELETE CASCADE
		)`,
		`INSERT OR IGNORE INTO native_discussion_lines_new
			(id, discussion_id, speaker, role, side, text, start_ms, is_user, sender_user_id, audio_url, audio_key, sources_json, judgement_comment, created_at)
			SELECT id, discussion_id, speaker, role, side, text, start_ms, is_user, sender_user_id, audio_url, audio_key, sources_json, judgement_comment, created_at
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
	if resp.Script != nil {
		if err := s.seedSpeakerModelOverrides(ctx, id, resp.Script); err != nil {
			return nil, err
		}
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

// Rename updates an owned discussion's display title without rewriting the
// embedded script_json plan.
func (s *DiscussionStore) Rename(ctx context.Context, owner, id, title string) (*Discussion, error) {
	if s == nil {
		return nil, errors.New("discussion store is not configured")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, errors.New("discussion title is required")
	}
	res, err := s.exec(ctx, `UPDATE native_discussions SET title = ?, updated_at = ?
		WHERE owner_user_id = ? AND id = ?`,
		title, time.Now().UnixMilli(), owner, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil
	}
	return s.Get(ctx, owner, id)
}

// CreatePlaceholder inserts an empty discussion in the planning state so the
// client gets an id immediately and can stream the plan into it. The plan body
// (script/sources/markdown) is filled in later via UpdatePlan once the planner
// finishes. No plan turn is appended yet — the first turn is written when the
// stream completes.
func (s *DiscussionStore) CreatePlaceholder(ctx context.Context, owner, topic, language, template string) (*Discussion, error) {
	if s == nil {
		return nil, errors.New("discussion store is not configured")
	}
	if language == "" {
		language = "en-US"
	}
	template = strings.TrimSpace(template)
	if template == "" {
		template = "default"
	}
	id := newJobID()
	now := time.Now()
	_, err := s.exec(ctx, `INSERT INTO native_discussions
		(id, owner_user_id, topic, title, status, language, script_json, markdown, sources_json, researched, plan_template, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING`,
		id, owner, topic, "", DiscussionPlanning, language, "", "", "", 0,
		template, now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, owner, id)
}

// SetReference records that id is a follow-up to referenceID. The caller must
// have already validated reference visibility.
func (s *DiscussionStore) SetReference(ctx context.Context, owner, id, referenceID string) (*Discussion, error) {
	referenceID = strings.TrimSpace(referenceID)
	if referenceID == "" {
		return s.Get(ctx, owner, id)
	}
	res, err := s.exec(ctx, `UPDATE native_discussions SET reference_discussion_id = ?, updated_at = ?
		WHERE owner_user_id = ? AND id = ?`,
		referenceID, time.Now().UnixMilli(), owner, id)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return nil, nil
	}
	return s.Get(ctx, owner, id)
}

// ListByReference returns the owner's discussions that reference referenceID
// (follow-up podcasts and audiobook chapter batches), oldest first. The full
// column set (including script_json) is selected because callers need each
// child's plan — e.g. AudioBookChapterIndices for chapter-progress tracking.
func (s *DiscussionStore) ListByReference(ctx context.Context, owner, referenceID string) ([]Discussion, error) {
	referenceID = strings.TrimSpace(referenceID)
	if referenceID == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+discussionSelectColumns+`
			FROM native_discussions
			WHERE owner_user_id = ? AND reference_discussion_id = ?
			ORDER BY created_at ASC, id ASC`, owner, referenceID)
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

// List returns discussions for an owner sorted by creation time, newest first.
// limit/offset paginate the result; a non-positive limit falls back to the
// default page size and offsets below zero are clamped to zero.
func (s *DiscussionStore) List(ctx context.Context, owner string, limit, offset int) ([]Discussion, error) {
	return s.list(ctx, owner, "", "", limit, offset)
}

// ListByVisibility returns an owner's public or private discussions, newest first.
func (s *DiscussionStore) ListByVisibility(ctx context.Context, owner string, visibility DiscussionVisibility, limit, offset int) ([]Discussion, error) {
	return s.list(ctx, owner, visibility, "", limit, offset)
}

// ListByFilters returns an owner's discussions filtered by visibility and/or
// content type. Empty filters are ignored.
func (s *DiscussionStore) ListByFilters(ctx context.Context, owner string, visibility DiscussionVisibility, contentType string, limit, offset int) ([]Discussion, error) {
	return s.list(ctx, owner, visibility, contentType, limit, offset)
}

func (s *DiscussionStore) list(ctx context.Context, owner string, visibility DiscussionVisibility, contentType string, limit, offset int) ([]Discussion, error) {
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
	if contentType != "" {
		where += " AND " + s.scriptTypePredicate("d")
		args = append(args, contentType)
	}
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, `SELECT `+prefixedDiscussionListSelectColumns("d", s.joinVideoJobs)+`
				, `+discussionSummaryListSelectColumns+`
			FROM native_discussions d`+s.videoJobsJoin()+summaryMetaJoin()+` WHERE `+where+`
			ORDER BY d.created_at DESC, d.id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Discussion, 0)
	for rows.Next() {
		d, err := scanDiscussionWithSummary(rows)
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
	return s.search(ctx, owner, query, "", "", limit, offset)
}

// SearchByVisibility returns matching owner discussions filtered to public or private visibility.
func (s *DiscussionStore) SearchByVisibility(ctx context.Context, owner, query string, visibility DiscussionVisibility, limit, offset int) ([]Discussion, error) {
	return s.search(ctx, owner, query, visibility, "", limit, offset)
}

// SearchByFilters returns matching owner discussions filtered by visibility
// and/or content type. Empty filters are ignored.
func (s *DiscussionStore) SearchByFilters(ctx context.Context, owner, query string, visibility DiscussionVisibility, contentType string, limit, offset int) ([]Discussion, error) {
	return s.search(ctx, owner, query, visibility, contentType, limit, offset)
}

// ListParentPodcasts returns owned podcasts that are finished and can be used as
// parent context for a follow-up discussion.
func (s *DiscussionStore) ListParentPodcasts(ctx context.Context, owner, query string, limit, offset int) ([]Discussion, error) {
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
	args := []any{owner, string(DiscussionReady)}
	where := "d.owner_user_id = ? AND d.status = ?"
	if query != "" {
		pattern := "%" + escapeLike(query) + "%"
		where += ` AND (d.topic LIKE ? ESCAPE '\' OR d.title LIKE ? ESCAPE '\' OR d.markdown LIKE ? ESCAPE '\')`
		args = append(args, pattern, pattern, pattern)
	}
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, `SELECT `+prefixedDiscussionListSelectColumns("d", s.joinVideoJobs)+`
			, `+discussionSummaryListSelectColumns+`
			FROM native_discussions d`+s.videoJobsJoin()+summaryMetaJoin()+`
			WHERE `+where+`
			ORDER BY d.created_at DESC, d.id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Discussion, 0)
	for rows.Next() {
		d, err := scanDiscussionWithSummary(rows)
		if err != nil {
			return nil, err
		}
		markDiscussionViewer(&d, owner)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *DiscussionStore) search(ctx context.Context, owner, query string, visibility DiscussionVisibility, contentType string, limit, offset int) ([]Discussion, error) {
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
	if contentType != "" {
		where += " AND " + s.scriptTypePredicate("d")
		args = append(args, contentType)
	}
	args = append(args, pattern, pattern, pattern, limit, offset)
	rows, err := s.db.QueryContext(ctx, `SELECT `+prefixedDiscussionListSelectColumns("d", s.joinVideoJobs)+`
			, `+discussionSummaryListSelectColumns+`
			FROM native_discussions d`+s.videoJobsJoin()+summaryMetaJoin()+`
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
		d, err := scanDiscussionWithSummary(rows)
		if err != nil {
			return nil, err
		}
		markDiscussionViewer(&d, owner)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *DiscussionStore) scriptTypePredicate(alias string) string {
	if alias == "" {
		alias = "native_discussions"
	}
	column := alias + ".script_json"
	if s != nil && s.db != nil && s.db.kind == databasePostgres {
		return "(NULLIF(" + column + ", '')::jsonb ->> 'type') = ?"
	}
	return "json_valid(" + column + ") AND json_extract(" + column + ", '$.type') = ?"
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
			d.owner_user_id = ? AS is_owner,
			`+discussionSummaryListSelectColumns+`
			FROM native_discussions d`+s.videoJobsJoin()+summaryMetaJoin()+where+`
			ORDER BY d.published_at DESC, d.created_at DESC, d.id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Discussion, 0)
	for rows.Next() {
		d, err := scanDiscussionWithMarketSummary(rows)
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
			d.owner_user_id = ? AS is_owner,
			`+discussionSummaryListSelectColumns+from+summaryMetaJoin()+where+`
			ORDER BY d.published_at DESC, d.created_at DESC, d.id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Discussion, 0)
	for rows.Next() {
		d, err := scanDiscussionWithMarketSummary(rows)
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
	if err := s.applySpeakerOverrides(ctx, &d); err != nil {
		return nil, err
	}
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
	res, err := s.exec(ctx, `INSERT INTO native_discussion_likes
		(user_id, discussion_id, created_at) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`, viewer, id, time.Now().UnixMilli())
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

func (s *DiscussionStore) SaveNotionConnection(ctx context.Context, conn NotionConnection) error {
	if s == nil || s.db == nil {
		return errors.New("discussion store is not configured")
	}
	conn.UserID = strings.TrimSpace(conn.UserID)
	conn.AccessToken = strings.TrimSpace(conn.AccessToken)
	if conn.UserID == "" || conn.AccessToken == "" {
		return errors.New("notion user id and access token are required")
	}
	now := time.Now().UnixMilli()
	_, err := s.exec(ctx, `INSERT INTO notion_connections
		(user_id, access_token, refresh_token, bot_id, workspace_id, workspace_name, workspace_icon, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET
			access_token = excluded.access_token,
			refresh_token = excluded.refresh_token,
			bot_id = excluded.bot_id,
			workspace_id = excluded.workspace_id,
			workspace_name = excluded.workspace_name,
			workspace_icon = excluded.workspace_icon,
			updated_at = excluded.updated_at`,
		conn.UserID, conn.AccessToken, conn.RefreshToken, conn.BotID, conn.WorkspaceID, conn.WorkspaceName, conn.WorkspaceIcon, now, now)
	return err
}

func (s *DiscussionStore) NotionConnection(ctx context.Context, userID string) (*NotionConnection, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("discussion store is not configured")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, nil
	}
	var conn NotionConnection
	var created, updated int64
	err := s.db.QueryRowContext(ctx, `SELECT user_id, access_token, refresh_token, bot_id, workspace_id, workspace_name, workspace_icon, created_at, updated_at
		FROM notion_connections WHERE user_id = ?`, userID).Scan(
		&conn.UserID, &conn.AccessToken, &conn.RefreshToken, &conn.BotID, &conn.WorkspaceID, &conn.WorkspaceName, &conn.WorkspaceIcon, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	conn.CreatedAt = time.UnixMilli(created)
	conn.UpdatedAt = time.UnixMilli(updated)
	return &conn, nil
}

// exec runs a write statement, retrying on a transient libsql/Turso connection
// error (e.g. "stream is closed: driver: bad connection"). The hosted libsql
// driver surfaces a stale HTTP stream as a plain error rather than the
// driver.ErrBadConn sentinel, so database/sql never retries it for us; the retry
// here re-runs the statement on a fresh connection.
//
// IMPORTANT: the retry re-executes the statement, so callers must only pass
// idempotent writes — UPDATE/DELETE, upserts (ON CONFLICT),
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
		_, err := s.exec(ctx, `INSERT INTO creator_follows
			(follower_user_id, creator_user_id, created_at) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`,
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
	return s.GetTimed(ctx, owner, id, nil)
}

func (s *DiscussionStore) GetTimed(ctx context.Context, owner, id string, timer *stationTimer) (*Discussion, error) {
	return s.getTimed(ctx, owner, id, true, timer)
}

func (s *DiscussionStore) GetWithoutEditTurnsTimed(ctx context.Context, owner, id string, timer *stationTimer) (*Discussion, error) {
	return s.getTimed(ctx, owner, id, false, timer)
}

func (s *DiscussionStore) getTimed(ctx context.Context, owner, id string, includeEditTurns bool, timer *stationTimer) (*Discussion, error) {
	t0 := time.Now()
	d, err := s.getDiscussionTimed(ctx, owner, id, timer)
	if timer != nil {
		timer.mark("store_discussion", t0)
	}
	if err != nil || d == nil {
		return d, err
	}
	t0 = time.Now()
	lines, err := s.lines(ctx, id)
	if timer != nil {
		timer.mark("store_lines", t0)
	}
	if err != nil {
		return nil, err
	}
	d.Lines = lines
	if !includeEditTurns {
		return d, nil
	}
	t0 = time.Now()
	turns, err := s.editTurns(ctx, id)
	if timer != nil {
		timer.mark("store_edit_turns", t0)
	}
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
	if err := s.applySpeakerOverrides(ctx, &d); err != nil {
		return nil, err
	}
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
	if err := s.applySpeakerOverrides(ctx, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *DiscussionStore) getDiscussion(ctx context.Context, owner, id string) (*Discussion, error) {
	return s.getDiscussionTimed(ctx, owner, id, nil)
}

func (s *DiscussionStore) getDiscussionTimed(ctx context.Context, owner, id string, timer *stationTimer) (*Discussion, error) {
	if s.joinVideoJobs {
		t0 := time.Now()
		row := s.db.QueryRowContext(ctx, `SELECT `+prefixedDiscussionSelectColumns("d")+`, `+joinedJobSelectColumns+`
			FROM native_discussions d`+s.videoJobsJoin()+` WHERE d.owner_user_id = ? AND d.id = ?`, owner, id)
		d, err := scanDiscussionWithJoinedJob(row)
		if timer != nil {
			timer.mark("store_discussion_row", t0)
		}
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, nil
			}
			return nil, err
		}
		markDiscussionViewer(&d, owner)
		t0 = time.Now()
		if err := s.applySpeakerOverridesTimed(ctx, &d, timer); err != nil {
			return nil, err
		}
		if timer != nil {
			timer.mark("store_speaker_overrides", t0)
		}
		return &d, nil
	}
	t0 := time.Now()
	row := s.db.QueryRowContext(ctx, `SELECT `+discussionSelectColumns+`
		FROM native_discussions WHERE owner_user_id = ? AND id = ?`, owner, id)
	d, err := scanDiscussion(row)
	if timer != nil {
		timer.mark("store_discussion_row", t0)
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	markDiscussionViewer(&d, owner)
	t0 = time.Now()
	if err := s.applySpeakerOverridesTimed(ctx, &d, timer); err != nil {
		return nil, err
	}
	if timer != nil {
		timer.mark("store_speaker_overrides", t0)
	}
	return &d, nil
}

func (s *DiscussionStore) GetWithEditTurnPage(ctx context.Context, owner, id string, limit int, beforeID int64) (*Discussion, error) {
	return s.GetWithEditTurnPageTimed(ctx, owner, id, limit, beforeID, nil)
}

func (s *DiscussionStore) GetWithEditTurnPageTimed(ctx context.Context, owner, id string, limit int, beforeID int64, timer *stationTimer) (*Discussion, error) {
	t0 := time.Now()
	d, err := s.getDiscussion(ctx, owner, id)
	if timer != nil {
		timer.mark("store_discussion", t0)
	}
	if err != nil || d == nil {
		return d, err
	}
	t0 = time.Now()
	lines, err := s.lines(ctx, id)
	if timer != nil {
		timer.mark("store_lines", t0)
	}
	if err != nil {
		return nil, err
	}
	d.Lines = lines
	t0 = time.Now()
	turns, hasMore, err := s.editTurnsPage(ctx, id, limit, beforeID)
	if timer != nil {
		timer.mark("store_edit_turns", t0)
	}
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
		current, err := s.getDiscussion(ctx, owner, id)
		if err != nil {
			return nil, err
		}
		if current != nil && current.Script != nil {
			if err := s.seedSpeakerModelOverrides(ctx, id, current.Script); err != nil {
				return nil, err
			}
		}
		if err := s.applySpeakerModelOverridesToScript(ctx, id, resp.Script); err != nil {
			return nil, err
		}
		// Voice overrides are deliberately NOT applied here: baking a voice
		// into the persisted script_json would survive a later "clear back to
		// auto" (the override row is deleted but the stored script keeps the
		// stale voice). Voices are layered on at read time only.
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
	set := `title = ?, language = ?, script_json = ?, markdown = ?, sources_json = ?, researched = ?, updated_at = ?`
	args := []any{title, language, scriptJSON, resp.Markdown, sourcesJSON, boolInt(resp.Researched), time.Now().UnixMilli()}
	if resp.Script != nil && resp.Script.Type == config.ContentTypeUploadedAudio && strings.TrimSpace(title) != "" {
		// An uploaded-audio topic starts as the raw upload filename; once the
		// plan carries a generated title, the visible topic follows it.
		set += `, topic = ?`
		args = append(args, title)
	}
	args = append(args, owner, id)
	res, err := s.exec(ctx, `UPDATE native_discussions SET `+set+` WHERE owner_user_id = ? AND id = ?`, args...)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil
	}
	if resp.Script != nil {
		if err := s.upsertSpeakerModelOverrides(ctx, id, resp.Script); err != nil {
			return nil, err
		}
	}
	return s.Get(ctx, owner, id)
}

// UpdateUploadedAudioPlan persists a transcript-plan snapshot and its visible
// edit-history turn atomically. Uploaded-audio plans cannot contain generative
// speaker models, so this narrow path can avoid UpdatePlan's model-override
// reads/upserts while still writing all caption changes in one script_json
// UPDATE regardless of how many segments changed.
func (s *DiscussionStore) UpdateUploadedAudioPlan(
	ctx context.Context,
	owner, id, label string,
	resp planResponse,
) (*Discussion, error) {
	if resp.Script == nil || resp.Script.Type != config.ContentTypeUploadedAudio {
		return nil, errors.New("uploaded-audio transcript update requires an uploaded-audio plan")
	}
	scriptJSON, err := marshalString(resp.Script)
	if err != nil {
		return nil, err
	}
	sourcesJSON, err := marshalString(resp.Sources)
	if err != nil {
		return nil, err
	}

	now := time.Now().UnixMilli()
	opID := newJobID()
	updated := false
	err = retryTransientDBConnection(ctx, func() error {
		updated = false
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		set := `title = ?, language = ?, script_json = ?, markdown = ?, sources_json = ?, researched = ?, updated_at = ?`
		args := []any{resp.Script.Title, resp.Script.Language, scriptJSON, resp.Markdown, sourcesJSON,
			boolInt(resp.Researched), now}
		if strings.TrimSpace(resp.Script.Title) != "" {
			// Keep the filename-seeded topic in step with the transcript title
			// (see UpdatePlan).
			set += `, topic = ?`
			args = append(args, resp.Script.Title)
		}
		args = append(args, owner, id)
		res, err := tx.ExecContext(ctx, `UPDATE native_discussions SET `+set+` WHERE owner_user_id = ? AND id = ?`, args...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO native_discussion_edit_turns
			(op_id, discussion_id, role, text, script_json, sources_json, markdown, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
			opID, id, "plan", label, scriptJSON, sourcesJSON, resp.Markdown, now); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		updated = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !updated {
		return nil, nil
	}
	return s.Get(ctx, owner, id)
}

// applySpeakerOverrides layers the user's per-speaker model and voice
// overrides onto a freshly loaded discussion's script.
func (s *DiscussionStore) applySpeakerOverrides(ctx context.Context, d *Discussion) error {
	return s.applySpeakerOverridesTimed(ctx, d, nil)
}

func (s *DiscussionStore) applySpeakerOverridesTimed(ctx context.Context, d *Discussion, timer *stationTimer) error {
	if d == nil || d.Script == nil {
		return nil
	}
	t0 := time.Now()
	models, voices, err := s.speakerOverrides(ctx, d.ID)
	if timer != nil {
		timer.mark("store_speaker_overrides_query", t0)
	}
	if err != nil {
		return err
	}
	t0 = time.Now()
	applySpeakerModelOverridesToTopic(d.Script, models)
	applySpeakerVoiceOverridesToTopic(d.Script, voices)
	if timer != nil {
		timer.mark("store_speaker_overrides_apply", t0)
	}
	return nil
}

func (s *DiscussionStore) applySpeakerModelOverridesToScript(ctx context.Context, discussionID string, script *config.DebateTopic) error {
	if script == nil {
		return nil
	}
	overrides, err := s.speakerModelOverrides(ctx, discussionID)
	if err != nil || len(overrides) == 0 {
		return err
	}
	applySpeakerModelOverridesToTopic(script, overrides)
	return nil
}

func (s *DiscussionStore) speakerModelOverrides(ctx context.Context, discussionID string) (map[string]string, error) {
	discussionID = strings.TrimSpace(discussionID)
	if discussionID == "" {
		return nil, nil
	}
	overrides := map[string]string{}
	err := retryTransientDBConnection(ctx, func() error {
		rows, err := s.db.QueryContext(ctx, `SELECT speaker_name, model
			FROM native_discussion_speaker_models WHERE discussion_id = ?`, discussionID)
		if err != nil {
			return err
		}
		defer rows.Close()
		next := map[string]string{}
		for rows.Next() {
			var speaker, model string
			if err := rows.Scan(&speaker, &model); err != nil {
				return err
			}
			speaker = strings.TrimSpace(speaker)
			model = strings.TrimSpace(model)
			if speaker == "" || model == "" {
				continue
			}
			next[speaker] = model
		}
		if err := rows.Err(); err != nil {
			return err
		}
		overrides = next
		return nil
	})
	return overrides, err
}

func (s *DiscussionStore) speakerOverrides(ctx context.Context, discussionID string) (map[string]string, map[string]string, error) {
	discussionID = strings.TrimSpace(discussionID)
	if discussionID == "" {
		return nil, nil, nil
	}
	models := map[string]string{}
	voices := map[string]string{}
	err := retryTransientDBConnection(ctx, func() error {
		rows, err := s.db.QueryContext(ctx, `SELECT kind, speaker_name, value FROM (
			SELECT 'model' AS kind, speaker_name, model AS value
			FROM native_discussion_speaker_models WHERE discussion_id = ?
			UNION ALL
			SELECT 'voice' AS kind, speaker_name, voice AS value
			FROM native_discussion_speaker_voices WHERE discussion_id = ?
		)`, discussionID, discussionID)
		if err != nil {
			return err
		}
		defer rows.Close()
		nextModels := map[string]string{}
		nextVoices := map[string]string{}
		for rows.Next() {
			var kind, speaker, value string
			if err := rows.Scan(&kind, &speaker, &value); err != nil {
				return err
			}
			speaker = strings.TrimSpace(speaker)
			value = strings.TrimSpace(value)
			if speaker == "" || value == "" {
				continue
			}
			switch kind {
			case "model":
				nextModels[speaker] = value
			case "voice":
				nextVoices[speaker] = value
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		models = nextModels
		voices = nextVoices
		return nil
	})
	return models, voices, err
}

func (s *DiscussionStore) seedSpeakerModelOverrides(ctx context.Context, discussionID string, script *config.DebateTopic) error {
	if script == nil {
		return nil
	}
	var count int
	err := retryTransientDBConnection(ctx, func() error {
		return s.db.QueryRowContext(ctx,
			`SELECT COUNT(1) FROM native_discussion_speaker_models WHERE discussion_id = ?`,
			discussionID).Scan(&count)
	})
	if err != nil || count > 0 {
		return err
	}
	return s.upsertSpeakerModelOverrides(ctx, discussionID, script)
}

func (s *DiscussionStore) upsertSpeakerModelOverrides(ctx context.Context, discussionID string, script *config.DebateTopic) error {
	if script == nil {
		return nil
	}
	now := time.Now().UnixMilli()
	for speaker, model := range speakerModelsFromTopic(script) {
		if _, err := s.exec(ctx, `INSERT INTO native_discussion_speaker_models
			(discussion_id, speaker_name, model, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(discussion_id, speaker_name) DO UPDATE SET
				model = excluded.model,
				updated_at = excluded.updated_at`,
			discussionID, speaker, model, now, now); err != nil {
			return err
		}
	}
	return nil
}

func speakerModelsFromTopic(topic *config.DebateTopic) map[string]string {
	out := map[string]string{}
	if topic == nil {
		return out
	}
	add := func(speaker, model string) {
		speaker = strings.TrimSpace(speaker)
		model = strings.TrimSpace(model)
		if speaker == "" || model == "" {
			return
		}
		out[speaker] = model
	}
	add(topic.Host.Name, topic.Host.Model)
	for _, d := range topic.Discussants {
		add(d.Name, d.Model)
	}
	add(topic.AudioBookHost.Name, topic.AudioBookHost.Model)
	for _, speaker := range topic.AudioBookSpeakers {
		add(speaker.Name, speaker.Model)
	}
	return out
}

func applySpeakerModelOverridesToTopic(topic *config.DebateTopic, overrides map[string]string) {
	if topic == nil || len(overrides) == 0 {
		return
	}
	if model := overrides[strings.TrimSpace(topic.Host.Name)]; model != "" {
		topic.Host.Model = model
	}
	if model := overrides[strings.TrimSpace(topic.AudioBookHost.Name)]; model != "" {
		topic.AudioBookHost.Model = model
	}
	for i := range topic.Discussants {
		if model := overrides[strings.TrimSpace(topic.Discussants[i].Name)]; model != "" {
			topic.Discussants[i].Model = model
		}
	}
	for i := range topic.AudioBookSpeakers {
		if model := overrides[strings.TrimSpace(topic.AudioBookSpeakers[i].Name)]; model != "" {
			topic.AudioBookSpeakers[i].Model = model
		}
	}
}

// SetSpeakerModel changes the LLM model override for a single speaker (the
// discussion host/discussant, audiobook narrator, or audiobook speaker, matched
// by name) in the plan. The override lives outside script_json so later plan
// regenerations that add/remove speakers keep the user's existing per-speaker
// assignments by speaker name. Returns nil (→ 404) when the discussion has no
// plan or no speaker matches the given name.
func (s *DiscussionStore) SetSpeakerModel(ctx context.Context, owner, id, speaker, model string) (*Discussion, error) {
	speaker = strings.TrimSpace(speaker)
	model = strings.TrimSpace(model)
	d, err := s.getDiscussion(ctx, owner, id)
	if err != nil || d == nil {
		return nil, err
	}
	if d.Script == nil {
		return nil, nil
	}
	if err := s.seedSpeakerModelOverrides(ctx, id, d.Script); err != nil {
		return nil, err
	}
	matched := false
	if d.Script.Host.Name == speaker {
		matched = true
	}
	if d.Script.AudioBookHost.Name == speaker {
		matched = true
	}
	for i := range d.Script.Discussants {
		if d.Script.Discussants[i].Name == speaker {
			matched = true
		}
	}
	for i := range d.Script.AudioBookSpeakers {
		if d.Script.AudioBookSpeakers[i].Name == speaker {
			matched = true
		}
	}
	if !matched {
		return nil, nil
	}
	now := time.Now().UnixMilli()
	_, err = s.exec(ctx, `INSERT INTO native_discussion_speaker_models
		(discussion_id, speaker_name, model, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(discussion_id, speaker_name) DO UPDATE SET
			model = excluded.model,
			updated_at = excluded.updated_at`,
		id, speaker, model, now, now)
	if err != nil {
		return nil, err
	}
	res, err := s.exec(ctx, `UPDATE native_discussions SET updated_at = ?
		WHERE owner_user_id = ? AND id = ?`, now, owner, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil
	}
	return s.Get(ctx, owner, id)
}

func (s *DiscussionStore) speakerVoiceOverrides(ctx context.Context, discussionID string) (map[string]string, error) {
	discussionID = strings.TrimSpace(discussionID)
	if discussionID == "" {
		return nil, nil
	}
	overrides := map[string]string{}
	err := retryTransientDBConnection(ctx, func() error {
		rows, err := s.db.QueryContext(ctx, `SELECT speaker_name, voice
			FROM native_discussion_speaker_voices WHERE discussion_id = ?`, discussionID)
		if err != nil {
			return err
		}
		defer rows.Close()
		next := map[string]string{}
		for rows.Next() {
			var speaker, voice string
			if err := rows.Scan(&speaker, &voice); err != nil {
				return err
			}
			speaker = strings.TrimSpace(speaker)
			voice = strings.TrimSpace(voice)
			if speaker == "" || voice == "" {
				continue
			}
			next[speaker] = voice
		}
		if err := rows.Err(); err != nil {
			return err
		}
		overrides = next
		return nil
	})
	return overrides, err
}

func (s *DiscussionStore) applySpeakerVoiceOverridesToScript(ctx context.Context, discussionID string, script *config.DebateTopic) error {
	if script == nil {
		return nil
	}
	overrides, err := s.speakerVoiceOverrides(ctx, discussionID)
	if err != nil || len(overrides) == 0 {
		return err
	}
	applySpeakerVoiceOverridesToTopic(script, overrides)
	return nil
}

func applySpeakerVoiceOverridesToTopic(topic *config.DebateTopic, overrides map[string]string) {
	if topic == nil || len(overrides) == 0 {
		return
	}
	if voice := overrides[strings.TrimSpace(topic.Host.Name)]; voice != "" {
		topic.Host.Voice = voice
	}
	if voice := overrides[strings.TrimSpace(topic.AudioBookHost.Name)]; voice != "" {
		topic.AudioBookHost.Voice = voice
	}
	for i := range topic.Discussants {
		if voice := overrides[strings.TrimSpace(topic.Discussants[i].Name)]; voice != "" {
			topic.Discussants[i].Voice = voice
		}
	}
	for i := range topic.AudioBookSpeakers {
		if voice := overrides[strings.TrimSpace(topic.AudioBookSpeakers[i].Name)]; voice != "" {
			topic.AudioBookSpeakers[i].Voice = voice
		}
	}
}

// SetSpeakerVoice changes the TTS voice override for a single speaker (the
// discussion host/discussant, audiobook narrator, or audiobook speaker, matched
// by name) in the plan. Like model overrides, the value lives outside
// script_json so plan regenerations keep the user's per-speaker voice by
// speaker name. An empty voice clears the override (back to automatic
// assignment). Unlike models there is nothing to seed: plans declare no
// per-speaker voice, so absent rows simply mean "auto". Returns nil (→ 404)
// when the discussion has no plan or no speaker matches the given name.
func (s *DiscussionStore) SetSpeakerVoice(ctx context.Context, owner, id, speaker, voice string) (*Discussion, error) {
	speaker = strings.TrimSpace(speaker)
	voice = strings.TrimSpace(voice)
	d, err := s.getDiscussion(ctx, owner, id)
	if err != nil || d == nil {
		return nil, err
	}
	if d.Script == nil {
		return nil, nil
	}
	matched := d.Script.Host.Name == speaker || d.Script.AudioBookHost.Name == speaker
	for i := range d.Script.Discussants {
		if d.Script.Discussants[i].Name == speaker {
			matched = true
		}
	}
	for i := range d.Script.AudioBookSpeakers {
		if d.Script.AudioBookSpeakers[i].Name == speaker {
			matched = true
		}
	}
	if !matched {
		return nil, nil
	}
	now := time.Now().UnixMilli()
	if voice == "" {
		_, err = s.exec(ctx, `DELETE FROM native_discussion_speaker_voices
			WHERE discussion_id = ? AND speaker_name = ?`, id, speaker)
	} else {
		_, err = s.exec(ctx, `INSERT INTO native_discussion_speaker_voices
			(discussion_id, speaker_name, voice, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(discussion_id, speaker_name) DO UPDATE SET
				voice = excluded.voice,
				updated_at = excluded.updated_at`,
			id, speaker, voice, now, now)
	}
	if err != nil {
		return nil, err
	}
	res, err := s.exec(ctx, `UPDATE native_discussions SET updated_at = ?
		WHERE owner_user_id = ? AND id = ?`, now, owner, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, nil
	}
	return s.Get(ctx, owner, id)
}

// GetVoicePreview returns the stored S3 key for a cached (voice, language)
// preview MP3, or "" when none has been rendered yet.
func (s *DiscussionStore) GetVoicePreview(ctx context.Context, voice, language string) (string, error) {
	voice = strings.TrimSpace(voice)
	language = strings.TrimSpace(language)
	if voice == "" || language == "" {
		return "", nil
	}
	var key string
	err := retryTransientDBConnection(ctx, func() error {
		row := s.db.QueryRowContext(ctx, `SELECT s3_key FROM voice_previews
			WHERE voice = ? AND language = ?`, voice, language)
		switch err := row.Scan(&key); {
		case errors.Is(err, sql.ErrNoRows):
			key = ""
			return nil
		default:
			return err
		}
	})
	return key, err
}

// PutVoicePreview records the S3 key of a rendered (voice, language) preview
// MP3 so later requests skip synthesis.
func (s *DiscussionStore) PutVoicePreview(ctx context.Context, voice, language, s3Key string) error {
	voice = strings.TrimSpace(voice)
	language = strings.TrimSpace(language)
	s3Key = strings.TrimSpace(s3Key)
	if voice == "" || language == "" || s3Key == "" {
		return errors.New("voice preview: empty voice, language, or key")
	}
	_, err := s.exec(ctx, `INSERT INTO voice_previews (voice, language, s3_key, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(voice, language) DO UPDATE SET s3_key = excluded.s3_key`,
		voice, language, s3Key, time.Now().UnixMilli())
	return err
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

// SetDurationSeconds records a podcast's real audio duration. Written by the
// uploaded-audio publish path, where the duration comes from probing the
// user's file rather than from a synthesis timeline.
func (s *DiscussionStore) SetDurationSeconds(ctx context.Context, id string, seconds float64) error {
	_, err := s.exec(ctx, `UPDATE native_discussions SET duration_seconds = ?, updated_at = ? WHERE id = ?`,
		seconds, time.Now().UnixMilli(), id)
	return err
}

func (s *DiscussionStore) SetJobResult(ctx context.Context, id string, status DiscussionStatus, downloadURL string) error {
	_, err := s.exec(ctx, `UPDATE native_discussions SET status = ?, download_url = ?, updated_at = ?
		WHERE id = ?`, status, downloadURL, time.Now().UnixMilli(), id)
	return err
}

func (s *DiscussionStore) SetJobResultAndUsage(ctx context.Context, id string, status DiscussionStatus, downloadURL string, usage PointsUsageDetail) error {
	_, err := s.exec(ctx, `UPDATE native_discussions SET
		status = ?, download_url = ?,
		prompt_tokens = ?, completion_tokens = ?, total_tokens = ?, llm_cost_usd = ?, llm_cost_known = ?,
		tts_cost_usd = ?, music_cost_usd = ?, updated_at = ?
		WHERE id = ?`,
		status, downloadURL,
		usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, usage.LLMCostUSD, boolInt(usage.LLMCostKnown),
		usage.TTSCostUSD, usage.MusicCostUSD, time.Now().UnixMilli(), id)
	return err
}

// SetVideoKey records the object-storage key of an audiobook's rendered video
// so the context menu can presign a playback URL on demand.
func (s *DiscussionStore) SetVideoKey(ctx context.Context, id, key string) error {
	res, err := s.exec(ctx, `UPDATE native_discussions SET video_key = ?, updated_at = ?
		WHERE id = ?`, strings.TrimSpace(key), time.Now().UnixMilli(), strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// SetVideoKeyForJob records an audiobook video key using the durable job link.
// This is the recovery path for post-audio video renders where the object was
// uploaded under the job id but the discussion id was missing or stale.
func (s *DiscussionStore) SetVideoKeyForJob(ctx context.Context, jobID, key string) error {
	res, err := s.exec(ctx, `UPDATE native_discussions SET video_key = ?, updated_at = ?
		WHERE job_id = ?`, strings.TrimSpace(key), time.Now().UnixMilli(), strings.TrimSpace(jobID))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// VideoKeyFor returns the stored video object key for a discussion, or "" when
// none has been rendered yet.
func (s *DiscussionStore) VideoKeyFor(ctx context.Context, id string) (string, error) {
	if s == nil {
		return "", nil
	}
	var key string
	err := s.db.QueryRowContext(ctx, `SELECT video_key FROM native_discussions WHERE id = ?`, id).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return key, err
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
	// and ON CONFLICT collapses a retried insert into a no-op — while two
	// genuinely separate appends (even with identical text) get distinct ids and
	// both persist.
	opID := newJobID()
	_, err := s.exec(ctx, `INSERT INTO native_discussion_edit_turns
		(op_id, discussion_id, role, text, script_json, sources_json, markdown, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
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
// idempotent (delete-all-agent-lines, then ON CONFLICT, then bump
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
		imageURL := strings.TrimSpace(l.ImageURL)
		// Keep image-only illustration lines (empty spoken text) so audiobook
		// pictures survive reload; only drop genuinely empty lines.
		if text == "" && imageURL == "" {
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
		sourcesJSON, err := marshalString(l.Sources)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO native_discussion_lines
			(discussion_id, speaker, role, side, text, start_ms, is_user, image_url, sources_json, judgement_comment, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
			id, l.Speaker, string(l.Role), string(l.Side), text, l.AudioOffsetMS, 0,
			imageURL, sourcesJSON, strings.TrimSpace(l.JudgementComment), createdAt)
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
	rows, err := s.db.QueryContext(ctx, `SELECT speaker, role, side, text, start_ms, is_user, sender_user_id, audio_url, audio_key, image_url, sources_json, judgement_comment
		FROM native_discussion_lines WHERE discussion_id = ? ORDER BY created_at, id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]DiscussionLine, 0)
	for rows.Next() {
		var line DiscussionLine
		var isUser int
		var sourcesJSON string
		if err := rows.Scan(&line.Speaker, &line.Role, &line.Side, &line.Text, &line.StartMS, &isUser, &line.SenderUserID, &line.AudioURL, &line.AudioKey, &line.ImageURL, &sourcesJSON, &line.JudgementComment); err != nil {
			return nil, err
		}
		if strings.TrimSpace(sourcesJSON) != "" {
			_ = json.Unmarshal([]byte(sourcesJSON), &line.Sources)
		}
		if !discussionLineHasDisplayablePayload(line) {
			continue
		}
		line.IsUser = isUser != 0
		out = append(out, line)
	}
	return out, rows.Err()
}

func discussionLineHasDisplayablePayload(line DiscussionLine) bool {
	return strings.TrimSpace(line.Text) != "" ||
		strings.TrimSpace(line.AudioURL) != "" ||
		strings.TrimSpace(line.AudioKey) != "" ||
		strings.TrimSpace(line.ImageURL) != "" ||
		len(line.Sources) > 0 ||
		strings.TrimSpace(line.JudgementComment) != ""
}

func (s *DiscussionStore) EditTurns(ctx context.Context, owner, id string) ([]DiscussionEditTurn, error) {
	if ok, err := s.owns(ctx, owner, id); err != nil || !ok {
		return nil, err
	}
	return s.editTurns(ctx, id)
}

func (s *DiscussionStore) editTurns(ctx context.Context, id string) ([]DiscussionEditTurn, error) {
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
	return s.editTurnsPage(ctx, id, limit, beforeID)
}

func (s *DiscussionStore) editTurnsPage(ctx context.Context, id string, limit int, beforeID int64) ([]DiscussionEditTurn, bool, error) {
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
	imageURL := strings.TrimSpace(line.ImageURL)
	audioURL := strings.TrimSpace(line.AudioURL)
	audioKey := strings.TrimSpace(line.AudioKey)
	// Image-only audiobook illustrations and transcriptless voice messages carry
	// no spoken text; keep them so they survive reload. Only genuinely empty
	// lines are dropped.
	if text == "" && imageURL == "" && audioURL == "" && audioKey == "" {
		return nil
	}
	_, err := s.exec(ctx, `INSERT INTO native_discussion_lines
		(discussion_id, speaker, role, side, text, start_ms, is_user, sender_user_id, audio_url, audio_key, image_url, sources_json, judgement_comment, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
		id, strings.TrimSpace(line.Speaker), strings.TrimSpace(line.Role), strings.TrimSpace(line.Side),
		text, line.StartMS, boolInt(line.IsUser), strings.TrimSpace(line.SenderUserID),
		audioURL, audioKey, imageURL, "", "",
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
		&d.TTSCostUSD, &d.MusicCostUSD, &d.PointsCharged, &d.PointsReserved, &d.Visibility, &published, &d.Cover.Type, &d.Cover.ImageURL,
		&d.Cover.ImageKey, &d.Cover.GradientStart, &d.Cover.GradientEnd, &d.Cover.Prompt,
		&scriptJSON, &d.Markdown, &sourcesJSON, &researched,
		&d.ReferenceDiscussionID, &d.Template, &created, &updated, &d.AlbumID)
	if err != nil {
		return d, err
	}
	finalizeScannedDiscussion(&d, scriptJSON, sourcesJSON, researched, costKnown, published, created, updated)
	return d, nil
}

func scanDiscussionWithJoinedJob(row discussionScanner) (Discussion, error) {
	var d Discussion
	var scriptJSON, sourcesJSON string
	var researched int
	var created, updated, published int64
	var costKnown int
	var jobID, jobStatus, jobS3Key, jobAudioS3Key sql.NullString
	var jobAudioOnly, jobCostKnown sqlBoolInt
	var jobPromptTokens, jobCompletionTokens, jobTotalTokens sql.NullInt64
	var jobLLMCostUSD, jobTTSCostUSD, jobMusicCostUSD sql.NullFloat64
	err := row.Scan(&d.ID, &d.OwnerUserID, &d.Topic, &d.Title, &d.Status, &d.Language, &d.JobID,
		&d.DownloadURL, &d.DurationSeconds, &d.PromptTokens, &d.CompletionTokens, &d.TotalTokens, &d.LLMCostUSD, &costKnown,
		&d.TTSCostUSD, &d.MusicCostUSD, &d.PointsCharged, &d.PointsReserved, &d.Visibility, &published, &d.Cover.Type, &d.Cover.ImageURL,
		&d.Cover.ImageKey, &d.Cover.GradientStart, &d.Cover.GradientEnd, &d.Cover.Prompt,
		&scriptJSON, &d.Markdown, &sourcesJSON, &researched,
		&d.ReferenceDiscussionID, &d.Template, &created, &updated, &d.AlbumID,
		&jobID, &jobStatus, &jobS3Key, &jobAudioS3Key, &jobAudioOnly,
		&jobPromptTokens, &jobCompletionTokens, &jobTotalTokens, &jobLLMCostUSD, &jobCostKnown,
		&jobTTSCostUSD, &jobMusicCostUSD)
	if err != nil {
		return d, err
	}
	finalizeScannedDiscussion(&d, scriptJSON, sourcesJSON, researched, costKnown, published, created, updated)
	if jobID.Valid && strings.TrimSpace(jobID.String) != "" {
		d.joinedJob = &Job{
			ID:               jobID.String,
			Status:           JobStatus(jobStatus.String),
			S3Key:            jobS3Key.String,
			AudioS3Key:       jobAudioS3Key.String,
			AudioOnly:        jobAudioOnly.Int() != 0,
			PromptTokens:     jobPromptTokens.Int64,
			CompletionTokens: jobCompletionTokens.Int64,
			TotalTokens:      jobTotalTokens.Int64,
			LLMCostUSD:       jobLLMCostUSD.Float64,
			LLMCostKnown:     jobCostKnown.Int() != 0,
			TTSCostUSD:       jobTTSCostUSD.Float64,
			MusicCostUSD:     jobMusicCostUSD.Float64,
		}
	}
	return d, nil
}

func scanDiscussionWithSummary(row discussionScanner) (Discussion, error) {
	var d Discussion
	var scriptJSON, sourcesJSON string
	var researched int
	var created, updated, published int64
	var costKnown int
	var summaryStatus sql.NullString
	var summaryGeneratedAt sql.NullInt64
	err := row.Scan(&d.ID, &d.OwnerUserID, &d.Topic, &d.Title, &d.Status, &d.Language, &d.JobID,
		&d.DownloadURL, &d.DurationSeconds, &d.PromptTokens, &d.CompletionTokens, &d.TotalTokens, &d.LLMCostUSD, &costKnown,
		&d.TTSCostUSD, &d.MusicCostUSD, &d.PointsCharged, &d.PointsReserved, &d.Visibility, &published, &d.Cover.Type, &d.Cover.ImageURL,
		&d.Cover.ImageKey, &d.Cover.GradientStart, &d.Cover.GradientEnd, &d.Cover.Prompt,
		&scriptJSON, &d.Markdown, &sourcesJSON, &researched,
		&d.ReferenceDiscussionID, &d.Template, &created, &updated, &d.AlbumID, &summaryStatus, &summaryGeneratedAt)
	if err != nil {
		return d, err
	}
	finalizeScannedDiscussion(&d, scriptJSON, sourcesJSON, researched, costKnown, published, created, updated)
	applyJoinedSummaryMeta(&d, summaryStatus, summaryGeneratedAt)
	return d, nil
}

func scanDiscussionWithMarket(row discussionScanner) (Discussion, error) {
	var d Discussion
	var scriptJSON, sourcesJSON string
	var researched int
	var created, updated, published int64
	var costKnown int
	var liked, owner sqlBoolInt
	err := row.Scan(&d.ID, &d.OwnerUserID, &d.Topic, &d.Title, &d.Status, &d.Language, &d.JobID,
		&d.DownloadURL, &d.DurationSeconds, &d.PromptTokens, &d.CompletionTokens, &d.TotalTokens, &d.LLMCostUSD, &costKnown,
		&d.TTSCostUSD, &d.MusicCostUSD, &d.PointsCharged, &d.PointsReserved, &d.Visibility, &published, &d.Cover.Type, &d.Cover.ImageURL,
		&d.Cover.ImageKey, &d.Cover.GradientStart, &d.Cover.GradientEnd, &d.Cover.Prompt,
		&scriptJSON, &d.Markdown, &sourcesJSON, &researched,
		&d.ReferenceDiscussionID, &d.Template, &created, &updated, &d.AlbumID, &d.LikeCount, &liked, &owner)
	if err != nil {
		return d, err
	}
	finalizeScannedDiscussion(&d, scriptJSON, sourcesJSON, researched, costKnown, published, created, updated)
	d.IsLiked = liked.Int() != 0
	d.IsOwner = owner.Int() != 0
	d.ShowUsageSummary = d.IsOwner
	return d, nil
}

func scanDiscussionWithMarketSummary(row discussionScanner) (Discussion, error) {
	var d Discussion
	var scriptJSON, sourcesJSON string
	var researched int
	var created, updated, published int64
	var costKnown int
	var liked, owner sqlBoolInt
	var summaryStatus sql.NullString
	var summaryGeneratedAt sql.NullInt64
	err := row.Scan(&d.ID, &d.OwnerUserID, &d.Topic, &d.Title, &d.Status, &d.Language, &d.JobID,
		&d.DownloadURL, &d.DurationSeconds, &d.PromptTokens, &d.CompletionTokens, &d.TotalTokens, &d.LLMCostUSD, &costKnown,
		&d.TTSCostUSD, &d.MusicCostUSD, &d.PointsCharged, &d.PointsReserved, &d.Visibility, &published, &d.Cover.Type, &d.Cover.ImageURL,
		&d.Cover.ImageKey, &d.Cover.GradientStart, &d.Cover.GradientEnd, &d.Cover.Prompt,
		&scriptJSON, &d.Markdown, &sourcesJSON, &researched,
		&d.ReferenceDiscussionID, &d.Template, &created, &updated, &d.AlbumID, &d.LikeCount, &liked, &owner, &summaryStatus, &summaryGeneratedAt)
	if err != nil {
		return d, err
	}
	finalizeScannedDiscussion(&d, scriptJSON, sourcesJSON, researched, costKnown, published, created, updated)
	d.IsLiked = liked.Int() != 0
	d.IsOwner = owner.Int() != 0
	d.ShowUsageSummary = d.IsOwner
	applyJoinedSummaryMeta(&d, summaryStatus, summaryGeneratedAt)
	return d, nil
}

func applyJoinedSummaryMeta(d *Discussion, status sql.NullString, generatedAt sql.NullInt64) {
	if d == nil {
		return
	}
	d.summaryMetaLoaded = true
	if !status.Valid {
		return
	}
	st := SummaryStatus(status.String)
	meta := &SummaryMeta{
		DocType:   SummaryDocTypeSummary,
		Status:    st,
		Available: st == SummaryReadyState,
		Pending:   st == SummaryGenerating,
	}
	if generatedAt.Valid && generatedAt.Int64 > 0 {
		t := time.UnixMilli(generatedAt.Int64)
		meta.GeneratedAt = &t
	}
	d.Summary = meta
}

func scanCreatorProfile(row discussionScanner) (CreatorProfile, bool, error) {
	var p CreatorProfile
	var followed, self, visible sqlBoolInt
	err := row.Scan(&p.ID, &p.DisplayName, &p.Username, &p.AvatarURL, &p.FollowerCount, &followed, &self, &visible)
	if err != nil {
		return p, false, err
	}
	if strings.TrimSpace(p.DisplayName) == "" {
		p.DisplayName = "Creator"
	}
	p.IsFollowed = followed.Int() != 0
	p.IsSelf = self.Int() != 0
	if p.IsSelf {
		p.IsFollowed = false
	}
	return p, visible.Int() != 0, nil
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
	if strings.TrimSpace(d.Template) == "" {
		d.Template = "default"
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
	return s.db.ensureColumn(ctx, table, column, definition)
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
