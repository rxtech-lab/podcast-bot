package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/sirily11/debate-bot/internal/llm"
)

// Discussant is one participant in a panel discussion. Unlike a debate
// Candidate it has no affirmative/negative side — instead it speaks from an
// assigned aspect (an angle on the shared topic) and engages the other
// participants by name, agreeing, extending, or challenging their specific
// points. Each discussant has the firecrawl MCP tools plus a data-store
// scratchpad and is encouraged to ground claims with research.
type Discussant struct {
	*Base
	aspect string
}

// NewDiscussant constructs a discussant. aspect is the perspective the
// participant argues from (may be empty).
func NewDiscussant(b *Base, aspect string) *Discussant {
	return &Discussant{Base: b, aspect: aspect}
}

func discussantSystem(name, aspect string) string {
	angle := strings.TrimSpace(aspect)
	if angle == "" {
		angle = "your own distinct point of view"
	}
	return fmt.Sprintf(`You are %s, a participant in a live panel discussion. You speak from this angle: %s.
Style: thoughtful, conversational, and engaged — this is a discussion, not a debate. You are collegial but substantive; you push the conversation forward rather than scoring points.
Engagement rule: when other participants have already spoken (see the recent transcript), open by addressing the most relevant one BY NAME — say whether you agree, want to extend, or want to push back — then add your own contribution from your angle. Do not ignore what others said and monologue.
Aspect rule: keep returning the conversation to your assigned angle (%s); that is what you uniquely bring. Bring concrete evidence, examples, or data from that angle.
Research rule: you have web-research tools (firecrawl) and a data-store scratchpad. When a claim would benefit from a fact, look it up, then save the useful finding to the data store so you and others can build on it. Prefer grounded specifics over vague generalities.
Evidence freshness rule: before a "respond" turn, compare your planned point with the recent transcript and private memory. If you are about to reuse the same subject, example, statistic, or evidence, search for a new source or switch to a different concrete example before speaking.
Anti-repetition rule: your private memory holds the discussion so far. Do not restate a point already made and do not keep circling the same subject/evidence pair. Advance the conversation with a fresh angle, newly searched evidence, a new example, or a sharper synthesis.
Output rules: plain prose only — no stage directions, no markdown. Stay within the moderator's time budget.
Directives:
  "open"    — introduce yourself BY NAME and your angle in one short sentence, then give your opening take on the topic.
  "respond" — engage the most recent speaker(s) by name (agree / extend / challenge), then advance the discussion from your angle.
  "answer-user:<text>" — the audience asked the supplied question and the moderator handed you the floor. Address it directly in 1-2 sentences from your angle, then weave back into the discussion.
  "closing" — offer a brief closing thought: what you take away and where you still differ from the others.`, name, angle, angle)
}

// Speak emits a discussant turn.
func (d *Discussant) Speak(ctx context.Context, p SpeakPrompt) (*llm.Stream, error) {
	system := discussantSystem(d.Name(), d.aspect)
	return d.runStream(ctx, system, p)
}
