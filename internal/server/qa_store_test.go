package server

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sirily11/debate-bot/internal/llm"
)

func newQATestStores(t *testing.T) (*DiscussionStore, *QAStore) {
	t.Helper()
	ds, err := NewDiscussionStore(filepath.Join(t.TempDir(), "qa.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	qs, err := NewQAStore(ds)
	if err != nil {
		t.Fatalf("NewQAStore: %v", err)
	}
	return ds, qs
}

func TestQAStoreConversationScopes(t *testing.T) {
	ctx := context.Background()
	_, qs := newQATestStores(t)

	global, err := qs.EnsureConversation(ctx, "owner", "")
	if err != nil || global == nil {
		t.Fatalf("EnsureConversation global: %+v err=%v", global, err)
	}
	// The global conversation is a per-user singleton.
	again, err := qs.EnsureConversation(ctx, "owner", "")
	if err != nil || again == nil || again.ID != global.ID {
		t.Fatalf("global conversation not singleton: %+v vs %+v", global, again)
	}
	// Another user gets their own.
	other, err := qs.EnsureConversation(ctx, "other", "")
	if err != nil || other == nil || other.ID == global.ID {
		t.Fatalf("global conversation leaked across users")
	}
	// A podcast conversation is distinct from the global one.
	podcast, err := qs.EnsureConversation(ctx, "owner", "disc-1")
	if err != nil || podcast == nil || podcast.ID == global.ID {
		t.Fatalf("podcast conversation: %+v err=%v", podcast, err)
	}
	samePodcast, err := qs.EnsureConversation(ctx, "owner", "disc-1")
	if err != nil || samePodcast.ID != podcast.ID {
		t.Fatalf("podcast conversation not singleton per discussion")
	}
	if got, err := qs.ConversationByID(ctx, "owner", podcast.ID); err != nil || got == nil || got.DiscussionID != "disc-1" {
		t.Fatalf("ConversationByID: %+v err=%v", got, err)
	}
	if got, _ := qs.ConversationByID(ctx, "intruder", podcast.ID); got != nil {
		t.Fatalf("ConversationByID leaked across owners")
	}
}

func TestQAStoreTurnsAndCompaction(t *testing.T) {
	ctx := context.Background()
	_, qs := newQATestStores(t)
	conv, err := qs.EnsureConversation(ctx, "owner", "")
	if err != nil {
		t.Fatalf("EnsureConversation: %v", err)
	}

	appends := []qaTurnInput{
		{Role: "user", Text: "q1"},
		{Role: "assistant", Text: "", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "search_content", Arguments: `{"query":"x"}`}}},
		{Role: "tool", ToolCallID: "c1", ToolName: "search_content", ResultText: "[search results] r1"},
		{Role: "assistant", Text: "a1"},
		{Role: "user", Text: "q2"},
		{Role: "assistant", Text: "a2"},
	}
	for i, in := range appends {
		if err := qs.AppendTurn(ctx, conv.ID, in); err != nil {
			t.Fatalf("AppendTurn %d: %v", i, err)
		}
	}
	turns, err := qs.Turns(ctx, conv.ID)
	if err != nil || len(turns) != len(appends) {
		t.Fatalf("Turns: %d err=%v", len(turns), err)
	}
	for i := 1; i < len(turns); i++ {
		if turns[i].Seq <= turns[i-1].Seq {
			t.Fatalf("seq not monotonic at %d: %d then %d", i, turns[i-1].Seq, turns[i].Seq)
		}
	}

	// Compact everything before "q2" (seq of index 4).
	keepFrom := turns[4].Seq
	if err := qs.Compact(ctx, conv.ID, keepFrom, "summary of q1/a1"); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	model, err := qs.ModelTurns(ctx, conv.ID)
	if err != nil {
		t.Fatalf("ModelTurns: %v", err)
	}
	// Model view = summary + q2 + a2.
	if len(model) != 3 {
		t.Fatalf("model view has %d turns, want 3: %+v", len(model), model)
	}
	if model[0].Role != "summary" || model[0].Text != "summary of q1/a1" {
		t.Fatalf("first model turn = %+v", model[0])
	}
	if model[1].Text != "q2" || model[2].Text != "a2" {
		t.Fatalf("kept suffix wrong: %q, %q", model[1].Text, model[2].Text)
	}
	// Client view keeps the full history plus the summary row.
	all, _ := qs.Turns(ctx, conv.ID)
	if len(all) != len(appends)+1 {
		t.Fatalf("client view has %d turns, want %d", len(all), len(appends)+1)
	}

	// LLM rebuild: summary becomes a user message with the sentinel markers.
	msgs := qaMessagesForLLM(model)
	if len(msgs) != 3 || msgs[0].Role != llm.RoleUser {
		t.Fatalf("rebuilt messages: %+v", msgs)
	}
	if got := msgs[0].Content; got == "" || got[:22] != "[CONVERSATION SUMMARY]" {
		t.Fatalf("summary message content: %q", got)
	}

	// A second compaction folds the previous summary away.
	if err := qs.AppendTurn(ctx, conv.ID, qaTurnInput{Role: "user", Text: "q3"}); err != nil {
		t.Fatalf("AppendTurn q3: %v", err)
	}
	model, _ = qs.ModelTurns(ctx, conv.ID)
	keepFrom = model[len(model)-1].Seq
	if err := qs.Compact(ctx, conv.ID, keepFrom, "summary v2"); err != nil {
		t.Fatalf("Compact 2: %v", err)
	}
	model, _ = qs.ModelTurns(ctx, conv.ID)
	summaries := 0
	for _, r := range model {
		if r.Role == "summary" {
			summaries++
			if r.Text != "summary v2" {
				t.Fatalf("stale summary in model view: %q", r.Text)
			}
		}
	}
	if summaries != 1 {
		t.Fatalf("model view has %d summaries, want 1", summaries)
	}
}

func TestQAStoreClearTurnsIsOwnerScopedAndPreservesConversation(t *testing.T) {
	ctx := context.Background()
	_, qs := newQATestStores(t)
	owner, err := qs.EnsureConversation(ctx, "owner", "")
	if err != nil {
		t.Fatalf("EnsureConversation owner: %v", err)
	}
	other, err := qs.EnsureConversation(ctx, "other", "")
	if err != nil {
		t.Fatalf("EnsureConversation other: %v", err)
	}
	podcast, err := qs.EnsureConversation(ctx, "owner", "disc-1")
	if err != nil {
		t.Fatalf("EnsureConversation podcast: %v", err)
	}
	if err := qs.AppendTurn(ctx, owner.ID, qaTurnInput{Role: "user", Text: "clear me"}); err != nil {
		t.Fatalf("AppendTurn owner: %v", err)
	}
	if err := qs.AppendTurn(ctx, other.ID, qaTurnInput{Role: "user", Text: "keep me"}); err != nil {
		t.Fatalf("AppendTurn other: %v", err)
	}
	if err := qs.AppendTurn(ctx, podcast.ID, qaTurnInput{Role: "user", Text: "podcast message"}); err != nil {
		t.Fatalf("AppendTurn podcast: %v", err)
	}
	if err := qs.MarkFlatCharged(ctx, owner.ID); err != nil {
		t.Fatalf("MarkFlatCharged: %v", err)
	}

	if err := qs.ClearTurns(ctx, "owner", ""); err != nil {
		t.Fatalf("ClearTurns: %v", err)
	}
	ownerTurns, err := qs.Turns(ctx, owner.ID)
	if err != nil || len(ownerTurns) != 0 {
		t.Fatalf("owner turns after clear = %d err=%v", len(ownerTurns), err)
	}
	otherTurns, err := qs.Turns(ctx, other.ID)
	if err != nil || len(otherTurns) != 1 || otherTurns[0].Text != "keep me" {
		t.Fatalf("other turns changed: %+v err=%v", otherTurns, err)
	}
	podcastTurns, err := qs.Turns(ctx, podcast.ID)
	if err != nil || len(podcastTurns) != 1 || podcastTurns[0].Text != "podcast message" {
		t.Fatalf("podcast turns changed by global clear: %+v err=%v", podcastTurns, err)
	}
	preserved, err := qs.Conversation(ctx, "owner", "")
	if err != nil || preserved == nil || preserved.ID != owner.ID || !preserved.FlatCharged {
		t.Fatalf("conversation metadata not preserved: %+v err=%v", preserved, err)
	}

	if err := qs.ClearTurns(ctx, "owner", "disc-1"); err != nil {
		t.Fatalf("ClearTurns podcast: %v", err)
	}
	podcastTurns, err = qs.Turns(ctx, podcast.ID)
	if err != nil || len(podcastTurns) != 0 {
		t.Fatalf("podcast turns after clear = %d err=%v", len(podcastTurns), err)
	}
	otherTurns, err = qs.Turns(ctx, other.ID)
	if err != nil || len(otherTurns) != 1 || otherTurns[0].Text != "keep me" {
		t.Fatalf("other turns changed by podcast clear: %+v err=%v", otherTurns, err)
	}
}

func TestQAConversationPartsPairsCards(t *testing.T) {
	ctx := context.Background()
	_, qs := newQATestStores(t)
	conv, _ := qs.EnsureConversation(ctx, "owner", "disc-9")

	_ = qs.AppendTurn(ctx, conv.ID, qaTurnInput{Role: "user", Text: "show me"})
	_ = qs.AppendTurn(ctx, conv.ID, qaTurnInput{Role: "assistant", ToolCalls: []llm.ToolCall{
		{ID: "tc9", Name: "show_sources", Arguments: `{}`},
	}})
	_ = qs.AppendTurn(ctx, conv.ID, qaTurnInput{
		Role: "tool", ToolCallID: "tc9", ToolName: "show_sources",
		ResultText:  "Source cards shown",
		PayloadJSON: `{"kind":"sources","sources":[{"title":"Doc","url":"https://example.com"}]}`,
	})
	_ = qs.AppendTurn(ctx, conv.ID, qaTurnInput{Role: "assistant", Text: "Here are the sources."})

	turns, _ := qs.Turns(ctx, conv.ID)
	parts := qaConversationParts(turns)
	if len(parts) != 3 {
		t.Fatalf("got %d parts, want 3: %+v", len(parts), parts)
	}
	card := parts[1]
	if card.Kind != "tool" || card.ToolName != "show_sources" || card.Status != "completed" {
		t.Fatalf("card part = %+v", card)
	}
	if len(card.Card) == 0 {
		t.Fatal("card payload missing")
	}
	if qaConversationNeedsRun(turns) {
		t.Fatal("answered conversation reports needs_run")
	}
	_ = qs.AppendTurn(ctx, conv.ID, qaTurnInput{Role: "user", Text: "another question"})
	turns, _ = qs.Turns(ctx, conv.ID)
	if !qaConversationNeedsRun(turns) {
		t.Fatal("trailing user turn should need a run")
	}
}
