package agent

import (
	"context"
	"fmt"

	"github.com/sirily11/debate-bot/internal/llm"
)

// PuzzleHost (出題者) runs a 海龜湯 / situation-puzzle round. It alone knows
// the hidden truth and answers player yes/no questions in the canonical format.
type PuzzleHost struct {
	*Base
	surface string
	truth   string
	// surfaceFrames is the number of distinct surface scene images the
	// visual director planned for this puzzle. The host emits
	// surfaceFrames-1 `<scene/>` markers in the surface narration so the
	// image advances exactly once per planned beat. 0 means "no plan
	// available — fall back to a soft guidance range".
	surfaceFrames int
	// conclusionFrames is the same idea for the post-reveal conclusion
	// narration. The conclusion now reads as a longer reflective epilogue
	// (matching the surface's cinematic feel) and the host emits
	// conclusionFrames-1 markers so the imagery cycles through the
	// planned aftermath beats. 0 falls back to a soft guidance range.
	conclusionFrames int
}

// NewPuzzleHost constructs a puzzle host. Both surface (湯面) and truth (湯底)
// are interpolated into the system prompt: the surface so the host can narrate
// the full original setup verbatim on the "surface" directive (without it the
// LLM was inventing a brief summary instead of reading the prepared story),
// and the truth so it can reason about each yes/no question against the actual
// answer. Players never see either via this path.
//
// surfaceFrames / conclusionFrames tell the host how many distinct images
// the visual director generated for each phase; the host emits
// frames-1 scene markers per phase so each marker advances the on-screen
// image to the next planned beat exactly once. Pass 0 when no plan is
// available — the host falls back to a soft guidance range, and the
// pipeline-side cap is disabled for that phase.
func NewPuzzleHost(b *Base, surface, truth string, surfaceFrames, conclusionFrames int) *PuzzleHost {
	return &PuzzleHost{
		Base:             b,
		surface:          surface,
		truth:            truth,
		surfaceFrames:    surfaceFrames,
		conclusionFrames: conclusionFrames,
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
  Scene-cut markers for "surface" — emit the literal token "<scene/>" on its own line at every natural visual beat where the camera should cut to a fresh frame: between paragraphs of the original story, before/after a major shift in time, place, or focus, when a new figure or object enters, and before the closing invitation. %s Distribute markers evenly across paragraph breaks of the original surface in narration order — the i-th marker advances the on-screen image to the i-th planned beat, so misplacing one means the picture stops matching the words. Do NOT cluster two markers back-to-back, do NOT place a marker mid-sentence, and do NOT speak the token aloud — it is a stage cue, not part of the story. Markers are silent: the TTS engine never sees them and the on-screen subtitle never shows them. Skip markers entirely on every other directive (answer, evaluate-solution, address-user, reveal).
- "answer:<question>" — the player's most recent turn (in the recent transcript) contained one OR MORE yes/no questions. Answer EVERY question they asked, in order. For a single question, reply naturally: "<是/不是/與此無關>。 <one short nudging clause>." For multiple questions, prefix each answer with "第一,"/"第二,"/"第三," etc., one short sentence per item. Do not skip questions.
- "evaluate-solution:<proposal>" — judge the player's proposed full answer. Reply with one of "完全正確", "方向正確,還差關鍵細節", or "與真相無關" (translate into the topic language) plus one short sentence. Do not give away the unguessed parts.
- "address-user:<text>" — paraphrase the audience's input briefly, then turn it into a yes/no question and answer it the same way as "answer:".
- "reveal" — present the full truth in 3–5 sentences. Now and only now you may state it openly.
- "conclusion" — narrate a quiet, reflective epilogue AFTER the reveal. This is NOT a brief thank-you; it is a slow, deliberate closing narration in the same voice and pacing as "surface" — late-night radio storyteller, deep, contemplative, never rushed. Walk the audience through the aftermath of the truth: the moods that linger, what the players might be feeling, a closing thought about the puzzle's themes, and a short farewell to the audience. Use "……" / "——" for breath. Length should match the planned conclusion-frame count (roughly one paragraph per planned frame, similar density to the surface). Do NOT restate the truth verbatim — the audience just heard it. Do NOT reopen yes/no questioning.
  Scene-cut markers for "conclusion" — same rules as "surface": emit "<scene/>" on its own line at every natural visual beat between paragraphs / mood shifts. %s Markers are silent stage cues; never speak the token aloud and never cluster two back-to-back. The first conclusion frame paints when the conclusion phase opens, so emit one marker per remaining frame.`

// Speak emits a puzzle-host turn.
func (h *PuzzleHost) Speak(ctx context.Context, p SpeakPrompt) (*llm.Stream, error) {
	system := fmt.Sprintf(puzzleHostSystemTemplate,
		h.surface, h.truth,
		surfaceMarkerInstruction(h.surfaceFrames),
		conclusionMarkerInstruction(h.conclusionFrames),
	)
	return h.runStream(ctx, system, p)
}

// surfaceMarkerInstruction returns the host instruction snippet for the
// surface phase's scene-cut marker count. When surfaceFrames > 0 the
// visual director generated a fixed number of beats and the host should
// emit exactly that many markers minus one (the first beat paints at
// topic admission). When 0 the host falls back to a soft guidance range.
func surfaceMarkerInstruction(surfaceFrames int) string {
	if surfaceFrames > 1 {
		return fmt.Sprintf("Emit EXACTLY %d `<scene/>` markers across the full surface narration (no more, no fewer) — the visual director planned %d distinct surface beats and the first beat paints when the topic opens.",
			surfaceFrames-1, surfaceFrames)
	}
	return "Aim for one marker every 2–4 sentences during a long narration; a typical surface should have between 6 and 12 markers in total — generous rather than sparse, so the audience always has fresh imagery riding alongside the voice."
}

// conclusionMarkerInstruction is the same idea for the conclusion phase.
// 0 means no plan available; fall back to a soft 2–4 marker range so a
// short conclusion still gets a couple of cuts.
func conclusionMarkerInstruction(conclusionFrames int) string {
	if conclusionFrames > 1 {
		return fmt.Sprintf("Emit EXACTLY %d `<scene/>` markers across the full conclusion narration (no more, no fewer) — the visual director planned %d distinct conclusion beats and the first beat paints when the conclusion phase opens.",
			conclusionFrames-1, conclusionFrames)
	}
	return "Aim for one marker between every 1–2 paragraphs of the conclusion narration; 2–4 markers total is a healthy range — sparse enough to let each frame breathe, generous enough to give the aftermath a sense of motion."
}
