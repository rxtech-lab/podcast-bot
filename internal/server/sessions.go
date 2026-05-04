package server

import (
	"sort"
	"sync"

	"github.com/sirily11/debate-bot/internal/audio"
	"github.com/sirily11/debate-bot/internal/debate"
)

// SessionStatus enumerates the lifecycle of one debate within its channel's
// queue.
type SessionStatus string

const (
	StatusPending SessionStatus = "pending"
	StatusRunning SessionStatus = "running"
	StatusDone    SessionStatus = "done"
	StatusError   SessionStatus = "error"
)

// Session is one debate's metadata view. Sessions are grouped under channels
// in the registry.
type Session struct {
	ID             string        `json:"id"`
	Title          string        `json:"title"`
	Status         SessionStatus `json:"status"`
	TranscriptPath string        `json:"transcript_path,omitempty"`
	AudioPath      string        `json:"audio_path,omitempty"`
	// DBPath points at the per-debate sqlite file. The server uses it to
	// serve /api/transcript snapshots after a debate ends (when there's no
	// longer a live orchestrator holding the in-memory transcript).
	// Not exposed in JSON — clients shouldn't see filesystem paths.
	DBPath string `json:"-"`
}

// ChannelInfo is the JSON-facing description of a channel surfaced via
// /api/topics. Off-air channels (no debates assigned) are still listed so the
// frontend can render an "off air" placeholder for them.
type ChannelInfo struct {
	ID              string    `json:"id"`
	Number          int       `json:"number"`
	Title           string    `json:"title"`
	OffAir          bool      `json:"off_air"`
	Debates         []Session `json:"debates"`
	CurrentDebateID string    `json:"current_debate_id,omitempty"`
}

// channelState is the runtime + metadata SessionRegistry tracks per channel.
// Only the metadata fields are exported via ChannelInfo; the live resources
// (orch / hlsDir / livestream) are reached through ChannelResources.
type channelState struct {
	id     string
	number int
	title  string
	hlsDir string
	live   *audio.LiveStream

	mu       sync.RWMutex
	debates  []Session
	currentI int                  // -1 when no debate is airing
	orch     *debate.Orchestrator // current orch; nil between debates
	// dbPath stays set after orch is cleared so /api/transcript can keep
	// serving the most-recently-aired debate's history from disk.
	dbPath string
}

// ChannelResources bundles the per-channel runtime state HTTP handlers need
// when serving /api/video/<id>, /api/audio/<id>, /api/transcript?channel=<id>.
//
// CurrentDBPath is the sqlite file for the channel's currently airing or
// most-recently-aired debate. It outlives Orch — when a debate ends and Orch
// becomes nil, the path stays so the server can keep serving the transcript
// from disk.
type ChannelResources struct {
	Orch          *debate.Orchestrator
	HLSDir        string
	LiveStream    *audio.LiveStream
	CurrentDBPath string
}

// SessionRegistry tracks every channel and its queue of debates. Channels are
// declared up front (from channels.json + the channel ids found on each
// debate.md); the live orchestrator within a channel rotates as that
// channel's queue plays out.
type SessionRegistry struct {
	mu       sync.RWMutex
	order    []string                 // channel ids in display order (by number)
	channels map[string]*channelState // id → state
}

// NewSessionRegistry builds an empty registry. Channels are added with
// RegisterChannel; debates are seeded with SeedChannelDebates.
func NewSessionRegistry() *SessionRegistry {
	return &SessionRegistry{channels: map[string]*channelState{}}
}

// RegisterChannel declares a channel up front with its display metadata and
// streaming resources. live/hlsDir may be empty when the channel is off-air
// (no debates assigned). Calling RegisterChannel a second time for the same
// id replaces the metadata + resources but keeps the existing debate queue.
func (r *SessionRegistry) RegisterChannel(id string, number int, title, hlsDir string, live *audio.LiveStream) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cur, ok := r.channels[id]; ok {
		cur.number = number
		cur.title = title
		cur.hlsDir = hlsDir
		cur.live = live
		return
	}
	r.channels[id] = &channelState{
		id:       id,
		number:   number,
		title:    title,
		hlsDir:   hlsDir,
		live:     live,
		currentI: -1,
	}
	r.order = append(r.order, id)
	// Keep display order stable by channel number.
	sort.SliceStable(r.order, func(i, j int) bool {
		return r.channels[r.order[i]].number < r.channels[r.order[j]].number
	})
}

// SeedChannelDebates installs the queue of debates for a channel (in play
// order). All entries start as pending.
func (r *SessionRegistry) SeedChannelDebates(channelID string, debates []Session) {
	r.mu.RLock()
	st := r.channels[channelID]
	r.mu.RUnlock()
	if st == nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.debates = append(st.debates[:0], debates...)
}

// AppendChannelDebate adds a single debate to the end of a channel's queue.
// Used by the folder watcher when a new debate.md is dropped into the watched
// directory at runtime. Returns false if the channel is unknown or a debate
// with the same id already exists in this channel's queue (callers should
// generate a unique id before calling).
func (r *SessionRegistry) AppendChannelDebate(channelID string, sess Session) bool {
	r.mu.RLock()
	st := r.channels[channelID]
	r.mu.RUnlock()
	if st == nil {
		return false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	for i := range st.debates {
		if st.debates[i].ID == sess.ID {
			return false
		}
	}
	st.debates = append(st.debates, sess)
	return true
}

// RemoveChannelDebate drops a debate from the channel's queue. Only Pending
// entries are removable — a Running debate is mid-flight (killing its
// metadata while audio/video keep streaming would leave the UI inconsistent),
// and Done/Error entries are kept as history. Returns the removed debate's
// status and ok=true on success; ok=false when the debate isn't found or
// isn't pending. The returned status lets callers log *why* a removal was
// skipped (running vs unknown).
func (r *SessionRegistry) RemoveChannelDebate(channelID, debateID string) (SessionStatus, bool) {
	r.mu.RLock()
	st := r.channels[channelID]
	r.mu.RUnlock()
	if st == nil {
		return "", false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	for i := range st.debates {
		if st.debates[i].ID != debateID {
			continue
		}
		status := st.debates[i].Status
		if status != StatusPending {
			return status, false
		}
		st.debates = append(st.debates[:i], st.debates[i+1:]...)
		// currentI tracks the airing slot's index. Removing a slot before
		// it shifts every later slot down by one — adjust so the live
		// pointer keeps pointing at the same debate.
		if st.currentI > i {
			st.currentI--
		}
		return StatusPending, true
	}
	return "", false
}

// HasDebate reports whether the named debate already exists on this channel.
// Lets the watcher dedupe before generating ids / loading topic files twice.
func (r *SessionRegistry) HasDebate(channelID, debateID string) bool {
	r.mu.RLock()
	st := r.channels[channelID]
	r.mu.RUnlock()
	if st == nil {
		return false
	}
	st.mu.RLock()
	defer st.mu.RUnlock()
	for i := range st.debates {
		if st.debates[i].ID == debateID {
			return true
		}
	}
	return false
}

// SetDebateStatus updates a single debate's lifecycle status within its
// channel.
func (r *SessionRegistry) SetDebateStatus(channelID, debateID string, status SessionStatus) {
	r.mu.RLock()
	st := r.channels[channelID]
	r.mu.RUnlock()
	if st == nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	for i := range st.debates {
		if st.debates[i].ID == debateID {
			st.debates[i].Status = status
			return
		}
	}
}

// SetDebateOutputs records on-disk artefacts produced by a finished debate.
func (r *SessionRegistry) SetDebateOutputs(channelID, debateID, transcriptPath, audioPath string) {
	r.mu.RLock()
	st := r.channels[channelID]
	r.mu.RUnlock()
	if st == nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	for i := range st.debates {
		if st.debates[i].ID == debateID {
			st.debates[i].TranscriptPath = transcriptPath
			st.debates[i].AudioPath = audioPath
			return
		}
	}
}

// SetCurrentOrch installs (or clears) the live orchestrator for a channel as
// its queue advances. debateID identifies which debate just became live; pass
// "" + nil to clear between debates. When orch is non-nil we also latch its
// per-debate sqlite path so the server can keep serving /api/transcript from
// disk after the orchestrator exits.
func (r *SessionRegistry) SetCurrentOrch(channelID, debateID string, orch *debate.Orchestrator) {
	r.mu.RLock()
	st := r.channels[channelID]
	r.mu.RUnlock()
	if st == nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.orch = orch
	st.currentI = -1
	for i := range st.debates {
		if st.debates[i].ID == debateID {
			st.currentI = i
			// Latch the new debate's DB path; a nil orch (cleared between
			// debates) leaves dbPath alone so the just-finished debate's
			// transcript stays readable until the next one starts.
			if orch != nil && st.debates[i].DBPath != "" {
				st.dbPath = st.debates[i].DBPath
			}
			return
		}
	}
}

// ChannelResources returns the HTTP-facing resources for a channel id.
// Returns nil when the channel is unknown.
func (r *SessionRegistry) ChannelResources(id string) *ChannelResources {
	r.mu.RLock()
	st := r.channels[id]
	r.mu.RUnlock()
	if st == nil {
		return nil
	}
	st.mu.RLock()
	defer st.mu.RUnlock()
	return &ChannelResources{
		Orch:          st.orch,
		HLSDir:        st.hlsDir,
		LiveStream:    st.live,
		CurrentDBPath: st.dbPath,
	}
}

// List returns the full channel list (with debate queues) for /api/topics.
func (r *SessionRegistry) List() []ChannelInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ChannelInfo, 0, len(r.order))
	for _, id := range r.order {
		st := r.channels[id]
		st.mu.RLock()
		debates := make([]Session, len(st.debates))
		copy(debates, st.debates)
		var currentID string
		if st.currentI >= 0 && st.currentI < len(st.debates) {
			currentID = st.debates[st.currentI].ID
		}
		offAir := st.hlsDir == "" || len(st.debates) == 0
		st.mu.RUnlock()
		out = append(out, ChannelInfo{
			ID:              id,
			Number:          st.number,
			Title:           st.title,
			OffAir:          offAir,
			Debates:         debates,
			CurrentDebateID: currentID,
		})
	}
	return out
}
