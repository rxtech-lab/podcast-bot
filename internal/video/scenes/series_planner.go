package scenes

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/llm"
)

// Bounds for the series narration plan. Mirrors the puzzle Surface bounds
// (the intuition is the same — episode synopses range from 6-paragraph
// pilots to dense multi-act stories that benefit from one frame per
// distinct beat).
const (
	minNarrationFrames = 6
	maxNarrationFrames = 60
)

// SeriesImageRefCandidate is one entry in the cross-episode reuse catalog
// supplied to PlanSeries. The planner returns a subset of these (by Key)
// in the resulting ScenePlan.ImageReuse slice — entries the LLM picked as
// genuinely reusable for this episode's beats.
type SeriesImageRefCandidate struct {
	Key         string // canonical key, e.g. "s1e3i7"
	Season      int
	Episode     int
	Beat        int
	Description string // the prior plan's per-beat direction text
}

// PlanSeries asks the LLM to plan a TV-series episode's narration beats.
// candidates is the list of reuse-eligible prior-episode images (may be nil
// for episode 1 / when no prior plans were found); the planner is told it
// MAY reference those keys in `image_reuse[i]` to indicate that beat i
// should re-use that archived frame instead of generating a fresh one.
//
// Returns (nil, error) on any LLM / JSON failure so the caller can fall
// back to FallbackSeriesPlan (heuristic split of the synopsis).
func PlanSeries(ctx context.Context, llmC *llm.Client, topic *config.DebateTopic,
	candidates []SeriesImageRefCandidate,
) (*ScenePlan, error) {
	if llmC == nil {
		return nil, fmt.Errorf("nil llm client")
	}
	if topic == nil {
		return nil, fmt.Errorf("nil topic")
	}
	synopsis := strings.TrimSpace(topic.Surface)
	if synopsis == "" {
		return nil, fmt.Errorf("empty synopsis (## Surface section)")
	}

	system := `You are the visual director planning the cut sequence for a
TV-series-style narrated podcast episode. The host narrates the synopsis
slowly and contemplatively over a music bed; behind the voice we cross-fade
between hand-drawn anime cinematic illustrations (Makoto Shinkai / Studio
Ghibli / Kyoto Animation idiom). Your job is to plan the per-frame visual
beats so imagery follows the storytelling and, when appropriate, re-uses
canonical imagery from earlier episodes of the same show.

Output strict JSON with this shape:
{
  "narration": ["...", "...", ...],
  "narration_anchors": ["...", "...", ...],
  "narration_animations": ["stall" | "panleft" | "panright" | "pantop" | "panbottom" | "zoomin" | "zoomout", ...],
  "image_reuse": ["", "s1e3i7", "", ...],
  "sounds": [
    {"mode": "overlap" | "replace", "prompt": "...", "anchor": "...", "duration_seconds": 0}
  ]
}

Rules:
- "narration" lists the frames cut in during the host's reading. Entries
  MUST appear in the same order as the synopsis — entry i depicts the
  visual beat for the i-th paragraph or scene chunk. Walk the synopsis
  paragraph by paragraph and produce one entry for each distinct visual
  beat in the order it appears. Do NOT reorder, do NOT shuffle for
  variety.
- Each entry is ONE short sentence (≤ 30 English words or ≤ 60 CJK
  characters) describing what the camera shows.
- "narration_anchors" is REQUIRED, parallel to narration, same length:
  short verbatim snippets (8–25 CJK characters or 4–12 English words)
  copied directly from the synopsis text marking the START of beat i.
  Same anchor rules as the puzzle plan: must appear verbatim, must be
  unique within the synopsis, must be in narration order.
- "narration_animations" is REQUIRED, parallel to narration, same
  length. Allowed values:
    "stall", "panleft", "panright", "pantop", "panbottom", "zoomin", "zoomout".
- "image_reuse" is OPTIONAL. When non-empty it MUST be parallel to
  narration (same length); each entry is either an empty string OR a
  reuse key drawn EXACTLY from the catalog of available archived
  images (see below). A non-empty entry at index i means: "for beat
  i, re-use the archived image identified by this key instead of
  generating a fresh one". Use reuse SPARINGLY — only when the beat
  clearly continues a recurring location or character. NEVER set
  image_reuse[0] to a non-empty key (the show always opens on a
  freshly-generated image).
- "sounds" follows the same shape as the puzzle planner's sound list.

Narration count: between %d and %d frames, scaled to synopsis length and
distinct beats. Prefer one frame per paragraph or scene shift.`

	system = fmt.Sprintf(system, minNarrationFrames, maxNarrationFrames)

	var catalog string
	if len(candidates) > 0 {
		var sb strings.Builder
		sb.WriteString("\n\nAvailable archived imagery (you MAY reference these keys via image_reuse[i] — only these keys are valid):\n")
		for _, c := range candidates {
			fmt.Fprintf(&sb, "  %s (season %d, episode %d, beat %d): %s\n",
				c.Key, c.Season, c.Episode, c.Beat, strings.TrimSpace(c.Description))
		}
		catalog = sb.String()
	}

	user := fmt.Sprintf(
		"Show: %s\nSeason: %d\nEpisode: %d\nTitle: %s\n\nSynopsis:\n%s%s",
		topic.Show, topic.Season, topic.Episode, topic.Title, synopsis, catalog)

	raw, err := llmC.JSON(ctx, system, user)
	if err != nil {
		return nil, fmt.Errorf("llm json call: %w", err)
	}
	raw = unwrapJSONFences(raw)

	var parsed struct {
		Narration           []string `json:"narration"`
		NarrationAnchors    []string `json:"narration_anchors"`
		NarrationAnimations []string `json:"narration_animations"`
		ImageReuse          []string `json:"image_reuse"`
		Sounds              []struct {
			Mode            string `json:"mode"`
			Prompt          string `json:"prompt"`
			Anchor          string `json:"anchor"`
			DurationSeconds int    `json:"duration_seconds"`
		} `json:"sounds"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("unmarshal series plan: %w (raw=%q)", err, truncateForLog(string(raw), 200))
	}
	parsed.Narration = clampSlice(parsed.Narration, minNarrationFrames, maxNarrationFrames)
	if len(parsed.Narration) == 0 {
		return nil, fmt.Errorf("series plan empty after clamp")
	}
	parsed.NarrationAnchors = matchAnchorLength(parsed.NarrationAnchors, len(parsed.Narration))
	parsed.NarrationAnimations = normaliseAnimations(parsed.NarrationAnimations, len(parsed.Narration))

	// Filter image_reuse to known catalog keys; clamp length to narration.
	candidateSet := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		candidateSet[c.Key] = true
	}
	imageReuse := make([]string, len(parsed.Narration))
	for i := 0; i < len(parsed.Narration) && i < len(parsed.ImageReuse); i++ {
		key := strings.TrimSpace(parsed.ImageReuse[i])
		if key == "" || !candidateSet[key] {
			continue
		}
		imageReuse[i] = key
	}
	// Hard rule: beat 0 always paints a freshly-generated image so the
	// show opens on novel visuals. Drop any reuse the planner proposed
	// for slot 0.
	if len(imageReuse) > 0 {
		imageReuse[0] = ""
	}

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
		dur := s.DurationSeconds
		if dur < 0 {
			dur = 0
		}
		sounds = append(sounds, SoundDirection{
			Mode:            mode,
			Prompt:          prompt,
			Anchor:          strings.TrimSpace(s.Anchor),
			DurationSeconds: dur,
		})
		if len(sounds) >= maxSoundClips {
			break
		}
	}

	return &ScenePlan{
		Narration:           parsed.Narration,
		NarrationAnchors:    parsed.NarrationAnchors,
		NarrationAnimations: parsed.NarrationAnimations,
		ImageReuse:          imageReuse,
		Sounds:              sounds,
	}, nil
}

// FallbackSeriesPlan builds a deterministic story-ordered narration plan
// from the topic's synopsis (## Surface) using the same paragraph /
// terminator splitter as the puzzle FallbackPlan. Used when PlanSeries
// fails (LLM outage / JSON parse error).
func FallbackSeriesPlan(topic *config.DebateTopic) *ScenePlan {
	if topic == nil {
		return nil
	}
	synopsis := strings.TrimSpace(topic.Surface)
	if synopsis == "" {
		return nil
	}
	chunks := splitSurfaceIntoChunks(synopsis, minNarrationFrames, maxNarrationFrames)
	if len(chunks) == 0 {
		return nil
	}
	dirs := make([]string, 0, len(chunks))
	anchors := make([]string, 0, len(chunks))
	for i, c := range chunks {
		dirs = append(dirs, fallbackSurfaceDirection(i, c))
		anchors = append(anchors, fallbackAnchor(c))
	}
	clamped := clampSlice(dirs, minNarrationFrames, maxNarrationFrames)
	return &ScenePlan{
		Narration:           clamped,
		NarrationAnchors:    matchAnchorLength(anchors, len(clamped)),
		NarrationAnimations: fallbackAnimations(len(clamped)),
		ImageReuse:          make([]string, len(clamped)),
	}
}
