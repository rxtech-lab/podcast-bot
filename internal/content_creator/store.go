package contentcreator

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// MessageRow is one persisted transcript line — both user-typed messages and
// AI-spoken turns share the same table because the chat UI renders them
// uniformly. Auto-incrementing ID gives us stable ordering on reload (the
// `at` timestamp resolution alone wasn't enough — sub-millisecond turns
// from the same agent could land out of order).
type MessageRow struct {
	ID               uint   `gorm:"primaryKey;autoIncrement"`
	Speaker          string `gorm:"index;size:64;not null"`
	Role             string `gorm:"index;size:32;not null"`
	Side             string `gorm:"size:32"`
	Text             string `gorm:"type:text;not null"`
	At               time.Time
	SourcesJSON      string `gorm:"type:text"`
	JudgementComment string `gorm:"type:text"`
}

// TableName pins the table to "messages" so future rename of the Go type
// doesn't accidentally invalidate existing on-disk schemas.
func (MessageRow) TableName() string { return "messages" }

// Store is the per-debate sqlite-backed persistence layer for the chat
// transcript. Each debate gets its own .db file (typically
// `{outdir}/session.db`), which keeps a debate's data co-located with its
// audio + text artefacts and makes archival a single-file copy.
//
// Append is non-blocking on writes that fail (logged and dropped) so a
// disk-full or locked-DB condition can't stall the live debate. Reads are
// strict — a load failure returns the error so the caller can fall back to
// the in-memory snapshot or surface the error to the UI.
type Store struct {
	db  *gorm.DB
	mu  sync.Mutex
	log *slog.Logger
}

// OpenStore creates / migrates the messages table at path. The parent dir
// must already exist (the orchestrator's debate.EnsureOutDir call covers
// this in production).
func OpenStore(path string, log *slog.Logger) (*Store, error) {
	if log == nil {
		log = slog.Default()
	}
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{
		// Quiet by default — we route errors through our own logger so they
		// land in the debate's log stream alongside everything else.
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	if err := db.AutoMigrate(&MessageRow{}); err != nil {
		return nil, fmt.Errorf("migrate messages: %w", err)
	}
	// Foreground writes — keep WAL on for concurrent readers (the server
	// reads the snapshot while the orchestrator is appending) without
	// stalling reads behind the writer.
	if err := db.Exec("PRAGMA journal_mode=WAL;").Error; err != nil {
		log.Warn("sqlite WAL failed", "path", path, "err", err)
	}
	return &Store{db: db, log: log}, nil
}

// Append persists one transcript line. Failures are logged and dropped —
// the in-memory transcript remains the source of truth for the live UI;
// the DB is for reload-after-end and post-mortem inspection.
func (s *Store) Append(line agent.TranscriptLine) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var sourcesJSON string
	if len(line.Sources) > 0 {
		if b, err := json.Marshal(line.Sources); err == nil {
			sourcesJSON = string(b)
		}
	}
	row := MessageRow{
		Speaker:          line.Speaker,
		Role:             string(line.Role),
		Side:             line.Side,
		Text:             line.Text,
		At:               line.At,
		SourcesJSON:      sourcesJSON,
		JudgementComment: line.JudgementComment,
	}
	if err := s.db.Create(&row).Error; err != nil {
		s.log.Warn("sqlite append failed", "speaker", line.Speaker, "err", err)
	}
}

// Snapshot returns every row in insertion order. Callers should treat the
// returned slice as read-only.
func (s *Store) Snapshot() ([]agent.TranscriptLine, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var rows []MessageRow
	if err := s.db.Order("id ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("snapshot: %w", err)
	}
	out := make([]agent.TranscriptLine, len(rows))
	for i, r := range rows {
		var sources []agent.TranscriptSource
		if strings.TrimSpace(r.SourcesJSON) != "" {
			_ = json.Unmarshal([]byte(r.SourcesJSON), &sources)
		}
		out[i] = agent.TranscriptLine{
			Speaker:          r.Speaker,
			Role:             agent.Role(r.Role),
			Side:             r.Side,
			Text:             r.Text,
			At:               r.At,
			Sources:          sources,
			JudgementComment: r.JudgementComment,
		}
	}
	return out, nil
}

// Close releases the underlying sqlite handle. Safe to call on a nil store.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// LoadSnapshot is a convenience for callers that just need to read a
// transcript out of an existing .db file (e.g. the HTTP server serving
// /api/transcript for a finished debate). Returns ErrNoStore if the file
// doesn't exist yet.
func LoadSnapshot(path string) ([]agent.TranscriptLine, error) {
	st, err := OpenStore(path, nil)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	return st.Snapshot()
}

// ErrNoStore signals that no on-disk transcript exists yet for the requested
// debate (e.g. the run was killed before any line was persisted).
var ErrNoStore = errors.New("no transcript store")
