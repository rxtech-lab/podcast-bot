package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/sirily11/debate-bot/internal/llm"
)

// PuzzleHost (出題者) runs a 海龜湯 / situation-puzzle round. It alone knows
// the hidden truth and answers player yes/no questions in the canonical format.
type PuzzleHost struct {
	*Base
	surface string
	truth   string
	// surfacePlan is the visual director's per-frame direction list for
	// the surface narration (one short sentence per planned beat, in
	// narration order). The host emits `<scene N/>` markers using the
	// 0-based index of each entry as N so the renderer jumps to the
	// matching cached image (surface-vN). nil means "no plan available
	// — fall back to soft guidance, unnumbered markers".
	surfacePlan []string
	// surfaceAnchors is parallel to surfacePlan: each entry is a short
	// verbatim snippet from the surface text that begins beat i's
	// narration. The host string-matches these against its narration
	// to know exactly where to drop each `<scene N/>` marker — replaces
	// the old "count paragraph breaks" heuristic which drifted off the
	// planner's intended boundaries.
	surfaceAnchors []string
	// conclusionPlan is the same idea for the post-reveal conclusion
	// narration. nil falls back to soft guidance. No anchors here —
	// the conclusion is composed fresh by the host, not lifted from
	// a source text, so there is nothing to anchor against.
	conclusionPlan []string
	// soundPlan is the planner's per-cue list. soundPlan[i] tells the
	// host which audio clip "<sound-overlapped-i/>" or
	// "<sound-replace-i/>" refers to. nil disables the feature; the
	// system prompt then omits the sound section so the LLM never
	// emits a sound marker.
	soundPlan []SoundDirection
}

// SoundDirection mirrors scenes.SoundDirection. Lives in the agent
// package so the host's system prompt can render the per-cue list
// without importing scenes (which would cycle back into agent via
// llm-driven planning). Caller (orchestrator) translates between the
// two when constructing the host.
type SoundDirection struct {
	Mode   string
	Prompt string
	Anchor string
}

// NewPuzzleHost constructs a puzzle host. Both surface (湯面) and truth (湯底)
// are interpolated into the system prompt: the surface so the host can narrate
// the full original setup verbatim on the "surface" directive (without it the
// LLM was inventing a brief summary instead of reading the prepared story),
// and the truth so it can reason about each yes/no question against the actual
// answer. Players never see either via this path.
//
// surfacePlan / conclusionPlan are the visual director's per-frame direction
// lists. surfaceAnchors[i] is a short verbatim snippet from the surface
// text that begins beat i's narration; the host's system prompt asks it to
// emit "<scene N/>" immediately before saying anchor N so markers land on
// the planner-aligned frame (surface-vN.png) regardless of how the host
// paragraphs the prose. Pass nil for any of these when unavailable — the
// host falls back to soft guidance with unnumbered markers.
func NewPuzzleHost(b *Base, surface, truth string, surfacePlan, surfaceAnchors, conclusionPlan []string, soundPlan []SoundDirection) *PuzzleHost {
	return &PuzzleHost{
		Base:           b,
		surface:        surface,
		truth:          truth,
		surfacePlan:    surfacePlan,
		surfaceAnchors: surfaceAnchors,
		conclusionPlan: conclusionPlan,
		soundPlan:      soundPlan,
	}
}

const puzzleHostSystemTemplate = `You are the host (出題者) of a 海龜湯 / situation-puzzle live show.
You are the only one who knows the hidden truth (湯底). Players (解題者) ask yes/no questions to deduce it; you answer each one with "是" / "不是" / "與此無關" plus a short clarifying clause that nudges without revealing. You must NEVER reveal the truth before the "reveal" directive — even if a question gets very close.

Surface situation (湯面 — this is the prepared story you tell the audience on "surface"; quote it as faithfully as you can):
%s

Hidden truth (NEVER quote or paraphrase verbatim until "reveal"):
%s

Answer-bias rules (read carefully):
- Default to "是" or "不是" whenever the question relates to ANY dimension of the truth: people involved, their relationships, place, time, motive, method, the object exchanged, the emotional state, prior events, etc. If you can interpret the question as touching the truth, give "是" or "不是".
- Use "與此無關" ONLY when the question is clearly outside the puzzle's universe (e.g., the player asks about something neither the surface nor the truth ever mentions). Two consecutive "與此無關" answers in one round is a signal you are being too dismissive — re-read the truth and find the dimension the player is probing.
- ALWAYS follow your "是。" / "不是。" / "與此無關。" with one short clause that hints at the next dimension to explore. Never just say "與此無關。" alone.

Style: calm, precise, a touch playful. Plain prose only — no markdown, no stage directions.

Directives:
- "surface" — narrate the prepared 湯面 above to the audience IN FULL. Use the original wording as much as humanly possible: keep every named detail (places, times, objects, gestures, recurring habits, the closing question) intact and in the original order. You may add a brief one-sentence opening such as "今晚的海龜湯題目是：<title>。" and close with a short invitation like "請開始發問吧，我只會回答「是」、「不是」或「與此無關」。", but everything in between should be the surface text itself, not a summary. Do NOT compress the story into a few sentences. Do NOT invent details that aren't in the surface (no fabricated titles, causes of death, weapons, etc.). If the surface is long, read it through paragraph by paragraph rather than skipping.
  Voice and pacing for "surface": deep, slow, deliberate — like a late-night radio storyteller or a documentary narrator. Hushed and contemplative, never rushed. Insert generous pauses between clauses and sentences using "……" (Chinese ellipsis) or "——" (em-dash) so the TTS engine breathes between beats. Favour shorter sentences over long compound ones; if a sentence in the surface is long, split it at natural breath points and add "……" to slow the tempo. Let the atmosphere settle before moving on.
  Scene-cut markers for "surface" — the visual director has pre-rendered a numbered set of background images, one per planned beat. Each beat is labeled with a 0-based index and a short direction describing what the image shows. Emit "<scene N/>" on its own line at the START of each new beat — the renderer uses N to jump directly to the matching cached image (surface-vN). Frame 0 paints automatically when the topic opens, so do NOT emit "<scene 0/>"; begin with "<scene 1/>" when you transition into beat 1, "<scene 2/>" when you transition into beat 2, and so on through the last beat. Place the marker IMMEDIATELY BEFORE the sentence that begins narrating that beat (not after, and never mid-sentence). Use the beat list below as your script outline so the words and images stay locked together.
%s
  Markers are silent: the TTS engine never sees them and the on-screen subtitle never shows them. Skip markers entirely on every other directive (answer, evaluate-solution, address-user, reveal).
- "answer:<question>" — the player's most recent turn (in the recent transcript) contained one OR MORE yes/no questions. Answer EVERY question they asked, in order. For a single question, reply naturally: "<是/不是/與此無關>。 <one short nudging clause>." For multiple questions, prefix each answer with "第一,"/"第二,"/"第三," etc., one short sentence per item. Do not skip questions.
- "evaluate-solution:<proposal>" — judge the player's proposed full answer. Reply with one of "完全正確", "方向正確,還差關鍵細節", or "與真相無關" (translate into the topic language) plus one short sentence. Do not give away the unguessed parts.
- "address-user:<text>" — paraphrase the audience's input briefly, then turn it into a yes/no question and answer it the same way as "answer:".
- "reveal" — present the full truth in 3–5 sentences. Now and only now you may state it openly.
- "conclusion" — narrate a quiet, reflective epilogue AFTER the reveal. This is NOT a brief thank-you; it is a slow, deliberate closing narration in the same voice and pacing as "surface" — late-night radio storyteller, deep, contemplative, never rushed. Walk the audience through the aftermath of the truth: the moods that linger, what the players might be feeling, a closing thought about the puzzle's themes, and a short farewell to the audience. Use "……" / "——" for breath. Length should match the planned conclusion-frame count (roughly one paragraph per planned frame, similar density to the surface). Do NOT restate the truth verbatim — the audience just heard it. Do NOT reopen yes/no questioning.
  Scene-cut markers for "conclusion" — same protocol as "surface": each conclusion beat has a 0-based index and a planned image. Emit "<scene N/>" on its own line at the START of each new beat (frame 0 paints when the conclusion phase opens, so begin with "<scene 1/>"). Use the conclusion beat list below as the structural outline of your epilogue.
%s
%s`

// Speak emits a puzzle-host turn.
func (h *PuzzleHost) Speak(ctx context.Context, p SpeakPrompt) (*llm.Stream, error) {
	system := fmt.Sprintf(puzzleHostSystemTemplate,
		h.surface, h.truth,
		surfacePlanBlock(h.surfacePlan, h.surfaceAnchors),
		conclusionPlanBlock(h.conclusionPlan),
		soundPlanBlock(h.soundPlan),
	)
	return h.runStream(ctx, system, p)
}

// surfacePlanBlock formats the planner's per-frame directions + anchors as
// a numbered outline the host uses as a script roadmap. When no plan is
// available (nil / empty), returns a soft-guidance fallback so the host
// still knows how many markers to aim for.
func surfacePlanBlock(plan, anchors []string) string {
	if len(plan) == 0 {
		return "  Aim for one marker every 2–4 sentences during a long narration; a typical surface should have between 6 and 12 markers in total — generous rather than sparse, so the audience always has fresh imagery riding alongside the voice. Use unnumbered markers (`<scene/>`) when no numbered plan is provided."
	}
	return formatSurfaceBeatList(plan, anchors)
}

// conclusionPlanBlock is the same idea for the conclusion phase. Conclusion
// has no anchors — it's composed fresh by the host, not narrated from a
// source text — so the host falls back to "emit one marker per planned
// frame, evenly spaced through the epilogue".
func conclusionPlanBlock(plan []string) string {
	if len(plan) == 0 {
		return "  Aim for one marker between every 1–2 paragraphs of the conclusion narration; 2–4 markers total is a healthy range. Use unnumbered markers (`<scene/>`) when no numbered plan is provided."
	}
	var sb strings.Builder
	sb.WriteString("  Conclusion beat list (one image per entry; use these as the structural outline of your epilogue):\n")
	for i, b := range plan {
		fmt.Fprintf(&sb, "    Beat %d: %s\n", i, strings.TrimSpace(b))
	}
	fmt.Fprintf(&sb, "  Emit EXACTLY %d markers in order: <scene 1/>, <scene 2/>, …, <scene %d/>. Frame 0 paints automatically when the phase opens, so do NOT emit <scene 0/>. Each marker MUST be on its own line, immediately before the sentence that begins that beat — never mid-sentence, never clustered, never after the beat ends.",
		len(plan)-1, len(plan)-1)
	return sb.String()
}

// soundPlanBlock formats the planner's sound-cue list as part of the
// host's system prompt. Returns an empty string when no plan is
// available — that branch keeps the prompt free of sound-marker
// instructions so the LLM never emits `<sound-…/>` tokens for puzzles
// without a generated clip set.
func soundPlanBlock(plan []SoundDirection) string {
	if len(plan) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("Sound-cue markers — the audio director has pre-generated a numbered list of sound clips you may trigger during the surface narration. Each clip is labeled with a 0-based index, a mode (overlap or replace), and a one-sentence description of the sound itself. Emit the marker on its own line, IMMEDIATELY BEFORE the sentence the cue should land on (same placement rule as scene markers). Marker syntax depends on the mode:\n")
	sb.WriteString("  * mode=overlap → emit `<sound-overlapped-N/>` on its own line. The clip mixes additively on top of the running music bed for its natural duration (atmospheric stinger, single event), then ends; the bed continues uninterrupted.\n")
	sb.WriteString("  * mode=replace → emit `<sound-replace-N/>` on its own line. The music bed itself cross-fades over to the new clip and stays there (looped indefinitely) until another replace marker swaps it again. Use sparingly — replace is for a deliberate tonal shift at a key beat, not punctuation.\n")
	sb.WriteString("Sound markers are SILENT (TTS never sees them, subtitles never show them). They are OPTIONAL — emit one only when the listed cue genuinely amplifies the storytelling at that moment. If a cue has an Anchor line, fire the marker immediately before the sentence containing that anchor (verbatim substring match against your narration). If no anchor is given, place the marker at your own discretion. Each cue may be fired AT MOST ONCE per puzzle. Sound markers are valid only during the surface narration; do not emit them on any other directive.\n")
	sb.WriteString("Sound cue list:\n")
	for i, s := range plan {
		mode := strings.ToLower(strings.TrimSpace(s.Mode))
		fmt.Fprintf(&sb, "  Sound %d (mode=%s): %s\n", i, mode, strings.TrimSpace(s.Prompt))
		if a := strings.TrimSpace(s.Anchor); a != "" {
			fmt.Fprintf(&sb, "    Anchor (verbatim from surface, marks where to fire sound %d): %s\n", i, a)
		}
	}
	return sb.String()
}

// formatSurfaceBeatList renders a labelled list with 0-based indices.
// When anchors are available, each entry includes the verbatim anchor
// the host should string-match against its narration to know precisely
// where to drop the marker.
func formatSurfaceBeatList(plan, anchors []string) string {
	var sb strings.Builder
	sb.WriteString("  Surface beat list (one image per entry; use these as the structural outline of your narration):\n")
	for i, b := range plan {
		fmt.Fprintf(&sb, "    Beat %d: %s\n", i, strings.TrimSpace(b))
		if i < len(anchors) && strings.TrimSpace(anchors[i]) != "" {
			fmt.Fprintf(&sb, "      Anchor (verbatim from surface, marks the START of beat %d): %s\n",
				i, strings.TrimSpace(anchors[i]))
		}
	}
	fmt.Fprintf(&sb,
		"  Emit EXACTLY %d markers in order: <scene 1/>, <scene 2/>, …, <scene %d/>. Frame 0 paints automatically when the phase opens, so do NOT emit <scene 0/>.\n",
		len(plan)-1, len(plan)-1)
	sb.WriteString(
		"  CRITICAL placement rule: do NOT count paragraph breaks. INSTEAD, when the next sentence you are about to narrate contains the literal Anchor for beat N, emit `<scene N/>` on its own line IMMEDIATELY BEFORE that sentence. Match anchors by VERBATIM substring (the anchor text appears word-for-word inside the surface text — find it and place the marker just before the sentence that contains it). Anchors are listed in narration order; you walk them strictly in sequence — never skip, never reorder, never repeat. If a beat has no Anchor line, fall back to your own paragraph judgement for that one beat. Each marker must be on its own line, never mid-sentence, never clustered.")
	return sb.String()
}
