package musicgen

import (
	"strings"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
)

func fixtureTopic() *config.DebateTopic {
	return &config.DebateTopic{
		Title:   "海龜湯測試",
		Surface: "一個男人走進餐廳點了海龜湯,喝了一口就離開,回家後結束了自己的生命。",
		Truth:   "他多年前漂流海上,同伴用犧牲的同伴的肉熬湯救他,謊稱是海龜湯。",
	}
}

// Both prompts must declare INSTRUMENTAL ONLY so Lyria does not return
// vocal music that would clash with the host's TTS narration. Locking
// this in via test so a future prompt edit can't accidentally drop the
// guidance.
func TestPromptsAreInstrumentalOnly(t *testing.T) {
	tp := fixtureTopic()
	for _, phase := range []string{PhaseSurface, PhaseReveal} {
		got := BuildPrompt(phase, tp)
		if !strings.Contains(got, "INSTRUMENTAL ONLY") {
			t.Errorf("phase %q prompt missing INSTRUMENTAL ONLY guidance:\n%s", phase, got)
		}
	}
}

// Surface prompt must NOT include the puzzle truth — leaking it into
// the music prompt is harmless audibly but a regression that suggests
// the truth is reaching surfaces it shouldn't.
func TestSurfacePromptDoesNotIncludeTruth(t *testing.T) {
	tp := fixtureTopic()
	got := BuildPrompt(PhaseSurface, tp)
	needle := "犧牲的同伴的肉"
	if strings.Contains(got, needle) {
		t.Errorf("surface prompt leaks truth: contains %q\nprompt:\n%s", needle, got)
	}
}

// Reveal prompt is the one place the truth is allowed in a music prompt
// (it shapes the emotional arc).
func TestRevealPromptUsesTruth(t *testing.T) {
	tp := fixtureTopic()
	got := BuildPrompt(PhaseReveal, tp)
	if !strings.Contains(got, "犧牲的同伴的肉") {
		t.Errorf("reveal prompt should reference the truth\nprompt:\n%s", got)
	}
}

func TestPromptsAreDeterministic(t *testing.T) {
	tp := fixtureTopic()
	for _, phase := range []string{PhaseSurface, PhaseReveal} {
		a := BuildPrompt(phase, tp)
		b := BuildPrompt(phase, tp)
		if a != b {
			t.Errorf("phase %q prompt is non-deterministic", phase)
		}
		if strings.TrimSpace(a) == "" {
			t.Errorf("phase %q produced empty prompt", phase)
		}
	}
}

func TestPromptKeyChangesWithPrompt(t *testing.T) {
	if promptKey("a") == promptKey("b") {
		t.Fatal("promptKey should differ for different inputs")
	}
}
