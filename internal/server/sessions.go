package server

import (
	"sync"
	"sync/atomic"

	"github.com/sirily11/debate-bot/internal/audio"
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

// Mode indicates whether the queue is running sequentially (one shared
// audio/video stream) or in parallel (one per channel). Surfaced via the
// /api/topics response so the frontend can shape its URLs accordingly.
type Mode string

const (
	ModeSequential Mode = "sequential"
	ModeParallel   Mode = "parallel"
)

// Session is the metadata view of one debate queued for play.
// The active debate's live state (transcript, push-user) is reached through
// SessionRegistry.Current (sequential mode) or ChannelOrch (parallel mode).
type Session struct {
	ID             string        `json:"id"`
	Title          string        `json:"title"`
	Status         SessionStatus `json:"status"`
	TranscriptPath string        `json:"transcript_path,omitempty"`
	AudioPath      string        `json:"audio_path,omitempty"`
}

// ChannelResources bundles the per-channel runtime state that the HTTP server
// needs to expose: the live orchestrator (for transcript / user messages), the
// HLS output directory (for /api/video/{id}/...) and the audio livestream (for
// /api/audio/{id}/stream).
type ChannelResources struct {
	Orch       *debate.Orchestrator
	HLSDir     string
	LiveStream *audio.LiveStream
}

// SessionRegistry tracks the queue of debates and which orchestrator is live.
// In sequential mode it tracks a single "current" orchestrator that rotates
// across topics; in parallel mode it tracks one orchestrator per channel id.
type SessionRegistry struct {
	mu       sync.RWMutex
	sessions []Session
	mode     Mode

	current  atomic.Pointer[debate.Orchestrator]
	channels map[string]*ChannelResources
}

// NewSessionRegistry seeds the registry with `pending` entries. mode reports
// whether the runtime is sequential or parallel — the HTTP server includes it
// in /api/topics so the frontend can pick the right URL shape.
func NewSessionRegistry(seed []Session, mode Mode) *SessionRegistry {
	r := &SessionRegistry{
		mode:     mode,
		channels: map[string]*ChannelResources{},
	}
	r.sessions = append(r.sessions, seed...)
	return r
}

// Mode reports the queue execution mode (sequential or parallel).
func (r *SessionRegistry) Mode() Mode { return r.mode }

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

// SetCurrent installs the orchestrator that handles unprefixed /api/transcript
// and /api/messages right now (sequential mode). Pass nil between topics or
// after the queue drains.
func (r *SessionRegistry) SetCurrent(o *debate.Orchestrator) {
	r.current.Store(o)
}

// Current returns the live orchestrator (may be nil between topics).
func (r *SessionRegistry) Current() *debate.Orchestrator {
	return r.current.Load()
}

// RegisterChannel exposes per-channel runtime resources to the HTTP server.
// In parallel mode each debate calls this once at startup with its own
// orchestrator + HLS dir + livestream so /api/{video,audio,transcript,...}
// can route requests to the right channel.
func (r *SessionRegistry) RegisterChannel(id string, res ChannelResources) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.channels[id] = &res
}

// ChannelResources looks up the runtime resources for a channel id. Returns
// nil if the channel is unknown.
func (r *SessionRegistry) ChannelResources(id string) *ChannelResources {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.channels[id]
}
