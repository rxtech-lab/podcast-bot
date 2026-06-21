package contentcreator

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
)

// userDebounceWindow is how long the planner waits after seeing a queued user
// message before pulling the rest of the queue. It exists so a user firing off
// several short messages in a row triggers one combined host address-user turn
// (followed by a single rebuttal exchange) rather than a separate host+candidate
// ping-pong per message.
const userDebounceWindow = 1500 * time.Millisecond

// userMessage is one message from one viewer. Username is the viewer-chosen
// (typically random / persisted in localStorage on the frontend) handle; the
// host weaves it into address-user turns so the AI says e.g. "Tom asks..."
type userMessage struct {
	Username string
	Text     string
}

// userQueue is a thread-safe FIFO of viewer messages (plus the `/end`
// sentinel — handled separately to keep that out-of-band).
type userQueue struct {
	mu  sync.Mutex
	buf []userMessage
	end bool
}

func (q *userQueue) push(m userMessage) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if isEndRequest(m.Text) {
		q.end = true
		return
	}
	q.buf = append(q.buf, m)
}

func isEndRequest(text string) bool {
	s := strings.ToLower(strings.TrimSpace(text))
	s = strings.Trim(s, " \t\r\n.!?。！？")
	switch s {
	case "/end", "end", "finish", "stop", "wrap up", "conclude":
		return true
	}
	for _, phrase := range []string{
		"end it fast",
		"end it quickly",
		"end the podcast",
		"finish it fast",
		"finish it quickly",
		"finish the podcast",
		"wrap it up",
		"skip to end",
		"skip to the end",
	} {
		if strings.Contains(s, phrase) {
			return true
		}
	}
	return false
}

func (q *userQueue) drain() ([]userMessage, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.buf
	q.buf = nil
	return out, q.end
}

// Planner is the per-content-type next-turn scheduler. The pipeline only calls
// Next; concrete implementations (DebatePlanner, PuzzlePlanner, ...) own their
// own state machines.
type Planner interface {
	Next(ctx context.Context) (*Turn, bool)
}

// DebatePlanner drives the affirmative-vs-negative debate format.
type DebatePlanner struct {
	topic      *config.DebateTopic
	tracker    *Tracker
	registry   *agent.Registry
	queue      *userQueue
	transcript *Transcript

	state plannerState
	turnN int
}

type plannerState struct {
	phase          agent.Phase
	openingIdx     int  // 0..(maxSide*2-1) — alternating
	openingSent    bool // whether host intro has been emitted
	freeDebateIdx  int  // counter for who speaks next in free debate
	affRRIdx       int  // round-robin cursor inside the affirmative side
	negRRIdx       int  // round-robin cursor inside the negative side
	closingIdx     int
	endRequested   bool
	verdictEmitted bool
	signoffSent    bool

	// pendingAnswerUser flags that the host just took an address-user turn and
	// the very next turn MUST be a candidate answering that question. Without
	// this, a fresh user message arriving during the host's turn would re-trigger
	// another host address-user turn on the next planner call, and the candidate
	// would never get to speak.
	pendingAnswerUser     bool
	pendingAnswerUserText string
}

// NewDebatePlanner constructs the debate-format planner; the queue is kept by
// the orchestrator.
func NewDebatePlanner(topic *config.DebateTopic, tracker *Tracker, reg *agent.Registry, q *userQueue, tr *Transcript) *DebatePlanner {
	return &DebatePlanner{
		topic:      topic,
		tracker:    tracker,
		registry:   reg,
		queue:      q,
		transcript: tr,
		state:      plannerState{phase: agent.PhaseOpening},
	}
}

// Next produces the next Turn, or returns false to end the debate.
// It is single-threaded — the pipeline calls it from one goroutine.
func (p *DebatePlanner) Next(ctx context.Context) (*Turn, bool) {
	if ctx.Err() != nil {
		return nil, false
	}

	queued, end := p.queue.drain()

	// /end sentinel flips us into wrap-up sequence (host -> verdict -> conclusion -> stop).
	if end {
		p.state.endRequested = true
	}

	// If the host just took an address-user turn, the next scheduled turn MUST
	// be a candidate answering that question — even if more user messages have
	// landed in the queue meanwhile. Re-queue any drained-but-deferred messages
	// so they get their own host+candidate cycle on the next call.
	if p.state.pendingAnswerUser {
		text := p.state.pendingAnswerUserText
		p.state.pendingAnswerUser = false
		p.state.pendingAnswerUserText = ""
		if ag := p.pickFreeDebateCandidate(); ag != nil {
			for _, q := range queued {
				p.queue.push(q)
			}
			return p.makeTurn(ag, "answer-user:"+text, p.segmentSeconds()), true
		}
		// Degenerate: no candidates at all. Fall through to phase logic.
	}

	// User questions take priority; weave in via host, then a candidate answers.
	if len(queued) > 0 && !p.state.endRequested {
		// Debounce: wait briefly and drain again so messages typed in rapid
		// succession collapse into a single host address-user turn instead of
		// kicking off a separate host+aff+host+neg ping-pong for each one.
		debounceCtx, cancel := context.WithTimeout(ctx, userDebounceWindow)
		<-debounceCtx.Done()
		cancel()
		more, e := p.queue.drain()
		if len(more) > 0 {
			queued = append(queued, more...)
		}
		if e {
			p.state.endRequested = true
		}
		// Format as "Username: text" so the host AI references each viewer by
		// name. Multiple messages from rapid-typing are joined with " | ";
		// when several viewers chime in at once each gets their own line so
		// the host can name them individually.
		parts := make([]string, len(queued))
		for i, m := range queued {
			if m.Username != "" {
				parts[i] = m.Username + ": " + m.Text
			} else {
				parts[i] = m.Text
			}
		}
		text := strings.Join(parts, " | ")
		p.state.pendingAnswerUser = true
		p.state.pendingAnswerUserText = text
		return p.makeTurn(p.registry.Host, "address-user:"+text, p.budgetSeconds(20)), true
	}

	switch p.state.phase {
	case agent.PhaseSetup, agent.PhaseOpening:
		return p.planOpening()
	case agent.PhaseFreeSpeech:
		if p.state.endRequested || p.tracker.Remaining() < 2*time.Minute {
			p.state.phase = agent.PhaseClosing
			return p.makeTurn(p.registry.Host, "transition:closing-statements", p.budgetSeconds(15)), true
		}
		return p.planFreeDebate(ctx)
	case agent.PhaseClosing:
		return p.planClosing()
	case agent.PhaseVerdict:
		if !p.state.verdictEmitted {
			p.state.verdictEmitted = true
			return p.makeTurn(p.registry.Judge, "verdict", p.budgetSeconds(45)), true
		}
		// Brief host sign-off after the verdict, then the debate ends. The
		// previous design ran a "conclusion" round here in which every
		// candidate, every viewer, and the judge each gave another reflection
		// turn — that confused viewers because it looped back through the
		// roster after the winner had already been declared.
		if !p.state.signoffSent {
			p.state.signoffSent = true
			p.state.phase = agent.PhaseEnded
			return p.makeTurn(p.registry.Host, "closing", p.budgetSeconds(15)), true
		}
		return nil, false
	case agent.PhaseEnded:
		return nil, false
	}
	return nil, false
}

// planOpening alternates affirmative / negative candidates with host transitions.
func (p *DebatePlanner) planOpening() (*Turn, bool) {
	if !p.state.openingSent {
		p.state.openingSent = true
		return p.makeTurn(p.registry.Host, "intro", p.budgetSeconds(30)), true
	}
	total := len(p.topic.Affirmative) + len(p.topic.Negative)
	if p.state.openingIdx >= total {
		p.state.phase = agent.PhaseFreeSpeech
		return p.makeTurn(p.registry.Host, "transition:free-debate", p.budgetSeconds(15)), true
	}
	idx := p.state.openingIdx
	p.state.openingIdx++
	var ag agent.Agent
	if idx%2 == 0 {
		ag = p.registry.Affirmatve[(idx/2)%len(p.registry.Affirmatve)]
	} else {
		ag = p.registry.Negative[(idx/2)%len(p.registry.Negative)]
	}
	return p.makeTurn(ag, "opening", p.segmentSeconds()), true
}

// planFreeDebate alternates sides, occasionally letting a viewer interject.
func (p *DebatePlanner) planFreeDebate(ctx context.Context) (*Turn, bool) {
	// Every 3rd inter-segment slot, probe viewers in parallel.
	if p.state.freeDebateIdx > 0 && p.state.freeDebateIdx%3 == 0 && len(p.registry.Viewers) > 0 {
		if v, q := p.askAnyViewer(ctx); v != nil {
			directive := "ask:" + q
			p.state.freeDebateIdx++
			return p.makeTurn(v, directive, p.budgetSeconds(25)), true
		}
	}

	pick := p.pickFreeDebateCandidate()
	if pick == nil {
		return p.makeTurn(p.registry.Host, "transition:next-side", p.budgetSeconds(10)), true
	}
	// If this candidate is already over their fair share, swap to host warn-time.
	totalCands := len(p.registry.Affirmatve) + len(p.registry.Negative)
	share := (p.tracker.Total() / 2) / time.Duration(max(1, totalCands/2))
	if p.tracker.Used(pick.Name()) > share+30*time.Second {
		return p.makeTurn(p.registry.Host, "warn-time:"+pick.Name(), p.budgetSeconds(10)), true
	}
	// "rebut" (no who) — the opponent's actual last claim is auto-injected
	// into the user prompt by base.runStream so the LLM gets the verbatim text.
	return p.makeTurn(pick, "rebut", p.segmentSeconds()), true
}

// pickFreeDebateCandidate advances the same alternating-side / per-side
// round-robin cursor that planFreeDebate uses, so weaving an audience answer
// in does not desync the rotation. Returns nil when neither side has any
// candidates — caller is expected to handle that degenerate case.
//
// We can't use "smallest accumulated speaking time" here because the planner
// runs ahead of the producer (turn channel is buffered) and tracker.Used only
// updates after a turn finishes playing, so the same speaker would be re-picked
// while their previous turn is still being synthesised — which produced two
// same-side answers in a row.
func (p *DebatePlanner) pickFreeDebateCandidate() agent.Agent {
	idx := p.state.freeDebateIdx
	p.state.freeDebateIdx++
	var candidates []agent.Agent
	var rrIdx *int
	if idx%2 == 0 {
		candidates = p.registry.Affirmatve
		rrIdx = &p.state.affRRIdx
	} else {
		candidates = p.registry.Negative
		rrIdx = &p.state.negRRIdx
	}
	if len(candidates) == 0 {
		// Try the other side before giving up.
		if idx%2 == 0 {
			candidates = p.registry.Negative
			rrIdx = &p.state.negRRIdx
		} else {
			candidates = p.registry.Affirmatve
			rrIdx = &p.state.affRRIdx
		}
		if len(candidates) == 0 {
			return nil
		}
	}
	pick := candidates[*rrIdx%len(candidates)]
	*rrIdx++
	return pick
}

func (p *DebatePlanner) askAnyViewer(ctx context.Context) (agent.Agent, string) {
	probeCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	type result struct {
		v *agent.Viewer
		q string
		t string
	}
	ch := make(chan result, len(p.registry.Viewers))
	var wg sync.WaitGroup
	for _, vag := range p.registry.Viewers {
		v, ok := vag.(*agent.Viewer)
		if !ok {
			continue
		}
		wg.Add(1)
		go func(v *agent.Viewer) {
			defer wg.Done()
			var recent []agent.TranscriptLine
			if p.transcript != nil {
				recent = p.transcript.RecentN(20)
			}
			d, err := v.WantsToAsk(probeCtx, recent)
			if err == nil && d.Ask && strings.TrimSpace(d.Question) != "" {
				ch <- result{v: v, q: d.Question, t: d.Target}
			}
		}(v)
	}
	go func() { wg.Wait(); close(ch) }()
	for r := range ch {
		if r.v != nil {
			return r.v, fmt.Sprintf("%s (target=%s)", r.q, r.t)
		}
	}
	return nil, ""
}

func (p *DebatePlanner) planClosing() (*Turn, bool) {
	total := len(p.topic.Affirmative) + len(p.topic.Negative)
	if p.state.closingIdx >= total {
		p.state.phase = agent.PhaseVerdict
		return p.makeTurn(p.registry.Host, "handoff-judge", p.budgetSeconds(15)), true
	}
	idx := p.state.closingIdx
	p.state.closingIdx++
	var ag agent.Agent
	if idx%2 == 0 {
		ag = p.registry.Affirmatve[(idx/2)%len(p.registry.Affirmatve)]
	} else {
		ag = p.registry.Negative[(idx/2)%len(p.registry.Negative)]
	}
	return p.makeTurn(ag, "closing", p.budgetSeconds(45)), true
}

func (p *DebatePlanner) makeTurn(ag agent.Agent, directive string, budget time.Duration) *Turn {
	p.turnN++
	return &Turn{
		ID:        p.turnN,
		Phase:     p.state.phase,
		Speaker:   ag,
		Directive: directive,
		Budget:    budget,
		TextOut:   make(chan string, 16),
	}
}

func (p *DebatePlanner) segmentSeconds() time.Duration {
	if p.topic.SegmentMaxSeconds <= 0 {
		return 60 * time.Second
	}
	return time.Duration(p.topic.SegmentMaxSeconds) * time.Second
}

func (p *DebatePlanner) budgetSeconds(n int) time.Duration {
	return time.Duration(n) * time.Second
}
