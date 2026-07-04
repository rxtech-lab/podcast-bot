package contentcreator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/tools"
)

type audioBookToolTestAgent struct{}

func (audioBookToolTestAgent) AgentName() string                  { return "Narrator" }
func (audioBookToolTestAgent) AppendMemory(string) error          { return nil }
func (audioBookToolTestAgent) Transcript() []tools.TranscriptLine { return nil }

func TestAudioBookPlannerRequiresEndTool(t *testing.T) {
	base := agent.NewBase("Narrator", agent.RoleSeriesHost, nil, nil, nil, nil, nil)
	reg := &agent.Registry{SeriesHost: agent.NewSeriesHost(base, "", 1, 1, "", "", nil, nil, nil, nil, nil)}
	state := &audioBookEndState{}
	planner := NewAudioBookPlanner(&config.DebateTopic{TotalMinutes: 12}, reg, state)
	ctx := context.Background()

	first, ok := planner.Next(ctx)
	if !ok {
		t.Fatalf("first audiobook turn not emitted")
	}
	if got, want := first.Directive, "narrate"; got != want {
		t.Fatalf("first directive = %q, want %q", got, want)
	}
	if got, want := first.Budget, 12*time.Minute; got != want {
		t.Fatalf("first budget = %v, want %v", got, want)
	}

	second, ok := planner.Next(ctx)
	if !ok {
		t.Fatalf("planner ended without end_audio_book")
	}
	if got := second.Directive; got == "narrate" {
		t.Fatalf("second directive should be a continuation, got %q", got)
	}
	for _, want := range []string{
		"Do not restart",
		"add filler",
		"call end_audio_book immediately with no spoken text",
		"as soon as the final planned chapter is fully narrated",
	} {
		if !strings.Contains(second.Directive, want) {
			t.Fatalf("second directive missing %q: %q", want, second.Directive)
		}
	}

	state.RequestDone()
	requested, accepted := planner.ValidateEndAfterTurn(0, 0)
	if !requested || !accepted {
		t.Fatalf("expected end request accepted without scene requirement, requested=%v accepted=%v", requested, accepted)
	}
	if _, ok := planner.Next(ctx); ok {
		t.Fatalf("planner emitted a turn after end_audio_book")
	}
}

func TestEndAudioBookToolRequestsPlannerCompletion(t *testing.T) {
	state := &audioBookEndState{}
	tool := endAudioBookTool{state: state}

	if state.Done() {
		t.Fatalf("state should start incomplete")
	}
	if _, err := tool.Call(context.Background(), map[string]any{}, audioBookToolTestAgent{}); err != nil {
		t.Fatalf("tool call: %v", err)
	}
	if !state.EndRequested() {
		t.Fatalf("end_audio_book did not request completion")
	}
	if state.Done() {
		t.Fatalf("end_audio_book should not mark state done before backend validation")
	}
}

func TestPipelineRejectsPrematureAudioBookEndRequest(t *testing.T) {
	base := agent.NewBase("Narrator", agent.RoleSeriesHost, nil, nil, nil, nil, nil)
	reg := &agent.Registry{SeriesHost: agent.NewSeriesHost(base, "", 1, 1, "", "", nil, nil, nil, nil, nil)}
	state := &audioBookEndState{}
	planner := NewAudioBookPlanner(&config.DebateTopic{TotalMinutes: 12}, reg, state)
	pipe := NewPipeline(Deps{
		Planner:            planner,
		ContentType:        config.ContentTypeAudioBook,
		AudioBookImageURLs: make([]string, 25),
	})

	state.RequestDone()
	pipe.validateAudioBookCompletionRequest(&Turn{ID: 1, maxSceneIndex: 18})
	if planner.Done() {
		t.Fatalf("planner accepted completion before final scene marker")
	}
	if state.EndRequested() {
		t.Fatalf("premature completion request should be cleared")
	}

	continued, ok := planner.Next(context.Background())
	if !ok {
		t.Fatalf("planner should continue after premature end request")
	}
	if !strings.HasPrefix(continued.Directive, "narrate") {
		t.Fatalf("continuation directive = %q", continued.Directive)
	}

	state.RequestDone()
	pipe.validateAudioBookCompletionRequest(&Turn{ID: 2, maxSceneIndex: 24})
	if !planner.Done() {
		t.Fatalf("planner did not accept completion after final scene marker")
	}
}
