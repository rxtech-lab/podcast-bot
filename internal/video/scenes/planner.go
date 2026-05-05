package scenes

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/llm"
)

// ScenePlan is the per-puzzle decision about how many surface and conclusion
// frames to generate and what each one should depict. Returned by Plan; passed
// to Generate so it overrides the static SurfaceVariantCount /
// ConclusionVariantCount defaults. A nil plan (or an empty Surface /
// Conclusion slice) falls back to the defaults built into buildPromptVariant.
//
// SurfaceAnchors is parallel to Surface: anchors[i] is a short verbatim
// snippet from the surface text that the host will narrate at the start
// of beat i. The puzzle host's prompt uses these as a string-match
// trigger — when its narration reaches anchors[i], it emits
// "<scene i/>". This replaces the old "count paragraph breaks"
// heuristic which drifted off the planner's intended beat boundaries.
// Empty / shorter than Surface means "no anchor for that beat" — the
// host falls back to its own judgement for that one beat.
//
// Sounds lists optional pre-generated sound clips the host can trigger
// via `<sound-overlapped-N/>` (mix the clip on top of the music bed) or
// `<sound-replace-N/>` (cross-fade the bed itself to the new clip). Sounds
// are independent of the scene-image rotation: planner picks beats where
// an audio stinger or texture shift would amplify the storytelling.
// Empty / nil disables the feature; the host's prompt then omits the
// sound-marker section entirely so the LLM never emits one. N indexes
// into Sounds in declaration order.
type ScenePlan struct {
	Surface        []string         `json:"surface"`
	SurfaceAnchors []string         `json:"surface_anchors"`
	Conclusion     []string         `json:"conclusion"`
	Sounds         []SoundDirection `json:"sounds,omitempty"`
}

// SoundDirection is one entry in the puzzle's sound plan. Mode is either
// "overlap" or "replace"; the host's marker verb mirrors the mode
// (`<sound-overlapped-N/>` vs `<sound-replace-N/>`). Prompt is the Lyria
// prompt the audio generator sends to produce the clip. Anchor mirrors
// SurfaceAnchors — when non-empty the host should emit the marker
// immediately before the sentence containing the anchor; when empty the
// host falls back to its own judgement for placement.
type SoundDirection struct {
	Mode   string `json:"mode"`
	Prompt string `json:"prompt"`
	Anchor string `json:"anchor,omitempty"`
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
	maxSurfaceFrames    = 100
	minConclusionFrames = 3
	maxConclusionFrames = 6
	// maxSoundClips caps per-puzzle Lyria calls for the sound-cue feature.
	// 0 sounds is fine — most puzzles won't need them; a generous ceiling
	// keeps a chatty planner from burning dollars. Lower bound is 0
	// because the feature is optional.
	maxSoundClips = 8
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
  "surface_anchors": ["...", "...", ...],
  "conclusion": ["...", "...", ...],
  "sounds": [
    {"mode": "overlap" | "replace", "prompt": "...", "anchor": "..."}
  ]
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
- "surface_anchors" is REQUIRED and MUST be a parallel array of EXACTLY the
  same length as "surface". Entry anchors[i] is a SHORT VERBATIM SNIPPET
  (8–25 CJK characters or 4–12 English words) copied directly from the
  surface text — it is the FIRST sentence (or unmistakable opening phrase)
  of the chunk of surface narration that beat i depicts. The host will
  string-match this snippet against its narration to know exactly when to
  switch to image i. Rules for anchors:
    * Must appear VERBATIM in the surface text — do not paraphrase, do not
      summarise, do not invent text that isn't there. Copy exactly.
    * Must be UNIQUE within the surface — if a phrase repeats, pick a
      longer span that disambiguates which occurrence you mean.
    * Must be in narration order (anchor i appears after anchor i-1 in the
      surface text) and non-overlapping.
    * No leading whitespace, no quote marks, no markdown.
- "conclusion" lists frames for the quiet aftermath after the truth has been
  revealed. Same one-sentence format. The conclusion narration is composed
  fresh by the host (not lifted from a source text), so it does NOT need
  anchors.
- DO NOT mention any character's face speaking or any text/UI — these are
  global constraints downstream.
- DO NOT leak the surface story's hidden truth in any "surface" entry. The
  truth may inform the "conclusion" entries' emotional weight, but never the
  surface frames.
- Surface count: between 6 and 100 frames, scaled to the surface text's length
  and number of distinct beats (paragraph breaks, time/place shifts, new
  figures, recurring objects). A short surface gets ~6; a long multi-scene
  surface should get one frame per distinct beat — do not cap arbitrarily.
  Prefer one frame per paragraph or scene shift so the picture follows the
  voice tightly.
- Conclusion count: between 3 and 6 frames.
- Plain prose only inside each entry. No markdown, no quotes, no bullet
  prefixes, no scene numbers.
- "sounds" is OPTIONAL — return an empty array when no sound design
  beats apply. Otherwise list at most 8 entries. Each entry is one
  audio cue the host will trigger during the surface narration via a
  marker (the host emits "<sound-overlapped-N/>" or
  "<sound-replace-N/>" where N is the entry's 0-based index in this
  list):
    * "mode" is "overlap" — clip mixes additively over the running
      music bed for the duration of the clip (ambient stinger, single
      gust of wind, distant thunder, etc.). Use overlap for short
      atmospheric punctuation that should not displace the main bed.
    * "mode" is "replace" — the music bed itself cross-fades over to
      this clip. Use replace ONLY for a deliberate tonal shift at a
      key beat (e.g. the texture of the music should fundamentally
      change here for the rest of the surface). Replace clips
      should be sustained and bed-like.
    * "prompt" is a one-sentence Lyria prompt describing the sound.
      Always instrumental / textural — no lyrics, no spoken word.
      Keep prompts brief but evocative. Examples:
      "A single low cello held note swelling with a faint distant
      train whistle." / "Soft rain on tile, distant thunder, wet
      stone resonance — ambient texture, no rhythm."
    * "anchor" is OPTIONAL: a short verbatim snippet from the
      surface text (same rules as surface_anchors — copy exactly,
      unique within surface) marking where the cue should fire.
      Empty anchor = let the host place the marker by judgement.
    * Sounds should be sparse: zero is the right answer for many
      puzzles. Add a sound only when a specific moment in the
      surface clearly benefits from one — never as filler.`

	user := fmt.Sprintf(
		"Title: %s\n\nSurface (湯面):\n%s\n\nTruth (湯底, for conclusion mood only — never visualize directly):\n%s",
		topic.Title, surface, truth)

	raw, err := llmC.JSON(ctx, system, user)
	if err != nil {
		return nil, fmt.Errorf("llm json call: %w", err)
	}
	raw = unwrapJSONFences(raw)

	var parsed struct {
		Surface        []string `json:"surface"`
		SurfaceAnchors []string `json:"surface_anchors"`
		Conclusion     []string `json:"conclusion"`
		Sounds         []struct {
			Mode   string `json:"mode"`
			Prompt string `json:"prompt"`
			Anchor string `json:"anchor"`
		} `json:"sounds"`
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
	// Anchors are paired with surface entries; trim/pad to match length.
	// Padding with "" leaves that beat anchorless — host falls back to
	// its own judgement for that beat. Verifying the anchor actually
	// appears in the surface is the host's responsibility (string match
	// at narration time) rather than failing the plan here, so a stray
	// hallucinated anchor degrades gracefully rather than aborting the
	// whole puzzle.
	parsed.SurfaceAnchors = matchAnchorLength(parsed.SurfaceAnchors, len(parsed.Surface))
	sounds := make([]SoundDirection, 0, len(parsed.Sounds))
	for _, s := range parsed.Sounds {
		mode := strings.ToLower(strings.TrimSpace(s.Mode))
		prompt := strings.TrimSpace(s.Prompt)
		if prompt == "" {
			continue
		}
		if mode != "overlap" && mode != "replace" {
			continue
		}
		sounds = append(sounds, SoundDirection{
			Mode:   mode,
			Prompt: prompt,
			Anchor: strings.TrimSpace(s.Anchor),
		})
		if len(sounds) >= maxSoundClips {
			break
		}
	}
	return &ScenePlan{
		Surface:        parsed.Surface,
		SurfaceAnchors: parsed.SurfaceAnchors,
		Conclusion:     parsed.Conclusion,
		Sounds:         sounds,
	}, nil
}

// WritePlan serialises the scene plan as pretty-printed JSON at path so
// a post-mortem viewer can see exactly which beats / anchors / sound
// cues the visual director picked for this puzzle. Always writes (even
// when the heuristic fallback was used) so the operator can tell the
// LLM-driven path from the fallback by inspecting the file. nil plan or
// empty path is a no-op; a write error is returned but is not fatal —
// caller should log and continue.
func WritePlan(plan *ScenePlan, path string) error {
	if plan == nil || strings.TrimSpace(path) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir scene plan: %w", err)
	}
	body, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal scene plan: %w", err)
	}
	body = append(body, '\n')
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return fmt.Errorf("write scene plan: %w", err)
	}
	return nil
}

// matchAnchorLength trims/pads the anchor slice to exactly n entries.
// Each entry is trimmed of surrounding whitespace; longer slices are
// truncated; shorter ones are padded with empty strings.
func matchAnchorLength(anchors []string, n int) []string {
	out := make([]string, n)
	for i := 0; i < n && i < len(anchors); i++ {
		out[i] = strings.TrimSpace(anchors[i])
	}
	return out
}

// truncateForLog clips s to n runes for log lines so a megabyte of LLM
// drivel doesn't end up in the journal. Adds an ellipsis on truncation.
func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// unwrapJSONFences strips a leading "```json" / "```" code fence and a
// trailing "```" fence from raw before json.Unmarshal sees it. Some LLMs
// (notably the gateway-routed Gemini family without response_format) wrap
// their JSON answer in markdown fences regardless of explicit "no
// markdown" instructions. Stripping is a no-op when the raw bytes don't
// start with a fence.
func unwrapJSONFences(raw []byte) []byte {
	s := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(s, "```") {
		return raw
	}
	// Drop the opening fence + optional language tag through end-of-line.
	if nl := strings.IndexByte(s, '\n'); nl >= 0 {
		s = s[nl+1:]
	} else {
		s = strings.TrimPrefix(s, "```")
	}
	// Drop the closing fence if present.
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "```")
	return []byte(strings.TrimSpace(s))
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
	surfaceAnchors := make([]string, 0, len(chunks))
	for i, c := range chunks {
		surfaceDirs = append(surfaceDirs, fallbackSurfaceDirection(i, c))
		surfaceAnchors = append(surfaceAnchors, fallbackAnchor(c))
	}

	conclusionDirs := make([]string, 0, minConclusionFrames)
	for i := 0; i < minConclusionFrames; i++ {
		conclusionDirs = append(conclusionDirs,
			conclusionVariantDirections[i%len(conclusionVariantDirections)])
	}

	clampedSurface := clampSlice(surfaceDirs, minSurfaceFrames, maxSurfaceFrames)
	return &ScenePlan{
		Surface:        clampedSurface,
		SurfaceAnchors: matchAnchorLength(surfaceAnchors, len(clampedSurface)),
		Conclusion:     clampSlice(conclusionDirs, minConclusionFrames, maxConclusionFrames),
	}
}

// fallbackAnchor extracts a short verbatim anchor (the first ~20 chars of
// chunk c, trimmed at the first sentence terminator) for use as a string-
// match trigger when no LLM-supplied anchor is available. Empty string for
// trivially short chunks — the host falls back to its own judgement.
func fallbackAnchor(c string) string {
	c = strings.TrimSpace(c)
	if c == "" {
		return ""
	}
	const maxRunes = 20
	runes := []rune(c)
	if len(runes) > maxRunes {
		runes = runes[:maxRunes]
	}
	return strings.TrimSpace(string(runes))
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
