package contentcreator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
)

func newsTestAgent(name string, role agent.Role) agent.Agent {
	base := agent.NewBase(name, role, nil, nil, nil, nil, nil)
	switch role {
	case agent.RoleHost:
		return agent.NewNewsAnchor(base, "", "")
	case agent.RoleDiscussant:
		return agent.NewNewsCommentator(base, "", "")
	}
	return nil
}

func newsTestTopic(stories ...config.NewsStory) *config.DebateTopic {
	return &config.DebateTopic{
		Type:              config.ContentTypeNews,
		SegmentMaxSeconds: 60,
		TotalMinutes:      30,
		NewsStories:       stories,
	}
}

func newsTestRegistry() *agent.Registry {
	return &agent.Registry{
		Host: newsTestAgent("Dana", agent.RoleHost),
		Discussants: []agent.Agent{
			newsTestAgent("Ravi", agent.RoleDiscussant),
			newsTestAgent("Mia", agent.RoleDiscussant),
		},
	}
}

// stubNewsWriter returns deterministic scripted lines and records every
// segment request the feeder makes.
type stubNewsWriter struct {
	mu       sync.Mutex
	requests []agent.NewsSegmentRequest
	fail     bool
	// badSpeaker, when non-empty, is used as the speaker of every story
	// line to exercise the planner's unknown-speaker fallback.
	badSpeaker string
}

func (s *stubNewsWriter) WriteSegment(_ context.Context, req agent.NewsSegmentRequest) ([]agent.NewsScriptLine, error) {
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()
	if s.fail {
		return nil, errors.New("writer down")
	}
	switch req.Kind {
	case agent.NewsSegmentIntro:
		return []agent.NewsScriptLine{{Speaker: "Dana", Text: "Good morning."}}, nil
	case agent.NewsSegmentClosing:
		return []agent.NewsScriptLine{{Speaker: "Dana", Text: "That's all for today."}}, nil
	default:
		lines := []agent.NewsScriptLine{{Speaker: "Dana", Text: fmt.Sprintf("Story %d read.", req.StoryNumber)}}
		for _, sp := range req.AddOnSpeakers {
			if s.badSpeaker != "" {
				sp = s.badSpeaker
			}
			lines = append(lines, agent.NewsScriptLine{Speaker: sp, Text: fmt.Sprintf("Add-on by %s.", sp)})
		}
		return lines, nil
	}
}

func (s *stubNewsWriter) recorded() []agent.NewsSegmentRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]agent.NewsSegmentRequest(nil), s.requests...)
}

// The whole broadcast replays the pre-written script in rundown order:
// intro, each story segment (anchor read + rotated co-host add-ons), then the
// sign-off — every line as a zero-latency "scripted:" directive.
func TestNewsPlannerScriptedFlow(t *testing.T) {
	topic := newsTestTopic(
		config.NewsStory{Headline: "Chips rally", Summary: "Chips up.", KeyFacts: []string{"Index +4%"}},
		config.NewsStory{Headline: "New AI rules", Summary: "Rules proposed."},
	)
	w := &stubNewsWriter{}
	p := NewNewsPlanner(topic, NewTracker(30*time.Minute), newsTestRegistry(), &userQueue{}, nil, w)
	ctx := context.Background()

	type step struct {
		speaker, directive string
		phase              agent.Phase
	}
	next := func() step {
		turn, ok := p.Next(ctx)
		if !ok {
			t.Fatalf("planner ended early")
		}
		return step{turn.Speaker.Name(), turn.Directive, turn.Phase}
	}

	want := []step{
		{"Dana", "scripted:Good morning.", agent.PhaseOpening},
		{"Dana", "scripted:Story 1 read.", agent.PhaseFreeSpeech},
		{"Ravi", "scripted:Add-on by Ravi.", agent.PhaseFreeSpeech},
		{"Mia", "scripted:Add-on by Mia.", agent.PhaseFreeSpeech},
		{"Dana", "scripted:Story 2 read.", agent.PhaseFreeSpeech},
		{"Ravi", "scripted:Add-on by Ravi.", agent.PhaseFreeSpeech},
		{"Mia", "scripted:Add-on by Mia.", agent.PhaseFreeSpeech},
		{"Dana", "scripted:That's all for today.", agent.PhaseEnded},
	}
	for i, wnt := range want {
		got := next()
		if got != wnt {
			t.Fatalf("turn %d: got %+v, want %+v", i+1, got, wnt)
		}
	}
	if _, ok := p.Next(ctx); ok {
		t.Fatalf("planner should have ended after the sign-off")
	}

	// The feeder pre-generated intro → closing → stories, in that order (the
	// sign-off comes second so an early /end never waits behind story
	// generation), with the rundown's stories in order and bridges wired.
	reqs := w.recorded()
	if len(reqs) != 4 {
		t.Fatalf("writer requests = %d, want 4 (intro, closing, 2 stories)", len(reqs))
	}
	if reqs[0].Kind != agent.NewsSegmentIntro || reqs[1].Kind != agent.NewsSegmentClosing {
		t.Fatalf("request order = %s,%s..., want intro,closing first", reqs[0].Kind, reqs[1].Kind)
	}
	if reqs[2].Headline != "Chips rally" || reqs[2].PrevHeadline != "" {
		t.Fatalf("story 1 request = %+v, want first story with no bridge", reqs[2])
	}
	if reqs[3].Headline != "New AI rules" || reqs[3].PrevHeadline != "Chips rally" {
		t.Fatalf("story 2 request = %+v, want bridge from story 1", reqs[3])
	}
	if got := strings.Join(reqs[2].AddOnSpeakers, ","); got != "Ravi,Mia" {
		t.Fatalf("story 1 add-on speakers = %q, want both co-hosts on a 2-story rundown", got)
	}
}

// A listener message interrupts the script: the anchor paraphrases and hands
// off (live turn), the named co-host answers (live turn), and the script then
// resumes at exactly the next pre-written line.
func TestNewsPlannerAudienceInterleave(t *testing.T) {
	topic := newsTestTopic(
		config.NewsStory{Headline: "Chips rally", Summary: "Chips up."},
		config.NewsStory{Headline: "New AI rules", Summary: "Rules proposed."},
	)
	q := &userQueue{}
	p := NewNewsPlanner(topic, NewTracker(30*time.Minute), newsTestRegistry(), q, nil, &stubNewsWriter{})
	ctx := context.Background()

	// intro + story 1 anchor read.
	for i := 0; i < 2; i++ {
		if _, ok := p.Next(ctx); !ok {
			t.Fatalf("planner ended early at turn %d", i+1)
		}
	}

	q.push(userMessage{Username: "Qiwei", Text: "what about prices?"})
	host, ok := p.Next(ctx)
	if !ok {
		t.Fatalf("planner ended before address-user")
	}
	if got := host.Speaker.Name(); got != "Dana" {
		t.Fatalf("address-user speaker = %q, want the anchor", got)
	}
	if !strings.HasPrefix(host.Directive, "address-user:") {
		t.Fatalf("directive = %q, want address-user prefix", host.Directive)
	}
	if !strings.Contains(host.Directive, "Qiwei: what about prices?") {
		t.Fatalf("directive = %q, want it to carry the listener message", host.Directive)
	}
	if !strings.HasSuffix(host.Directive, "[hand off to: Ravi]") {
		t.Fatalf("directive = %q, want the answerer named at the end", host.Directive)
	}

	ans, ok := p.Next(ctx)
	if !ok {
		t.Fatalf("planner ended before answer-user")
	}
	if got := ans.Speaker.Name(); got != "Ravi" {
		t.Fatalf("answerer = %q, want Ravi (the name the anchor said on-air)", got)
	}
	if !strings.HasPrefix(ans.Directive, "answer-user:") {
		t.Fatalf("directive = %q, want answer-user prefix", ans.Directive)
	}

	// The script resumes at the next pre-written line of story 1.
	resumed, ok := p.Next(ctx)
	if !ok {
		t.Fatalf("planner ended before the script resumed")
	}
	if got := resumed.Directive; got != "scripted:Add-on by Ravi." {
		t.Fatalf("post-answer directive = %q, want the script to resume", got)
	}
}

// /end jumps straight to the pre-generated sign-off, skipping the remaining
// rundown.
func TestNewsPlannerEndJumpsToClosing(t *testing.T) {
	topic := newsTestTopic(
		config.NewsStory{Headline: "Chips rally", Summary: "Chips up."},
		config.NewsStory{Headline: "New AI rules", Summary: "Rules proposed."},
	)
	q := &userQueue{}
	p := NewNewsPlanner(topic, NewTracker(30*time.Minute), newsTestRegistry(), q, nil, &stubNewsWriter{})
	ctx := context.Background()

	if _, ok := p.Next(ctx); !ok { // intro
		t.Fatalf("planner ended before the intro")
	}
	q.push(userMessage{Text: "/end"})
	closing, ok := p.Next(ctx)
	if !ok {
		t.Fatalf("planner ended before the sign-off")
	}
	if got := closing.Directive; got != "scripted:That's all for today." {
		t.Fatalf("post-/end directive = %q, want the sign-off script", got)
	}
	if _, ok := p.Next(ctx); ok {
		t.Fatalf("planner should have ended after the sign-off")
	}
}

// A tracker with less than the closing threshold remaining plays the intro,
// then signs off without starting a story segment (mirrors the E2E 1-minute
// short-run path).
func TestNewsPlannerTimeoutClosesEarly(t *testing.T) {
	topic := newsTestTopic(config.NewsStory{Headline: "Chips rally", Summary: "Chips up."})
	p := NewNewsPlanner(topic, NewTracker(1*time.Minute), newsTestRegistry(), &userQueue{}, nil, &stubNewsWriter{})
	ctx := context.Background()

	intro, ok := p.Next(ctx)
	if !ok || intro.Directive != "scripted:Good morning." {
		t.Fatalf("first turn = %+v, want the scripted intro", intro)
	}
	closing, ok := p.Next(ctx)
	if !ok || closing.Directive != "scripted:That's all for today." {
		t.Fatalf("second turn directive = %q, want the sign-off under a tiny budget", closing.Directive)
	}
}

// When the script writer fails, the broadcast degrades to verbatim rundown
// copy read by the anchor instead of dying.
func TestNewsPlannerFallbackWhenWriterFails(t *testing.T) {
	topic := newsTestTopic(
		config.NewsStory{Headline: "Chips rally", Summary: "Chips up.", KeyFacts: []string{"Index +4%"}},
	)
	p := NewNewsPlanner(topic, NewTracker(30*time.Minute), newsTestRegistry(), &userQueue{}, nil, &stubNewsWriter{fail: true})
	ctx := context.Background()

	intro, ok := p.Next(ctx)
	if !ok {
		t.Fatalf("planner ended before the fallback intro")
	}
	if intro.Speaker.Name() != "Dana" || intro.Directive != "scripted:Chips rally." {
		t.Fatalf("fallback intro = %s %q, want the anchor reading the headlines", intro.Speaker.Name(), intro.Directive)
	}
	story, ok := p.Next(ctx)
	if !ok {
		t.Fatalf("planner ended before the fallback story")
	}
	if got := story.Directive; got != "scripted:Chips rally Chips up." {
		t.Fatalf("fallback story = %q, want headline and summary copy", got)
	}
	for _, wantSpeaker := range []string{"Ravi", "Mia"} {
		addOn, addOnOK := p.Next(ctx)
		if !addOnOK {
			t.Fatalf("planner ended before fallback add-on for %s", wantSpeaker)
		}
		if addOn.Speaker.Name() != wantSpeaker || addOn.Directive != "scripted:Index +4%" {
			t.Fatalf("fallback add-on = %s %q, want %s to receive a factual line", addOn.Speaker.Name(), addOn.Directive, wantSpeaker)
		}
	}
	closing, ok := p.Next(ctx)
	if !ok {
		t.Fatalf("planner ended before the fallback sign-off")
	}
	if got := closing.Directive; got != "closing" {
		t.Fatalf("fallback sign-off = %q, want the detail-free live closing directive", got)
	}
}

// A scripted line naming a speaker who isn't on the desk is read by the
// anchor rather than crashing the roster lookup.
func TestNewsPlannerUnknownSpeakerFallsBackToAnchor(t *testing.T) {
	topic := newsTestTopic(config.NewsStory{Headline: "Chips rally", Summary: "Chips up."})
	p := NewNewsPlanner(topic, NewTracker(30*time.Minute), newsTestRegistry(), &userQueue{}, nil,
		&stubNewsWriter{badSpeaker: "Nobody"})
	ctx := context.Background()

	if _, ok := p.Next(ctx); !ok { // intro
		t.Fatalf("planner ended before the intro")
	}
	if _, ok := p.Next(ctx); !ok { // anchor read
		t.Fatalf("planner ended before the anchor read")
	}
	addon, ok := p.Next(ctx)
	if !ok {
		t.Fatalf("planner ended before the add-on line")
	}
	if got := addon.Speaker.Name(); got != "Dana" {
		t.Fatalf("unknown-speaker line read by %q, want the anchor", got)
	}
}
