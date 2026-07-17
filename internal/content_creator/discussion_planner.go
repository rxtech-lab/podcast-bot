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

	// pendingNext is the discussant pre-selected while emitting a host
	// handoff directive (its name is embedded in that directive), so the
	// next discussant turn goes to exactly the person the host named
	// on-air instead of an independent round-robin pick.
	pendingNext agent.Agent
	// pendingAnswerAgent is the discussant named in the host's most recent
	// address-user handoff; the following answer-user turn is routed to it.
	pendingAnswerAgent agent.Agent

	// judgeNotes holds fact-check notes queued by the pipeline (producer
	// goroutine) for the host to relay on-air; Next runs on the planner
	// goroutine, hence the mutex. Capped and latest-wins per speaker so a
	// live show never stockpiles stale fact-checks.
	noteMu     sync.Mutex
	judgeNotes []judgementNote
}

// judgementNote is one queued fact-check the host should relay on-air.
type judgementNote struct {
	speaker string
	comment string
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

// EnqueueJudgementNote queues a fact-check note for the host to relay on-air.
// Called from the pipeline once the judgement flags a discussant turn. Empty
// speaker/comment are dropped; a note for a speaker already queued is replaced
// (latest wins) and the queue is capped at 2 so stale corrections never pile up.
func (p *DiscussionPlanner) EnqueueJudgementNote(speaker, comment string) {
	speaker = strings.TrimSpace(speaker)
	comment = strings.Join(strings.Fields(comment), " ")
	if speaker == "" || comment == "" {
		return
	}
	p.noteMu.Lock()
	defer p.noteMu.Unlock()
	for i := range p.judgeNotes {
		if p.judgeNotes[i].speaker == speaker {
			p.judgeNotes[i].comment = comment
			return
		}
	}
	if len(p.judgeNotes) >= 2 {
		p.judgeNotes = p.judgeNotes[1:]
	}
	p.judgeNotes = append(p.judgeNotes, judgementNote{speaker: speaker, comment: comment})
}

func (p *DiscussionPlanner) dequeueJudgementNote() (judgementNote, bool) {
	p.noteMu.Lock()
	defer p.noteMu.Unlock()
	if len(p.judgeNotes) == 0 {
		return judgementNote{}, false
	}
	n := p.judgeNotes[0]
	p.judgeNotes = p.judgeNotes[1:]
	return n, true
}

func (p *DiscussionPlanner) discussantByName(name string) agent.Agent {
	for _, d := range p.registry.Discussants {
		if d.Name() == name {
			return d
		}
	}
	return nil
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

	// A discussant must answer the question the host just paraphrased. The
	// host's address-user directive already named this discussant on-air, so
	// route the answer to exactly that agent.
	if p.state.pendingAnswerUser {
		text := p.state.pendingAnswerUserText
		p.state.pendingAnswerUser = false
		p.state.pendingAnswerUserText = ""
		ag := p.pendingAnswerAgent
		p.pendingAnswerAgent = nil
		if ag == nil {
			ag = p.commitNextDiscussant()
		}
		if ag != nil {
			for _, q := range queued {
				p.queue.push(q)
			}
			p.state.freeIdx++
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
		// Pick the answerer NOW and embed their name in the host directive,
		// so the person the host hands the floor to is the one who answers.
		// A pending named handoff (from a transition) is reused rather than
		// skipped — the host already said that name on-air.
		ans := p.pendingNext
		p.pendingNext = nil
		if ans == nil {
			ans = p.commitNextDiscussant()
		}
		p.pendingAnswerAgent = ans
		directive := "address-user:" + text
		if ans != nil {
			directive += "\n[hand off to: " + ans.Name() + "]"
		}
		return p.makeTurn(p.registry.Host, directive, p.budgetSeconds(20)), true
	}

	switch p.state.phase {
	case agent.PhaseSetup, agent.PhaseOpening:
		return p.planOpening()
	case agent.PhaseFreeSpeech:
		if p.state.endRequested || p.tracker.Remaining() < 90*time.Second {
			p.state.phase = agent.PhaseClosing
			p.pendingNext = nil // a pending handoff is superseded by closing
			directive := "transition:closing-thoughts"
			if len(p.registry.Discussants) > 0 {
				directive += ";first=" + p.registry.Discussants[0].Name()
			}
			return p.makeTurn(p.registry.Host, directive, p.budgetSeconds(15)), true
		}
		// Judgement relay: the host reads the fact-check note on-air and hands
		// the floor back to the flagged discussant to respond. Deliberately
		// below the closing check (a note queued as closing starts is stale
		// and silently dropped) and below the audience-question path above.
		if n, ok := p.dequeueJudgementNote(); ok {
			directive := "judgement-note:" + n.speaker + "|" + n.comment
			// An on-air promise from a prior handoff wins over the flagged
			// speaker — the host already named that person to the audience.
			ans := p.pendingNext
			p.pendingNext = nil
			if ans == nil {
				ans = p.discussantByName(n.speaker)
			}
			if ans != nil {
				p.pendingNext = ans
				directive += "\n[hand off to: " + ans.Name() + "]"
			}
			return p.makeTurn(p.registry.Host, directive, p.budgetSeconds(20)), true
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
		// Opening takes run in roster order, so the first speaker is known
		// now — embed the name so the host's intro hands off to exactly the
		// person who actually speaks next.
		directive := "intro"
		if len(p.registry.Discussants) > 0 {
			directive += ":first=" + p.registry.Discussants[0].Name()
		}
		return p.makeTurn(p.registry.Host, directive, p.budgetSeconds(30)), true
	}
	if p.state.openingIdx >= len(p.registry.Discussants) {
		p.state.phase = agent.PhaseFreeSpeech
		// Pre-select who opens the free round and embed their name in the
		// host's handoff; planFree consumes pendingNext so the named person
		// is guaranteed to speak next.
		directive := "transition:open-discussion"
		if ag := p.commitNextDiscussant(); ag != nil {
			p.pendingNext = ag
			directive += ";call:" + ag.Name()
		}
		return p.makeTurn(p.registry.Host, directive, p.budgetSeconds(15)), true
	}
	ag := p.registry.Discussants[p.state.openingIdx]
	p.state.openingIdx++
	return p.makeTurn(ag, "open", p.segmentSeconds()), true
}

func (p *DiscussionPlanner) planFree(ctx context.Context) (*Turn, bool) {
	// A host handoff named this discussant on-air — honor it before anything
	// else (including viewer probes) so the addressed person actually speaks.
	if p.pendingNext != nil {
		ag := p.pendingNext
		p.pendingNext = nil
		p.state.freeIdx++
		return p.makeTurn(ag, "respond", p.segmentSeconds()), true
	}
	// Every 4th slot, give the audience a chance to interject.
	if p.state.freeIdx > 0 && p.state.freeIdx%4 == 0 && len(p.registry.Viewers) > 0 {
		if v, q := p.askAnyViewer(ctx); v != nil {
			p.state.freeIdx++
			return p.makeTurn(v, "ask:"+q, p.budgetSeconds(25)), true
		}
	}
	ag := p.commitNextDiscussant()
	if ag == nil {
		return p.makeTurn(p.registry.Host, "transition:open-discussion", p.budgetSeconds(10)), true
	}
	p.state.freeIdx++
	return p.makeTurn(ag, "respond", p.segmentSeconds()), true
}

// commitNextDiscussant advances the round-robin cursor across the discussants
// and returns the pick. Like the debate planner it uses a cursor rather than
// least-spoken-time because the planner runs ahead of the producer, so
// tracker.Used isn't updated yet. Callers that emit a discussant speak turn
// bump state.freeIdx themselves (freeIdx counts spoken slots and drives the
// viewer-probe cadence, while rrIdx may advance early for a pre-selected
// handoff).
func (p *DiscussionPlanner) commitNextDiscussant() agent.Agent {
	ds := p.registry.Discussants
	if len(ds) == 0 {
		return nil
	}
	ag := ds[p.state.rrIdx%len(ds)]
	p.state.rrIdx++
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
