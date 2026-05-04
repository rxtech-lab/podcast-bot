package scenes

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/llm"
)

// ScenePlan is the per-puzzle decision about how many surface and conclusion
// frames to generate and what each one should depict. Returned by Plan; passed
// to Generate so it overrides the static SurfaceVariantCount /
// ConclusionVariantCount defaults. A nil plan (or an empty Surface /
// Conclusion slice) falls back to the defaults built into buildPromptVariant.
type ScenePlan struct {
	Surface    []string
	Conclusion []string
}

// SurfaceCount and ConclusionCount report how many variants the plan calls for.
// Zero on either field means "use the default count".
func (p *ScenePlan) SurfaceCount() int {
	if p == nil {
		return 0
	}
	return len(p.Surface)
}

func (p *ScenePlan) ConclusionCount() int {
	if p == nil {
		return 0
	}
	return len(p.Conclusion)
}

// Bounds for the plan. The lower bound keeps a documentary-style cut sequence;
// the upper bound caps the imagegen cost per puzzle so a runaway LLM can't
// burn dollars. Surface is more generous because the surface narration is the
// long, music-driven monologue that benefits most from extra imagery.
const (
	minSurfaceFrames    = 6
	maxSurfaceFrames    = 14
	minConclusionFrames = 3
	maxConclusionFrames = 6
)

// Plan asks the LLM to read the surface (湯面) and truth (湯底) and propose
// a list of distinct visual beats for the surface narration and the
// conclusion epilogue. Each entry is a short directional sentence the
// downstream prompt builder folds into the per-variant image prompt.
//
// On any failure (no LLM client, network error, invalid JSON, out-of-bounds
// counts) Plan returns nil so the caller falls back to the static directions
// in buildPromptVariant. This keeps a partial outage degrading visibly
// (fewer/static frames) rather than failing the whole run.
func Plan(ctx context.Context, llmC *llm.Client, topic *config.DebateTopic) *ScenePlan {
	if llmC == nil || topic == nil {
		return nil
	}
	surface := strings.TrimSpace(topic.Surface)
	truth := strings.TrimSpace(topic.Truth)
	if surface == "" {
		return nil
	}

	system := `You are a visual director planning the cut sequence for a 海龜湯
(situation puzzle) podcast. The host narrates the surface story slowly and
contemplatively over a music bed; behind the voice we cross-fade between a
hand-drawn anime cinematic illustrations (Makoto Shinkai / Studio Ghibli /
Kyoto Animation idiom). Your job is to plan the per-frame visual beats so
imagery follows the storytelling.

Output strict JSON with this shape:
{
  "surface": ["...", "...", ...],
  "conclusion": ["...", "...", ...]
}

Rules:
- "surface" lists frames cut in during the surface narration. Each entry is
  ONE short sentence (≤ 30 English words or ≤ 60 CJK characters) describing
  what the camera shows and the framing/composition choice — for example
  "Wide cinematic establishing shot of an empty diner at dusk, neon sign
  buzzing." or "Close detail on a bowl of soup, steam curling under warm
  lamplight." Vary the framing across the list (wide / mid / close / pure
  environment / silhouette / object detail) so the cuts feel like a
  documentary edit, not a slideshow.
- "conclusion" lists frames for the quiet aftermath after the truth has been
  revealed. Same one-sentence format.
- DO NOT mention any character's face speaking or any text/UI — these are
  global constraints downstream.
- DO NOT leak the surface story's hidden truth in any "surface" entry. The
  truth may inform the "conclusion" entries' emotional weight, but never the
  surface frames.
- Surface count: between 6 and 14 frames, scaled to the surface text's length
  and number of distinct beats (paragraph breaks, time/place shifts, new
  figures, recurring objects). A short surface gets ~6, a long multi-scene
  surface gets 10–14.
- Conclusion count: between 3 and 6 frames.
- Plain prose only inside each entry. No markdown, no quotes, no bullet
  prefixes, no scene numbers.`

	user := fmt.Sprintf(
		"Title: %s\n\nSurface (湯面):\n%s\n\nTruth (湯底, for conclusion mood only — never visualize directly):\n%s",
		topic.Title, surface, truth)

	raw, err := llmC.JSON(ctx, system, user)
	if err != nil {
		return nil
	}

	var parsed struct {
		Surface    []string `json:"surface"`
		Conclusion []string `json:"conclusion"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil
	}
	parsed.Surface = clampSlice(parsed.Surface, minSurfaceFrames, maxSurfaceFrames)
	parsed.Conclusion = clampSlice(parsed.Conclusion, minConclusionFrames, maxConclusionFrames)
	if len(parsed.Surface) == 0 || len(parsed.Conclusion) == 0 {
		return nil
	}
	return &ScenePlan{Surface: parsed.Surface, Conclusion: parsed.Conclusion}
}

// clampSlice trims/pads a string slice to [min,max]. Trimming caps the cost;
// padding reuses the last entry so a model that returned too few items still
// renders the configured floor count instead of falling back to defaults.
// Empty entries are dropped before clamping so a sparse model response (e.g.
// trailing nulls) doesn't burn an image-gen slot on whitespace.
func clampSlice(xs []string, min, max int) []string {
	out := xs[:0]
	for _, s := range xs {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	if len(out) > max {
		out = out[:max]
	}
	for len(out) < min {
		out = append(out, out[len(out)-1])
	}
	return out
}
