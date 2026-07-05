package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
)

func createReadyMindmapDiscussion(t *testing.T, store *DiscussionStore, contentType string) *Discussion {
	t.Helper()
	ctx := context.Background()
	d, err := store.Create(ctx, "anonymous", "AI safety", planResponse{
		Script: &config.DebateTopic{Type: contentType, Title: "AI safety", Language: "en-US"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.SetJobResult(ctx, d.ID, DiscussionReady, "https://audio.example/ready.mp3"); err != nil {
		t.Fatalf("SetJobResult: %v", err)
	}
	return d
}

func mindmapRequest(srv *Server, method, path, body string) *httptest.ResponseRecorder {
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestMindmapHTTPGetAndSaveRoundTrip(t *testing.T) {
	srv, store := newUIActionsTestServer(t)
	ctx := context.Background()
	d := createReadyMindmapDiscussion(t, store, config.ContentTypeDiscussion)

	// No mindmap yet: content endpoint 404s, menu offers generation.
	if rec := mindmapRequest(srv, http.MethodGet, "/api/discussions/"+d.ID+"/mindmap", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("GET before generation status = %d, want 404", rec.Code)
	}
	actions := getUIActions(t, srv, d.ID, "podcast-documents")
	if !hasAction(actions.Items, "generate-mindmap") {
		t.Fatalf("generate-mindmap missing for ready discussion: %+v", actions.Items)
	}

	// Simulate a completed generation and read it back as a typed tree.
	stored := `{"version":1,"root":{"id":"root","title":"AI safety","children":[{"id":"n1","title":"Theme","children":[]}]}}`
	if err := store.SaveSummary(ctx, d.ID, SummaryDocTypeMindmap, stored, "model", SummaryUsage{}); err != nil {
		t.Fatalf("SaveSummary: %v", err)
	}
	rec := mindmapRequest(srv, http.MethodGet, "/api/discussions/"+d.ID+"/mindmap", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var doc MindmapDocument
	if err := json.NewDecoder(rec.Body).Decode(&doc); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if doc.Mindmap == nil || doc.Mindmap.Root == nil || doc.Mindmap.Root.Title != "AI safety" {
		t.Fatalf("mindmap document = %+v, want typed tree", doc)
	}
	if len(doc.Mindmap.Root.Children) != 1 || doc.Mindmap.Root.Children[0].ID != "n1" {
		t.Fatalf("mindmap children = %+v", doc.Mindmap.Root.Children)
	}
	actions = getUIActions(t, srv, d.ID, "podcast-documents")
	if !hasAction(actions.Items, "open-mindmap") || hasAction(actions.Items, "generate-mindmap") {
		t.Fatalf("ready mindmap: items = %+v, want open-mindmap only", actions.Items)
	}

	// Owner edit round-trips through PUT.
	edited := `{"mindmap":{"version":1,"root":{"id":"root","title":"Edited","children":[{"id":"n1","title":"Theme","children":[{"id":"n2","title":"New child","children":[]}]}]}}}`
	rec = mindmapRequest(srv, http.MethodPut, "/api/discussions/"+d.ID+"/mindmap", edited)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	rec = mindmapRequest(srv, http.MethodGet, "/api/discussions/"+d.ID+"/mindmap", "")
	doc = MindmapDocument{}
	if err := json.NewDecoder(rec.Body).Decode(&doc); err != nil {
		t.Fatalf("Decode after edit: %v", err)
	}
	if doc.Mindmap == nil || doc.Mindmap.Root.Title != "Edited" {
		t.Fatalf("edited mindmap = %+v, want root title Edited", doc.Mindmap)
	}
}

func TestMindmapHTTPSaveValidation(t *testing.T) {
	srv, store := newUIActionsTestServer(t)
	ctx := context.Background()
	d := createReadyMindmapDiscussion(t, store, config.ContentTypeDiscussion)
	if err := store.SaveSummary(ctx, d.ID, SummaryDocTypeMindmap, `{"version":1,"root":{"id":"root","title":"t"}}`, "m", SummaryUsage{}); err != nil {
		t.Fatalf("SaveSummary: %v", err)
	}

	// Structural validation failures are 400s.
	for name, body := range map[string]string{
		"missing root title": `{"mindmap":{"version":1,"root":{"id":"root","title":""}}}`,
		"duplicate ids":      `{"mindmap":{"version":1,"root":{"id":"root","title":"t","children":[{"id":"a","title":"x"},{"id":"a","title":"y"}]}}}`,
		"empty body":         `{}`,
	} {
		rec := mindmapRequest(srv, http.MethodPut, "/api/discussions/"+d.ID+"/mindmap", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: PUT status = %d, want 400", name, rec.Code)
		}
	}

	// Edits are rejected while a regeneration is in flight.
	if err := store.BeginSummary(ctx, d.ID, SummaryDocTypeMindmap, "m"); err != nil {
		t.Fatalf("BeginSummary: %v", err)
	}
	rec := mindmapRequest(srv, http.MethodPut, "/api/discussions/"+d.ID+"/mindmap", `{"mindmap":{"version":1,"root":{"id":"root","title":"t"}}}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("PUT while generating status = %d, want 409", rec.Code)
	}
}

func TestMindmapHTTPOwnerAndTypeGates(t *testing.T) {
	srv, store := newUIActionsTestServer(t)
	ctx := context.Background()

	// Non-owner cannot reach the edit endpoint (owner-scoped Get returns nil).
	other := createReadyMindmapDiscussion(t, store, config.ContentTypeDiscussion)
	if err := store.SaveSummary(ctx, other.ID, SummaryDocTypeMindmap, `{"version":1,"root":{"id":"root","title":"t"}}`, "m", SummaryUsage{}); err != nil {
		t.Fatalf("SaveSummary: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE native_discussions SET owner_user_id = 'someone-else' WHERE id = ?`, other.ID); err != nil {
		t.Fatalf("reassign owner: %v", err)
	}
	rec := mindmapRequest(srv, http.MethodPut, "/api/discussions/"+other.ID+"/mindmap", `{"mindmap":{"version":1,"root":{"id":"root","title":"t"}}}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("non-owner PUT status = %d, want 404", rec.Code)
	}

	// Generation is refused for non-discussion content types.
	debate := createReadyMindmapDiscussion(t, store, config.ContentTypeDebate)
	rec = mindmapRequest(srv, http.MethodPost, "/api/discussions/"+debate.ID+"/mindmap/generate", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("generate for debate status = %d, want 409", rec.Code)
	}
	actions := getUIActions(t, srv, debate.ID, "podcast-documents")
	for _, id := range []string{"open-mindmap", "mindmap-pending", "generate-mindmap"} {
		if hasAction(actions.Items, id) {
			t.Fatalf("debate documents menu must not contain %s: %+v", id, actions.Items)
		}
	}
}
