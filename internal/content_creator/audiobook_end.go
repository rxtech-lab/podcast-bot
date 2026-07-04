package contentcreator

import (
	"context"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/tools"
)

const endAudioBookToolName = "end_audio_book"

type audioBookEndState struct {
	requested atomic.Bool
	done      atomic.Bool
}

func (s *audioBookEndState) RequestDone() {
	if s != nil {
		s.requested.Store(true)
	}
}

func (s *audioBookEndState) EndRequested() bool {
	return s != nil && s.requested.Load()
}

func (s *audioBookEndState) ClearRequest() {
	if s != nil {
		s.requested.Store(false)
	}
}

func (s *audioBookEndState) MarkDone() {
	if s != nil {
		s.requested.Store(false)
		s.done.Store(true)
	}
}

func (s *audioBookEndState) Done() bool {
	return s != nil && s.done.Load()
}

type endAudioBookTool struct {
	state *audioBookEndState
}

func (t endAudioBookTool) Name() string { return endAudioBookToolName }

func (t endAudioBookTool) Description() string {
	return "Call exactly once, only after the final planned audiobook chapter has been fully narrated. The backend will not mark audiobook narration complete until this tool is called."
}

func (t endAudioBookTool) Schema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

func (t endAudioBookTool) Call(_ context.Context, _ map[string]any, _ tools.AgentContext) (string, error) {
	t.state.RequestDone()
	return "audiobook completion requested; backend will verify the final planned beat before stopping", nil
}

// AudioBookPlanner keeps asking the narrator to continue until the narrator
// explicitly calls end_audio_book. A clean LLM stream close is not enough to
// finish the audiobook, because providers can stop mid-story without surfacing
// an error.
type AudioBookPlanner struct {
	topic    *config.DebateTopic
	registry *agent.Registry
	state    *audioBookEndState
	turnN    int
}

func NewAudioBookPlanner(topic *config.DebateTopic, reg *agent.Registry, state *audioBookEndState) *AudioBookPlanner {
	return &AudioBookPlanner{topic: topic, registry: reg, state: state}
}

func (p *AudioBookPlanner) Next(ctx context.Context) (*Turn, bool) {
	if ctx.Err() != nil || p.Done() || p.registry == nil || p.registry.SeriesHost == nil {
		return nil, false
	}
	p.turnN++
	directive := "narrate"
	if p.turnN > 1 {
		directive = "narrate continuation: Continue exactly from where the previous audiobook narration stopped. Do not restart, recap, summarize earlier material, or add filler. Keep following the planned chapter order. If the recent transcript already fully narrated the final planned chapter, call end_audio_book immediately with no spoken text. Otherwise call end_audio_book exactly once as soon as the final planned chapter is fully narrated."
	}
	budget := 30 * time.Minute
	if p.topic != nil && p.topic.TotalMinutes > 0 {
		budget = time.Duration(p.topic.TotalMinutes) * time.Minute
	}
	return &Turn{
		ID:        p.turnN,
		Phase:     agent.PhaseFreeSpeech,
		Speaker:   p.registry.SeriesHost,
		Directive: strings.TrimSpace(directive),
		Budget:    budget,
		TextOut:   make(chan string, 16),
	}, true
}

func (p *AudioBookPlanner) Done() bool {
	return p != nil && p.state.Done()
}

func (p *AudioBookPlanner) ValidateEndAfterTurn(maxSceneIndex, requiredFinalSceneIndex int) (requested, accepted bool) {
	if p == nil || p.state == nil || !p.state.EndRequested() {
		return false, false
	}
	if requiredFinalSceneIndex > 0 && maxSceneIndex < requiredFinalSceneIndex {
		p.state.ClearRequest()
		return true, false
	}
	p.state.MarkDone()
	return true, true
}
