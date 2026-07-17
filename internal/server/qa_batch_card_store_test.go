package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sirily11/debate-bot/internal/llm"
	"github.com/sirily11/debate-bot/internal/qa"
)

func TestQAConversationPartsRestoresBatchPodcastCard(t *testing.T) {
	ctx := context.Background()
	_, store := newQATestStores(t)
	conversation, err := store.EnsureConversation(ctx, "owner", "")
	if err != nil {
		t.Fatalf("EnsureConversation: %v", err)
	}
	call := llm.ToolCall{
		ID:        "batch-card",
		Name:      "display_podcasts",
		Arguments: `{"discussion_ids":["a","b"]}`,
	}
	if err := store.AppendTurn(ctx, conversation.ID, qaTurnInput{Role: "assistant", ToolCalls: []llm.ToolCall{call}}); err != nil {
		t.Fatalf("AppendTurn assistant: %v", err)
	}
	payload, err := json.Marshal(qa.Card{
		Kind: qa.CardPodcasts,
		Podcasts: []qa.PodcastInfo{
			{ID: "a", Title: "Alpha"},
			{ID: "b", Title: "Beta"},
		},
	})
	if err != nil {
		t.Fatalf("Marshal card: %v", err)
	}
	if err := store.AppendTurn(ctx, conversation.ID, qaTurnInput{
		Role:        "tool",
		ToolCallID:  call.ID,
		ToolName:    call.Name,
		ResultText:  "Podcast grid shown",
		PayloadJSON: string(payload),
	}); err != nil {
		t.Fatalf("AppendTurn tool: %v", err)
	}
	turns, err := store.Turns(ctx, conversation.ID)
	if err != nil {
		t.Fatalf("Turns: %v", err)
	}
	parts := qaConversationParts(turns)
	if len(parts) != 1 || parts[0].ToolName != "display_podcasts" || parts[0].Status != "completed" {
		t.Fatalf("restored parts = %+v", parts)
	}
	var restored qa.Card
	if err := json.Unmarshal(parts[0].Card, &restored); err != nil {
		t.Fatalf("Unmarshal restored card: %v", err)
	}
	if restored.Kind != qa.CardPodcasts || len(restored.Podcasts) != 2 || restored.Podcasts[1].ID != "b" {
		t.Fatalf("restored batch card = %+v", restored)
	}
}
