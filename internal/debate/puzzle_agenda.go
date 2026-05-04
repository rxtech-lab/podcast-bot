package debate

import (
	"context"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
)

// PuzzlePlanner drives a 海龜湯 / situation-puzzle round. The flow is:
//
//  1. Surface (PhaseOpening): the puzzle host presents the soup-surface
//     situation and invites players to ask questions.
//  2. Q&A loop (PhaseFreeSpeech): players ask yes/no questions in round-robin
//     order; the puzzle host answers each one. Every few rounds the planner
//     probes audience viewers for an interjection (mirrors the debate-side
//     askAnyViewer behaviour). Live audience messages are surfaced as the
//     host paraphrasing then answering.
//  3. Reveal (PhaseVerdict): when time is short OR /end has been pushed OR a
//     player's proposed solution has been judged correct, the host reveals
//     the full truth.
//  4. Conclusion (PhaseEnded): each player gives a short reaction, the host
//     signs off, and the planner returns ok=false.
//
// Phase constants are reused (rather than introducing new ones) so the rest
// of the system — video stage, web UI, transcript persistence — needs no
// changes for the new format.
type PuzzlePlanner struct {
	topic      *config.DebateTopic
	tracker    *Tracker
	registry   *agent.Registry
	queue      *userQueue
	transcript *Transcript

	state puzzleState
	turnN int
}

type puzzleState struct {
	phase agent.Phase

	surfaceSent     bool
	playerRR        int  // round-robin cursor across Players
	qaCount         int  // number of completed player→host Q&A exchanges
	awaitingAnswer  bool // a player has just asked; the next turn must be the host's answer
	lastQuestion    string
	lastWasProposal bool // last directive was propose-solution rather than ask-question

	endRequested  bool
	revealEmitted bool

	// Conclusion round: each player speaks once, then the host signs off.
	conclIdx     int
	signoffSent  bool
}

// askViewerEvery sets how many Q&A rounds elapse between viewer probes during
// the puzzle's free-question phase. Mirrors the debate planner's "every 3rd
// slot" probe cadence but is independently tunable.
const askViewerEvery = 3

// NewPuzzlePlanner constructs the puzzle-format planner.
func NewPuzzlePlanner(topic *config.DebateTopic, tracker *Tracker, reg *agent.Registry, q *userQueue, tr *Transcript) *PuzzlePlanner {
	return &PuzzlePlanner{
		topic:      topic,
		tracker:    tracker,
		registry:   reg,
		queue:      q,
		transcript: tr,
		state:      puzzleState{phase: agent.PhaseOpening},
	}
}

// Next produces the next Turn or returns false to end the round.
func (p *PuzzlePlanner) Next(ctx context.Context) (*Turn, bool) {
	if ctx.Err() != nil {
		return nil, false
	}

	queued, end := p.queue.drain()
	if end {
		p.state.endRequested = true
	}

	// Audience interjection: paraphrase + answer in two consecutive turns.
	// The puzzle host's "address-user" directive nudges them to phrase the
	// audience input as a yes/no question and answer it inline.
	if len(queued) > 0 && !p.state.endRequested {
		parts := make([]string, len(queued))
		for i, m := range queued {
			if m.Username != "" {
				parts[i] = m.Username + ": " + m.Text
			} else {
				parts[i] = m.Text
			}
		}
		text := strings.Join(parts, " | ")
		// Treat audience input like a player question: host answers it
		// directly. No separate paraphrase turn — keeps the stream tight.
		return p.makeTurn(p.registry.PuzzleHost, "address-user:"+text, p.budgetSeconds(20)), true
	}

	switch p.state.phase {
	case agent.PhaseSetup, agent.PhaseOpening:
		return p.planSurface()
	case agent.PhaseFreeSpeech:
		return p.planQA(ctx)
	case agent.PhaseVerdict:
		return p.planReveal()
	case agent.PhaseEnded:
		return p.planConclusion()
	}
	return nil, false
}

// planSurface emits the host's opening surface presentation.
func (p *PuzzlePlanner) planSurface() (*Turn, bool) {
	if !p.state.surfaceSent {
		p.state.surfaceSent = true
		return p.makeTurn(p.registry.PuzzleHost, "surface", p.budgetSeconds(45)), true
	}
	p.state.phase = agent.PhaseFreeSpeech
	return p.planQA(context.Background())
}

// planQA alternates between player questions and host answers. When a player's
// utterance looks like a full-solution proposal, the host evaluates it instead.
// Solution heuristic stays simple — operators can also force the reveal with
// /end from the chat input.
func (p *PuzzlePlanner) planQA(ctx context.Context) (*Turn, bool) {
	// Reveal trigger: time low, end requested, or a proposal was judged correct
	// (we track this implicitly: no automatic correctness flag yet, so today
	// the human operator types /end after a satisfying proposal).
	if p.state.endRequested || p.tracker.Remaining() < 90*time.Second {
		p.state.phase = agent.PhaseVerdict
		return p.planReveal()
	}

	// Host answers the player's most recent question.
	if p.state.awaitingAnswer {
		p.state.awaitingAnswer = false
		directive := "answer:" + p.state.lastQuestion
		if p.state.lastWasProposal {
			directive = "evaluate-solution:" + p.state.lastQuestion
		}
		p.state.lastQuestion = ""
		p.state.lastWasProposal = false
		p.state.qaCount++
		return p.makeTurn(p.registry.PuzzleHost, directive, p.budgetSeconds(15)), true
	}

	// Periodic audience probe — gives a viewer a chance to inject a steering
	// question before the next player speaks. Failure to find a willing
	// viewer is silent; we just fall through to the next player.
	if p.state.qaCount > 0 && p.state.qaCount%askViewerEvery == 0 && len(p.registry.Viewers) > 0 {
		if v, q := p.askAnyViewer(ctx); v != nil {
			p.state.lastQuestion = q
			p.state.awaitingAnswer = true
			return p.makeTurn(v, "ask:"+q, p.budgetSeconds(20)), true
		}
	}

	// Pick the next player round-robin.
	if len(p.registry.Players) == 0 {
		// Degenerate: no players configured. Fall through to reveal.
		p.state.phase = agent.PhaseVerdict
		return p.planReveal()
	}
	pl := p.registry.Players[p.state.playerRR%len(p.registry.Players)]
	p.state.playerRR++

	// Heuristic: every (askViewerEvery * 2) rounds, ask a player to attempt a
	// full solution rather than another yes/no question. This keeps the round
	// from running forever when nobody volunteers a guess. The host's
	// evaluate-solution response will generally not give the truth away.
	directive := "ask-question"
	if p.state.qaCount > 0 && p.state.qaCount%(askViewerEvery*2) == 0 {
		directive = "propose-solution"
		p.state.lastWasProposal = true
	}
	// Stash the directive marker for the host's follow-up. The actual question
	// text is captured from the produced transcript via Recent in the host's
	// prompt; we just need to know which evaluator branch to take.
	p.state.lastQuestion = directive
	p.state.awaitingAnswer = true
	return p.makeTurn(pl, directive, p.segmentSeconds()), true
}

// planReveal emits one host turn that reveals the truth, then advances to the
// conclusion phase.
func (p *PuzzlePlanner) planReveal() (*Turn, bool) {
	if !p.state.revealEmitted {
		p.state.revealEmitted = true
		p.state.awaitingAnswer = false
		return p.makeTurn(p.registry.PuzzleHost, "reveal", p.budgetSeconds(60)), true
	}
	p.state.phase = agent.PhaseEnded
	return p.planConclusion()
}

// planConclusion gives each player one closing line, then a host sign-off.
func (p *PuzzlePlanner) planConclusion() (*Turn, bool) {
	if p.state.conclIdx < len(p.registry.Players) {
		pl := p.registry.Players[p.state.conclIdx]
		p.state.conclIdx++
		return p.makeTurn(pl, "conclusion", p.budgetSeconds(15)), true
	}
	if !p.state.signoffSent {
		p.state.signoffSent = true
		return p.makeTurn(p.registry.PuzzleHost, "conclusion", p.budgetSeconds(15)), true
	}
	return nil, false
}

// askAnyViewer mirrors DebatePlanner.askAnyViewer — concurrent probe of every
// viewer agent for a willingness to interject a question. Reused logic kept
// here to avoid adding a public accessor on DebatePlanner just for sharing.
func (p *PuzzlePlanner) askAnyViewer(ctx context.Context) (agent.Agent, string) {
	probeCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	type result struct {
		v *agent.Viewer
		q string
	}
	ch := make(chan result, len(p.registry.Viewers))
	for _, vag := range p.registry.Viewers {
		v, ok := vag.(*agent.Viewer)
		if !ok {
			continue
		}
		go func(v *agent.Viewer) {
			var recent []agent.TranscriptLine
			if p.transcript != nil {
				recent = p.transcript.RecentN(20)
			}
			d, err := v.WantsToAsk(probeCtx, recent)
			if err == nil && d.Ask && strings.TrimSpace(d.Question) != "" {
				ch <- result{v: v, q: d.Question}
			} else {
				ch <- result{}
			}
		}(v)
	}
	for range p.registry.Viewers {
		select {
		case r := <-ch:
			if r.v != nil {
				return r.v, r.q
			}
		case <-probeCtx.Done():
			return nil, ""
		}
	}
	return nil, ""
}

func (p *PuzzlePlanner) makeTurn(ag agent.Agent, directive string, budget time.Duration) *Turn {
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

func (p *PuzzlePlanner) segmentSeconds() time.Duration {
	if p.topic.SegmentMaxSeconds <= 0 {
		return 60 * time.Second
	}
	return time.Duration(p.topic.SegmentMaxSeconds) * time.Second
}

func (p *PuzzlePlanner) budgetSeconds(n int) time.Duration {
	return time.Duration(n) * time.Second
}
