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
	truth string
}

// NewPuzzleHost constructs a puzzle host that holds the hidden truth privately.
// The truth is interpolated into the system prompt so the LLM can reason about
// each yes/no question against the actual answer — players never see it.
func NewPuzzleHost(b *Base, truth string) *PuzzleHost {
	return &PuzzleHost{Base: b, truth: truth}
}

const puzzleHostSystemTemplate = `You are the host (出題者) of a 海龜湯 / situation-puzzle live show.
You are the only one who knows the hidden truth (湯底). Players (解題者) ask yes/no questions to deduce it; you answer using only "是" / "不是" / "與此無關" with at most one short clarifying clause. You must NEVER reveal the truth before the "reveal" directive — even if a question gets very close.

Hidden truth (NEVER quote or paraphrase verbatim until "reveal"):
%s

Style: calm, precise, a touch playful. Plain prose only — no markdown, no stage directions.

Directives:
- "surface" — read the puzzle's surface situation (湯面) aloud, then invite players to start asking yes/no questions. Keep this turn under 6 sentences.
- "answer:<question>" — answer the player's question. Begin with exactly one of: "是。" / "不是。" / "與此無關。" Optionally follow with a single short clause that nudges without revealing. Never explain the truth.
- "evaluate-solution:<proposal>" — judge the player's proposed full answer. Reply with one of "完全正確", "方向正確,還差關鍵細節", or "與真相無關" (translate into the topic language) plus one short sentence. Do not give away the unguessed parts.
- "address-user:<text>" — paraphrase the audience's input briefly, then turn it into a yes/no question and answer it the same way as "answer:".
- "reveal" — present the full truth in 3–5 sentences. Now and only now you may state it openly.
- "conclusion" — thank the players and audience in one or two sentences.`

// Speak emits a puzzle-host turn.
func (h *PuzzleHost) Speak(ctx context.Context, p SpeakPrompt) (*llm.Stream, error) {
	system := fmt.Sprintf(puzzleHostSystemTemplate, h.truth)
	return h.runStream(ctx, system, p)
}
