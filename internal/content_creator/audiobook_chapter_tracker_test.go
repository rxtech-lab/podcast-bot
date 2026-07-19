package contentcreator

import (
	"context"
	"strings"
	"testing"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
)

func newTrackerPlanner(t *testing.T, topic *config.DebateTopic, texts map[int]string) *AudioBookPlanner {
	t.Helper()
	base := agent.NewBase("Narrator", agent.RoleSeriesHost, nil, nil, nil, nil, nil)
	reg := &agent.Registry{SeriesHost: agent.NewSeriesHost(base, "", 1, 1, "", "", nil, nil, nil, nil, nil)}
	return NewAudioBookPlanner(topic, reg, &audioBookEndState{}).WithChapterTexts(texts)
}

func trackerTopic() *config.DebateTopic {
	return &config.DebateTopic{
		TotalMinutes: 10,
		AudioBookChapters: []config.AudioBookChapter{
			{Title: "The Road", Summary: "s"},
			{Title: "The Clinic", Summary: "s"},
			{Title: "Epilogue", Summary: "s"},
		},
	}
}

func TestCurrentAudioBookChapterInitializesToFirstSelected(t *testing.T) {
	p := newTrackerPlanner(t, trackerTopic(), map[int]string{1: "text one", 2: "text two", 3: "text three"})
	label, text, ok := p.CurrentAudioBookChapter()
	if !ok {
		t.Fatal("expected source-text mode")
	}
	if label != "Chapter 1: The Road" || text != "text one" {
		t.Fatalf("got label=%q text=%q", label, text)
	}
}

func TestChapterTrackerAdvancesOnJudgeChapterComplete(t *testing.T) {
	p := newTrackerPlanner(t, trackerTopic(), map[int]string{1: "text one", 2: "text two", 3: "text three"})
	p.boundaryJudge = func(context.Context, string, string, int) (audioBookBoundaryDecision, bool) {
		return audioBookBoundaryDecision{Action: "keep", ChapterComplete: true}, true
	}
	if _, _, ok := p.CurrentAudioBookChapter(); !ok {
		t.Fatal("tracker init failed")
	}
	if stopped := p.ReviewAudioBookLoop(context.Background(), "chapter one narration"); stopped {
		t.Fatal("keep decision should not stop the loop")
	}
	label, text, ok := p.CurrentAudioBookChapter()
	if !ok || label != "Chapter 2: The Clinic" || text != "text two" {
		t.Fatalf("tracker should advance to chapter 2, got label=%q text=%q ok=%v", label, text, ok)
	}
	// Advancing at the last selected chapter stays put.
	p.advanceCurrentChapter()
	p.advanceCurrentChapter()
	p.advanceCurrentChapter()
	label, _, _ = p.CurrentAudioBookChapter()
	if label != "Chapter 3: Epilogue" {
		t.Fatalf("tracker moved past the last chapter: %q", label)
	}
}

func TestChapterTrackerHoldsOnJudgeFailure(t *testing.T) {
	p := newTrackerPlanner(t, trackerTopic(), map[int]string{1: "text one", 2: "text two"})
	p.boundaryJudge = func(context.Context, string, string, int) (audioBookBoundaryDecision, bool) {
		return audioBookBoundaryDecision{}, false
	}
	if _, _, ok := p.CurrentAudioBookChapter(); !ok {
		t.Fatal("tracker init failed")
	}
	p.ReviewAudioBookLoop(context.Background(), "some narration")
	if label, _, _ := p.CurrentAudioBookChapter(); label != "Chapter 1: The Road" {
		t.Fatalf("judge failure must not advance the tracker, got %q", label)
	}
}

func TestChapterTrackerWalksNonContiguousBatch(t *testing.T) {
	topic := &config.DebateTopic{
		TotalMinutes: 10,
		AudioBookChapters: []config.AudioBookChapter{
			{Title: "Six", Summary: "s"},
			{Title: "Nine", Summary: "s"},
		},
		AudioBookChapterIndices: []int{6, 9},
	}
	p := newTrackerPlanner(t, topic, map[int]string{6: "text six", 9: "text nine"})
	label, text, ok := p.CurrentAudioBookChapter()
	if !ok || label != "Chapter 6: Six" || text != "text six" {
		t.Fatalf("got label=%q text=%q ok=%v", label, text, ok)
	}
	p.advanceCurrentChapter()
	label, text, _ = p.CurrentAudioBookChapter()
	if label != "Chapter 9: Nine" || text != "text nine" {
		t.Fatalf("non-contiguous advance failed: label=%q text=%q", label, text)
	}
}

func TestCurrentAudioBookChapterLegacyMode(t *testing.T) {
	p := newTrackerPlanner(t, trackerTopic(), nil)
	if _, _, ok := p.CurrentAudioBookChapter(); ok {
		t.Fatal("legacy mode (no chapter texts) must report ok=false")
	}
	// Directive must not mention source text in legacy mode.
	turn, ok := p.Next(context.Background())
	if !ok {
		t.Fatal("no turn emitted")
	}
	if strings.Contains(turn.Directive, "current chapter source text") {
		t.Fatalf("legacy directive should not mention source text: %q", turn.Directive)
	}
}

func TestNextDirectiveMentionsSourceTextInSourceMode(t *testing.T) {
	p := newTrackerPlanner(t, trackerTopic(), map[int]string{1: "text"})
	turn, ok := p.Next(context.Background())
	if !ok {
		t.Fatal("no turn emitted")
	}
	if !strings.Contains(turn.Directive, "current chapter source text") {
		t.Fatalf("source-mode directive missing source-text instruction: %q", turn.Directive)
	}
}

func TestCurrentAudioBookChapterMissingTextFallsBack(t *testing.T) {
	p := newTrackerPlanner(t, trackerTopic(), map[int]string{2: "text two"})
	if _, _, ok := p.CurrentAudioBookChapter(); ok {
		t.Fatal("chapter 1 has no fetched text; expected ok=false for this turn")
	}
}
