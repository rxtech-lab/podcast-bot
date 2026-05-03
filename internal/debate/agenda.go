package debate

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
)

// userQueue is a thread-safe FIFO of user input strings (and the `/end` sentinel).
type userQueue struct {
	mu  sync.Mutex
	buf []string
	end bool
}

func (q *userQueue) push(s string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if s == "/end" {
		q.end = true
		return
	}
	q.buf = append(q.buf, s)
}
func (q *userQueue) drain() ([]string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.buf
	q.buf = nil
	return out, q.end
}

// Planner decides what turn happens next.
type Planner struct {
	topic      *config.Topic
	tracker    *Tracker
	registry   *agent.Registry
	queue      *userQueue
	transcript *Transcript

	state plannerState
	turnN int
}

type plannerState struct {
	phase           agent.Phase
	openingIdx      int  // 0..(maxSide*2-1) — alternating
	openingSent     bool // whether host intro has been emitted
	freeSpeechIdx   int  // counter for who speaks next in free speech
	closingIdx      int
	conclusionIdx   int
	endRequested    bool
	verdictEmitted  bool
	conclusionDone  bool
}

// NewPlanner constructs the planner; the queue is kept by the orchestrator.
func NewPlanner(topic *config.Topic, tracker *Tracker, reg *agent.Registry, q *userQueue, tr *Transcript) *Planner {
	return &Planner{
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
func (p *Planner) Next(ctx context.Context) (*Turn, bool) {
	if ctx.Err() != nil {
		return nil, false
	}

	queued, end := p.queue.drain()

	// /end sentinel flips us into wrap-up sequence (host -> verdict -> conclusion -> stop).
	if end {
		p.state.endRequested = true
	}

	// User questions take priority; weave in via host.
	if len(queued) > 0 && !p.state.endRequested {
		text := strings.Join(queued, " | ")
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
		return p.planFreeSpeech(ctx)
	case agent.PhaseClosing:
		return p.planClosing()
	case agent.PhaseVerdict:
		if !p.state.verdictEmitted {
			p.state.verdictEmitted = true
			return p.makeTurn(p.registry.Judge, "verdict", p.budgetSeconds(45)), true
		}
		p.state.phase = agent.PhaseConclusion
		return p.makeTurn(p.registry.Host, "conclusion-intro", p.budgetSeconds(10)), true
	case agent.PhaseConclusion:
		return p.planConclusion()
	case agent.PhaseEnded:
		return nil, false
	}
	return nil, false
}

// planOpening alternates affirmative / negative candidates with host transitions.
func (p *Planner) planOpening() (*Turn, bool) {
	if !p.state.openingSent {
		p.state.openingSent = true
		return p.makeTurn(p.registry.Host, "intro", p.budgetSeconds(30)), true
	}
	total := len(p.topic.Affirmative) + len(p.topic.Negative)
	if p.state.openingIdx >= total {
		p.state.phase = agent.PhaseFreeSpeech
		return p.makeTurn(p.registry.Host, "transition:free-speech", p.budgetSeconds(15)), true
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

// planFreeSpeech alternates sides, occasionally letting a viewer interject.
func (p *Planner) planFreeSpeech(ctx context.Context) (*Turn, bool) {
	idx := p.state.freeSpeechIdx
	p.state.freeSpeechIdx++

	// Every 3rd inter-segment slot, probe viewers in parallel.
	if idx > 0 && idx%3 == 0 && len(p.registry.Viewers) > 0 {
		if v, q := p.askAnyViewer(ctx); v != nil {
			directive := "ask:" + q
			return p.makeTurn(v, directive, p.budgetSeconds(25)), true
		}
	}

	var side string
	if idx%2 == 0 {
		side = "affirmative"
	} else {
		side = "negative"
	}

	candidates := p.registry.Affirmatve
	if side == "negative" {
		candidates = p.registry.Negative
	}
	// Find the candidate on this side with the smallest accumulated speaking time.
	var pick agent.Agent
	var min time.Duration = -1
	for _, c := range candidates {
		used := p.tracker.Used(c.Name())
		if min < 0 || used < min {
			min = used
			pick = c
		}
	}
	if pick == nil {
		// Defensive: skip ahead.
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

func (p *Planner) askAnyViewer(ctx context.Context) (agent.Agent, string) {
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

func (p *Planner) planClosing() (*Turn, bool) {
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

func (p *Planner) planConclusion() (*Turn, bool) {
	all := append([]agent.Agent{}, p.registry.Affirmatve...)
	all = append(all, p.registry.Negative...)
	all = append(all, p.registry.Viewers...)
	all = append(all, p.registry.Judge)

	if p.state.conclusionIdx >= len(all) {
		p.state.phase = agent.PhaseEnded
		return p.makeTurn(p.registry.Host, "closing", p.budgetSeconds(20)), true
	}
	idx := p.state.conclusionIdx
	p.state.conclusionIdx++
	return p.makeTurn(all[idx], "conclusion", p.budgetSeconds(20)), true
}

func (p *Planner) makeTurn(ag agent.Agent, directive string, budget time.Duration) *Turn {
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

func (p *Planner) segmentSeconds() time.Duration {
	if p.topic.SegmentMaxSeconds <= 0 {
		return 60 * time.Second
	}
	return time.Duration(p.topic.SegmentMaxSeconds) * time.Second
}

func (p *Planner) budgetSeconds(n int) time.Duration {
	return time.Duration(n) * time.Second
}
