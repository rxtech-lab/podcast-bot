package scenes

import (
	"strings"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
)

func audioBookFixtureTopic() *config.DebateTopic {
	return &config.DebateTopic{
		Title:        "The Little Lighthouse",
		TotalMinutes: 15,
		Background:   "A lighthouse keeper's daughter learns to run the lamp alone through one long winter storm season.",
		AudioBookChapters: []config.AudioBookChapter{
			{Title: "The Keeper's Daughter", Summary: "Mara grows up in the lighthouse, learning the lamp, the logbook, and the moods of the sea from her father."},
			{Title: "The Long Storm", Summary: "Her father falls ill as the worst storm in decades rolls in, and Mara must keep the light burning through the night alone."},
			{Title: "First Light", Summary: "The storm breaks at dawn; a rescued fishing crew comes ashore, and Mara understands the lamp is hers now."},
		},
	}
}

func TestAudioBookFrameFloor(t *testing.T) {
	cases := []struct{ minutes, want int }{
		{0, minAudioBookFrames},
		{-3, minAudioBookFrames},
		{1, minAudioBookFrames},  // 2 < min → clamp up
		{3, minAudioBookFrames},  // 6 == min
		{10, 20},                 // 2/min
		{15, 30},                 // 2/min
		{30, maxAudioBookFrames}, // 60 > max → clamp down
		{500, maxAudioBookFrames},
	}
	for _, c := range cases {
		if got := audioBookFrameFloor(c.minutes); got != c.want {
			t.Errorf("audioBookFrameFloor(%d) = %d, want %d", c.minutes, got, c.want)
		}
	}
}

func TestFallbackAudioBookScenePlan(t *testing.T) {
	tp := audioBookFixtureTopic()
	plan := FallbackAudioBookScenePlan(tp)
	if plan == nil {
		t.Fatal("fallback plan is nil for a valid topic")
	}
	n := len(plan.Narration)
	if n < minAudioBookFrames || n > maxAudioBookFrames {
		t.Fatalf("fallback narration count %d outside [%d, %d]", n, minAudioBookFrames, maxAudioBookFrames)
	}
	if len(plan.NarrationAnchors) != n || len(plan.NarrationAnimations) != n || len(plan.BeatChapters) != n {
		t.Fatalf("parallel slices mismatched: narration=%d anchors=%d anims=%d chapters=%d",
			n, len(plan.NarrationAnchors), len(plan.NarrationAnimations), len(plan.BeatChapters))
	}
	allowed := map[string]bool{
		AnimationStall: true, AnimationPanLeft: true, AnimationPanRight: true,
		AnimationPanTop: true, AnimationPanBottom: true, AnimationZoomIn: true, AnimationZoomOut: true,
	}
	for i, a := range plan.NarrationAnimations {
		if !allowed[a] {
			t.Errorf("animation[%d] = %q not in allowed set", i, a)
		}
	}
	for i, c := range plan.BeatChapters {
		if c < 0 || c >= len(tp.AudioBookChapters) {
			t.Errorf("beat chapter[%d] = %d out of range", i, c)
		}
		if i > 0 && c < plan.BeatChapters[i-1] {
			t.Errorf("beat chapters not non-decreasing at %d: %d < %d", i, c, plan.BeatChapters[i-1])
		}
	}
}

func TestFallbackAudioBookScenePlanNilCases(t *testing.T) {
	if p := FallbackAudioBookScenePlan(nil); p != nil {
		t.Error("expected nil plan for nil topic")
	}
	if p := FallbackAudioBookScenePlan(&config.DebateTopic{Title: "x"}); p != nil {
		t.Error("expected nil plan for topic without chapters")
	}
}

func TestNormaliseBeatChapters(t *testing.T) {
	// Out-of-range values clamp, decreasing values are pinned non-decreasing.
	got := normaliseBeatChapters([]int{-1, 5, 1, 0}, 4, 3)
	want := []int{0, 2, 2, 2}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normaliseBeatChapters clamp: got %v, want %v", got, want)
		}
	}
	// Nil input spreads evenly across chapters and stays non-decreasing.
	got = normaliseBeatChapters(nil, 6, 3)
	if len(got) != 6 {
		t.Fatalf("expected 6 entries, got %d", len(got))
	}
	if got[0] != 0 || got[len(got)-1] != 2 {
		t.Errorf("even spread should start at 0 and end at last chapter: %v", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i] < got[i-1] {
			t.Errorf("even spread not non-decreasing: %v", got)
		}
	}
	// Zero chapters yields all zeros without panicking.
	got = normaliseBeatChapters([]int{3}, 2, 0)
	if got[0] != 0 || got[1] != 0 {
		t.Errorf("zero chapterCount should produce zeros: %v", got)
	}
}

func TestAudioBookOutlineTextFallsBackToChapters(t *testing.T) {
	tp := audioBookFixtureTopic()
	out := AudioBookOutlineText(tp)
	if !strings.Contains(out, "The Long Storm") {
		t.Errorf("rebuilt outline should contain chapter titles:\n%s", out)
	}
	tp.Surface = "custom outline text"
	if got := AudioBookOutlineText(tp); got != "custom outline text" {
		t.Errorf("Surface should win when present, got %q", got)
	}
}
