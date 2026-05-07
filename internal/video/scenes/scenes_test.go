package scenes

import (
	"strings"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
)

func fixtureTopic() *config.DebateTopic {
	return &config.DebateTopic{
		Title:   "海龜湯測試",
		Surface: "一個男人走進餐廳點了海龜湯,喝了一口就離開,回家後結束了自己的生命。",
		Truth:   "他多年前漂流海上,同伴用犧牲的同伴的肉熬湯救他,謊稱是海龜湯。今日他第一次嚐到真正的海龜湯——味道完全不同——才驚覺真相。",
	}
}

// Surface and QA prompts must NOT contain any of the truth — leaking the
// truth into the bg image prompt would let a determined viewer reconstruct
// it from the picture, defeating the puzzle.
func TestPromptsDoNotLeakTruth(t *testing.T) {
	tp := fixtureTopic()
	for _, scene := range []string{SceneSurface, SceneQA} {
		got := BuildPrompt(scene, tp)
		// Spot-check: the truth contains "犧牲的同伴的肉" — the surface does
		// not. If that phrase ever appears in the surface/qa prompt we've
		// regressed.
		needle := "犧牲的同伴的肉"
		if strings.Contains(got, needle) {
			t.Errorf("scene %q prompt leaks truth: contains %q\nprompt:\n%s",
				scene, needle, got)
		}
	}
}

func TestPromptsAreDeterministic(t *testing.T) {
	tp := fixtureTopic()
	for _, scene := range []string{SceneSurface, SceneQA, SceneReveal, SceneConclusion} {
		a := BuildPrompt(scene, tp)
		b := BuildPrompt(scene, tp)
		if a != b {
			t.Errorf("scene %q prompt is non-deterministic", scene)
		}
		if strings.TrimSpace(a) == "" {
			t.Errorf("scene %q produced empty prompt", scene)
		}
	}
}

func TestRevealPromptUsesTruth(t *testing.T) {
	tp := fixtureTopic()
	got := BuildPrompt(SceneReveal, tp)
	// The reveal scene is the one place the truth is allowed to surface in
	// a prompt — it's never rendered as text, only as an image direction.
	if !strings.Contains(got, "犧牲的同伴的肉") {
		t.Errorf("reveal prompt should reference the truth\nprompt:\n%s", got)
	}
}

func TestPromptKeyChangesWithPrompt(t *testing.T) {
	if promptKey("a") == promptKey("b") {
		t.Fatal("promptKey should differ for different inputs")
	}
}

func TestSceneFrameSizeIs1080p(t *testing.T) {
	if frameW != 1920 || frameH != 1080 {
		t.Fatalf("scene frame size = %dx%d, want 1920x1080", frameW, frameH)
	}
}
