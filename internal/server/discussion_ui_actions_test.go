package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/storage"
)

func newUIActionsTestServer(t *testing.T) (*Server, *DiscussionStore) {
	t.Helper()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "ui-actions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	srv := New(Deps{Discussions: store, WebsiteBaseURL: "https://podcast.test"})
	t.Cleanup(func() {
		_ = store.Close()
	})
	return srv, store
}

func TestDiscussionUIActionsDocumentsUseConcreteID(t *testing.T) {
	srv, store := newUIActionsTestServer(t)
	ctx := context.Background()
	d, err := store.Create(ctx, "anonymous", "Backend menus", planResponse{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.SetJobResult(ctx, d.ID, DiscussionReady, "https://audio.example/ready.mp3"); err != nil {
		t.Fatalf("SetJobResult: %v", err)
	}
	if err := store.SaveSummary(ctx, d.ID, SummaryDocTypeSummary, "summary", "model", SummaryUsage{}); err != nil {
		t.Fatalf("SaveSummary: %v", err)
	}

	resp := getUIActions(t, srv, d.ID, "podcast-documents")
	openSummary := findAction(t, resp.Items, "open-summary")
	if openSummary.Action.Link != "debatepod://discussion/"+d.ID+"/sheet/summary" {
		t.Fatalf("open-summary link = %q, want concrete discussion id", openSummary.Action.Link)
	}
	if strings.Contains(openSummary.Action.Link, "{id}") {
		t.Fatalf("open-summary link still has placeholder: %q", openSummary.Action.Link)
	}
}

func TestDiscussionUIActionsPodcastActionsReflectOwnerState(t *testing.T) {
	srv, store := newUIActionsTestServer(t)
	ctx := context.Background()
	d, err := store.Create(ctx, "anonymous", "Publish me", planResponse{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	const jobID = "job-captions"
	if _, err := store.SetJob(ctx, "anonymous", d.ID, jobID); err != nil {
		t.Fatalf("SetJob: %v", err)
	}
	if err := store.SetJobResult(ctx, d.ID, DiscussionReady, "https://audio.example/ready.mp3"); err != nil {
		t.Fatalf("SetJobResult: %v", err)
	}

	resp := getUIActions(t, srv, d.ID, "podcast-actions&supports_create_from_plan=true&supports_points=true")
	publish := findAction(t, resp.Items, "publish")
	if publish.Action.Link != "debatepod://discussion/"+d.ID+"/sheet/publish" {
		t.Fatalf("publish link = %q, want concrete publish sheet link", publish.Action.Link)
	}
	if !hasAction(resp.Items, "download-podcast") {
		t.Fatalf("download-podcast action missing from ready podcast actions: %+v", resp.Items)
	}
	captions := findAction(t, resp.Items, "download-captions")
	if captions.Action.Link != "debatepod://discussion/"+d.ID+"/sheet/captions" {
		t.Fatalf("download-captions link = %q, want concrete captions sheet link", captions.Action.Link)
	}
	if !hasAction(resp.Items, "points") {
		t.Fatalf("points action missing when supports_points=true: %+v", resp.Items)
	}
}

func TestDiscussionUIActionsSummaryActionsIncludeRealIDExports(t *testing.T) {
	srv, store := newUIActionsTestServer(t)
	ctx := context.Background()
	d, err := store.Create(ctx, "anonymous", "Summary exports", planResponse{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.SetJobResult(ctx, d.ID, DiscussionReady, "https://audio.example/ready.mp3"); err != nil {
		t.Fatalf("SetJobResult: %v", err)
	}
	if err := store.SaveSummary(ctx, d.ID, SummaryDocTypeSummary, "summary", "model", SummaryUsage{}); err != nil {
		t.Fatalf("SaveSummary: %v", err)
	}

	resp := getUIActions(t, srv, d.ID, "summary-actions&doc_type=summary")
	pdf := findAction(t, resp.Items, "download-pdf")
	if !pdf.Enabled {
		t.Fatalf("download-pdf enabled = false, want true")
	}
	if pdf.Action.Link != "debatepod://discussion/"+d.ID+"/summary/export/pdf" {
		t.Fatalf("download-pdf link = %q, want concrete export link", pdf.Action.Link)
	}
}

func TestDiscussionUIActionsShowVideoRenderingWhileAudioBookVideoRuns(t *testing.T) {
	srv, store := newUIActionsTestServer(t)
	ctx := context.Background()
	jobs, err := NewJobRegistry(filepath.Join(t.TempDir(), "jobs.db"), "", "")
	if err != nil {
		t.Fatalf("NewJobRegistry: %v", err)
	}
	uploader, err := storage.New(ctx, storage.Config{
		Bucket:          "podcasts",
		Region:          "auto",
		DownloadBaseURL: "https://media.example.com",
	})
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	srv.d.Jobs = jobs
	srv.d.Uploader = uploader
	srv.d.UploadRoot = t.TempDir()

	d, err := store.Create(ctx, "anonymous", "Audio Book", planResponse{
		Script: &config.DebateTopic{
			Title: "Audio Book",
			Type:  config.ContentTypeAudioBook,
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	const jobID = "job-video-rendering"
	if _, err := store.SetJob(ctx, "anonymous", d.ID, jobID); err != nil {
		t.Fatalf("SetJob: %v", err)
	}
	if err := store.SetJobResult(ctx, d.ID, DiscussionReady, "https://audio.example/ready.mp3"); err != nil {
		t.Fatalf("SetJobResult: %v", err)
	}
	jobs.Add(jobID)
	jobs.Update(jobID, func(j *Job) {
		j.Phase = "video-rendering"
		j.PhaseLabel = "Rendering video"
	})

	resp := getUIActions(t, srv, d.ID, "podcast-documents")
	rendering := findAction(t, resp.Items, "video-rendering")
	if rendering.Enabled {
		t.Fatalf("video-rendering enabled = true, want disabled")
	}
	if rendering.Title != "Generating Video" {
		t.Fatalf("video-rendering title = %q, want Generating Video", rendering.Title)
	}
	if hasAction(resp.Items, "generate-video") {
		t.Fatalf("generate-video action shown while video job is rendering: %+v", resp.Items)
	}
}

func TestHomeUIActionsRenderToolbarGroups(t *testing.T) {
	srv, _ := newUIActionsTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/home/ui-actions?supports_points=true&visibility=public&type=audio-book", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var out discussionUIActionsResponse
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	account := findAction(t, out.Toolbars, "account")
	if len(account.Children) == 0 {
		t.Fatalf("account toolbar children missing: %+v", account)
	}
	if !hasAction(account.Children, "points") {
		t.Fatalf("points child missing when supports_points=true: %+v", account.Children)
	}
	create := findAction(t, out.Toolbars, "create")
	if !hasAction(create.Children, "new-station") || !hasAction(create.Children, "new-album") {
		t.Fatalf("create children missing station/album actions: %+v", create.Children)
	}
	filter := findAction(t, out.Toolbars, "filter")
	publicFilter := findAction(t, filter.Children, "filter-public")
	if publicFilter.SystemImage != "checkmark" {
		t.Fatalf("public filter image = %q, want checkmark", publicFilter.SystemImage)
	}
	divider := findAction(t, filter.Children, "filter-type-divider")
	if divider.Action.Type != "divider" {
		t.Fatalf("divider action type = %q, want divider", divider.Action.Type)
	}
	audioBookFilter := findAction(t, filter.Children, "type-audio-book")
	if audioBookFilter.SystemImage != "checkmark" {
		t.Fatalf("audio book filter image = %q, want checkmark", audioBookFilter.SystemImage)
	}
}

func getUIActions(t *testing.T, srv *Server, id, query string) discussionUIActionsResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/discussions/"+id+"/ui-actions?surface="+query, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var out discussionUIActionsResponse
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	return out
}

func findAction(t *testing.T, items []discussionUIActionItem, id string) discussionUIActionItem {
	t.Helper()
	for _, item := range items {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("action %q missing from %+v", id, items)
	return discussionUIActionItem{}
}

func hasAction(items []discussionUIActionItem, id string) bool {
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
}
