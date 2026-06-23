package server

import (
	"context"
	"errors"
	"fmt"
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

func TestDiscussionStoreSearch(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "native-discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()

	owner := "oauth:user-1"
	otherOwner := "oauth:user-2"

	// title comes from Script.Title, markdown from planResponse.Markdown, and the
	// topic is the second Create argument — each row isolates one searchable field.
	mk := func(o, topic, title, markdown string) *Discussion {
		d, err := store.Create(ctx, o, topic, planResponse{
			Script:   &config.DebateTopic{Title: title, Type: config.ContentTypeDiscussion, Language: "en-US"},
			Markdown: markdown,
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		return d
	}

	byTopic := mk(owner, "Quantum computing breakthroughs", "Panel A", "body a")
	byTitle := mk(owner, "topic b", "Climate Policy Debate", "body b")
	byMarkdown := mk(owner, "topic c", "Panel C", "A deep dive into renewable energy.")
	mk(owner, "unrelated", "Panel D", "nothing here")
	// Another user owns a row that would match — it must never surface.
	mk(otherOwner, "Quantum leaps", "Quantum Panel", "quantum body")

	ids := func(ds []Discussion) map[string]bool {
		out := map[string]bool{}
		for _, d := range ds {
			out[d.ID] = true
		}
		return out
	}

	// Matches on topic.
	res, err := store.Search(ctx, owner, "quantum", 0, 0)
	if err != nil {
		t.Fatalf("Search topic: %v", err)
	}
	if got := ids(res); len(got) != 1 || !got[byTopic.ID] {
		t.Fatalf("topic search = %+v, want only %s (owner-scoped)", res, byTopic.ID)
	}

	// Matches on title.
	if res, err = store.Search(ctx, owner, "climate", 0, 0); err != nil {
		t.Fatalf("Search title: %v", err)
	}
	if got := ids(res); len(got) != 1 || !got[byTitle.ID] {
		t.Fatalf("title search = %+v, want only %s", res, byTitle.ID)
	}

	// Matches on markdown body.
	if res, err = store.Search(ctx, owner, "renewable", 0, 0); err != nil {
		t.Fatalf("Search markdown: %v", err)
	}
	if got := ids(res); len(got) != 1 || !got[byMarkdown.ID] {
		t.Fatalf("markdown search = %+v, want only %s", res, byMarkdown.ID)
	}

	// No matches yields an empty (non-nil) slice.
	if res, err = store.Search(ctx, owner, "nonexistent-xyz", 0, 0); err != nil {
		t.Fatalf("Search miss: %v", err)
	}
	if res == nil || len(res) != 0 {
		t.Fatalf("miss search = %+v, want empty slice", res)
	}

	// LIKE wildcards in the query are matched literally, not as wildcards: "%"
	// must not match the rows that contain no literal percent sign.
	if res, err = store.Search(ctx, owner, "%", 0, 0); err != nil {
		t.Fatalf("Search wildcard: %v", err)
	}
	if len(res) != 0 {
		t.Fatalf("literal %% search = %+v, want no matches (wildcards escaped)", res)
	}
}

func TestDiscussionStoreMarketPublishSearchAndLikes(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "native-discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()

	owner := "oauth:owner"
	viewer := "oauth:viewer"
	otherViewer := "oauth:other"
	resp := planResponse{
		Script: &config.DebateTopic{
			Title:    "Space Markets",
			Type:     config.ContentTypeDiscussion,
			Language: "en-US",
		},
		Markdown: "Orbital economics and reusable launch systems.",
	}
	created, err := store.Create(ctx, owner, "space economy", resp)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.SetJob(ctx, owner, created.ID, "job-market"); err != nil {
		t.Fatalf("SetJob: %v", err)
	}

	if _, err := store.SetVisibility(ctx, owner, created.ID, DiscussionPublic, DiscussionCover{}); err == nil {
		t.Fatal("SetVisibility public without cover succeeded")
	}
	cover := DiscussionCover{Type: "gradient", GradientStart: "#111111", GradientEnd: "#777777"}
	published, err := store.SetVisibility(ctx, owner, created.ID, DiscussionPublic, cover)
	if err != nil {
		t.Fatalf("SetVisibility public: %v", err)
	}
	if published.Visibility != DiscussionPublic || published.PublishedAt == nil || !published.Cover.Valid() {
		t.Fatalf("published discussion mismatch: %+v", published)
	}

	public, err := store.ListPublic(ctx, viewer, "orbital", 20, 0)
	if err != nil {
		t.Fatalf("ListPublic: %v", err)
	}
	if len(public) != 1 || public[0].ID != created.ID || public[0].IsLiked || public[0].IsOwner {
		t.Fatalf("ListPublic = %+v, want one unliked non-owner item", public)
	}

	liked, err := store.Like(ctx, viewer, created.ID)
	if err != nil {
		t.Fatalf("Like: %v", err)
	}
	if liked == nil || !liked.IsLiked || liked.LikeCount != 1 {
		t.Fatalf("Like returned %+v", liked)
	}
	likedPage, err := store.ListLiked(ctx, viewer, "", 20, 0)
	if err != nil {
		t.Fatalf("ListLiked: %v", err)
	}
	if len(likedPage) != 1 || likedPage[0].ID != created.ID || !likedPage[0].IsLiked {
		t.Fatalf("ListLiked = %+v, want liked discussion", likedPage)
	}
	otherLikedPage, err := store.ListLiked(ctx, otherViewer, "", 20, 0)
	if err != nil {
		t.Fatalf("ListLiked other: %v", err)
	}
	if len(otherLikedPage) != 0 {
		t.Fatalf("other viewer liked page = %+v, want empty", otherLikedPage)
	}

	unliked, err := store.Unlike(ctx, viewer, created.ID)
	if err != nil {
		t.Fatalf("Unlike: %v", err)
	}
	if unliked == nil || unliked.IsLiked || unliked.LikeCount != 0 {
		t.Fatalf("Unlike returned %+v", unliked)
	}
}

func TestDiscussionStoreParticipationAuthorization(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "native-discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()

	owner := "oauth:owner"
	viewer := "oauth:viewer"
	d, err := store.Create(ctx, owner, "live station", planResponse{
		Script: &config.DebateTopic{Title: "Live Station", Type: config.ContentTypeDiscussion, Language: "en-US"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.SetJob(ctx, owner, d.ID, "job-live"); err != nil {
		t.Fatalf("SetJob: %v", err)
	}

	if err := store.AuthorizeJobOwner(ctx, owner, "job-live"); err != nil {
		t.Fatalf("owner force-stop authorization failed: %v", err)
	}
	if err := store.AuthorizeJobOwner(ctx, viewer, "job-live"); !errors.Is(err, errDiscussionForbidden) {
		t.Fatalf("viewer force-stop authorization = %v, want forbidden", err)
	}
	if err := store.AuthorizeJobParticipation(ctx, viewer, d.ID, "job-live"); !errors.Is(err, errDiscussionForbidden) {
		t.Fatalf("private viewer participation = %v, want forbidden", err)
	}
	if err := store.AuthorizeJobParticipation(ctx, owner, d.ID, "job-live"); err != nil {
		t.Fatalf("owner participation failed: %v", err)
	}
	if err := store.AuthorizeJobParticipation(ctx, viewer, d.ID, "wrong-job"); !errors.Is(err, errDiscussionNotVisible) {
		t.Fatalf("job mismatch participation = %v, want not visible", err)
	}
	if err := store.AppendLineVisible(ctx, viewer, d.ID, DiscussionLine{Speaker: "Viewer", Role: "user", Text: "hello", IsUser: true}); !errors.Is(err, errDiscussionForbidden) {
		t.Fatalf("private visible append = %v, want forbidden", err)
	}
	if lines, err := store.Lines(ctx, owner, d.ID); err != nil {
		t.Fatalf("Lines: %v", err)
	} else if len(lines) != 0 {
		t.Fatalf("private append persisted lines = %+v, want none", lines)
	}

	cover := DiscussionCover{Type: "gradient", GradientStart: "#111111", GradientEnd: "#777777"}
	if _, err := store.SetVisibility(ctx, owner, d.ID, DiscussionPublic, cover); err != nil {
		t.Fatalf("SetVisibility public: %v", err)
	}
	if err := store.AuthorizeJobParticipation(ctx, viewer, d.ID, "job-live"); err != nil {
		t.Fatalf("public generating viewer participation failed: %v", err)
	}
	if err := store.AppendLineVisible(ctx, viewer, d.ID, DiscussionLine{Speaker: "Viewer", Role: "user", Text: "hello", IsUser: true}); err != nil {
		t.Fatalf("public visible append: %v", err)
	}
	if err := store.SetJobResult(ctx, d.ID, DiscussionReady, "https://audio.example/live.mp3"); err != nil {
		t.Fatalf("SetJobResult ready: %v", err)
	}
	if err := store.AuthorizeDiscussionParticipation(ctx, viewer, d.ID); !errors.Is(err, errDiscussionForbidden) {
		t.Fatalf("ready public viewer participation = %v, want forbidden", err)
	}
}

func TestDiscussionStoreCoverImageKeyPersistedWithoutSignedURL(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "native-discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()

	owner := "oauth:owner"
	viewer := "oauth:viewer"
	d, err := store.Create(ctx, owner, "cover station", planResponse{
		Script: &config.DebateTopic{Title: "Cover Station", Type: config.ContentTypeDiscussion, Language: "en-US"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.SetJob(ctx, owner, d.ID, "job-cover"); err != nil {
		t.Fatalf("SetJob: %v", err)
	}
	signedURL := "https://storage.example/covers/cover.png?X-Amz-Expires=900"
	imageKey := "covers/oauth-owner/cover.png"
	published, err := store.SetVisibility(ctx, owner, d.ID, DiscussionPublic, DiscussionCover{
		Type:     "image",
		ImageURL: signedURL,
		ImageKey: imageKey,
	})
	if err != nil {
		t.Fatalf("SetVisibility public image: %v", err)
	}
	if published.Cover.ImageKey != imageKey {
		t.Fatalf("published image key = %q, want %q", published.Cover.ImageKey, imageKey)
	}
	if published.Cover.ImageURL != "" {
		t.Fatalf("published image url = %q, want empty stored URL", published.Cover.ImageURL)
	}
	if !published.Cover.Valid() {
		t.Fatalf("cover with durable key should be valid: %+v", published.Cover)
	}
	public, err := store.ListPublic(ctx, viewer, "", 20, 0)
	if err != nil {
		t.Fatalf("ListPublic: %v", err)
	}
	if len(public) != 1 || public[0].Cover.ImageKey != imageKey || public[0].Cover.ImageURL != "" {
		t.Fatalf("public cover = %+v, want durable key without stored signed URL", public)
	}
}

func TestDiscussionStoreEditTurnPagination(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "native-discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()

	owner := "oauth:user-1"
	d, err := store.Create(ctx, owner, "topic", planResponse{
		Script: &config.DebateTopic{Title: "T", Type: config.ContentTypeDiscussion, Language: "en-US"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := store.AppendEditTurn(ctx, owner, d.ID, "user", fmt.Sprintf("edit-%d", i)); err != nil {
			t.Fatalf("AppendEditTurn %d: %v", i, err)
		}
	}

	page1, more, err := store.EditTurnsPage(ctx, owner, d.ID, 2, 0)
	if err != nil {
		t.Fatalf("EditTurnsPage page1: %v", err)
	}
	if !more {
		t.Fatal("page1 more = false, want true")
	}
	if len(page1) != 2 || page1[0].Text != "edit-3" || page1[1].Text != "edit-4" {
		t.Fatalf("page1 = %+v, want latest two in chronological order", page1)
	}

	page2, more, err := store.EditTurnsPage(ctx, owner, d.ID, 2, page1[0].ID)
	if err != nil {
		t.Fatalf("EditTurnsPage page2: %v", err)
	}
	if !more {
		t.Fatal("page2 more = false, want true")
	}
	if len(page2) != 2 || page2[0].Text != "edit-1" || page2[1].Text != "edit-2" {
		t.Fatalf("page2 = %+v, want previous two in chronological order", page2)
	}

	page3, more, err := store.EditTurnsPage(ctx, owner, d.ID, 2, page2[0].ID)
	if err != nil {
		t.Fatalf("EditTurnsPage page3: %v", err)
	}
	if more {
		t.Fatal("page3 more = true, want false")
	}
	if len(page3) != 2 || page3[0].Role != "plan" || page3[1].Text != "edit-0" {
		t.Fatalf("page3 = %+v, want initial plan plus oldest edit", page3)
	}
}
