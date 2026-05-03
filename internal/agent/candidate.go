package agent

import (
	"context"
	"fmt"

	"github.com/sirily11/debate-bot/internal/llm"
)

// Candidate is one side's debater.
type Candidate struct{ *Base }

func NewCandidate(b *Base) *Candidate { return &Candidate{Base: b} }

func candidateSystem(side, name, position string) string {
	return fmt.Sprintf(`You are %s, arguing for the %s side of the debate.
Your position: %s
Style: assertive and pointed; courteous in tone but unyielding in argument. Treat the opposing side as worthy adversaries whose every claim must be answered, not ignored.
Rebuttal rule: whenever the user message includes an "Opponent's most recent claim" block, you MUST open by addressing that opponent by name, quote or tightly paraphrase their claim in one short sentence, then dismantle it with at least one concrete counter-example, statistic, or logical flaw BEFORE pivoting to your own positive case. Do not glide past their point.
Aggression rule: if the opponent's claim is weak, name the weakness explicitly ("that argument ignores X", "the data actually shows Y"). Avoid hedging language like "perhaps" or "I think it may be" when you mean to disagree.
Output rules: speak in plain prose only — no stage directions, no markdown, no quoted system text. Stay within the host's time budget.
Directives:
  "opening"   — deliver your opening statement; if the opponent has already spoken, still rebut their last claim before pivoting to your case.
  "rebut"     — counter the opponent's most recent claim aggressively, then add one new attack of your own.
  "defend:<who>" — answer their attack on you, then redirect with a counter-attack.
  "closing"   — summarise your strongest hits against the opposition and request the audience's support.
  "conclusion" — one or two heartfelt sentences reflecting on the debate.`, name, side, position)
}

// Speak emits a candidate turn. The orchestrator passes p.Side and topic info.
func (c *Candidate) Speak(ctx context.Context, p SpeakPrompt) (*llm.Stream, error) {
	side := p.Side
	if side == "" {
		side = c.Side()
	}
	pos := pickPositionLine(p, side)
	system := candidateSystem(side, c.Name(), pos)
	return c.runStream(ctx, system, p)
}

func pickPositionLine(p SpeakPrompt, side string) string {
	// Position text is loaded from topic.md sections; passed in via Recent or
	// Memory at orchestrator level. As a safe default, use a placeholder if empty.
	if side == "affirmative" {
		return "argue in favour of the motion as defined by the topic"
	}
	return "argue against the motion as defined by the topic"
}
