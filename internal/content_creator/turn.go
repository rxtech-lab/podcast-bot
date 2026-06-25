package contentcreator

import (
	"net/url"
	"regexp"
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

	metaMu           sync.Mutex
	sources          []agent.TranscriptSource
	judgementComment string
}

var toolURLRe = regexp.MustCompile(`https?://[^\s\]\)"'<>]+`)

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

// RecordToolResult captures public source links returned by research/search
// tools used while the speaker composed this turn.
func (t *Turn) RecordToolResult(name, _, result string) {
	if t == nil || !toolLooksLikeResearch(name) {
		return
	}
	for _, raw := range toolURLRe.FindAllString(result, -1) {
		raw = strings.TrimRight(raw, ".,;:)]}")
		u, err := url.Parse(raw)
		if err != nil || u.Scheme == "" || u.Host == "" {
			continue
		}
		src := agent.TranscriptSource{
			Title:   u.Host,
			URL:     u.String(),
			Snippet: truncateSourceSnippet(result, 220),
		}
		t.addSource(src)
	}
}

func toolLooksLikeResearch(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "search") ||
		strings.Contains(n, "firecrawl") ||
		strings.Contains(n, "crawl") ||
		strings.Contains(n, "web") ||
		strings.Contains(n, "url")
}

func truncateSourceSnippet(s string, n int) string {
	flat := strings.Join(strings.Fields(s), " ")
	r := []rune(flat)
	if len(r) <= n {
		return flat
	}
	return string(r[:n]) + "..."
}

func (t *Turn) addSource(src agent.TranscriptSource) {
	t.metaMu.Lock()
	defer t.metaMu.Unlock()
	for _, existing := range t.sources {
		if existing.URL == src.URL {
			return
		}
	}
	t.sources = append(t.sources, src)
	if len(t.sources) > 5 {
		t.sources = t.sources[:5]
	}
}

func (t *Turn) Sources() []agent.TranscriptSource {
	t.metaMu.Lock()
	defer t.metaMu.Unlock()
	out := make([]agent.TranscriptSource, len(t.sources))
	copy(out, t.sources)
	return out
}

func (t *Turn) SetJudgementComment(comment string) {
	t.metaMu.Lock()
	defer t.metaMu.Unlock()
	t.judgementComment = strings.TrimSpace(comment)
}

func (t *Turn) JudgementComment() string {
	t.metaMu.Lock()
	defer t.metaMu.Unlock()
	return t.judgementComment
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
