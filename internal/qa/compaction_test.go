package qa

import (
	"strings"
	"testing"

	"github.com/sirily11/debate-bot/internal/llm"
)

func TestNeedsCompactionThreshold(t *testing.T) {
	small := []llm.Message{{Role: llm.RoleUser, Content: "hi"}}
	if NeedsCompaction(small, 1000) {
		t.Fatal("tiny history should not need compaction")
	}
	// Enough characters to exceed 75% of the 75k-token window at 4 chars/token.
	big := make([]llm.Message, 0, 60)
	for i := 0; i < 60; i++ {
		big = append(big, llm.Message{Role: llm.RoleUser, Content: strings.Repeat("x", 4_000)})
	}
	if !NeedsCompaction(big, 1000) {
		t.Fatal("oversized history should need compaction")
	}
}

func TestCompactionBoundaryKeepsRecentAndToolPairs(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: "q1"},
		{Role: llm.RoleAssistant, Content: "a1"},
		{Role: llm.RoleUser, Content: "q2"},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c1", Name: "search_content"}}},
		{Role: llm.RoleTool, Content: "r1", ToolCallID: "c1"},
		{Role: llm.RoleTool, Content: "r2", ToolCallID: "c2"},
		{Role: llm.RoleAssistant, Content: "a2"},
		{Role: llm.RoleUser, Content: "q3"},
		{Role: llm.RoleAssistant, Content: "a3"},
		{Role: llm.RoleUser, Content: "q4"},
	}
	// len 10, keep 6 → naive boundary 4 lands on a tool result whose call
	// (index 3) would be evicted; the boundary must walk back to 3.
	boundary := CompactionBoundary(msgs)
	if boundary != 3 {
		t.Fatalf("boundary = %d, want 3", boundary)
	}
	if msgs[boundary].Role == llm.RoleTool {
		t.Fatal("kept slice starts with an orphan tool result")
	}
}

func TestCompactionBoundaryNothingToEvict(t *testing.T) {
	short := []llm.Message{
		{Role: llm.RoleUser, Content: "q1"},
		{Role: llm.RoleAssistant, Content: "a1"},
	}
	if b := CompactionBoundary(short); b != 0 {
		t.Fatalf("boundary = %d, want 0", b)
	}
	// All-tool prefix walks back to zero rather than panicking.
	tools := []llm.Message{
		{Role: llm.RoleTool, Content: "r", ToolCallID: "c"},
		{Role: llm.RoleTool, Content: "r", ToolCallID: "c"},
		{Role: llm.RoleTool, Content: "r", ToolCallID: "c"},
		{Role: llm.RoleTool, Content: "r", ToolCallID: "c"},
		{Role: llm.RoleTool, Content: "r", ToolCallID: "c"},
		{Role: llm.RoleTool, Content: "r", ToolCallID: "c"},
		{Role: llm.RoleTool, Content: "r", ToolCallID: "c"},
	}
	if b := CompactionBoundary(tools); b != 0 {
		t.Fatalf("all-tool boundary = %d, want 0", b)
	}
}

func TestEstimateTokensCountsToolTraffic(t *testing.T) {
	plain := []llm.Message{{Role: llm.RoleUser, Content: strings.Repeat("x", 400)}}
	withTools := []llm.Message{{
		Role:      llm.RoleAssistant,
		Content:   strings.Repeat("x", 400),
		ToolCalls: []llm.ToolCall{{Name: "search_content", Arguments: strings.Repeat("y", 4_000)}},
	}}
	if EstimateTokens(withTools) <= EstimateTokens(plain) {
		t.Fatal("tool arguments not counted")
	}
}

func TestSummaryMessageShape(t *testing.T) {
	msg := SummaryMessage("the facts")
	if msg.Role != llm.RoleUser {
		t.Fatalf("summary role = %q", msg.Role)
	}
	if !strings.Contains(msg.Content, "[CONVERSATION SUMMARY]") ||
		!strings.Contains(msg.Content, "the facts") ||
		!strings.Contains(msg.Content, "[END SUMMARY]") {
		t.Fatalf("summary content = %q", msg.Content)
	}
}
