package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
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
