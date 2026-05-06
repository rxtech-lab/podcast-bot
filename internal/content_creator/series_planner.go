package contentcreator

import (
	"context"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
)

// SeriesPlanner emits the ordered turn list for a TV-series episode. It is
// intentionally tiny — series episodes are non-interactive, so the entire
// flow is at most:
//
//  1. PhaseOpening  — directive "previously" (only when previouslyOn is set)
//  2. PhaseFreeSpeech — directive "narrate" (the main episode)
//  3. PhaseEnded     — return false
//
// We reuse the existing Phase enum rather than introducing a series-only one
// (the puzzle planner did the same — see comment on
// situation_puzzle_planner.go:26-29).
type SeriesPlanner struct {
	topic    *config.DebateTopic
	registry *agent.Registry
	tracker  *Tracker

	// hasRecap is set when the orchestrator pre-populated a non-empty
	// "previously on …" recap text. False → skip the recap turn entirely
	// (episode 1, or compression-LLM unavailable).
	hasRecap bool

	emittedRecap bool
	emittedMain  bool
	turnN        int
}

// NewSeriesPlanner constructs the series-format planner. hasRecap controls
// whether the recap turn is emitted; the recap text itself lives on the
// orchestrator and is read from the host agent's prompt.
func NewSeriesPlanner(topic *config.DebateTopic, tracker *Tracker, reg *agent.Registry, hasRecap bool) *SeriesPlanner {
	return &SeriesPlanner{
		topic:    topic,
		registry: reg,
		tracker:  tracker,
		hasRecap: hasRecap,
	}
}

// Next produces the recap turn (if applicable), then the main narration turn.
// Returns (nil, false) once both have been emitted.
func (p *SeriesPlanner) Next(ctx context.Context) (*Turn, bool) {
	if ctx.Err() != nil {
		return nil, false
	}
	host := p.registry.SeriesHost
	if host == nil {
		return nil, false
	}
	if p.hasRecap && !p.emittedRecap {
		p.emittedRecap = true
		return p.makeTurn(host, agent.PhaseOpening, "previously", 60*time.Second), true
	}
	if !p.emittedMain {
		p.emittedMain = true
		// Main narration runs for ~most of the configured episode length;
		// give the LLM a generous budget so it doesn't summarise. The
		// real ceiling is enforced upstream by the renderer (the audio
		// is what plays for as long as it plays — there's no time-up cut
		// for a series episode).
		budget := time.Duration(p.topic.TotalMinutes) * time.Minute
		if budget <= 0 {
			budget = 30 * time.Minute
		}
		return p.makeTurn(host, agent.PhaseFreeSpeech, "narrate", budget), true
	}
	return nil, false
}

func (p *SeriesPlanner) makeTurn(ag agent.Agent, phase agent.Phase, directive string, budget time.Duration) *Turn {
	p.turnN++
	return &Turn{
		ID:        p.turnN,
		Phase:     phase,
		Speaker:   ag,
		Directive: directive,
		Budget:    budget,
		TextOut:   make(chan string, 16),
	}
}
