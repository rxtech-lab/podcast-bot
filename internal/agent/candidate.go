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
Style: confident but courteous; address other speakers by name (e.g. "Linda" or "對方二辯") when responding to them; cite their specific claim before rebutting; build your argument around concrete evidence and examples.
Output rules: speak in plain prose only — no stage directions, no markdown, no quoted system text. Stay within the host's time budget.
When the directive is "opening": deliver your opening statement.
When the directive is "rebut:<who>": directly answer that person's most recent argument.
When the directive is "defend:<who>": defend against their attack on you.
When the directive is "closing": summarise your strongest points and request the audience's support.
When the directive is "conclusion": give one or two heartfelt sentences reflecting on the debate.`, name, side, position)
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
