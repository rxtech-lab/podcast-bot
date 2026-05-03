package debate

import (
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
)

// Tea-style messages the orchestrator pushes to the TUI via Send.
// They are defined here (not in tui/) so the orchestrator does not depend on tui.

// TranscriptMsg is one sentence (or fragment) of one turn.
type TranscriptMsg struct {
	Speaker string
	Role    agent.Role
	Side    string
	Text    string
	Done    bool
}

// TickMsg updates the elapsed/remaining clock display.
type TickMsg struct {
	Elapsed   time.Duration
	Remaining time.Duration
}

// PhaseMsg announces a phase change.
type PhaseMsg struct{ Phase agent.Phase }

// StatusMsg pushes a status-line note (e.g. "MCP server X connected").
type StatusMsg struct{ Text string }

// ErrorMsg surfaces a non-fatal error.
type ErrorMsg struct{ Err error }

// EndedMsg tells the TUI the orchestrator has finished and it should quit.
type EndedMsg struct {
	TranscriptPath string
	AudioPath      string
}

// TopicMsg announces that the active debate topic has changed (sequential
// multi-topic runs). The Stage uses it to reset the encoder title + side
// panels; the web UI uses it to clear the live transcript and refresh the
// topic list.
type TopicMsg struct {
	ID          string
	Title       string
	Index       int // 0-based position in the queue
	Total       int // total topics in the queue
	AffNames    []string
	NegNames    []string
}
