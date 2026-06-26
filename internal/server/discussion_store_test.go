package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/planner"
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
	if ready.Markdown != "# AI Safety Panel" || len(ready.Sources) != 1 {
		t.Fatalf("ready full content missing: %+v", ready)
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
	if list[0].Title != "AI Safety Panel" {
		t.Fatalf("List should retain title for row rendering: %+v", list[0])
	}
	if list[0].Script != nil || list[0].Markdown != "" || len(list[0].Sources) != 0 {
		t.Fatalf("List should omit heavy script/markdown/sources payloads: script=%+v markdown=%q sources=%+v", list[0].Script, list[0].Markdown, list[0].Sources)
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

func TestDiscussionStoreListJoinsVideoJobsForStatus(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "shared.db")
	jobs, err := NewJobRegistry(dbPath, "", "")
	if err != nil {
		t.Fatalf("NewJobRegistry: %v", err)
	}
	store, err := NewDiscussionStore(dbPath, "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()

	owner := "oauth:user-1"
	created, err := store.Create(ctx, owner, "AI safety", planResponse{
		Script: &config.DebateTopic{Title: "AI Safety", Type: config.ContentTypeDiscussion, Language: "en-US"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.SetJob(ctx, owner, created.ID, "job-ready"); err != nil {
		t.Fatalf("SetJob: %v", err)
	}
	jobs.Add("job-ready")
	jobs.Update("job-ready", func(j *Job) {
		j.Status = JobDone
		j.HasAudio = true
	})

	items, err := store.List(ctx, owner, 10, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("List len = %d, want 1", len(items))
	}
	if items[0].Status != DiscussionReady {
		t.Fatalf("status = %q, want %q", items[0].Status, DiscussionReady)
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

func TestDiscussionStoreListAndSearchByVisibility(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "native-discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()

	owner := "oauth:user-1"
	mk := func(topic, title string) *Discussion {
		d, err := store.Create(ctx, owner, topic, planResponse{
			Script: &config.DebateTopic{Title: title, Type: config.ContentTypeDiscussion, Language: "en-US"},
		})
		if err != nil {
			t.Fatalf("Create %s: %v", title, err)
		}
		return d
	}
	private := mk("station visibility", "Private Station")
	public := mk("station visibility", "Public Station")
	if _, err := store.SetVisibility(ctx, owner, public.ID, DiscussionPublic, DiscussionCover{
		Type:          "gradient",
		GradientStart: "#111111",
		GradientEnd:   "#777777",
	}); err != nil {
		t.Fatalf("SetVisibility public: %v", err)
	}

	publicRows, err := store.ListByVisibility(ctx, owner, DiscussionPublic, 20, 0)
	if err != nil {
		t.Fatalf("ListByVisibility public: %v", err)
	}
	if len(publicRows) != 1 || publicRows[0].ID != public.ID {
		t.Fatalf("public rows = %+v, want only %s", publicRows, public.ID)
	}

	privateRows, err := store.SearchByVisibility(ctx, owner, "station", DiscussionPrivate, 20, 0)
	if err != nil {
		t.Fatalf("SearchByVisibility private: %v", err)
	}
	if len(privateRows) != 1 || privateRows[0].ID != private.ID {
		t.Fatalf("private search rows = %+v, want only %s", privateRows, private.ID)
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
		Sources:  []config.Source{{Title: "Orbital Source", URL: "https://example.com/orbit", Markdown: "Large source body"}},
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
	if !published.ShowUsageSummary {
		t.Fatalf("published owner ShowUsageSummary = false, want true")
	}

	public, err := store.ListPublic(ctx, viewer, "orbital", 20, 0)
	if err != nil {
		t.Fatalf("ListPublic: %v", err)
	}
	if len(public) != 1 || public[0].ID != created.ID || public[0].IsLiked || public[0].IsOwner {
		t.Fatalf("ListPublic = %+v, want one unliked non-owner item", public)
	}
	if public[0].ShowUsageSummary {
		t.Fatalf("ListPublic ShowUsageSummary = true, want false for non-owner")
	}
	assertMarketListPayloadIsLightweight(t, public[0])
	if public[0].Creator == nil {
		t.Fatalf("ListPublic missing creator profile: %+v", public[0])
	}

	visible, err := store.GetVisible(ctx, viewer, created.ID)
	if err != nil {
		t.Fatalf("GetVisible: %v", err)
	}
	if visible == nil || visible.Script == nil || visible.Script.Title != "Space Markets" ||
		visible.Markdown != resp.Markdown || len(visible.Sources) != 1 {
		t.Fatalf("GetVisible full content = %+v", visible)
	}
	if visible.ShowUsageSummary {
		t.Fatalf("GetVisible ShowUsageSummary = true, want false for non-owner")
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
	assertMarketListPayloadIsLightweight(t, likedPage[0])
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

func TestSanitizeDiscussionUsageHidesPointsForNonCreator(t *testing.T) {
	srv := New(Deps{})
	creator := &Discussion{PointsCharged: 42, ShowUsageSummary: true}
	viewer := &Discussion{PointsCharged: 42, ShowUsageSummary: false}

	srv.sanitizeDiscussionUsage(creator)
	srv.sanitizeDiscussionUsage(viewer)

	if creator.PointsCharged != 42 {
		t.Fatalf("creator PointsCharged = %d, want 42", creator.PointsCharged)
	}
	if viewer.PointsCharged != 0 {
		t.Fatalf("viewer PointsCharged = %d, want 0", viewer.PointsCharged)
	}
}

func assertMarketListPayloadIsLightweight(t *testing.T, d Discussion) {
	t.Helper()
	if d.Script != nil || d.Markdown != "" || len(d.Sources) != 0 {
		t.Fatalf("market list row should omit heavy script/markdown/sources payloads: script=%+v markdown=%q sources=%+v", d.Script, d.Markdown, d.Sources)
	}
}

func TestDiscussionStoreCreatorProfilesAndFollows(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "native-discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()

	owner := "oauth:owner"
	viewer := "oauth:viewer"
	if err := store.UpsertCreatorProfile(ctx, CreatorProfile{
		ID:          owner,
		DisplayName: "Creator One",
		Username:    "creator",
		AvatarURL:   "https://auth.example/avatar.png",
	}); err != nil {
		t.Fatalf("UpsertCreatorProfile: %v", err)
	}
	d, err := store.Create(ctx, owner, "creator topic", planResponse{
		Script: &config.DebateTopic{Title: "Creator Station", Type: config.ContentTypeDiscussion, Language: "en-US"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.SetJob(ctx, owner, d.ID, "job-creator"); err != nil {
		t.Fatalf("SetJob: %v", err)
	}
	if _, err := store.SetVisibility(ctx, owner, d.ID, DiscussionPublic, DiscussionCover{Type: "gradient", GradientStart: "#111111", GradientEnd: "#777777"}); err != nil {
		t.Fatalf("SetVisibility: %v", err)
	}

	profile, err := store.CreatorProfile(ctx, viewer, owner)
	if err != nil {
		t.Fatalf("CreatorProfile: %v", err)
	}
	if profile == nil || profile.DisplayName != "Creator One" || profile.AvatarURL == "" || profile.IsFollowed || profile.IsSelf {
		t.Fatalf("creator profile = %+v", profile)
	}

	followed, err := store.FollowCreator(ctx, viewer, owner)
	if err != nil {
		t.Fatalf("FollowCreator: %v", err)
	}
	if followed == nil || !followed.IsFollowed || followed.FollowerCount != 1 {
		t.Fatalf("followed profile = %+v", followed)
	}
	following, err := store.ListFollowing(ctx, viewer, 20, 0)
	if err != nil {
		t.Fatalf("ListFollowing: %v", err)
	}
	if len(following) != 1 || following[0].ID != owner {
		t.Fatalf("following = %+v", following)
	}

	stations, err := store.ListByCreator(ctx, viewer, owner, "", 20, 0)
	if err != nil {
		t.Fatalf("ListByCreator: %v", err)
	}
	if len(stations) != 1 || stations[0].Creator == nil || stations[0].Creator.DisplayName != "Creator One" {
		t.Fatalf("creator stations = %+v", stations)
	}
	assertMarketListPayloadIsLightweight(t, stations[0])

	unfollowed, err := store.UnfollowCreator(ctx, viewer, owner)
	if err != nil {
		t.Fatalf("UnfollowCreator: %v", err)
	}
	if unfollowed == nil || unfollowed.IsFollowed || unfollowed.FollowerCount != 0 {
		t.Fatalf("unfollowed profile = %+v", unfollowed)
	}
}

func TestDiscussionStoreAttachCreatorProfilesBatchesMultipleCreators(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "native-discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()

	viewer := "oauth:viewer"
	owners := []string{"oauth:owner-a", "oauth:owner-b"}
	for i, owner := range owners {
		if err := store.UpsertCreatorProfile(ctx, CreatorProfile{
			ID:          owner,
			DisplayName: fmt.Sprintf("Creator %d", i+1),
		}); err != nil {
			t.Fatalf("UpsertCreatorProfile %s: %v", owner, err)
		}
		d, err := store.Create(ctx, owner, fmt.Sprintf("topic %d", i+1), planResponse{
			Script: &config.DebateTopic{Title: fmt.Sprintf("Station %d", i+1), Type: config.ContentTypeDiscussion, Language: "en-US"},
		})
		if err != nil {
			t.Fatalf("Create %s: %v", owner, err)
		}
		if _, err := store.SetJob(ctx, owner, d.ID, fmt.Sprintf("job-%d", i+1)); err != nil {
			t.Fatalf("SetJob %s: %v", owner, err)
		}
		if _, err := store.SetVisibility(ctx, owner, d.ID, DiscussionPublic, DiscussionCover{Type: "gradient", GradientStart: "#111111", GradientEnd: "#777777"}); err != nil {
			t.Fatalf("SetVisibility %s: %v", owner, err)
		}
	}
	if followed, err := store.FollowCreator(ctx, viewer, owners[0]); err != nil {
		t.Fatalf("FollowCreator: %v", err)
	} else if followed == nil {
		t.Fatalf("FollowCreator returned nil")
	}

	stations, err := store.ListPublic(ctx, viewer, "", 20, 0)
	if err != nil {
		t.Fatalf("ListPublic: %v", err)
	}
	if len(stations) != 2 {
		t.Fatalf("ListPublic returned %d stations, want 2: %+v", len(stations), stations)
	}
	profiles := map[string]*CreatorProfile{}
	for _, station := range stations {
		profiles[station.OwnerUserID] = station.Creator
	}
	if profiles[owners[0]] == nil || profiles[owners[0]].DisplayName != "Creator 1" || !profiles[owners[0]].IsFollowed || profiles[owners[0]].FollowerCount != 1 {
		t.Fatalf("owner-a profile = %+v", profiles[owners[0]])
	}
	if profiles[owners[1]] == nil || profiles[owners[1]].DisplayName != "Creator 2" || profiles[owners[1]].IsFollowed || profiles[owners[1]].FollowerCount != 0 {
		t.Fatalf("owner-b profile = %+v", profiles[owners[1]])
	}
}

func TestRetryTransientDBConnection(t *testing.T) {
	ctx := context.Background()
	attempts := 0
	err := retryTransientDBConnection(ctx, func() error {
		attempts++
		if attempts == 1 {
			return errors.New("stream is closed: driver: bad connection")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retryTransientDBConnection returned %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}

	nonTransient := errors.New("syntax error")
	attempts = 0
	err = retryTransientDBConnection(ctx, func() error {
		attempts++
		return nonTransient
	})
	if !errors.Is(err, nonTransient) {
		t.Fatalf("retryTransientDBConnection returned %v, want %v", err, nonTransient)
	}
	if attempts != 1 {
		t.Fatalf("non-transient attempts = %d, want 1", attempts)
	}
}

func TestDiscussionStoreCreateFromVisiblePlan(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "native-discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()

	owner := "oauth:owner"
	viewer := "oauth:viewer"
	source, err := store.Create(ctx, owner, "space economy", planResponse{
		Script: &config.DebateTopic{
			Title:    "Space Markets",
			Type:     config.ContentTypeDiscussion,
			Language: "en-US",
		},
		Markdown:   "Orbital economics and reusable launch systems.",
		Sources:    []config.Source{{Title: "Launch data", URL: "https://example.com/launch"}},
		Researched: true,
	})
	if err != nil {
		t.Fatalf("Create source: %v", err)
	}

	if clone, err := store.CreateFromVisiblePlan(ctx, viewer, source.ID); err != nil || clone != nil {
		t.Fatalf("private source clone = (%+v, %v), want not found", clone, err)
	}
	if _, err := store.SetVisibility(ctx, owner, source.ID, DiscussionPublic, DiscussionCover{
		Type:          "gradient",
		GradientStart: "#111111",
		GradientEnd:   "#777777",
	}); err != nil {
		t.Fatalf("SetVisibility public: %v", err)
	}

	clone, err := store.CreateFromVisiblePlan(ctx, viewer, source.ID)
	if err != nil {
		t.Fatalf("CreateFromVisiblePlan: %v", err)
	}
	if clone == nil {
		t.Fatal("CreateFromVisiblePlan returned nil")
	}
	if clone.ID == source.ID || clone.OwnerUserID != viewer || clone.Visibility != DiscussionPrivate {
		t.Fatalf("clone identity/visibility = %+v, source id %s", clone, source.ID)
	}
	if clone.Status != DiscussionPlanning || clone.JobID != "" || clone.DownloadURL != "" || clone.PointsCharged != 0 {
		t.Fatalf("clone carried generated state: %+v", clone)
	}
	if clone.Script == nil || clone.Script.Title != "Space Markets" || clone.Markdown != "Orbital economics and reusable launch systems." {
		t.Fatalf("clone plan mismatch: %+v", clone)
	}
	if len(clone.Sources) != 1 || clone.Sources[0].URL != "https://example.com/launch" || !clone.Researched {
		t.Fatalf("clone sources/research mismatch: %+v", clone)
	}
	if len(clone.EditTurns) != 1 || clone.EditTurns[0].Role != "plan" || clone.EditTurns[0].Text != "Current plan" {
		t.Fatalf("clone edit turns = %+v, want current plan snapshot", clone.EditTurns)
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
	generating, err := store.SetJob(ctx, owner, d.ID, "job-live")
	if err != nil {
		t.Fatalf("SetJob: %v", err)
	}
	if generating == nil || !generating.AllowSendingMessage {
		t.Fatalf("generating AllowSendingMessage = %+v, want true", generating)
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
	ready, err := store.Get(ctx, owner, d.ID)
	if err != nil {
		t.Fatalf("Get ready: %v", err)
	}
	if ready == nil || ready.AllowSendingMessage {
		t.Fatalf("ready AllowSendingMessage = %+v, want false", ready)
	}
	data, err := json.Marshal(ready)
	if err != nil {
		t.Fatalf("marshal ready JSON: %v", err)
	}
	var encoded map[string]any
	if err := json.Unmarshal(data, &encoded); err != nil {
		t.Fatalf("decode ready JSON: %v", err)
	}
	if got, ok := encoded["allowSendingMessage"].(bool); !ok || got {
		t.Fatalf("allowSendingMessage JSON = %#v, want false", encoded["allowSendingMessage"])
	}
	if err := store.AuthorizeJobParticipation(ctx, owner, d.ID, "job-live"); !errors.Is(err, errDiscussionForbidden) {
		t.Fatalf("ready owner job participation = %v, want forbidden", err)
	}
	if err := store.AuthorizeDiscussionParticipation(ctx, owner, d.ID); !errors.Is(err, errDiscussionForbidden) {
		t.Fatalf("ready owner discussion participation = %v, want forbidden", err)
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

func TestDiscussionStoreSetCoverLeavesVisibilityPrivate(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "native-discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()

	owner := "oauth:owner"
	d, err := store.CreatePlaceholder(ctx, owner, "cover anytime", "en-US", planner.DefaultTemplateID)
	if err != nil {
		t.Fatalf("CreatePlaceholder: %v", err)
	}

	// A gradient cover can be set without publishing.
	updated, err := store.SetCover(ctx, owner, d.ID, DiscussionCover{
		Type:          "gradient",
		GradientStart: "#8E5CF7",
		GradientEnd:   "#00A3FF",
	})
	if err != nil {
		t.Fatalf("SetCover gradient: %v", err)
	}
	if updated.Visibility != DiscussionPrivate {
		t.Fatalf("visibility = %q, want private after SetCover", updated.Visibility)
	}
	if updated.Cover.GradientStart != "#8E5CF7" || updated.Cover.GradientEnd != "#00A3FF" {
		t.Fatalf("gradient cover not persisted: %+v", updated.Cover)
	}

	// An AI image cover stores the durable key, not the signed URL.
	imageKey := "covers/oauth-owner/" + d.ID + ".webp"
	updated, err = store.SetCover(ctx, owner, d.ID, DiscussionCover{
		Type:     "ai",
		ImageURL: "https://storage.example/" + imageKey + "?X-Amz-Expires=900",
		ImageKey: imageKey,
		Prompt:   "a clean flat cover",
	})
	if err != nil {
		t.Fatalf("SetCover ai: %v", err)
	}
	if updated.Cover.ImageKey != imageKey || updated.Cover.ImageURL != "" {
		t.Fatalf("ai cover = %+v, want durable key without stored signed URL", updated.Cover)
	}
	if updated.Visibility != DiscussionPrivate {
		t.Fatalf("visibility = %q, want still private", updated.Visibility)
	}

	// Setting a cover for an unknown id reports no rows updated.
	missing, err := store.SetCover(ctx, owner, "does-not-exist", DiscussionCover{Type: "gradient", GradientStart: "#000", GradientEnd: "#fff"})
	if err != nil {
		t.Fatalf("SetCover missing: %v", err)
	}
	if missing != nil {
		t.Fatalf("SetCover for missing id = %+v, want nil", missing)
	}
}

func TestDiscussionStoreCreatePlaceholderPersistsTemplate(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "native-discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()

	created, err := store.CreatePlaceholder(ctx, "oauth:owner", "template topic", "en-US", planner.DefaultTemplateID)
	if err != nil {
		t.Fatalf("CreatePlaceholder: %v", err)
	}
	if created.Template != planner.DefaultTemplateID {
		t.Fatalf("created template = %q, want %q", created.Template, planner.DefaultTemplateID)
	}
	stored, err := store.Get(ctx, "oauth:owner", created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Template != planner.DefaultTemplateID {
		t.Fatalf("stored template = %q, want %q", stored.Template, planner.DefaultTemplateID)
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

func TestDiscussionStoreVoiceMessageLineRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "native-discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()

	owner := "oauth:owner"
	d, err := store.CreatePlaceholder(ctx, owner, "voice round trip", "en-US", planner.DefaultTemplateID)
	if err != nil {
		t.Fatalf("CreatePlaceholder: %v", err)
	}

	const (
		key = "uploads/oauth-owner/abc123.m4a"
		url = "https://storage.example/" + key + "?X-Amz-Expires=900"
	)
	line := DiscussionLine{
		Speaker:  "Listener",
		Role:     "user",
		Text:     "what about the counterargument?",
		IsUser:   true,
		AudioURL: url,
		AudioKey: key,
	}
	if err := store.AppendLine(ctx, owner, d.ID, line); err != nil {
		t.Fatalf("AppendLine: %v", err)
	}

	lines, err := store.Lines(ctx, owner, d.ID)
	if err != nil {
		t.Fatalf("Lines: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("lines = %+v, want exactly one", lines)
	}
	got := lines[0]
	if got.AudioURL != url {
		t.Fatalf("AudioURL = %q, want %q", got.AudioURL, url)
	}
	if got.AudioKey != key {
		t.Fatalf("AudioKey = %q, want %q", got.AudioKey, key)
	}
	if got.Text != line.Text {
		t.Fatalf("Text = %q, want %q", got.Text, line.Text)
	}

	// A second voice note with the SAME transcript but a distinct upload key must
	// coexist — it is a different recording, not a duplicate. The legacy text-only
	// uniqueness would have dropped it (and lost its audio).
	const key2 = "uploads/oauth-owner/def456.m4a"
	if err := store.AppendLine(ctx, owner, d.ID, DiscussionLine{
		Speaker: "Listener", Role: "user", Text: line.Text, IsUser: true,
		AudioURL: "https://storage.example/" + key2, AudioKey: key2,
	}); err != nil {
		t.Fatalf("AppendLine second voice: %v", err)
	}
	// A plain text line with the same transcript (no audio) must also coexist.
	if err := store.AppendLine(ctx, owner, d.ID, DiscussionLine{
		Speaker: "Listener", Role: "user", Text: line.Text, IsUser: true,
	}); err != nil {
		t.Fatalf("AppendLine text dup: %v", err)
	}
	lines, err = store.Lines(ctx, owner, d.ID)
	if err != nil {
		t.Fatalf("Lines after dups: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("lines after dups = %d, want 3 (two voice + one text)", len(lines))
	}

	// Re-appending the exact same voice note (identical key) is still idempotent.
	if err := store.AppendLine(ctx, owner, d.ID, line); err != nil {
		t.Fatalf("AppendLine idempotent re-send: %v", err)
	}
	if again, err := store.Lines(ctx, owner, d.ID); err != nil {
		t.Fatalf("Lines after re-send: %v", err)
	} else if len(again) != 3 {
		t.Fatalf("lines after identical re-send = %d, want 3", len(again))
	}
}

func TestDiscussionStoreLineUniquenessMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")

	// Stand up a legacy-shaped lines table: text-only uniqueness, no audio columns.
	raw, err := sql.Open("sqlite3", sqliteDSN(path))
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `CREATE TABLE native_discussion_lines (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		discussion_id TEXT NOT NULL,
		speaker TEXT NOT NULL,
		role TEXT NOT NULL,
		side TEXT NOT NULL DEFAULT '',
		text TEXT NOT NULL,
		start_ms INTEGER NOT NULL DEFAULT 0,
		is_user INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL,
		UNIQUE(discussion_id, speaker, role, text, is_user),
		FOREIGN KEY(discussion_id) REFERENCES native_discussions(id) ON DELETE CASCADE
	)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `INSERT INTO native_discussion_lines
		(discussion_id, speaker, role, side, text, start_ms, is_user, created_at)
		VALUES ('d1','Host','host','','welcome',0,0,1)`); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	// Opening the store runs ensureSchema → adds audio columns → migrates uniqueness.
	store, err := NewDiscussionStore(path, "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()

	var ddl string
	if err := store.db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='native_discussion_lines'`).Scan(&ddl); err != nil {
		t.Fatalf("read ddl: %v", err)
	}
	if !strings.Contains(ddl, "is_user, audio_key") {
		t.Fatalf("post-migration ddl missing audio_key uniqueness:\n%s", ddl)
	}

	// The pre-existing row survived the table rebuild.
	var n int
	if err := store.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM native_discussion_lines WHERE text='welcome'`).Scan(&n); err != nil {
		t.Fatalf("count legacy: %v", err)
	}
	if n != 1 {
		t.Fatalf("legacy row count = %d, want 1", n)
	}

	// Re-running ensureSchema is an idempotent no-op (no second rebuild).
	if err := store.ensureSchema(ctx); err != nil {
		t.Fatalf("re-run ensureSchema: %v", err)
	}
}

func TestDiscussionSpeakerModelOverridesSurvivePlanRegeneration(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "native-discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()

	owner := "oauth:user-1"
	created, err := store.Create(ctx, owner, "AI safety", planResponse{
		Script: &config.DebateTopic{
			Title:    "AI Safety Panel",
			Type:     config.ContentTypeDiscussion,
			Language: "en-US",
			Host:     config.AgentSpec{Name: "Host", Model: "model-a"},
			Discussants: []config.AgentSpec{
				{Name: "Alice", Model: "model-a", Aspect: "technical"},
				{Name: "Bob", Model: "model-a", Aspect: "policy"},
			},
			Commander: config.AgentSpec{Model: "model-a"},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	updated, err := store.SetSpeakerModel(ctx, owner, created.ID, "Alice", "model-x")
	if err != nil {
		t.Fatalf("SetSpeakerModel: %v", err)
	}
	if got := discussantModel(t, updated.Script, "Alice"); got != "model-x" {
		t.Fatalf("Alice model after update = %q, want model-x", got)
	}
	if got := discussantModel(t, updated.Script, "Bob"); got != "model-a" {
		t.Fatalf("Bob model after Alice update = %q, want model-a", got)
	}

	updated, err = store.UpdatePlan(ctx, owner, created.ID, planResponse{
		Script: &config.DebateTopic{
			Title:    "Expanded AI Safety Panel",
			Type:     config.ContentTypeDiscussion,
			Language: "en-US",
			Host:     config.AgentSpec{Name: "Host", Model: "model-b"},
			Discussants: []config.AgentSpec{
				{Name: "Alice", Model: "model-b", Aspect: "technical"},
				{Name: "Charlie", Model: "model-b", Aspect: "economic"},
			},
			Commander: config.AgentSpec{Model: "model-b"},
		},
	})
	if err != nil {
		t.Fatalf("UpdatePlan with new speaker: %v", err)
	}
	if got := discussantModel(t, updated.Script, "Alice"); got != "model-x" {
		t.Fatalf("Alice model after regenerated plan = %q, want model-x", got)
	}
	if got := discussantModel(t, updated.Script, "Charlie"); got != "model-b" {
		t.Fatalf("Charlie model after regenerated plan = %q, want model-b", got)
	}
	rowsAfterAdd := speakerModelRows(t, store, created.ID)
	if got := rowsAfterAdd["Alice"]; got != "model-x" {
		t.Fatalf("speaker model row Alice after add = %q, want model-x; rows=%+v", got, rowsAfterAdd)
	}
	if got := rowsAfterAdd["Bob"]; got != "model-a" {
		t.Fatalf("speaker model row Bob after add = %q, want model-a; rows=%+v", got, rowsAfterAdd)
	}
	if got := rowsAfterAdd["Charlie"]; got != "model-b" {
		t.Fatalf("speaker model row Charlie after add = %q, want model-b; rows=%+v", got, rowsAfterAdd)
	}

	if _, err := store.UpdatePlan(ctx, owner, created.ID, planResponse{
		Script: &config.DebateTopic{
			Title:    "Reduced AI Safety Panel",
			Type:     config.ContentTypeDiscussion,
			Language: "en-US",
			Host:     config.AgentSpec{Name: "Host", Model: "model-c"},
			Discussants: []config.AgentSpec{
				{Name: "Charlie", Model: "model-c", Aspect: "economic"},
			},
			Commander: config.AgentSpec{Model: "model-c"},
		},
	}); err != nil {
		t.Fatalf("UpdatePlan removing Alice: %v", err)
	}
	rowsAfterRemove := speakerModelRows(t, store, created.ID)
	if !sameStringMap(rowsAfterRemove, rowsAfterAdd) {
		t.Fatalf("speaker model rows changed after removing a speaker: got %+v, want %+v", rowsAfterRemove, rowsAfterAdd)
	}
	updated, err = store.UpdatePlan(ctx, owner, created.ID, planResponse{
		Script: &config.DebateTopic{
			Title:    "Readded AI Safety Panel",
			Type:     config.ContentTypeDiscussion,
			Language: "en-US",
			Host:     config.AgentSpec{Name: "Host", Model: "model-d"},
			Discussants: []config.AgentSpec{
				{Name: "Alice", Model: "model-d", Aspect: "technical"},
				{Name: "Charlie", Model: "model-d", Aspect: "economic"},
			},
			Commander: config.AgentSpec{Model: "model-d"},
		},
	})
	if err != nil {
		t.Fatalf("UpdatePlan readding Alice: %v", err)
	}
	if got := discussantModel(t, updated.Script, "Alice"); got != "model-x" {
		t.Fatalf("Alice model after readd = %q, want model-x", got)
	}

	if _, err := store.SetJob(ctx, owner, created.ID, "job-speaker-models"); err != nil {
		t.Fatalf("SetJob: %v", err)
	}
	byJob, err := store.GetByJobID(ctx, "job-speaker-models")
	if err != nil {
		t.Fatalf("GetByJobID: %v", err)
	}
	if got := discussantModel(t, byJob.Script, "Alice"); got != "model-x" {
		t.Fatalf("Alice model by job id = %q, want model-x", got)
	}
}

func discussantModel(t *testing.T, script *config.DebateTopic, name string) string {
	t.Helper()
	if script == nil {
		t.Fatal("script is nil")
	}
	for _, d := range script.Discussants {
		if d.Name == name {
			return d.Model
		}
	}
	t.Fatalf("discussant %q not found in %+v", name, script.Discussants)
	return ""
}

func speakerModelRows(t *testing.T, store *DiscussionStore, discussionID string) map[string]string {
	t.Helper()
	rows, err := store.db.Query(`SELECT speaker_name, model
		FROM native_discussion_speaker_models WHERE discussion_id = ?`, discussionID)
	if err != nil {
		t.Fatalf("query speaker model rows: %v", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var speaker, model string
		if err := rows.Scan(&speaker, &model); err != nil {
			t.Fatalf("scan speaker model row: %v", err)
		}
		out[speaker] = model
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("speaker model row iteration: %v", err)
	}
	return out
}

func sameStringMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		if b[k] != av {
			return false
		}
	}
	return true
}

// TestDiscussionLineSenderAndOrdering covers two guarantees the player relies on:
// (1) sender_user_id is server-owned — stamped from the authenticated principal on
// user lines and cleared on agent lines, never trusted from the caller's payload;
// and (2) lines read back in true chronological order by created_at, so a user
// message sent between two agent turns interleaves between them even though the
// agent lines are (re-)inserted by ReplaceTranscript after the user line.
func TestDiscussionLineSenderAndOrdering(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "native-discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()

	owner := "oauth:owner-1"
	created, err := store.Create(ctx, owner, "topic", planResponse{
		Script: &config.DebateTopic{
			Title:    "Ordering Panel",
			Type:     config.ContentTypeDiscussion,
			Language: "en-US",
			Host:     config.AgentSpec{Name: "Host"},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	base := time.Now()
	// Client tries to spoof a different sender id; the store must overwrite it with
	// the authenticated owner. The user line is sent "now" (created_at ~ base).
	if err := store.AppendLine(ctx, owner, created.ID, DiscussionLine{
		Speaker:      "Me",
		Role:         "user",
		Text:         "second",
		IsUser:       true,
		SenderUserID: "oauth:attacker",
	}); err != nil {
		t.Fatalf("AppendLine: %v", err)
	}
	// Agent turns straddle the user line in wall-clock time. They are inserted after
	// the user line, so id-order would clump them after it; created_at-order must not.
	if err := store.ReplaceTranscript(ctx, created.ID, []agent.TranscriptLine{
		{Speaker: "Host", Role: agent.RoleHost, Text: "first", At: base.Add(-2 * time.Second)},
		{Speaker: "Host", Role: agent.RoleHost, Text: "third", At: base.Add(2 * time.Second)},
	}); err != nil {
		t.Fatalf("ReplaceTranscript: %v", err)
	}

	got, err := store.Get(ctx, owner, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Lines) != 3 {
		t.Fatalf("line count = %d, want 3: %+v", len(got.Lines), got.Lines)
	}
	wantOrder := []string{"first", "second", "third"}
	for i, w := range wantOrder {
		if got.Lines[i].Text != w {
			t.Fatalf("line[%d].Text = %q, want %q (full order %+v)", i, got.Lines[i].Text, w, got.Lines)
		}
	}
	// User line carries the authenticated owner id, not the spoofed value.
	if userLine := got.Lines[1]; userLine.SenderUserID != owner {
		t.Fatalf("user line SenderUserID = %q, want %q", userLine.SenderUserID, owner)
	}
	// Agent lines never carry a sender id.
	for _, idx := range []int{0, 2} {
		if got.Lines[idx].SenderUserID != "" {
			t.Fatalf("agent line[%d] SenderUserID = %q, want empty", idx, got.Lines[idx].SenderUserID)
		}
	}
}
