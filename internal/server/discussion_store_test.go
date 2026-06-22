package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
)

func TestDiscussionStoreLifecycle(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "native-discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()

	owner := "oauth:user-1"
	otherOwner := "oauth:user-2"
	empty, err := store.List(ctx, owner, 0, 0)
	if err != nil {
		t.Fatalf("List empty: %v", err)
	}
	if empty == nil {
		t.Fatal("List empty returned nil slice; API would encode null instead of []")
	}

	resp := planResponse{
		Script: &config.DebateTopic{
			Title:    "AI Safety Panel",
			Type:     config.ContentTypeDiscussion,
			Language: "en-US",
			Host:     config.AgentSpec{Name: "Host"},
			Discussants: []config.AgentSpec{
				{Name: "Alice", Aspect: "technical"},
			},
			Commander:  config.AgentSpec{Name: "Director"},
			Background: "A panel about server-side persistence.",
			Sources: []config.Source{
				{Title: "Reference", URL: "https://example.com/ref", Snippet: "Useful context", Markdown: "## Useful context"},
			},
		},
		Markdown:   "# AI Safety Panel",
		Sources:    []config.Source{{Title: "Reference", URL: "https://example.com/ref", Markdown: "## Useful context"}},
		Researched: true,
	}

	created, err := store.Create(ctx, owner, "AI safety", resp)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("Create returned empty id")
	}
	if created.Title != "AI Safety Panel" || created.Language != "en-US" || !created.Researched {
		t.Fatalf("created discussion mismatch: %+v", created)
	}
	if created.Script == nil || created.Script.Commander.Name != "Director" {
		t.Fatalf("script was not persisted with commander: %+v", created.Script)
	}
	if len(created.Sources) != 1 || created.Sources[0].Markdown != "## Useful context" {
		t.Fatalf("source markdown was not persisted: %+v", created.Sources)
	}

	if hidden, err := store.Get(ctx, otherOwner, created.ID); err != nil {
		t.Fatalf("Get as other owner: %v", err)
	} else if hidden != nil {
		t.Fatalf("Get as other owner returned discussion: %+v", hidden)
	}

	if err := store.AppendLine(ctx, owner, created.ID, DiscussionLine{
		Speaker: "User",
		Role:    "user",
		Text:    "Can you cover implementation risk?",
		IsUser:  true,
	}); err != nil {
		t.Fatalf("AppendLine: %v", err)
	}
	if err := store.ReplaceTranscript(ctx, created.ID, []agent.TranscriptLine{
		{Speaker: "Host", Role: agent.RoleHost, Text: "Welcome to the panel."},
		{Speaker: "Alice", Role: agent.RoleDiscussant, Side: "technical", Text: "Persistence changes the product shape."},
	}); err != nil {
		t.Fatalf("ReplaceTranscript: %v", err)
	}

	got, err := store.Get(ctx, owner, created.ID)
	if err != nil {
		t.Fatalf("Get after transcript: %v", err)
	}
	if got == nil {
		t.Fatal("Get after transcript returned nil")
	}
	if len(got.Lines) != 3 {
		t.Fatalf("line count = %d, want 3: %+v", len(got.Lines), got.Lines)
	}
	if !got.Lines[0].IsUser {
		t.Fatalf("first line should be preserved user line: %+v", got.Lines[0])
	}

	generated, err := store.SetJob(ctx, owner, created.ID, "job-123")
	if err != nil {
		t.Fatalf("SetJob: %v", err)
	}
	if generated == nil || generated.Status != DiscussionGenerating || generated.JobID != "job-123" {
		t.Fatalf("SetJob mismatch: %+v", generated)
	}
	if err := store.SetUsage(ctx, created.ID, 1000, 250, 1250, 0.00375, true, 0.0012, 0.16); err != nil {
		t.Fatalf("SetUsage: %v", err)
	}
	if err := store.SetJobResult(ctx, created.ID, DiscussionReady, "https://example.com/audio.m4a"); err != nil {
		t.Fatalf("SetJobResult: %v", err)
	}
	ready, err := store.Get(ctx, owner, created.ID)
	if err != nil {
		t.Fatalf("Get ready: %v", err)
	}
	if ready.Status != DiscussionReady || ready.DownloadURL == "" {
		t.Fatalf("ready mismatch: %+v", ready)
	}
	if ready.PromptTokens != 1000 || ready.CompletionTokens != 250 || ready.TotalTokens != 1250 ||
		ready.LLMCostUSD != 0.00375 || !ready.LLMCostKnown ||
		ready.TTSCostUSD != 0.0012 || ready.MusicCostUSD != 0.16 {
		t.Fatalf("usage mismatch: %+v", ready)
	}

	list, err := store.List(ctx, owner, 0, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("List returned %+v, want created discussion", list)
	}

	deleted, err := store.Delete(ctx, owner, created.ID)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !deleted {
		t.Fatal("Delete returned false")
	}
	if got, err := store.Get(ctx, owner, created.ID); err != nil {
		t.Fatalf("Get after delete: %v", err)
	} else if got != nil {
		t.Fatalf("Get after delete returned %+v", got)
	}
}

func TestDiscussionStoreListOrderingAndPagination(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "native-discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()

	owner := "oauth:user-1"
	// Create discussions in a known order; each Create stamps created_at = time.Now(),
	// so later creations are newer and must sort first.
	var ids []string
	for i := 0; i < 5; i++ {
		d, err := store.Create(ctx, owner, "topic", planResponse{
			Script: &config.DebateTopic{Title: "T", Type: config.ContentTypeDiscussion, Language: "en-US"},
		})
		if err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
		ids = append(ids, d.ID)
		time.Sleep(2 * time.Millisecond)
	}

	// Newest first: the reverse of creation order.
	page1, err := store.List(ctx, owner, 2, 0)
	if err != nil {
		t.Fatalf("List page1: %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("page1 len = %d, want 2", len(page1))
	}
	if page1[0].ID != ids[4] || page1[1].ID != ids[3] {
		t.Fatalf("page1 order = [%s %s], want newest-first [%s %s]",
			page1[0].ID, page1[1].ID, ids[4], ids[3])
	}
	if !page1[0].CreatedAt.After(page1[1].CreatedAt) && !page1[0].CreatedAt.Equal(page1[1].CreatedAt) {
		t.Fatalf("page1 not sorted by created_at desc: %v then %v", page1[0].CreatedAt, page1[1].CreatedAt)
	}

	// Offset paginates without overlap.
	page2, err := store.List(ctx, owner, 2, 2)
	if err != nil {
		t.Fatalf("List page2: %v", err)
	}
	if len(page2) != 2 || page2[0].ID != ids[2] || page2[1].ID != ids[1] {
		t.Fatalf("page2 unexpected: %+v", page2)
	}

	// Past the end yields an empty (non-nil) slice.
	page4, err := store.List(ctx, owner, 2, 10)
	if err != nil {
		t.Fatalf("List page4: %v", err)
	}
	if page4 == nil || len(page4) != 0 {
		t.Fatalf("page4 = %+v, want empty slice", page4)
	}
}
