package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// anonUser matches requestUser's fallback identity for unauthenticated
// httptest requests, so store-seeded rows are visible to API calls.
const anonUser = "anonymous"

func seedAudioBook(t *testing.T, store *DiscussionStore, chapterCount int) *Discussion {
	t.Helper()
	plan := testAudioBookPlan(chapterCount)
	md, err := plan.RenderMarkdown()
	if err != nil {
		t.Fatalf("render seed plan: %v", err)
	}
	d, err := store.Create(context.Background(), anonUser, plan.Title, planResponse{Script: plan, Markdown: md})
	if err != nil || d == nil {
		t.Fatalf("seed audiobook: %v", err)
	}
	return d
}

// markGenerated records that a discussion ran a batch over the given chapter
// indices and finished: indices persisted on the plan, a job attached, and the
// status set to ready.
func markGenerated(t *testing.T, store *DiscussionStore, d *Discussion, indices []int, jobID string) {
	t.Helper()
	ctx := context.Background()
	plan := *d.Script
	plan.AudioBookChapterIndices = indices
	md, err := plan.RenderMarkdown()
	if err != nil {
		t.Fatalf("render generated plan: %v", err)
	}
	if _, err := store.UpdatePlan(ctx, anonUser, d.ID, planResponse{Script: &plan, Markdown: md}); err != nil {
		t.Fatalf("persist generated indices: %v", err)
	}
	if _, err := store.SetJob(ctx, anonUser, d.ID, jobID); err != nil {
		t.Fatalf("set job: %v", err)
	}
	if err := store.SetJobResult(ctx, d.ID, DiscussionReady, ""); err != nil {
		t.Fatalf("set ready: %v", err)
	}
}

func apiJSON(t *testing.T, method, url string, body any, into any) (int, string) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if into != nil && resp.StatusCode < 300 {
		if err := json.Unmarshal(raw, into); err != nil {
			t.Fatalf("decode %s %s: %v\n%s", method, url, err, raw)
		}
	}
	return resp.StatusCode, string(raw)
}

func TestDiscussionChaptersEndpointTracksProgress(t *testing.T) {
	env := newDiscussionAPITestEnv(t)
	root := seedAudioBook(t, env.store, 8)

	var chapters audioBookChaptersResponse
	status, body := apiJSON(t, "GET", env.ts.URL+"/api/discussions/"+root.ID+"/chapters", nil, &chapters)
	if status != http.StatusOK {
		t.Fatalf("chapters status = %d body=%s", status, body)
	}
	if chapters.RootID != root.ID || chapters.MaxBatchSize != audioBookMaxBatchChapters || len(chapters.Chapters) != 8 {
		t.Fatalf("chapters response = %+v", chapters)
	}
	for _, ch := range chapters.Chapters {
		if ch.Status != chapterStatusPending {
			t.Fatalf("fresh plan chapter %d status = %q, want pending", ch.Index, ch.Status)
		}
	}

	// Root generated chapters 1-3; a follow-up batch generated 4-5.
	markGenerated(t, env.store, root, []int{1, 2, 3}, "job-root")
	child, err := env.store.CreatePlaceholder(context.Background(), anonUser, root.Topic, "en-US", "default")
	if err != nil {
		t.Fatalf("create batch child: %v", err)
	}
	if _, err := env.store.SetReference(context.Background(), anonUser, child.ID, root.ID); err != nil {
		t.Fatalf("set reference: %v", err)
	}
	batch, err := deriveAudioBookBatchScript(root.Script, []int{4, 5}, "", false)
	if err != nil {
		t.Fatalf("derive child batch: %v", err)
	}
	child.Script = batch
	markGenerated(t, env.store, child, []int{4, 5}, "job-child")

	status, body = apiJSON(t, "GET", env.ts.URL+"/api/discussions/"+root.ID+"/chapters", nil, &chapters)
	if status != http.StatusOK {
		t.Fatalf("chapters status = %d body=%s", status, body)
	}
	wantStatus := map[int]string{1: "done", 2: "done", 3: "done", 4: "done", 5: "done", 6: "pending", 7: "pending", 8: "pending"}
	for _, ch := range chapters.Chapters {
		if ch.Status != wantStatus[ch.Index] {
			t.Fatalf("chapter %d status = %q, want %q (body=%s)", ch.Index, ch.Status, wantStatus[ch.Index], body)
		}
	}
	// The chain resolves the same from the batch child.
	var fromChild audioBookChaptersResponse
	status, body = apiJSON(t, "GET", env.ts.URL+"/api/discussions/"+child.ID+"/chapters", nil, &fromChild)
	if status != http.StatusOK || fromChild.RootID != root.ID || len(fromChild.Chapters) != 8 {
		t.Fatalf("child chapters status=%d root=%q chapters=%d body=%s", status, fromChild.RootID, len(fromChild.Chapters), body)
	}
}

func TestDiscussionGenerateChapterValidation(t *testing.T) {
	env := newDiscussionAPITestEnv(t)
	root := seedAudioBook(t, env.store, 8)

	generateURL := env.ts.URL + "/api/discussions/" + root.ID + "/generate"
	// Over the batch cap → 400.
	status, body := apiJSON(t, "POST", generateURL, map[string]any{"chapters": []int{1, 2, 3, 4, 5, 6}}, nil)
	if status != http.StatusBadRequest || !strings.Contains(body, "at most 5") {
		t.Fatalf("over-cap status=%d body=%s", status, body)
	}
	// Out of range → 400.
	status, body = apiJSON(t, "POST", generateURL, map[string]any{"chapters": []int{9}}, nil)
	if status != http.StatusBadRequest || !strings.Contains(body, "invalid chapter selection") {
		t.Fatalf("out-of-range status=%d body=%s", status, body)
	}
	// A chapter already generated by a follow-up batch → 400.
	child, err := env.store.CreatePlaceholder(context.Background(), anonUser, root.Topic, "en-US", "default")
	if err != nil {
		t.Fatalf("create batch child: %v", err)
	}
	if _, err := env.store.SetReference(context.Background(), anonUser, child.ID, root.ID); err != nil {
		t.Fatalf("set reference: %v", err)
	}
	batch, err := deriveAudioBookBatchScript(root.Script, []int{4, 5}, "", false)
	if err != nil {
		t.Fatalf("derive child batch: %v", err)
	}
	child.Script = batch
	markGenerated(t, env.store, child, []int{4, 5}, "job-child")
	status, body = apiJSON(t, "POST", generateURL, map[string]any{"chapters": []int{4}}, nil)
	if status != http.StatusBadRequest || !strings.Contains(body, "already generated") {
		t.Fatalf("already-generated status=%d body=%s", status, body)
	}
	// Chapter selection on a non-audiobook → 400.
	other := apiCreateDiscussion(t, env.ts.URL, "Plain talk show")
	status, body = apiJSON(t, "POST", env.ts.URL+"/api/discussions/"+other.ID+"/generate",
		map[string]any{"chapters": []int{1}}, nil)
	if status == http.StatusOK {
		t.Fatalf("non-audiobook chapter selection unexpectedly succeeded: %s", body)
	}

	// Follow-up batch endpoint: 409 before any batch is ready.
	fresh := seedAudioBook(t, env.store, 6)
	status, body = apiJSON(t, "POST", env.ts.URL+"/api/discussions/"+fresh.ID+"/chapters/generate",
		map[string]any{"chapters": []int{1, 2}}, nil)
	if status != http.StatusConflict {
		t.Fatalf("follow-up before first batch status=%d body=%s", status, body)
	}
	// Over-cap on the follow-up endpoint once the root is ready → 400.
	markGenerated(t, env.store, fresh, []int{1}, "job-fresh")
	status, body = apiJSON(t, "POST", env.ts.URL+"/api/discussions/"+fresh.ID+"/chapters/generate",
		map[string]any{"chapters": []int{2, 3, 4, 5, 6, 1}}, nil)
	if status != http.StatusBadRequest || !strings.Contains(body, "at most 5") {
		t.Fatalf("follow-up over-cap status=%d body=%s", status, body)
	}
}

func TestAlbumEndpoints(t *testing.T) {
	env := newDiscussionAPITestEnv(t)
	first := seedAudioBook(t, env.store, 3)
	second := seedAudioBook(t, env.store, 2)
	third := seedAudioBook(t, env.store, 2)

	// Manual album creation groups the given podcasts.
	var album Album
	status, body := apiJSON(t, "POST", env.ts.URL+"/api/albums",
		map[string]any{"title": "My Novel", "discussion_ids": []string{first.ID, second.ID}}, &album)
	if status != http.StatusOK || album.ID == "" || album.Kind != albumKindManual {
		t.Fatalf("create album status=%d album=%+v body=%s", status, album, body)
	}
	if album.EpisodeCount != 2 {
		t.Fatalf("album episode count = %d, want 2", album.EpisodeCount)
	}

	var albums []Album
	status, body = apiJSON(t, "GET", env.ts.URL+"/api/albums", nil, &albums)
	if status != http.StatusOK || len(albums) != 1 {
		t.Fatalf("list albums status=%d n=%d body=%s", status, len(albums), body)
	}

	var detail albumDetailResponse
	status, body = apiJSON(t, "GET", env.ts.URL+"/api/albums/"+album.ID, nil, &detail)
	if status != http.StatusOK || detail.Album == nil || len(detail.Episodes) != 2 {
		t.Fatalf("album detail status=%d body=%s", status, body)
	}

	// A podcast can't be pulled into a second album → 400.
	status, body = apiJSON(t, "POST", env.ts.URL+"/api/albums",
		map[string]any{"title": "Steal", "discussion_ids": []string{first.ID}}, nil)
	if status != http.StatusBadRequest || !strings.Contains(body, "already belongs") {
		t.Fatalf("conflict status=%d body=%s", status, body)
	}

	// Add + remove members.
	status, body = apiJSON(t, "POST", env.ts.URL+"/api/albums/"+album.ID+"/discussions",
		map[string]any{"discussion_ids": []string{third.ID}}, &album)
	if status != http.StatusOK || album.EpisodeCount != 3 {
		t.Fatalf("add member status=%d count=%d body=%s", status, album.EpisodeCount, body)
	}
	status, _ = apiJSON(t, "DELETE", env.ts.URL+"/api/albums/"+album.ID+"/discussions/"+third.ID, nil, nil)
	if status != http.StatusNoContent {
		t.Fatalf("remove member status=%d", status)
	}

	// Rename.
	status, body = apiJSON(t, "PATCH", env.ts.URL+"/api/albums/"+album.ID,
		map[string]any{"title": "Renamed Novel"}, &album)
	if status != http.StatusOK || album.Title != "Renamed Novel" {
		t.Fatalf("rename status=%d title=%q body=%s", status, album.Title, body)
	}
	status, body = apiJSON(t, "PATCH", env.ts.URL+"/api/albums/"+album.ID,
		map[string]any{"title": "  "}, nil)
	if status != http.StatusBadRequest {
		t.Fatalf("blank rename status=%d body=%s", status, body)
	}

	// Cover set persists on the album.
	status, body = apiJSON(t, "PATCH", env.ts.URL+"/api/albums/"+album.ID+"/cover",
		map[string]any{"cover": map[string]any{"type": "gradient", "gradient_start": "#111111", "gradient_end": "#777777"}}, &album)
	if status != http.StatusOK || album.Cover.Type != "gradient" || album.Cover.GradientStart != "#111111" {
		t.Fatalf("cover set status=%d cover=%+v body=%s", status, album.Cover, body)
	}
	var coverDetail albumDetailResponse
	status, body = apiJSON(t, "GET", env.ts.URL+"/api/albums/"+album.ID, nil, &coverDetail)
	if status != http.StatusOK || coverDetail.Album.Cover.GradientEnd != "#777777" {
		t.Fatalf("cover fetch status=%d cover=%+v body=%s", status, coverDetail.Album.Cover, body)
	}
	status, body = apiJSON(t, "PATCH", env.ts.URL+"/api/albums/missing/cover",
		map[string]any{"cover": map[string]any{"type": "gradient"}}, nil)
	if status != http.StatusNotFound {
		t.Fatalf("missing album cover set status=%d body=%s", status, body)
	}

	// Home list rows carry the album summary for grouping.
	var rows []Discussion
	status, body = apiJSON(t, "GET", env.ts.URL+"/api/discussions?limit=20", nil, &rows)
	if status != http.StatusOK {
		t.Fatalf("list discussions status=%d body=%s", status, body)
	}
	byID := map[string]Discussion{}
	for _, row := range rows {
		byID[row.ID] = row
	}
	if got := byID[first.ID]; got.AlbumID != album.ID || got.Album == nil || got.Album.ID != album.ID {
		t.Fatalf("member row album = %q / %+v, want %q", got.AlbumID, got.Album, album.ID)
	}
	if got := byID[third.ID]; got.AlbumID != "" || got.Album != nil {
		t.Fatalf("removed row still carries album: %+v", got)
	}

	// Disband: the album disappears and members are ungrouped but kept.
	status, _ = apiJSON(t, "DELETE", env.ts.URL+"/api/albums/"+album.ID, nil, nil)
	if status != http.StatusNoContent {
		t.Fatalf("disband status=%d", status)
	}
	status, body = apiJSON(t, "GET", env.ts.URL+"/api/albums/"+album.ID, nil, nil)
	if status != http.StatusNotFound {
		t.Fatalf("disbanded album still resolves: status=%d body=%s", status, body)
	}
	rows = nil
	status, body = apiJSON(t, "GET", env.ts.URL+"/api/discussions?limit=20", nil, &rows)
	if status != http.StatusOK || len(rows) != 3 {
		t.Fatalf("list after disband status=%d n=%d body=%s", status, len(rows), body)
	}
	for _, row := range rows {
		if row.AlbumID != "" {
			t.Fatalf("row %s still grouped after disband", row.ID)
		}
	}
}

func TestAlbumUIActions(t *testing.T) {
	env := newDiscussionAPITestEnv(t)
	root := seedAudioBook(t, env.store, 8)
	markGenerated(t, env.store, root, []int{1, 2, 3}, "job-root")

	var album Album
	status, body := apiJSON(t, "POST", env.ts.URL+"/api/albums",
		map[string]any{"title": "Novel", "discussion_ids": []string{root.ID}}, &album)
	if status != http.StatusOK {
		t.Fatalf("create album status=%d body=%s", status, body)
	}

	var resp discussionUIActionsResponse
	status, body = apiJSON(t, "GET", env.ts.URL+"/api/albums/"+album.ID+"/ui-actions", nil, &resp)
	if status != http.StatusOK {
		t.Fatalf("album ui-actions status=%d body=%s", status, body)
	}
	ids := make([]string, 0, len(resp.Items))
	for _, item := range resp.Items {
		ids = append(ids, item.ID)
	}
	want := []string{"generate-more-chapters", "add-podcasts", "rename-album", "edit-cover", "remove-album"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("album actions = %v, want %v", ids, want)
	}
	if !strings.HasPrefix(resp.Items[0].Action.Link, "debatepod://album/"+album.ID+"/") {
		t.Fatalf("album action link = %q, want album deep link", resp.Items[0].Action.Link)
	}

	// Once every chapter is generated the generate action disappears.
	markGenerated(t, env.store, root, []int{1, 2, 3, 4, 5, 6, 7, 8}, "job-root")
	status, body = apiJSON(t, "GET", env.ts.URL+"/api/albums/"+album.ID+"/ui-actions", nil, &resp)
	if status != http.StatusOK {
		t.Fatalf("album ui-actions status=%d body=%s", status, body)
	}
	for _, item := range resp.Items {
		if item.ID == "generate-more-chapters" {
			t.Fatalf("generate action still offered with no pending chapters: %s", body)
		}
	}
}

func TestPodcastMenuHidesFollowUpWhileChaptersPending(t *testing.T) {
	env := newDiscussionAPITestEnv(t)
	root := seedAudioBook(t, env.store, 5)
	markGenerated(t, env.store, root, []int{1, 2, 3}, "job-root")

	const query = "?surface=podcast-actions&supports_follow_up=true&supports_chapter_batches=true"
	var resp discussionUIActionsResponse
	status, body := apiJSON(t, "GET", env.ts.URL+"/api/discussions/"+root.ID+"/ui-actions"+query, nil, &resp)
	if status != http.StatusOK {
		t.Fatalf("ui-actions status=%d body=%s", status, body)
	}
	if !hasAction(resp.Items, "generate-more-chapters") {
		t.Fatalf("generate-more-chapters missing with pending chapters: %+v", resp.Items)
	}
	if hasAction(resp.Items, "create-follow-up") {
		t.Fatalf("create-follow-up offered while chapters are pending: %+v", resp.Items)
	}

	// Once every chapter is generated the follow-up returns and the
	// chapter action disappears.
	markGenerated(t, env.store, root, []int{1, 2, 3, 4, 5}, "job-root")
	status, body = apiJSON(t, "GET", env.ts.URL+"/api/discussions/"+root.ID+"/ui-actions"+query, nil, &resp)
	if status != http.StatusOK {
		t.Fatalf("ui-actions status=%d body=%s", status, body)
	}
	if hasAction(resp.Items, "generate-more-chapters") {
		t.Fatalf("generate-more-chapters offered with no pending chapters: %+v", resp.Items)
	}
	if !hasAction(resp.Items, "create-follow-up") {
		t.Fatalf("create-follow-up missing once all chapters are generated: %+v", resp.Items)
	}
}

func TestFollowUpCreateAutoBundlesAlbum(t *testing.T) {
	env := newDiscussionAPITestEnv(t)
	parent := seedAudioBook(t, env.store, 3)
	markGenerated(t, env.store, parent, []int{1, 2, 3}, "job-parent")

	body := fmt.Sprintf(`{"form":{"prompt":{"topic":"Continue it"},"settings":{"language":"en-US"}},"reference_discussion_id":%q}`, parent.ID)
	resp, err := http.Post(env.ts.URL+"/api/discussions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("create follow-up: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create follow-up status = %d body=%s", resp.StatusCode, raw)
	}
	var child Discussion
	if err := json.Unmarshal(raw, &child); err != nil {
		t.Fatalf("decode follow-up: %v", err)
	}
	if child.ReferenceDiscussionID != parent.ID {
		t.Fatalf("follow-up reference = %q, want %q", child.ReferenceDiscussionID, parent.ID)
	}
	if child.AlbumID == "" {
		t.Fatalf("follow-up was not auto-bundled into an album: %+v", child)
	}
	refreshedParent, err := env.store.Get(context.Background(), anonUser, parent.ID)
	if err != nil || refreshedParent == nil {
		t.Fatalf("reload parent: %v", err)
	}
	if refreshedParent.AlbumID != child.AlbumID {
		t.Fatalf("parent album = %q, child album = %q, want same", refreshedParent.AlbumID, child.AlbumID)
	}
	album, err := env.store.GetAlbum(context.Background(), anonUser, child.AlbumID)
	if err != nil || album == nil {
		t.Fatalf("load auto album: %v", err)
	}
	if album.Kind != albumKindAuto || album.RootDiscussionID != parent.ID || album.EpisodeCount != 2 {
		t.Fatalf("auto album = %+v, want kind=auto root=%s count=2", album, parent.ID)
	}
}
