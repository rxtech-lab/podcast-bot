package contentcreator

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
)

// Transcript is the orchestrator-wide append-only log of completed turns.
// store is optional: when set, every appended line is also persisted to
// sqlite so reloads after the orchestrator exits can recover the full chat
// history.
type Transcript struct {
	mu    sync.RWMutex
	lines []agent.TranscriptLine
	store *Store

	// Streaming-merge state for the currently-open agent line. As one turn
	// streams, consecutive sentences from the same speaker grow a single line
	// in place (openIdx points at it, openRowID is its persisted row). A speaker
	// change, a user message, an image, or CloseTurn closes it so the next
	// sentence starts a fresh line — this is what splits a turn into per-speaker
	// bubbles and keeps user messages interleaved in chronological order.
	openIdx     int // index into lines of the open line, -1 when none
	openTurnID  int
	openSpeaker string
	openRowID   uint
}

// NewTranscript constructs an empty Transcript with no persistence.
func NewTranscript() *Transcript { return &Transcript{openIdx: -1} }

// NewTranscriptWithStore constructs a Transcript backed by the given Store.
// On construction, any lines already in the store are loaded so a reload
// after a server restart (or tuning into a finished debate) preserves the
// chat history. If the load fails the transcript starts empty — the live
// debate is still usable, just without recovered history.
func NewTranscriptWithStore(s *Store) *Transcript {
	t := &Transcript{store: s, openIdx: -1}
	if existing, err := s.Snapshot(); err == nil {
		t.lines = existing
	}
	return t
}

// AppendAgentSegment records one streamed speaker-segment of a turn. When the
// segment continues the currently-open line (same turn + speaker) it grows that
// line and its persisted row in place; otherwise it closes the open line and
// starts a new one. Segments are persisted as they stream, so the sqlite row
// order (and thus a reload) matches the live order and interleaves correctly
// with any user message that arrives mid-turn.
func (t *Transcript) AppendAgentSegment(turnID int, line agent.TranscriptLine) {
	if strings.TrimSpace(line.Text) == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.openIdx >= 0 && t.openIdx == len(t.lines)-1 &&
		t.openTurnID == turnID && t.openSpeaker == line.Speaker {
		if strings.TrimSpace(t.lines[t.openIdx].Text) == "" {
			t.lines[t.openIdx].Text = line.Text
		} else {
			t.lines[t.openIdx].Text += " " + line.Text
		}
		if len(line.Sources) > 0 {
			t.lines[t.openIdx].Sources = line.Sources
		}
		t.store.UpdateText(t.openRowID, t.lines[t.openIdx].Text)
		return
	}
	t.lines = append(t.lines, line)
	t.openIdx = len(t.lines) - 1
	t.openTurnID = turnID
	t.openSpeaker = line.Speaker
	t.openRowID = t.store.Append(line)
}

// CloseTurn finalizes the open line for turnID, attaching the turn-level
// sources / judgement comment (known only once the whole turn is produced) and
// preventing the next turn from merging into it.
func (t *Transcript) CloseTurn(turnID int, sources []agent.TranscriptSource, judgement string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.openIdx >= 0 && t.openIdx == len(t.lines)-1 && t.openTurnID == turnID {
		if len(sources) > 0 {
			t.lines[t.openIdx].Sources = sources
		}
		if strings.TrimSpace(judgement) != "" {
			t.lines[t.openIdx].JudgementComment = strings.TrimSpace(judgement)
		}
		t.store.UpdateMeta(t.openRowID, t.lines[t.openIdx].Sources, t.lines[t.openIdx].JudgementComment)
	}
	t.openIdx = -1
}

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
		Speaker:          tn.Speaker.Name(),
		Role:             tn.Speaker.Role(),
		Side:             tn.Speaker.Side(),
		Text:             b.String(),
		At:               time.Now(),
		Sources:          tn.Sources(),
		JudgementComment: tn.JudgementComment(),
	}
	t.mu.Lock()
	t.lines = append(t.lines, line)
	t.mu.Unlock()
	t.store.Append(line)
	return line
}

// AppendLine inserts a fully-formed line — used by tests and by callers
// that already have an agent.TranscriptLine in hand (e.g. replaying a
// recovered transcript). Production code should prefer AppendFromTurn /
// AppendUser so the same input → output mapping is exercised in tests.
func (t *Transcript) AppendLine(line agent.TranscriptLine) {
	t.mu.Lock()
	t.openIdx = -1
	t.lines = append(t.lines, line)
	t.mu.Unlock()
	t.store.Append(line)
}

// AppendUser inserts a synthetic user line so the user's input shows up
// alongside agent turns in the transcript. Marked Pending until the host
// addresses it on-air, which keeps it out of agent prompts in the meantime
// (so already-buffered candidate turns don't prematurely respond to it).
// speaker carries the viewer's display name (cookie-issued); empty falls
// back to "user" so old clients still render reasonably.
func (t *Transcript) AppendUser(speaker, text string) agent.TranscriptLine {
	if speaker == "" {
		speaker = "user"
	}
	line := agent.TranscriptLine{
		Speaker: speaker,
		Role:    "user",
		Text:    text,
		At:      time.Now(),
		Pending: true,
	}
	t.mu.Lock()
	// Closing the open agent line here is what makes a message sent mid-turn
	// break the agent's bubble and land in chronological order on reload.
	t.openIdx = -1
	t.lines = append(t.lines, line)
	t.mu.Unlock()
	t.store.Append(line)
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

// MarkUserLinesAddressed flips Addressed=true on every non-pending audience
// line. Called after a turn whose directive answered the audience finishes
// (host's address-user, candidate's answer-user, puzzle host's address-user).
// Without this, latestUserLine keeps returning the same audience line on
// every subsequent prompt and the audience-steering block makes each
// player open with "since the audience asked..." for the rest of the round.
func (t *Transcript) MarkUserLinesAddressed() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for i := range t.lines {
		if t.lines[i].Role == "user" && !t.lines[i].Pending && !t.lines[i].Addressed {
			t.lines[i].Addressed = true
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
