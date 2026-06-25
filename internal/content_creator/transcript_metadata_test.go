package contentcreator

import (
	"context"
	"testing"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/llm"
	"github.com/sirily11/debate-bot/internal/tts"
)

type metadataTestSpeaker struct{}

func (metadataTestSpeaker) Name() string       { return "Maya" }
func (metadataTestSpeaker) SafeName() string   { return "Maya" }
func (metadataTestSpeaker) Role() agent.Role   { return agent.RoleDiscussant }
func (metadataTestSpeaker) Side() string       { return "" }
func (metadataTestSpeaker) Model() string      { return "" }
func (metadataTestSpeaker) Voice() tts.Voice   { return tts.Voice{} }
func (metadataTestSpeaker) SetVoice(tts.Voice) {}
func (metadataTestSpeaker) Speak(context.Context, agent.SpeakPrompt) (*llm.Stream, error) {
	return nil, nil
}
func (metadataTestSpeaker) Listen(context.Context, agent.TranscriptLine) error { return nil }
func (metadataTestSpeaker) Compress(context.Context) error                     { return nil }

func TestAppendFromTurnPreservesMetadata(t *testing.T) {
	turn := &Turn{
		ID:      1,
		Phase:   agent.PhaseFreeSpeech,
		Speaker: metadataTestSpeaker{},
		Budget:  time.Second,
		TextOut: make(chan string, 2),
	}
	turn.TextOut <- "This claim needs support."
	close(turn.TextOut)
	turn.addSource(agent.TranscriptSource{
		Title: "Example",
		URL:   "https://example.com/research",
	})
	turn.SetJudgementComment("This lacks enough evidence.")

	line := NewTranscript().AppendFromTurn(turn)
	if got := len(line.Sources); got != 1 {
		t.Fatalf("sources length = %d, want 1", got)
	}
	if line.Sources[0].URL != "https://example.com/research" {
		t.Fatalf("source URL = %q", line.Sources[0].URL)
	}
	if line.JudgementComment != "This lacks enough evidence." {
		t.Fatalf("judgement comment = %q", line.JudgementComment)
	}
}

func TestFallbackJudgementCommentFlagsUnsourcedStrongClaim(t *testing.T) {
	comment := fallbackJudgementComment(agent.TranscriptLine{
		Text: "这是目前拿到面试机会的最快路径，必须把结果量化出来。",
	})
	if comment == "" {
		t.Fatal("expected fallback judgement comment")
	}
}
