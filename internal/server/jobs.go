package server

import (
	"sync"
	"time"
)

// JobStatus is the lifecycle of a video-mode upload job.
type JobStatus string

const (
	JobPending JobStatus = "pending"
	JobRunning JobStatus = "running"
	JobDone    JobStatus = "done"
	JobError   JobStatus = "error"
)

// Job is one upload-and-render request tracked by the registry.
//
// VideoPath / ArchivePath are absolute on-disk paths populated when the
// job finishes successfully. The HTTP layer serves them via
// /api/jobs/<id>/video and /api/jobs/<id>/archive — clients never see
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

// JobRegistry holds every submitted video-mode job for the lifetime of
// the server process. Jobs aren't persisted across restarts — a restart
// is a clean slate. Callers concurrently submit, poll, and update.
type JobRegistry struct {
	mu   sync.RWMutex
	jobs map[string]*Job
}

// NewJobRegistry returns an empty registry.
func NewJobRegistry() *JobRegistry {
	return &JobRegistry{jobs: map[string]*Job{}}
}

// Add inserts a fresh pending job. Caller picks the id.
func (r *JobRegistry) Add(id string) *Job {
	now := time.Now()
	j := &Job{
		ID:        id,
		Status:    JobPending,
		CreatedAt: now,
		UpdatedAt: now,
	}
	r.mu.Lock()
	r.jobs[id] = j
	r.mu.Unlock()
	return j
}

// Get returns a snapshot of the named job, or nil when unknown.
func (r *JobRegistry) Get(id string) *Job {
	r.mu.RLock()
	j, ok := r.jobs[id]
	r.mu.RUnlock()
	if !ok {
		return nil
	}
	cp := *j
	return &cp
}

// Update applies fn under the registry lock. fn receives the live
// pointer; UpdatedAt is bumped automatically. No-op when the id is
// unknown (the runner can have raced past a removal).
func (r *JobRegistry) Update(id string, fn func(j *Job)) {
	r.mu.Lock()
	j, ok := r.jobs[id]
	if !ok {
		r.mu.Unlock()
		return
	}
	fn(j)
	j.UpdatedAt = time.Now()
	r.mu.Unlock()
}

// List returns a stable-order snapshot of every known job (newest first
// by CreatedAt). Useful for an admin/debug endpoint; the frontend only
// reads its own job by id today.
func (r *JobRegistry) List() []Job {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Job, 0, len(r.jobs))
	for _, j := range r.jobs {
		out = append(out, *j)
	}
	// Newest first.
	for i := 1; i < len(out); i++ {
		for k := i; k > 0 && out[k-1].CreatedAt.Before(out[k].CreatedAt); k-- {
			out[k-1], out[k] = out[k], out[k-1]
		}
	}
	return out
}
