package contentcreator

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
)

// DiscussionPlanner drives the panel-discussion format: a moderator (host)
// opens, each discussant gives an opening take from their assigned aspect,
// then the floor opens for a free round-robin where each discussant responds
// to the others. Audience questions and viewer interjections weave in exactly
// like the debate format. When the time budget runs low (or /end arrives) the
// host transitions to brief closing thoughts and signs off.
//
// There is no judge and no sides. The commander (silent visual/music
// director) is NOT scheduled here — it runs independently in the
// DiscussionDirector loop.
type DiscussionPlanner struct {
	topic      *config.DebateTopic
	tracker    *Tracker
	registry   *agent.Registry
	queue      *userQueue
	transcript *Transcript

	state discussionState
	turnN int
}

type discussionState struct {
	phase        agent.Phase
	introSent    bool // host intro emitted
	openingIdx   int  // which discussant gives the next opening take
	freeIdx      int  // free-discussion slot counter (drives viewer probes)
	rrIdx        int  // round-robin cursor across discussants
	closingIdx   int  // which discussant gives the next closing thought
	endRequested bool
	signoffSent  bool

	pendingAnswerUser     bool
	pendingAnswerUserText string
}

// NewDiscussionPlanner constructs the discussion-format planner.
func NewDiscussionPlanner(topic *config.DebateTopic, tracker *Tracker, reg *agent.Registry, q *userQueue, tr *Transcript) *DiscussionPlanner {
	return &DiscussionPlanner{
		topic:      topic,
		tracker:    tracker,
		registry:   reg,
		queue:      q,
		transcript: tr,
		state:      discussionState{phase: agent.PhaseOpening},
	}
}

// Next produces the next Turn or returns false to end the discussion.
func (p *DiscussionPlanner) Next(ctx context.Context) (*Turn, bool) {
	if ctx.Err() != nil {
		return nil, false
	}

	queued, end := p.queue.drain()
	if end {
		p.state.endRequested = true
	}

	// A discussant must answer the question the host just paraphrased.
	if p.state.pendingAnswerUser {
		text := p.state.pendingAnswerUserText
		p.state.pendingAnswerUser = false
		p.state.pendingAnswerUserText = ""
		if ag := p.pickDiscussant(); ag != nil {
			for _, q := range queued {
				p.queue.push(q)
			}
			return p.makeTurn(ag, "answer-user:"+text, p.segmentSeconds()), true
		}
	}

	// Audience questions take priority: host paraphrases, then a discussant
	// answers on the next call.
	if len(queued) > 0 && !p.state.endRequested {
		debounceCtx, cancel := context.WithTimeout(ctx, userDebounceWindow)
		<-debounceCtx.Done()
		cancel()
		if more, e := p.queue.drain(); len(more) > 0 {
			queued = append(queued, more...)
			if e {
				p.state.endRequested = true
			}
		} else if e {
			p.state.endRequested = true
		}
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
		if p.state.endRequested || p.tracker.Remaining() < 90*time.Second {
			p.state.phase = agent.PhaseClosing
			return p.makeTurn(p.registry.Host, "transition:closing-thoughts", p.budgetSeconds(15)), true
		}
		return p.planFree(ctx)
	case agent.PhaseClosing:
		return p.planClosing()
	case agent.PhaseConclusion, agent.PhaseEnded:
		return nil, false
	}
	return nil, false
}

func (p *DiscussionPlanner) planOpening() (*Turn, bool) {
	if !p.state.introSent {
		p.state.introSent = true
		return p.makeTurn(p.registry.Host, "intro", p.budgetSeconds(30)), true
	}
	if p.state.openingIdx >= len(p.registry.Discussants) {
		p.state.phase = agent.PhaseFreeSpeech
		return p.makeTurn(p.registry.Host, "transition:open-discussion", p.budgetSeconds(15)), true
	}
	ag := p.registry.Discussants[p.state.openingIdx]
	p.state.openingIdx++
	return p.makeTurn(ag, "open", p.segmentSeconds()), true
}

func (p *DiscussionPlanner) planFree(ctx context.Context) (*Turn, bool) {
	// Every 4th slot, give the audience a chance to interject.
	if p.state.freeIdx > 0 && p.state.freeIdx%4 == 0 && len(p.registry.Viewers) > 0 {
		if v, q := p.askAnyViewer(ctx); v != nil {
			p.state.freeIdx++
			return p.makeTurn(v, "ask:"+q, p.budgetSeconds(25)), true
		}
	}
	ag := p.pickDiscussant()
	if ag == nil {
		return p.makeTurn(p.registry.Host, "transition:open-discussion", p.budgetSeconds(10)), true
	}
	return p.makeTurn(ag, "respond", p.segmentSeconds()), true
}

// pickDiscussant advances the round-robin cursor across the discussants. Like
// the debate planner it uses a cursor rather than least-spoken-time because
// the planner runs ahead of the producer, so tracker.Used isn't updated yet.
func (p *DiscussionPlanner) pickDiscussant() agent.Agent {
	ds := p.registry.Discussants
	if len(ds) == 0 {
		return nil
	}
	ag := ds[p.state.rrIdx%len(ds)]
	p.state.rrIdx++
	p.state.freeIdx++
	return ag
}

func (p *DiscussionPlanner) planClosing() (*Turn, bool) {
	if p.state.closingIdx < len(p.registry.Discussants) {
		ag := p.registry.Discussants[p.state.closingIdx]
		p.state.closingIdx++
		return p.makeTurn(ag, "closing", p.budgetSeconds(30)), true
	}
	if !p.state.signoffSent {
		p.state.signoffSent = true
		p.state.phase = agent.PhaseEnded
		return p.makeTurn(p.registry.Host, "closing", p.budgetSeconds(20)), true
	}
	return nil, false
}

// askAnyViewer probes every viewer in parallel and returns the first that
// wants to ask. Mirrors the debate planner's probe.
func (p *DiscussionPlanner) askAnyViewer(ctx context.Context) (agent.Agent, string) {
	probeCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	type result struct {
		v *agent.Viewer
		q string
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
				ch <- result{v: v, q: d.Question}
			}
		}(v)
	}
	go func() { wg.Wait(); close(ch) }()
	for r := range ch {
		if r.v != nil {
			return r.v, r.q
		}
	}
	return nil, ""
}

func (p *DiscussionPlanner) makeTurn(ag agent.Agent, directive string, budget time.Duration) *Turn {
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

func (p *DiscussionPlanner) segmentSeconds() time.Duration {
	if p.topic.SegmentMaxSeconds <= 0 {
		return 60 * time.Second
	}
	return time.Duration(p.topic.SegmentMaxSeconds) * time.Second
}

func (p *DiscussionPlanner) budgetSeconds(n int) time.Duration {
	return time.Duration(n) * time.Second
}
