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
// downstream prompt builder folds into the per-variant image prompt. The
// surface entries MUST be in narration order (paragraph by paragraph) so
// the on-screen image advances with the storytelling.
//
// Returns (nil, error) on any failure so the caller can log the reason
// and decide between FallbackPlan (heuristic story-order split) and the
// static composition cycle baked into buildPromptVariant.
func Plan(ctx context.Context, llmC *llm.Client, topic *config.DebateTopic) (*ScenePlan, error) {
	if llmC == nil {
		return nil, fmt.Errorf("nil llm client")
	}
	if topic == nil {
		return nil, fmt.Errorf("nil topic")
	}
	surface := strings.TrimSpace(topic.Surface)
	truth := strings.TrimSpace(topic.Truth)
	if surface == "" {
		return nil, fmt.Errorf("empty surface text")
	}

	system := `You are a visual director planning the cut sequence for a 海龜湯
(situation puzzle) podcast. The host narrates the surface story slowly and
contemplatively over a music bed; behind the voice we cross-fade between
hand-drawn anime cinematic illustrations (Makoto Shinkai / Studio Ghibli /
Kyoto Animation idiom). Your job is to plan the per-frame visual beats so
imagery follows the storytelling.

Output strict JSON with this shape:
{
  "surface": ["...", "...", ...],
  "conclusion": ["...", "...", ...]
}

Rules:
- "surface" lists frames cut in during the surface narration. Entries MUST
  appear in the same order as the surface narration — entry i depicts the
  visual beat for the i-th paragraph or scene chunk of the surface text.
  Walk the surface paragraph by paragraph and produce one entry for each
  distinct visual beat in the order it appears. Do NOT reorder entries to
  group similar framings; do NOT shuffle for variety. Variety comes from
  varying framing within the existing narrative order, never by reshuffling.
- Each entry is ONE short sentence (≤ 30 English words or ≤ 60 CJK
  characters) describing what the camera shows and the framing/composition
  choice — for example "Wide cinematic establishing shot of an empty diner
  at dusk, neon sign buzzing." or "Close detail on a bowl of soup, steam
  curling under warm lamplight." A secondary constraint: vary the framing
  across consecutive entries (wide / mid / close / pure environment /
  silhouette / object detail) so the cuts feel like a documentary edit, but
  never at the cost of order.
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
  surface gets 10–14. Prefer the higher end for long multi-paragraph
  narrations so each paragraph has its own image.
- Conclusion count: between 3 and 6 frames.
- Plain prose only inside each entry. No markdown, no quotes, no bullet
  prefixes, no scene numbers.`

	user := fmt.Sprintf(
		"Title: %s\n\nSurface (湯面):\n%s\n\nTruth (湯底, for conclusion mood only — never visualize directly):\n%s",
		topic.Title, surface, truth)

	raw, err := llmC.JSON(ctx, system, user)
	if err != nil {
		return nil, fmt.Errorf("llm json call: %w", err)
	}

	var parsed struct {
		Surface    []string `json:"surface"`
		Conclusion []string `json:"conclusion"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("unmarshal plan: %w (raw=%q)", err, truncateForLog(string(raw), 200))
	}
	parsed.Surface = clampSlice(parsed.Surface, minSurfaceFrames, maxSurfaceFrames)
	parsed.Conclusion = clampSlice(parsed.Conclusion, minConclusionFrames, maxConclusionFrames)
	if len(parsed.Surface) == 0 || len(parsed.Conclusion) == 0 {
		return nil, fmt.Errorf("plan empty after clamp (surface=%d, conclusion=%d)",
			len(parsed.Surface), len(parsed.Conclusion))
	}
	return &ScenePlan{Surface: parsed.Surface, Conclusion: parsed.Conclusion}, nil
}

// truncateForLog clips s to n runes for log lines so a megabyte of LLM
// drivel doesn't end up in the journal. Adds an ellipsis on truncation.
func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// FallbackPlan builds a deterministic story-ordered ScenePlan from the
// topic's surface text by splitting on paragraph breaks first and then on
// CJK / Latin sentence terminators until at least minSurfaceFrames chunks
// are produced. Each entry uses the chunk's first ~60 chars as a short
// prose direction the image-gen prompt can lean on. Conclusion entries
// reuse the static conclusionVariantDirections cycle so the conclusion
// path doesn't get its own ad-hoc heuristic.
//
// Used as a guaranteed-available fallback when scenes.Plan() fails (LLM
// outage, JSON parse error, etc.). Always returns a non-nil plan when
// the topic has any surface text — only returns nil for an empty surface.
func FallbackPlan(topic *config.DebateTopic) *ScenePlan {
	if topic == nil {
		return nil
	}
	surface := strings.TrimSpace(topic.Surface)
	if surface == "" {
		return nil
	}

	chunks := splitSurfaceIntoChunks(surface, minSurfaceFrames, maxSurfaceFrames)
	if len(chunks) == 0 {
		return nil
	}

	surfaceDirs := make([]string, 0, len(chunks))
	for i, c := range chunks {
		surfaceDirs = append(surfaceDirs, fallbackSurfaceDirection(i, c))
	}

	conclusionDirs := make([]string, 0, minConclusionFrames)
	for i := 0; i < minConclusionFrames; i++ {
		conclusionDirs = append(conclusionDirs,
			conclusionVariantDirections[i%len(conclusionVariantDirections)])
	}

	return &ScenePlan{
		Surface:    clampSlice(surfaceDirs, minSurfaceFrames, maxSurfaceFrames),
		Conclusion: clampSlice(conclusionDirs, minConclusionFrames, maxConclusionFrames),
	}
}

// splitSurfaceIntoChunks splits s into between min and max story-ordered
// chunks. Tries paragraph breaks (`\n\n`) first; if that yields too few
// pieces, splits the longest chunks on CJK sentence terminators (。 —— ……
// ; ?) until either min is reached or no chunk is splittable. Result is in
// document order — never reshuffled.
func splitSurfaceIntoChunks(s string, min, max int) []string {
	parts := splitNonEmpty(s, "\n\n")
	if len(parts) == 0 {
		// Some surface texts use single newlines between paragraphs.
		parts = splitNonEmpty(s, "\n")
	}
	if len(parts) == 0 {
		parts = []string{s}
	}

	terminators := []string{"。", "——", "……", "！", "？", ". ", "! ", "? "}

	for len(parts) < min {
		// Find the longest chunk; split it on the first terminator that
		// produces 2+ non-empty pieces. If none works, give up — pad in
		// clampSlice instead.
		idx := longestIndex(parts)
		if idx < 0 {
			break
		}
		split := splitOnFirstTerminator(parts[idx], terminators)
		if len(split) < 2 {
			break
		}
		next := make([]string, 0, len(parts)+len(split)-1)
		next = append(next, parts[:idx]...)
		next = append(next, split...)
		next = append(next, parts[idx+1:]...)
		parts = next
	}

	if len(parts) > max {
		parts = parts[:max]
	}
	return parts
}

// splitNonEmpty is strings.Split filtered to non-blank trimmed pieces.
func splitNonEmpty(s, sep string) []string {
	raw := strings.Split(s, sep)
	out := raw[:0]
	for _, p := range raw {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// longestIndex returns the index of the longest entry, or -1 for empty.
func longestIndex(xs []string) int {
	if len(xs) == 0 {
		return -1
	}
	best := 0
	for i := 1; i < len(xs); i++ {
		if len(xs[i]) > len(xs[best]) {
			best = i
		}
	}
	return best
}

// splitOnFirstTerminator splits s on the first terminator present, keeping
// the terminator with the preceding chunk so the prose remains readable.
// Returns one-element slice if no terminator is found.
func splitOnFirstTerminator(s string, terminators []string) []string {
	type hit struct {
		idx int
		t   string
	}
	first := hit{idx: -1}
	for _, t := range terminators {
		if i := strings.Index(s, t); i >= 0 {
			// Skip if the terminator lands at the very start or end —
			// splitting there produces an empty piece.
			if i == 0 || i+len(t) >= len(s) {
				continue
			}
			if first.idx < 0 || i < first.idx {
				first = hit{idx: i, t: t}
			}
		}
	}
	if first.idx < 0 {
		return []string{strings.TrimSpace(s)}
	}
	left := strings.TrimSpace(s[:first.idx+len(first.t)])
	right := strings.TrimSpace(s[first.idx+len(first.t):])
	if left == "" || right == "" {
		return []string{strings.TrimSpace(s)}
	}
	return []string{left, right}
}

// fallbackSurfaceDirection composes a short prose direction for chunk i
// using the chunk's leading ~60 chars. The composition cycle
// (wide/close/mid/pure) rotates underneath so consecutive frames don't
// repeat framing.
func fallbackSurfaceDirection(i int, chunk string) string {
	const lead = 60
	leadText := chunk
	runes := []rune(chunk)
	if len(runes) > lead {
		leadText = string(runes[:lead]) + "…"
	}
	framing := surfaceVariantDirections[i%len(surfaceVariantDirections)]
	return fmt.Sprintf("Visual beat #%d, in narration order, depicting: %s. %s",
		i+1, leadText, framing)
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
