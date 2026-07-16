package contentcreator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
)

func discTestAgent(name string, role agent.Role) agent.Agent {
	base := agent.NewBase(name, role, nil, nil, nil, nil, nil)
	switch role {
	case agent.RoleHost:
		return agent.NewDiscussionHost(base, "")
	case agent.RoleDiscussant:
		return agent.NewDiscussant(base, "", "")
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
		{"Mod", "intro:first=Ann"},
		{"Ann", "open"},
		{"Bo", "open"},
		{"Mod", "transition:open-discussion;call:Ann"},
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
		{"Mod", "transition:closing-thoughts;first=Ann"},
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
	if got, want := turn.Directive, "transition:closing-thoughts;first=Ann"; got != want {
		t.Fatalf("directive = %q, want %q", got, want)
	}
}

// The host's address-user directive must name the same discussant who then
// receives the answer-user turn — the two used to be chosen independently, so
// the host would hand the floor to Ann and Bo would answer.
func TestDiscussionPlannerAudienceHandoffMatchesAnswerer(t *testing.T) {
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

	// Run through the opening into the free round: intro, two opens, the
	// open-discussion transition (which pre-selects Ann), and Ann's respond.
	for i := 0; i < 5; i++ {
		if _, ok := p.Next(ctx); !ok {
			t.Fatalf("planner ended early at turn %d", i+1)
		}
	}

	q.push(userMessage{Username: "Qiwei", Text: "what about cost?"})
	host, ok := p.Next(ctx)
	if !ok {
		t.Fatalf("planner ended before address-user")
	}
	if got, want := host.Speaker.Name(), "Mod"; got != want {
		t.Fatalf("speaker = %q, want %q", got, want)
	}
	if !strings.HasPrefix(host.Directive, "address-user:") {
		t.Fatalf("directive = %q, want address-user prefix", host.Directive)
	}
	// Round-robin stands at Bo (Ann consumed the free-round handoff).
	if !strings.HasSuffix(host.Directive, "[hand off to: Bo]") {
		t.Fatalf("directive = %q, want it to end with the answerer's name", host.Directive)
	}
	ans, ok := p.Next(ctx)
	if !ok {
		t.Fatalf("planner ended before answer-user")
	}
	if got, want := ans.Speaker.Name(), "Bo"; got != want {
		t.Fatalf("answerer = %q, want %q (the name the host said on-air)", got, want)
	}
	if !strings.HasPrefix(ans.Directive, "answer-user:") {
		t.Fatalf("directive = %q, want answer-user prefix", ans.Directive)
	}
}

// A named handoff (pendingNext) must survive an intervening audience
// question: the host reuses the already-announced name for the answer.
func TestDiscussionPlannerAudienceQuestionReusesPendingHandoff(t *testing.T) {
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

	// intro, Ann open, Bo open, transition (pendingNext = Ann).
	for i := 0; i < 4; i++ {
		if _, ok := p.Next(ctx); !ok {
			t.Fatalf("planner ended early at turn %d", i+1)
		}
	}

	q.push(userMessage{Username: "Qiwei", Text: "what about cost?"})
	host, ok := p.Next(ctx)
	if !ok {
		t.Fatalf("planner ended before address-user")
	}
	if !strings.HasSuffix(host.Directive, "[hand off to: Ann]") {
		t.Fatalf("directive = %q, want the pending handoff (Ann) reused", host.Directive)
	}
	ans, ok := p.Next(ctx)
	if !ok {
		t.Fatalf("planner ended before answer-user")
	}
	if got, want := ans.Speaker.Name(), "Ann"; got != want {
		t.Fatalf("answerer = %q, want %q", got, want)
	}
	// The free round then continues with Bo — nobody is skipped.
	turn, ok := p.Next(ctx)
	if !ok {
		t.Fatalf("planner ended before free round resumed")
	}
	if got, want := turn.Speaker.Name(), "Bo"; got != want {
		t.Fatalf("next respond speaker = %q, want %q", got, want)
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
