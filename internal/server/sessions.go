package server

import (
	"sync"
	"sync/atomic"

	"github.com/sirily11/debate-bot/internal/debate"
)

// SessionStatus enumerates what a queued debate looks like to the web UI.
type SessionStatus string

const (
	StatusPending SessionStatus = "pending"
	StatusRunning SessionStatus = "running"
	StatusDone    SessionStatus = "done"
	StatusError   SessionStatus = "error"
)

// Session is the metadata view of one topic queued for sequential play.
// The active topic's live state (transcript, push-user) is reached through
// SessionRegistry.Current — only one orchestrator is alive at a time.
type Session struct {
	ID             string        `json:"id"`
	Title          string        `json:"title"`
	Status         SessionStatus `json:"status"`
	TranscriptPath string        `json:"transcript_path,omitempty"`
	AudioPath      string        `json:"audio_path,omitempty"`
}

// SessionRegistry tracks the queue of topics and which orchestrator is live.
type SessionRegistry struct {
	mu       sync.RWMutex
	sessions []Session

	current atomic.Pointer[debate.Orchestrator]
}

// NewSessionRegistry seeds the registry with `pending` entries.
func NewSessionRegistry(seed []Session) *SessionRegistry {
	r := &SessionRegistry{}
	r.sessions = append(r.sessions, seed...)
	return r
}

// List returns a copy of all sessions in queue order.
func (r *SessionRegistry) List() []Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Session, len(r.sessions))
	copy(out, r.sessions)
	return out
}

// SetStatus updates a single session's lifecycle status.
func (r *SessionRegistry) SetStatus(id string, status SessionStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.sessions {
		if r.sessions[i].ID == id {
			r.sessions[i].Status = status
			return
		}
	}
}

// SetOutputs records on-disk artefacts produced by a finished session.
func (r *SessionRegistry) SetOutputs(id, transcriptPath, audioPath string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.sessions {
		if r.sessions[i].ID == id {
			r.sessions[i].TranscriptPath = transcriptPath
			r.sessions[i].AudioPath = audioPath
			return
		}
	}
}

// SetCurrent installs the orchestrator that handles /api/transcript and
// /api/messages right now. Pass nil between topics or after the queue drains.
func (r *SessionRegistry) SetCurrent(o *debate.Orchestrator) {
	r.current.Store(o)
}

// Current returns the live orchestrator (may be nil between topics).
func (r *SessionRegistry) Current() *debate.Orchestrator {
	return r.current.Load()
}
