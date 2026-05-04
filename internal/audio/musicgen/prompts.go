package musicgen

import (
	"fmt"
	"strings"

	"github.com/sirily11/debate-bot/internal/config"
)

// Phase names used for cache filenames and prompt selection. Matches the
// directive strings the puzzle planner emits ("surface", "reveal") so the
// pipeline can look up music by directive at produce-time without a
// translation layer.
const (
	PhaseSurface = "surface"
	PhaseReveal  = "reveal"
)

// BuildPrompt is exported so a smoke test or future tooling can probe the
// prompt for a given phase without invoking generation.
func BuildPrompt(phase string, topic *config.DebateTopic) string {
	return buildPrompt(phase, topic)
}

// buildPrompt produces the per-phase Lyria prompt.
//
// The two phases get different moods:
//   - surface (湯面): quiet, mysterious, suspended — sets the puzzle's
//     scene without giving anything away. Sparse instrumentation so the
//     host's narration sits clearly on top after amix ducking.
//   - reveal (湯底): emotional resolution — warmer harmonies, gentle
//     swell. Still instrumental so the host's truth narration carries.
//
// Both prompts include the puzzle's surface text so the model gets
// thematic context (the surface is safe to feed to either prompt — it
// does not leak the truth). The reveal prompt also gets the truth so
// the model can match the emotional arc; that mirrors how
// scenes.buildPrompt feeds the truth only to the reveal scene.
func buildPrompt(phase string, topic *config.DebateTopic) string {
	surface := strings.TrimSpace(topic.Surface)
	truth := strings.TrimSpace(topic.Truth)

	// Style suffix shared by every phase: instrumental only, no vocals,
	// no lyrics. The host's TTS provides the only vocal layer; vocals
	// from the music would clash and pull the listener's attention away
	// from the narration. Length guidance (~90 s) is approximate — Lyria
	// Pro decides actual duration; the amix stage loops shorter clips.
	const styleSuffix = `

INSTRUMENTAL ONLY — no vocals, no lyrics, no spoken word.
Cinematic film score sensibility — sits cleanly under spoken narration.
Approximately 90 seconds long. Slow tempo, clear stereo image, mastered
quietly so it functions as a background bed without competing with a
voice on top.`

	switch phase {
	case PhaseSurface:
		return strings.TrimSpace(fmt.Sprintf(`
A quiet, mysterious cinematic ambient bed introducing this scenario:

%s

Soft sustained piano, warm low strings, occasional distant bell or
breathy synth pad. Suspended, contemplative, faintly uneasy — the
listener should feel the mystery without being given any emotional
resolution yet. Minimal melody; mostly atmosphere.
%s`, surface, styleSuffix))

	case PhaseReveal:
		return strings.TrimSpace(fmt.Sprintf(`
An emotional cinematic score for the reveal moment of this story.

Surface scenario: %s

Underlying truth (use this to shape the emotional arc, do not literalise):
%s

Begin with a held string note that opens up — soft piano enters with a
rising melodic line, gentle string swell, then a tender resolution.
Bittersweet, cathartic, weighted with quiet acceptance. Builds gradually
across the clip; ends settled, not triumphant.
%s`, surface, truth, styleSuffix))
	}
	return ""
}
