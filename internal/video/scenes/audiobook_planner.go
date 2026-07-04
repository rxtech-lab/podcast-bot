package scenes

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/llm"
)

// Bounds for the audiobook illustration plan. Audiobooks follow the series
// density (~2 frames per configured minute) but cap lower — an audiobook is
// a narrated reading, not a cut-heavy episode, and each frame costs a Gemini
// generation + an S3 upload.
const (
	minAudioBookFrames = 6
	maxAudioBookFrames = 40
)

func audioBookFrameFloor(totalMinutes int) int {
	if totalMinutes <= 0 {
		return minAudioBookFrames
	}
	n := totalMinutes * 2
	if n < minAudioBookFrames {
		return minAudioBookFrames
	}
	if n > maxAudioBookFrames {
		return maxAudioBookFrames
	}
	return n
}

// AudioBookScenePlan is the per-audiobook illustration plan: one entry per
// visual beat, in narration order. All slices are parallel and equal length.
// BeatChapters[i] is the 0-based chapter index beat i belongs to, so the
// image prompt can carry the right chapter context and the host prompt can
// pin chapter-opening beats to their spoken titles.
type AudioBookScenePlan struct {
	Narration           []string `json:"narration"`
	NarrationAnchors    []string `json:"narration_anchors"`
	NarrationAnimations []string `json:"narration_animations"`
	BeatChapters        []int    `json:"beat_chapters"`
}

// AudioBookOutlineText returns the planning outline for an audiobook topic:
// the ## Surface chapter outline when present, otherwise a Markdown outline
// rebuilt from the overall summary + chapter list (mirrors the orchestrator's
// audioBookOutline).
func AudioBookOutlineText(t *config.DebateTopic) string {
	if t == nil {
		return ""
	}
	if outline := strings.TrimSpace(t.Surface); outline != "" {
		return outline
	}
	var b strings.Builder
	if summary := strings.TrimSpace(t.Background); summary != "" {
		b.WriteString("# Overall Summary\n\n")
		b.WriteString(summary)
		b.WriteString("\n\n")
	}
	for i, ch := range t.AudioBookChapters {
		fmt.Fprintf(&b, "## Chapter %d: %s", i+1, strings.TrimSpace(ch.Title))
		b.WriteString("\n\n")
		b.WriteString(strings.TrimSpace(ch.Summary))
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}

// PlanAudioBookScenes asks the LLM to plan an audiobook's illustration beats
// — the dense per-scene image list the host walks with `<scene N/>` markers
// and the video post-pass animates with Ken Burns camera moves.
//
// Returns (nil, error) on any LLM / JSON failure so the caller can fall back
// to FallbackAudioBookScenePlan.
func PlanAudioBookScenes(ctx context.Context, llmC *llm.Client, topic *config.DebateTopic,
) (*AudioBookScenePlan, error) {
	if llmC == nil {
		return nil, fmt.Errorf("nil llm client")
	}
	if topic == nil {
		return nil, fmt.Errorf("nil topic")
	}
	outline := AudioBookOutlineText(topic)
	if outline == "" {
		return nil, fmt.Errorf("empty audiobook outline")
	}
	chapters := topic.AudioBookChapters
	if len(chapters) == 0 {
		return nil, fmt.Errorf("no audiobook chapters")
	}
	minFrames := audioBookFrameFloor(topic.TotalMinutes)

	system := `You are the visual director planning the illustration sequence
for a narrated audiobook video. The narrator reads the book chapter by
chapter over a music bed; behind the voice we cut between animated-feature-
film illustrations with slow camera moves (pan / zoom). Your job is to plan
the per-frame visual beats so the imagery follows the storytelling.

Output strict JSON with this shape:
{
  "narration": ["...", "...", ...],
  "narration_anchors": ["...", "...", ...],
  "narration_animations": ["stall" | "panleft" | "panright" | "pantop" | "panbottom" | "zoomin" | "zoomout", ...],
  "narration_chapters": [0, 0, 1, ...]
}

Rules:
- "narration" lists the illustration beats in narration order. Walk the
  chapters strictly in sequence and produce one entry for each distinct
  visual beat — a location change, an action, an object reveal, an
  emotional turn, a new concept. Do NOT reorder, do NOT shuffle for
  variety. Each entry is ONE short sentence (≤ 30 English words or ≤ 60
  CJK characters) describing what the camera shows.
- "narration_anchors" is REQUIRED, parallel to narration, same length:
  short verbatim snippets (8–25 CJK characters or 4–12 English words)
  copied directly from the outline text marking the START of beat i.
  Anchors must appear verbatim in the outline, must be unique within it,
  and must be in narration order. The FIRST beat of each chapter MUST use
  that chapter's exact title as its anchor. Use "" when no good verbatim
  snippet exists for a mid-chapter beat.
- "narration_animations" is REQUIRED, parallel to narration, same length.
  Allowed values:
    "stall", "panleft", "panright", "pantop", "panbottom", "zoomin", "zoomout".
  Vary the moves; avoid the same move twice in a row.
- "narration_chapters" is REQUIRED, parallel to narration, same length:
  the 0-based chapter index each beat belongs to. Values must be
  non-decreasing and every chapter must get at least one beat.

Narration count: between %d and %d frames. This audiobook is configured
for %d minute(s), so produce AT LEAST %d narration frames unless the
outline is physically too short to support that many distinct beats. A
15-minute audiobook should normally have about 25-30 frames, not 6.`

	system = fmt.Sprintf(system, minFrames, maxAudioBookFrames, topic.TotalMinutes, minFrames)

	var chapterList strings.Builder
	for i, ch := range chapters {
		fmt.Fprintf(&chapterList, "  %d: %s\n", i, strings.TrimSpace(ch.Title))
	}
	user := fmt.Sprintf("Audiobook: %s\n\nChapters (0-based index: title):\n%s\nOutline:\n%s",
		topic.Title, chapterList.String(), outline)

	raw, err := llmC.JSON(ctx, system, user)
	if err != nil {
		return nil, fmt.Errorf("llm json call: %w", err)
	}
	raw = unwrapJSONFences(raw)

	var parsed struct {
		Narration           []string `json:"narration"`
		NarrationAnchors    []string `json:"narration_anchors"`
		NarrationAnimations []string `json:"narration_animations"`
		NarrationChapters   []int    `json:"narration_chapters"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("unmarshal audiobook plan: %w (raw=%q)", err, truncateForLog(string(raw), 200))
	}
	if nonEmptyStringCount(parsed.Narration) == 0 {
		return nil, fmt.Errorf("audiobook plan has no narration beats")
	}
	narration := clampSlice(parsed.Narration, minFrames, maxAudioBookFrames)
	if len(narration) == 0 {
		return nil, fmt.Errorf("audiobook plan empty after clamp")
	}
	return &AudioBookScenePlan{
		Narration:           narration,
		NarrationAnchors:    matchAnchorLength(parsed.NarrationAnchors, len(narration)),
		NarrationAnimations: normaliseAnimations(parsed.NarrationAnimations, len(narration)),
		BeatChapters:        normaliseBeatChapters(parsed.NarrationChapters, len(narration), len(chapters)),
	}, nil
}

// FallbackAudioBookScenePlan builds a deterministic beat plan by splitting
// the outline into chunks, mirroring FallbackSeriesPlan. Used when
// PlanAudioBookScenes fails (LLM outage / JSON parse error / no creds).
func FallbackAudioBookScenePlan(topic *config.DebateTopic) *AudioBookScenePlan {
	if topic == nil || len(topic.AudioBookChapters) == 0 {
		return nil
	}
	outline := AudioBookOutlineText(topic)
	if outline == "" {
		return nil
	}
	minFrames := audioBookFrameFloor(topic.TotalMinutes)
	chunks := splitSurfaceIntoChunks(outline, minFrames, maxAudioBookFrames)
	if len(chunks) == 0 {
		return nil
	}
	dirs := make([]string, 0, len(chunks))
	anchors := make([]string, 0, len(chunks))
	for i, c := range chunks {
		dirs = append(dirs, fallbackSurfaceDirection(i, c))
		anchors = append(anchors, fallbackAnchor(c))
	}
	clamped := clampSlice(dirs, minFrames, maxAudioBookFrames)
	return &AudioBookScenePlan{
		Narration:           clamped,
		NarrationAnchors:    matchAnchorLength(anchors, len(clamped)),
		NarrationAnimations: fallbackAnimations(len(clamped)),
		BeatChapters:        normaliseBeatChapters(nil, len(clamped), len(topic.AudioBookChapters)),
	}
}

// normaliseBeatChapters trims / pads the per-beat chapter indices to exactly
// n entries, clamps each into [0, chapterCount), and forces the sequence
// non-decreasing so a planner glitch can't send beats backwards across
// chapters. A nil / short input is filled by spreading beats evenly across
// the chapters in order.
func normaliseBeatChapters(chaptersIdx []int, n, chapterCount int) []int {
	out := make([]int, n)
	if chapterCount <= 0 {
		return out
	}
	filled := 0
	for i := 0; i < n && i < len(chaptersIdx); i++ {
		v := chaptersIdx[i]
		if v < 0 {
			v = 0
		}
		if v >= chapterCount {
			v = chapterCount - 1
		}
		if i > 0 && v < out[i-1] {
			v = out[i-1]
		}
		out[i] = v
		filled = i + 1
	}
	for i := filled; i < n; i++ {
		// Even spread for the unfilled tail (and the fully-nil case).
		v := i * chapterCount / n
		if v >= chapterCount {
			v = chapterCount - 1
		}
		if i > 0 && v < out[i-1] {
			v = out[i-1]
		}
		out[i] = v
	}
	return out
}
