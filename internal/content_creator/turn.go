package contentcreator

import (
	"strings"
	"sync"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
)

// Turn is the unit of work that flows through the pipeline. The producer
// streams sentences via TextOut and writes audio bytes into the shared
// LiveStream + per-turn file in Deps.
type Turn struct {
	ID        int
	Phase     agent.Phase
	Speaker   agent.Agent
	Directive string
	Budget    time.Duration

	// PrevTurn, when non-nil, points to a previously-emitted turn whose
	// produced text must be inlined into this turn's directive at production
	// time. Used by the situation-puzzle planner: the host's "answer:"
	// directive needs the player's actual rendered question, but the planner
	// runs ahead of the producer (turnCh is buffered) so transcript-based
	// lookup at planner time always misses. Storing a pointer lets produce()
	// resolve the directive once the predecessor's full text is known.
	PrevTurn *Turn

	// Filled by the producer. TextOut emits sentence-level text for the TUI.
	TextOut   chan string
	AudioPath string

	// fullText accumulates every sentence the LLM emits during produce(). A
	// child turn reads it via FullText() to inline the predecessor's text
	// into its own directive. Distinct from TextOut (which is drained by
	// AppendFromTurn into the transcript) so the two consumers don't fight.
	textMu   sync.Mutex
	fullText strings.Builder

	// sceneAdvances counts how many SceneAdvanceMsg events the producer
	// has already emitted for this turn (driven by `<scene/>` markers in
	// the host's surface narration). Mutated only inside the producer
	// goroutine which serializes synthSentence calls per turn, so no
	// mutex is needed. Used by Pipeline.synthSentence to cap excess
	// markers at SurfaceFrames-1 — the visual director generated exactly
	// SurfaceFrames beats and we don't want the rotation to wrap.
	sceneAdvances int

	// Played sets to true after the producer finishes; protected by mu.
	mu     sync.Mutex
	played bool
	err    error
}

// AppendText accumulates one sentence into the turn's full-text buffer.
// Called by the producer for every sentence the LLM emits.
func (t *Turn) AppendText(s string) {
	if s == "" {
		return
	}
	t.textMu.Lock()
	defer t.textMu.Unlock()
	if t.fullText.Len() > 0 {
		t.fullText.WriteByte(' ')
	}
	t.fullText.WriteString(s)
}

// FullText returns everything AppendText has captured so far. Safe to call
// after the producer finishes producing this turn.
func (t *Turn) FullText() string {
	t.textMu.Lock()
	defer t.textMu.Unlock()
	return t.fullText.String()
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
