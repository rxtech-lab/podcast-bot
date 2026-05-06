package server

import (
	"encoding/json"
	"strings"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
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
	HasVideo    bool      `json:"has_video"`
	HasArchive  bool      `json:"has_archive"`

	ElapsedMS   int64    `json:"elapsed_ms,omitempty"`
	RemainingMS int64    `json:"remaining_ms,omitempty"`
	Phase       string   `json:"phase,omitempty"`
	PhaseLabel  string   `json:"phase_label,omitempty"`
	Logs        []JobLog `json:"logs,omitempty"`
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
}

// JobRegistry persists video-mode jobs and progress logs to SQLite.
type JobRegistry struct {
	db       *gorm.DB
	logLimit int
}

type videoJobRecord struct {
	ID          string `gorm:"primaryKey"`
	Status      string
	Title       string
	Type        string
	Show        string
	Season      int
	Episode     int
	Error       string
	VideoPath   string
	ArchivePath string
	HasVideo    bool
	HasArchive  bool
	ElapsedMS   int64
	RemainingMS int64
	Phase       string
	PhaseLabel  string
	CreatedAt   time.Time
	UpdatedAt   time.Time
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
	db, err := gorm.Open(sqlite.Open(sqliteDSN(dbPath)), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(&videoJobRecord{}, &videoJobLogRecord{}); err != nil {
		return nil, err
	}
	return &JobRegistry{db: db, logLimit: 500}, nil
}

func sqliteDSN(dbPath string) string {
	sep := "?"
	if strings.Contains(dbPath, "?") {
		sep = "&"
	}
	return dbPath + sep + "_busy_timeout=5000&_journal_mode=WAL"
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
	_ = r.db.Create(&rec).Error
	j := jobFromRecord(rec)
	return &j
}

// Get returns a snapshot of the named job, or nil when unknown.
func (r *JobRegistry) Get(id string) *Job {
	var rec videoJobRecord
	if err := r.db.First(&rec, "id = ?", id).Error; err != nil {
		return nil
	}
	j := jobFromRecord(rec)
	j.Logs = r.logs(id)
	return &j
}

// Update applies fn to a snapshot and writes it back. No-op when the id is
// unknown.
func (r *JobRegistry) Update(id string, fn func(j *Job)) {
	var rec videoJobRecord
	if err := r.db.First(&rec, "id = ?", id).Error; err != nil {
		return
	}
	j := jobFromRecord(rec)
	fn(&j)
	j.UpdatedAt = time.Now()
	rec = recordFromJob(j)
	_ = r.db.Save(&rec).Error
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
	_ = r.db.Create(&videoJobLogRecord{
		JobID:   jobID,
		Kind:    kind,
		Text:    text,
		Payload: payloadJSON,
	}).Error
}

// List returns a stable-order snapshot of every known job (newest first by
// CreatedAt). Useful for an admin/debug endpoint; the frontend only reads its
// own job by id today.
func (r *JobRegistry) List() []Job {
	var recs []videoJobRecord
	_ = r.db.Order("created_at desc").Find(&recs).Error
	out := make([]Job, 0, len(recs))
	for _, rec := range recs {
		out = append(out, jobFromRecord(rec))
	}
	return out
}

func (r *JobRegistry) logs(jobID string) []JobLog {
	var recs []videoJobLogRecord
	_ = r.db.
		Where("job_id = ?", jobID).
		Order("id desc").
		Limit(r.logLimit).
		Find(&recs).Error
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
		ID:          rec.ID,
		Status:      JobStatus(rec.Status),
		Title:       rec.Title,
		Type:        rec.Type,
		Show:        rec.Show,
		Season:      rec.Season,
		Episode:     rec.Episode,
		Error:       rec.Error,
		CreatedAt:   rec.CreatedAt,
		UpdatedAt:   rec.UpdatedAt,
		VideoPath:   rec.VideoPath,
		ArchivePath: rec.ArchivePath,
		HasVideo:    rec.HasVideo,
		HasArchive:  rec.HasArchive,
		ElapsedMS:   rec.ElapsedMS,
		RemainingMS: rec.RemainingMS,
		Phase:       rec.Phase,
		PhaseLabel:  rec.PhaseLabel,
	}
}

func recordFromJob(j Job) videoJobRecord {
	return videoJobRecord{
		ID:          j.ID,
		Status:      string(j.Status),
		Title:       j.Title,
		Type:        j.Type,
		Show:        j.Show,
		Season:      j.Season,
		Episode:     j.Episode,
		Error:       j.Error,
		VideoPath:   j.VideoPath,
		ArchivePath: j.ArchivePath,
		HasVideo:    j.HasVideo,
		HasArchive:  j.HasArchive,
		ElapsedMS:   j.ElapsedMS,
		RemainingMS: j.RemainingMS,
		Phase:       j.Phase,
		PhaseLabel:  j.PhaseLabel,
		CreatedAt:   j.CreatedAt,
		UpdatedAt:   j.UpdatedAt,
	}
}
