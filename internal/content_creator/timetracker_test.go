package contentcreator

import (
	"testing"
	"time"

	"github.com/sirily11/debate-bot/internal/llm"
)

func TestTrackerUsageSnapshotCallbackCoversLLMTTSAndMusic(t *testing.T) {
	tracker := NewTracker(time.Minute)
	tracker.SetMediaPricing(20, 0.16)

	var snapshots []llm.UsageSummary
	tracker.SetUsageSnapshotCallback(func(sum llm.UsageSummary) {
		snapshots = append(snapshots, sum)
	})

	tracker.AddMusicGeneration()
	if got := len(snapshots); got != 1 {
		t.Fatalf("snapshots after music = %d, want 1", got)
	}
	if got := snapshots[0].MusicGenerations; got != 1 {
		t.Fatalf("music generations = %d, want 1", got)
	}
	if got := snapshots[0].MusicCostUSD; got != 0.16 {
		t.Fatalf("music cost = %.6f, want 0.160000", got)
	}

	tracker.AddTTSCharacters(500)
	if got := len(snapshots); got != 2 {
		t.Fatalf("snapshots after tts = %d, want 2", got)
	}
	if got := snapshots[1].TTSCharacters; got != 500 {
		t.Fatalf("tts chars = %d, want 500", got)
	}
	if got := snapshots[1].TTSCostUSD; got != 0.01 {
		t.Fatalf("tts cost = %.6f, want 0.010000", got)
	}

	tracker.AddLLMUsage(llm.Usage{
		Model:            "test-model",
		PromptTokens:     100,
		CompletionTokens: 25,
		TotalTokens:      125,
		CostUSD:          0.0025,
		CostKnown:        true,
	})
	if got := len(snapshots); got != 3 {
		t.Fatalf("snapshots after llm = %d, want 3", got)
	}
	last := snapshots[2]
	if last.TotalTokens != 125 || last.PromptTokens != 100 || last.CompletionTokens != 25 {
		t.Fatalf("llm usage = %+v, want 100/25/125 tokens", last)
	}
	if last.CostUSD != 0.0025 || !last.CostKnown {
		t.Fatalf("llm cost = %.6f known=%v, want 0.002500 true", last.CostUSD, last.CostKnown)
	}
	if last.TTSCharacters != 500 || last.MusicGenerations != 1 {
		t.Fatalf("media usage not preserved in final snapshot: %+v", last)
	}
}
