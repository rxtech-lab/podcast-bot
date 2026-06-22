package contentcreator

import (
	"context"
	"testing"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
)

func discTestAgent(name string, role agent.Role) agent.Agent {
	base := agent.NewBase(name, role, nil, nil, nil, nil, nil)
	switch role {
	case agent.RoleHost:
		return agent.NewHost(base)
	case agent.RoleDiscussant:
		return agent.NewDiscussant(base, "")
	}
	return nil
}

func TestDiscussionPlannerTurnOrder(t *testing.T) {
	reg := &agent.Registry{
		Host: discTestAgent("Mod", agent.RoleHost),
		Discussants: []agent.Agent{
			discTestAgent("Ann", agent.RoleDiscussant),
			discTestAgent("Bo", agent.RoleDiscussant),
		},
	}
	topic := &config.DebateTopic{SegmentMaxSeconds: 60, TotalMinutes: 30}
	q := &userQueue{}
	p := NewDiscussionPlanner(topic, NewTracker(30*time.Minute), reg, q, nil)
	ctx := context.Background()

	type step struct{ speaker, directive string }
	next := func() step {
		turn, ok := p.Next(ctx)
		if !ok {
			t.Fatalf("planner ended early")
		}
		return step{turn.Speaker.Name(), turn.Directive}
	}

	want := []step{
		{"Mod", "intro"},
		{"Ann", "open"},
		{"Bo", "open"},
		{"Mod", "transition:open-discussion"},
		{"Ann", "respond"},
		{"Bo", "respond"},
		{"Ann", "respond"},
	}
	for i, w := range want {
		got := next()
		if got != w {
			t.Fatalf("turn %d: got %+v, want %+v", i+1, got, w)
		}
	}

	// /end flips into the closing sequence: host transition, each discussant
	// gives a closing thought, host signs off, then the planner ends.
	q.push(userMessage{Text: "/end"})
	wantClosing := []step{
		{"Mod", "transition:closing-thoughts"},
		{"Ann", "closing"},
		{"Bo", "closing"},
		{"Mod", "closing"},
	}
	for i, w := range wantClosing {
		got := next()
		if got != w {
			t.Fatalf("closing turn %d: got %+v, want %+v", i+1, got, w)
		}
	}
	if _, ok := p.Next(ctx); ok {
		t.Fatalf("planner should have ended after sign-off")
	}
}

func TestDiscussionPlannerNaturalFinishRequestSkipsToClosing(t *testing.T) {
	reg := &agent.Registry{
		Host: discTestAgent("Mod", agent.RoleHost),
		Discussants: []agent.Agent{
			discTestAgent("Ann", agent.RoleDiscussant),
			discTestAgent("Bo", agent.RoleDiscussant),
		},
	}
	topic := &config.DebateTopic{SegmentMaxSeconds: 60, TotalMinutes: 30}
	q := &userQueue{}
	p := NewDiscussionPlanner(topic, NewTracker(30*time.Minute), reg, q, nil)
	ctx := context.Background()

	for i := 0; i < 4; i++ {
		if _, ok := p.Next(ctx); !ok {
			t.Fatalf("planner ended before free discussion")
		}
	}

	q.push(userMessage{Username: "Qiwei", Text: "end it fast"})
	turn, ok := p.Next(ctx)
	if !ok {
		t.Fatalf("planner ended before closing transition")
	}
	if got, want := turn.Speaker.Name(), "Mod"; got != want {
		t.Fatalf("speaker = %q, want %q", got, want)
	}
	if got, want := turn.Directive, "transition:closing-thoughts"; got != want {
		t.Fatalf("directive = %q, want %q", got, want)
	}
}

func TestUserQueueEndRequestDetection(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{"/end", true},
		{"end it fast", true},
		{"please skip to the end", true},
		{"wrap it up please", true},
		{"what did the host mean?", false},
		{"tell me about the ending", false},
	}
	for _, tt := range tests {
		if got := isEndRequest(tt.text); got != tt.want {
			t.Fatalf("isEndRequest(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}
