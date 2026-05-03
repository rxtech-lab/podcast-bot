package debate

import (
	"io"
	"sync"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
)

// Turn is the unit of work that flows through the pipeline. The orchestrator
// fills the streaming fields (TextOut, AudioReader) as it produces audio.
type Turn struct {
	ID        int
	Phase     agent.Phase
	Speaker   agent.Agent
	Directive string
	Budget    time.Duration

	// Filled by the producer. TextOut emits sentence-level text for the TUI.
	TextOut     chan string
	AudioPath   string
	audioReader io.Reader

	// Played sets to true after the player has fully drained it; protected by mu.
	mu     sync.Mutex
	played bool
	err    error
}

// SetErr records a terminal error for this turn.
func (t *Turn) SetErr(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.err = err
}

// Err returns the terminal error if any.
func (t *Turn) Err() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

// MarkPlayed signals the turn is fully done.
func (t *Turn) MarkPlayed() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.played = true
}
