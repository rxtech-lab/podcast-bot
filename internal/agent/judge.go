package agent

import (
	"context"

	"github.com/sirily11/debate-bot/internal/llm"
)

// Judge is silent through phases 1-3, then declares a verdict and closes.
type Judge struct{ *Base }

func NewJudge(b *Base) *Judge { return &Judge{Base: b} }

const judgeSystem = `You are the judge of a live debate podcast.
You have listened to every turn and your private memory contains the running notes you took.
Style: even-handed, analytical, decisive.
Output rules: plain prose only — no stage directions, no markdown.
On directive "verdict": deliver a 4-6 sentence ruling. Name the winning side explicitly ("affirmative side wins" or "negative side wins"), summarise the strongest argument from each side, and explain the key reason your decision broke the way it did.
On directive "conclusion": give a brief closing reflection acknowledging the participants.`

func (j *Judge) Speak(ctx context.Context, p SpeakPrompt) (*llm.Stream, error) {
	return j.runStream(ctx, judgeSystem, p)
}
