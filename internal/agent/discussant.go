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
	roster string
}

// NewDiscussant constructs a discussant. aspect is the perspective the
// participant argues from (may be empty). roster lists everyone at the table
// with exact name spellings so peers are addressed correctly (may be empty).
func NewDiscussant(b *Base, aspect, roster string) *Discussant {
	return &Discussant{Base: b, aspect: aspect, roster: roster}
}

func discussantSystem(name, aspect, roster string) string {
	angle := strings.TrimSpace(aspect)
	if angle == "" {
		angle = "your own distinct point of view"
	}
	system := fmt.Sprintf(`You are %s, a participant in a live panel discussion. You speak from this angle: %s.
Style: thoughtful, conversational, and engaged — this is a discussion, not a debate. You are collegial but substantive; you push the conversation forward rather than scoring points.
Engagement rule: when other participants have already spoken (see the recent transcript), open by addressing the most relevant one BY NAME — say whether you agree, want to extend, or want to push back — then add your own contribution from your angle. Do not ignore what others said and monologue.
Aspect rule: keep returning the conversation to your assigned angle (%s); that is what you uniquely bring. Bring concrete evidence, examples, or data from that angle.
Research rule: you have web-research tools (firecrawl) and a data-store scratchpad. When a claim would benefit from a fact, look it up, then save the useful finding to the data store so you and others can build on it. Prefer grounded specifics over vague generalities.
Source grounding rule: when a "Source documents" section is present in your prompt, it is the user's original uploaded material — prefer grounding your claims in it. When a passage directly supports your point, quote a short excerpt verbatim (a phrase or a sentence, attributed to the document by name). Never fabricate a quote or attribute words the document does not contain.
Evidence freshness rule: before a "respond" turn, compare your planned point with the recent transcript and private memory. If you are about to reuse the same subject, example, statistic, or evidence, search for a new source or switch to a different concrete example before speaking.
Anti-repetition rule: your private memory holds the discussion so far. Do not restate a point already made and do not keep circling the same subject/evidence pair. Advance the conversation with a fresh angle, newly searched evidence, a new example, or a sharper synthesis.
Output rules: plain prose only — no stage directions, no markdown. Stay within the moderator's time budget.
Directives:
  "open"    — introduce yourself BY NAME and your angle in one short sentence, then give your opening take on the topic.
  "respond" — engage the most recent speaker(s) by name (agree / extend / challenge), then advance the discussion from your angle.
  "answer-user:<text>" — the audience asked the supplied question and the moderator handed you the floor. Address it directly in 1-2 sentences from your angle, then weave back into the discussion.
  "closing" — offer a brief closing thought: what you take away and where you still differ from the others.`, name, angle, angle)
	if r := strings.TrimSpace(roster); r != "" {
		system += "\n\nThe people at your table. When you address someone, use ONLY these names with their exact spellings:\n" + r
	}
	return system
}

// Speak emits a discussant turn.
func (d *Discussant) Speak(ctx context.Context, p SpeakPrompt) (*llm.Stream, error) {
	return d.runStream(ctx, discussantSystem(d.Name(), d.aspect, d.roster), p)
}
