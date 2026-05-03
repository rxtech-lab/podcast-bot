package debate

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
)

// Transcript is the orchestrator-wide append-only log of completed turns.
type Transcript struct {
	mu    sync.RWMutex
	lines []agent.TranscriptLine
}

// NewTranscript constructs an empty Transcript.
func NewTranscript() *Transcript { return &Transcript{} }

// Snapshot returns a copy of all current lines.
func (t *Transcript) Snapshot() []agent.TranscriptLine {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]agent.TranscriptLine, len(t.lines))
	copy(out, t.lines)
	return out
}

// RecentN returns the last N lines.
func (t *Transcript) RecentN(n int) []agent.TranscriptLine {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if len(t.lines) <= n {
		out := make([]agent.TranscriptLine, len(t.lines))
		copy(out, t.lines)
		return out
	}
	start := len(t.lines) - n
	out := make([]agent.TranscriptLine, n)
	copy(out, t.lines[start:])
	return out
}

// AppendFromTurn drains all sentences accumulated by a played turn and appends
// one consolidated line to the transcript.
func (t *Transcript) AppendFromTurn(tn *Turn) agent.TranscriptLine {
	var b strings.Builder
	for sent := range tn.TextOut {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(sent)
	}
	line := agent.TranscriptLine{
		Speaker: tn.Speaker.Name(),
		Role:    tn.Speaker.Role(),
		Side:    tn.Speaker.Side(),
		Text:    b.String(),
		At:      time.Now(),
	}
	t.mu.Lock()
	t.lines = append(t.lines, line)
	t.mu.Unlock()
	return line
}

// AppendUser inserts a synthetic user line so the user's input shows up
// alongside agent turns in the transcript. Marked Pending until the host
// addresses it on-air, which keeps it out of agent prompts in the meantime
// (so already-buffered candidate turns don't prematurely respond to it).
func (t *Transcript) AppendUser(text string) agent.TranscriptLine {
	line := agent.TranscriptLine{
		Speaker: "user",
		Role:    "user",
		Text:    text,
		At:      time.Now(),
		Pending: true,
	}
	t.mu.Lock()
	t.lines = append(t.lines, line)
	t.mu.Unlock()
	return line
}

// AcknowledgeUserLines clears the Pending flag on every audience line, making
// them visible to subsequent agent prompts. Called when the host's
// address-user turn begins producing.
func (t *Transcript) AcknowledgeUserLines() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for i := range t.lines {
		if t.lines[i].Role == "user" && t.lines[i].Pending {
			t.lines[i].Pending = false
			n++
		}
	}
	return n
}

// Save writes the transcript to disk in `name: text` format.
func (t *Transcript) Save(path string) error {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var b strings.Builder
	for _, l := range t.lines {
		tag := l.Speaker
		if l.Side != "" {
			tag = l.Side + " - " + l.Speaker
		}
		fmt.Fprintf(&b, "%s: %s\n", tag, l.Text)
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}
