package server

import (
	"encoding/json"
	"errors"
	"fmt"
	stdlog "log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/sirily11/debate-bot/internal/content_creator"
)

// JobStatus is the lifecycle of a video-mode upload job.
type JobStatus string

const (
	JobPending JobStatus = "pending"
	JobRunning JobStatus = "running"
	JobDone    JobStatus = "done"
	JobError   JobStatus = "error"
)

// JobLog is one persisted progress/log line for a video-mode job.
type JobLog struct {
	TS   int64  `json:"ts"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// Job is one upload-and-render request tracked by the registry.
//
// VideoPath / ArchivePath are absolute on-disk paths populated when the
// job finishes successfully. The HTTP layer serves them via
// /api/jobs/<id>/video and /api/jobs/<id>/archive; clients never see
// the underlying paths, only the URLs.
//
// Topic / Show / Type are echoed back so the SPA can render a finished-
// job header without re-parsing the user's upload.
type Job struct {
	ID          string    `json:"id"`
	Status      JobStatus `json:"status"`
	Title       string    `json:"title,omitempty"`
	Type        string    `json:"type,omitempty"`
	Show        string    `json:"show,omitempty"`
	Season      int       `json:"season,omitempty"`
	Episode     int       `json:"episode,omitempty"`
	Error       string    `json:"error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	VideoPath   string    `json:"-"`
	ArchivePath string    `json:"-"`
	AudioPath   string    `json:"-"`
	HasVideo    bool      `json:"has_video"`
	HasArchive  bool      `json:"has_archive"`
	// HasAudio is set when an audio-only job produced a downloadable mp3.
	// AudioOnly echoes the submission flag so the SPA can render the audio
	// player instead of the video player.
	HasAudio  bool `json:"has_audio"`
	AudioOnly bool `json:"audio_only,omitempty"`
	// S3Key is the object key of the uploaded mp4 when S3 upload is enabled
	// (empty otherwise). The download URL is presigned on demand from it.
	S3Key string `json:"-"`
	// AudioS3Key is the object key of the uploaded mp3 for an audio-only job.
	// Kept separate from S3Key so the /video and /audio endpoints never serve
	// each other's artefact.
	AudioS3Key string `json:"-"`
	// DownloadURL is a presigned S3 link populated on the job-detail response
	// when the finished artefact (mp4 or, for audio-only jobs, mp3) lives in
	// object storage; empty when served from disk.
	DownloadURL string `json:"download_url,omitempty"`

	ElapsedMS        int64   `json:"elapsed_ms,omitempty"`
	RemainingMS      int64   `json:"remaining_ms,omitempty"`
	Phase            string  `json:"phase,omitempty"`
	PhaseLabel       string  `json:"phase_label,omitempty"`
	PromptTokens     int64   `json:"prompt_tokens,omitempty"`
	CompletionTokens int64   `json:"completion_tokens,omitempty"`
	TotalTokens      int64   `json:"total_tokens,omitempty"`
	LLMCostUSD       float64 `json:"llm_cost_usd,omitempty"`
	LLMCostKnown     bool    `json:"llm_cost_known,omitempty"`
	// TTSCostUSD and MusicCostUSD are the non-LLM API costs (Azure speech
	// synthesis and Lyria music generation). They are added to LLMCostUSD to
	// form the run's grand total cost shown to the user.
	TTSCostUSD   float64  `json:"tts_cost_usd,omitempty"`
	MusicCostUSD float64  `json:"music_cost_usd,omitempty"`
	Logs         []JobLog `json:"logs,omitempty"`
}

// JobSubmission is the parsed multipart payload the server hands off to
// the job runner. ScriptPath / PriorsZipPath are absolute paths in the
// session OutDir where the HTTP handler saved the uploaded files.
//
// SoftSubs / BurnSubs are forwarded verbatim from the form. The runner
// is responsible for validating that the topic actually permits them
// (series only); the HTTP handler does a coarse pre-check based on the
// raw form values.
//
// Resolution overrides the topic.md `resolution:` field when non-empty
// — empty means "respect the script's declared resolution" so users
// who don't pick from the UI still get the topic-author's intent.
type JobSubmission struct {
	ScriptPath        string
	PriorsZipPath     string
	SoftSubs          bool
	BurnSubs          bool
	Resolution        string
	SubtitleLanguages []string
	// AudioOnly renders an audio-only feed: the runner skips the encoder,
	// the render stages, and all image generation, producing a downloadable
	// mp3 (+ subtitles.vtt sidecar) instead of an mp4.
	AudioOnly bool
	// DiscussionID links a native-client discussion record to this render job.
	// Empty for dashboard uploads and legacy multipart jobs.
	DiscussionID string
}

// JobRegistry persists video-mode jobs and progress logs to SQLite.
type JobRegistry struct {
	db       *gorm.DB
	logLimit int
	mu       sync.Mutex

	// orchs tracks the live orchestrator for each currently-running job so
	// the WebSocket endpoint can inject viewer participation messages into
	// an in-flight discussion. Entries exist only while a job is running
	// (set at run start, cleared on exit); they are never persisted.
	orchMu sync.RWMutex
	orchs  map[string]*contentcreator.Orchestrator
}

type videoJobRecord struct {
	ID               string `gorm:"primaryKey"`
	Status           string
	Title            string
	Type             string
	Show             string
	Season           int
	Episode          int
	Error            string
	VideoPath        string
	ArchivePath      string
	AudioPath        string
	HasVideo         bool
	HasArchive       bool
	HasAudio         bool
	AudioOnly        bool
	S3Key            string
	AudioS3Key       string
	ElapsedMS        int64
	RemainingMS      int64
	Phase            string
	PhaseLabel       string
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	LLMCostUSD       float64
	LLMCostKnown     bool
	TTSCostUSD       float64
	MusicCostUSD     float64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (videoJobRecord) TableName() string { return "video_jobs" }

type videoJobLogRecord struct {
	ID        uint   `gorm:"primaryKey"`
	JobID     string `gorm:"index;not null"`
	Kind      string
	Text      string
	Payload   string
	CreatedAt time.Time
}

func (videoJobLogRecord) TableName() string { return "video_job_logs" }

// NewJobRegistry opens or creates a SQLite-backed registry at dbPath.
func NewJobRegistry(dbPath string) (*JobRegistry, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, err
	}
	gormLogger := logger.New(
		stdlog.New(os.Stdout, "\r\n", stdlog.LstdFlags),
		logger.Config{
			SlowThreshold:             time.Second,
			LogLevel:                  logger.Warn,
			IgnoreRecordNotFoundError: true,
		},
	)
	db, err := gorm.Open(sqlite.Open(sqliteDSN(dbPath)), &gorm.Config{Logger: gormLogger})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.Exec("PRAGMA busy_timeout = 5000").Error; err != nil {
		return nil, err
	}
	if err := db.Exec("PRAGMA journal_mode = WAL").Error; err != nil {
		return nil, err
	}
	r := &JobRegistry{db: db, logLimit: 500, orchs: map[string]*contentcreator.Orchestrator{}}
	if err := r.ensureSchema(); err != nil {
		return nil, err
	}
	if !db.Migrator().HasTable(&videoJobRecord{}) || !db.Migrator().HasTable(&videoJobLogRecord{}) {
		return nil, errors.New("jobs db migration did not create required tables")
	}
	return r, nil
}

func sqliteDSN(dbPath string) string {
	sep := "?"
	if strings.Contains(dbPath, "?") {
		sep = "&"
	}
	return dbPath + sep + "_busy_timeout=5000"
}

func (r *JobRegistry) ensureSchema() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.db.AutoMigrate(&videoJobRecord{}, &videoJobLogRecord{})
}

func (r *JobRegistry) retryMissingTable(err error, op func() error) error {
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "no such table") {
		return err
	}
	if migrateErr := r.ensureSchema(); migrateErr != nil {
		return fmt.Errorf("%w; remigrate: %v", err, migrateErr)
	}
	return op()
}

// SetOrch records the live orchestrator for a running job. The video-job
// runner calls this once the orchestrator is built, and pairs it with a
// deferred ClearOrch so the entry never outlives the run.
func (r *JobRegistry) SetOrch(id string, orch *contentcreator.Orchestrator) {
	r.orchMu.Lock()
	r.orchs[id] = orch
	r.orchMu.Unlock()
}

// Orch returns the live orchestrator for a running job, or nil when the job
// is unknown, finished, or never started one.
func (r *JobRegistry) Orch(id string) *contentcreator.Orchestrator {
	r.orchMu.RLock()
	defer r.orchMu.RUnlock()
	return r.orchs[id]
}

// ClearOrch drops the live-orchestrator entry for a job once its run exits.
func (r *JobRegistry) ClearOrch(id string) {
	r.orchMu.Lock()
	delete(r.orchs, id)
	r.orchMu.Unlock()
}

// Add inserts a fresh pending job. Caller picks the id.
func (r *JobRegistry) Add(id string) *Job {
	now := time.Now()
	rec := videoJobRecord{
		ID:        id,
		Status:    string(JobPending),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := r.db.Create(&rec).Error; err != nil {
		_ = r.retryMissingTable(err, func() error {
			return r.db.Create(&rec).Error
		})
	}
	j := jobFromRecord(rec)
	return &j
}

// Get returns a snapshot of the named job, or nil when unknown.
func (r *JobRegistry) Get(id string) *Job {
	var rec videoJobRecord
	query := func() error {
		return r.db.First(&rec, "id = ?", id).Error
	}
	if err := query(); err != nil {
		err = r.retryMissingTable(err, query)
		if err != nil {
			return nil
		}
	}
	j := jobFromRecord(rec)
	j.Logs = r.logs(id)
	return &j
}

// Update applies fn to a snapshot and writes it back. No-op when the id is
// unknown.
func (r *JobRegistry) Update(id string, fn func(j *Job)) {
	var rec videoJobRecord
	query := func() error {
		return r.db.First(&rec, "id = ?", id).Error
	}
	if err := query(); err != nil {
		err = r.retryMissingTable(err, query)
		if err != nil {
			return
		}
	}
	if rec.ID == "" {
		return
	}
	j := jobFromRecord(rec)
	fn(&j)
	j.UpdatedAt = time.Now()
	rec = recordFromJob(j)
	if err := r.db.Save(&rec).Error; err != nil {
		_ = r.retryMissingTable(err, func() error {
			return r.db.Save(&rec).Error
		})
	}
}

// AppendLog persists one user-visible progress line for a job.
func (r *JobRegistry) AppendLog(jobID, kind, text string, payload any) {
	if text == "" {
		return
	}
	payloadJSON := ""
	if payload != nil {
		if b, err := json.Marshal(payload); err == nil {
			payloadJSON = string(b)
		}
	}
	rec := videoJobLogRecord{
		JobID:   jobID,
		Kind:    kind,
		Text:    text,
		Payload: payloadJSON,
	}
	if err := r.db.Create(&rec).Error; err != nil {
		_ = r.retryMissingTable(err, func() error {
			return r.db.Create(&rec).Error
		})
	}
}

// List returns a stable-order snapshot of every known job (newest first by
// CreatedAt). Useful for an admin/debug endpoint; the frontend only reads its
// own job by id today.
func (r *JobRegistry) List() []Job {
	var recs []videoJobRecord
	query := func() error {
		return r.db.Order("created_at desc").Find(&recs).Error
	}
	if err := query(); err != nil {
		_ = r.retryMissingTable(err, query)
	}
	out := make([]Job, 0, len(recs))
	for _, rec := range recs {
		out = append(out, jobFromRecord(rec))
	}
	return out
}

func (r *JobRegistry) logs(jobID string) []JobLog {
	var recs []videoJobLogRecord
	query := func() error {
		return r.db.
			Where("job_id = ?", jobID).
			Order("id desc").
			Limit(r.logLimit).
			Find(&recs).Error
	}
	if err := query(); err != nil {
		_ = r.retryMissingTable(err, query)
	}
	out := make([]JobLog, len(recs))
	for i := range recs {
		rec := recs[len(recs)-1-i]
		out[i] = JobLog{
			TS:   rec.CreatedAt.UnixMilli(),
			Kind: rec.Kind,
			Text: rec.Text,
		}
	}
	return out
}

func jobFromRecord(rec videoJobRecord) Job {
	return Job{
		ID:               rec.ID,
		Status:           JobStatus(rec.Status),
		Title:            rec.Title,
		Type:             rec.Type,
		Show:             rec.Show,
		Season:           rec.Season,
		Episode:          rec.Episode,
		Error:            rec.Error,
		CreatedAt:        rec.CreatedAt,
		UpdatedAt:        rec.UpdatedAt,
		VideoPath:        rec.VideoPath,
		ArchivePath:      rec.ArchivePath,
		AudioPath:        rec.AudioPath,
		HasVideo:         rec.HasVideo,
		HasArchive:       rec.HasArchive,
		HasAudio:         rec.HasAudio,
		AudioOnly:        rec.AudioOnly,
		S3Key:            rec.S3Key,
		AudioS3Key:       rec.AudioS3Key,
		ElapsedMS:        rec.ElapsedMS,
		RemainingMS:      rec.RemainingMS,
		Phase:            rec.Phase,
		PhaseLabel:       rec.PhaseLabel,
		PromptTokens:     rec.PromptTokens,
		CompletionTokens: rec.CompletionTokens,
		TotalTokens:      rec.TotalTokens,
		LLMCostUSD:       rec.LLMCostUSD,
		LLMCostKnown:     rec.LLMCostKnown,
		TTSCostUSD:       rec.TTSCostUSD,
		MusicCostUSD:     rec.MusicCostUSD,
	}
}

func recordFromJob(j Job) videoJobRecord {
	return videoJobRecord{
		ID:               j.ID,
		Status:           string(j.Status),
		Title:            j.Title,
		Type:             j.Type,
		Show:             j.Show,
		Season:           j.Season,
		Episode:          j.Episode,
		Error:            j.Error,
		VideoPath:        j.VideoPath,
		ArchivePath:      j.ArchivePath,
		AudioPath:        j.AudioPath,
		HasVideo:         j.HasVideo,
		HasArchive:       j.HasArchive,
		HasAudio:         j.HasAudio,
		AudioOnly:        j.AudioOnly,
		S3Key:            j.S3Key,
		AudioS3Key:       j.AudioS3Key,
		ElapsedMS:        j.ElapsedMS,
		RemainingMS:      j.RemainingMS,
		Phase:            j.Phase,
		PhaseLabel:       j.PhaseLabel,
		PromptTokens:     j.PromptTokens,
		CompletionTokens: j.CompletionTokens,
		TotalTokens:      j.TotalTokens,
		LLMCostUSD:       j.LLMCostUSD,
		LLMCostKnown:     j.LLMCostKnown,
		TTSCostUSD:       j.TTSCostUSD,
		MusicCostUSD:     j.MusicCostUSD,
		CreatedAt:        j.CreatedAt,
		UpdatedAt:        j.UpdatedAt,
	}
}
