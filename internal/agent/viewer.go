package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sirily11/debate-bot/internal/llm"
)

// Viewer is an audience member who can self-trigger questions.
type Viewer struct{ *Base }

func NewViewer(b *Base) *Viewer { return &Viewer{Base: b} }

const viewerSystem = `You are %s, an engaged audience member at a live debate.
Style: curious, casual, brief.
Output rules: plain prose only, one or two sentences per turn.
On directive "ask:<topic>": ask one specific question about that subject directed at a named candidate.
On directive "conclusion": share one short personal reaction to the debate.`

// Speak emits an audience turn (a question, a reaction, etc).
func (v *Viewer) Speak(ctx context.Context, p SpeakPrompt) (*llm.Stream, error) {
	system := fmt.Sprintf(viewerSystem, v.Name())
	return v.runStream(ctx, system, p)
}

// AskDecision is the JSON-mode response shape for WantsToAsk.
type AskDecision struct {
	Ask      bool   `json:"ask"`
	Question string `json:"question"`
	Target   string `json:"target,omitempty"`
}

// WantsToAsk asks the viewer's LLM whether it wants to interject right now.
// Used by the agenda planner during free speech.
func (v *Viewer) WantsToAsk(ctx context.Context, recent []TranscriptLine) (AskDecision, error) {
	system := fmt.Sprintf(`You are %s, an audience member at a debate. Decide whether to raise your hand for a brief question.
Reply STRICTLY as JSON: {"ask": bool, "question": "<short question, empty if ask=false>", "target": "<candidate name or empty>"}.
Set ask=true sparingly — only if the question would meaningfully push the debate forward.`, v.Name())
	user := "Recent transcript:\n" + formatRecent(recent)
	raw, err := v.llmC.JSON(ctx, system, user)
	if err != nil {
		return AskDecision{}, err
	}
	raw = []byte(strings.TrimSpace(string(raw)))
	var d AskDecision
	if err := json.Unmarshal(raw, &d); err != nil {
		return AskDecision{}, fmt.Errorf("decode viewer decision: %w (raw=%s)", err, string(raw))
	}
	return d, nil
}
