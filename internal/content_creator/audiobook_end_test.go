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

	state.MarkDone()
	if _, ok := planner.Next(ctx); ok {
		t.Fatalf("planner emitted a turn after end_audio_book")
	}
}

func TestEndAudioBookToolMarksPlannerDone(t *testing.T) {
	state := &audioBookEndState{}
	tool := endAudioBookTool{state: state}

	if state.Done() {
		t.Fatalf("state should start incomplete")
	}
	if _, err := tool.Call(context.Background(), map[string]any{}, audioBookToolTestAgent{}); err != nil {
		t.Fatalf("tool call: %v", err)
	}
	if !state.Done() {
		t.Fatalf("end_audio_book did not mark state done")
	}
}
