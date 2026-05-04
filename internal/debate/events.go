package debate

import (
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
)

// Tea-style messages the orchestrator pushes to the TUI via Send.
// They are defined here (not in tui/) so the orchestrator does not depend on tui.
//
// Every message carries a ChannelID so the bus can fan one shared event stream
// out to per-channel subscribers in parallel mode. Empty ChannelID means
// "broadcast" — sequential mode emits empty IDs, and per-channel filters
// treat empty as a match so today's behavior is preserved.

// TranscriptMsg is one sentence (or fragment) of one turn.
type TranscriptMsg struct {
	ChannelID string
	Speaker   string
	Role      agent.Role
	Side      string
	Text      string
	Done      bool
}

// TickMsg updates the elapsed/remaining clock display.
type TickMsg struct {
	ChannelID string
	Elapsed   time.Duration
	Remaining time.Duration
}

// PhaseMsg announces a phase change.
type PhaseMsg struct {
	ChannelID string
	Phase     agent.Phase
}

// StatusMsg pushes a status-line note (e.g. "MCP server X connected").
type StatusMsg struct {
	ChannelID string
	Text      string
}

// ErrorMsg surfaces a non-fatal error.
type ErrorMsg struct {
	ChannelID string
	Err       error
}

// EndedMsg tells the TUI the orchestrator has finished and it should quit.
type EndedMsg struct {
	ChannelID      string
	TranscriptPath string
	AudioPath      string
}

// TopicMsg announces that the active debate topic has changed (sequential
// multi-topic runs). The Stage uses it to reset the encoder title + side
// panels; the web UI uses it to clear the live transcript and refresh the
// topic list. In parallel mode, ID and ChannelID are equal — each channel
// emits its own TopicMsg at start.
type TopicMsg struct {
	ChannelID string
	ID        string
	Title     string
	Index     int // 0-based position in the queue
	Total     int // total topics in the queue
	AffNames  []string
	NegNames  []string
}

// TopicsChangedMsg signals that the channel/debate list has changed (e.g. a
// new debate.md was discovered by the folder watcher and added to a channel's
// queue). The frontend reacts by re-fetching /api/topics. Broadcast only —
// ChannelID is intentionally empty so every connected SSE client gets it
// regardless of which channel they're tuned to.
type TopicsChangedMsg struct{}

// MsgChannelID extracts the channel id from any debate event message. Returns
// "" for unknown types (which are treated as broadcast by per-channel filters).
func MsgChannelID(v any) string {
	switch m := v.(type) {
	case TranscriptMsg:
		return m.ChannelID
	case TickMsg:
		return m.ChannelID
	case PhaseMsg:
		return m.ChannelID
	case StatusMsg:
		return m.ChannelID
	case ErrorMsg:
		return m.ChannelID
	case EndedMsg:
		return m.ChannelID
	case TopicMsg:
		return m.ChannelID
	case TopicsChangedMsg:
		return ""
	}
	return ""
}

// StampChannelID returns a copy of v with its ChannelID set. Used by the
// per-channel send wrapper so orchestrators don't need to know their own id.
// Unknown types are returned unchanged.
func StampChannelID(v any, id string) any {
	switch m := v.(type) {
	case TranscriptMsg:
		m.ChannelID = id
		return m
	case TickMsg:
		m.ChannelID = id
		return m
	case PhaseMsg:
		m.ChannelID = id
		return m
	case StatusMsg:
		m.ChannelID = id
		return m
	case ErrorMsg:
		m.ChannelID = id
		return m
	case EndedMsg:
		m.ChannelID = id
		return m
	case TopicMsg:
		m.ChannelID = id
		return m
	case TopicsChangedMsg:
		return m
	}
	return v
}
