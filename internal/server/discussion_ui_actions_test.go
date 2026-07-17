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

func TestDiscussionUIActionsGroupDocumentsBeforeSearchAndAsk(t *testing.T) {
	srv, store := newUIActionsTestServer(t)
	ctx := context.Background()
	d, err := store.Create(ctx, "anonymous", "Grouped document menu", planResponse{
		Script: &config.DebateTopic{
			Title:    "Grouped document menu",
			Type:     config.ContentTypeDiscussion,
			Language: "en-US",
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.SetJobResult(ctx, d.ID, DiscussionReady, "https://audio.example/ready.mp3"); err != nil {
		t.Fatalf("SetJobResult: %v", err)
	}
	if err := store.SaveSummary(ctx, d.ID, SummaryDocTypeSummary, "summary", "model", SummaryUsage{}); err != nil {
		t.Fatalf("SaveSummary: %v", err)
	}
	if err := store.SaveSummary(ctx, d.ID, SummaryDocTypeMindmap, `{"version":1,"root":{"id":"root","title":"Menu"}}`, "model", SummaryUsage{}); err != nil {
		t.Fatalf("Save mindmap: %v", err)
	}
	embeddings, err := NewEmbeddingStore(store, 3)
	if err != nil {
		t.Fatalf("NewEmbeddingStore: %v", err)
	}
	qa, err := NewQAStore(store)
	if err != nil {
		t.Fatalf("NewQAStore: %v", err)
	}
	srv.d.Embeddings = embeddings
	srv.d.QA = qa
	srv.d.Env = &config.Env{
		OpenAIBaseURL:  "https://api.example.test/v1",
		EmbeddingModel: "test-embedding",
	}
	if err := embeddings.MarkReady(ctx, d.ID, "test-embedding", "content-hash"); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}

	resp := getUIActions(t, srv, d.ID, "podcast-documents")
	assertActionOrder(t, resp.Items,
		"open-plan",
		"open-summary",
		"open-mindmap",
		"podcast-qa-divider",
		"open-search",
		"open-qa",
	)
	if divider := findAction(t, resp.Items, "podcast-qa-divider"); divider.Action.Type != "divider" {
		t.Fatalf("podcast-qa-divider action type = %q, want divider", divider.Action.Type)
	}
}

func TestDiscussionUIActionsIncludesLinkedChatDocuments(t *testing.T) {
	srv, store := newUIActionsTestServer(t)
	documents, err := NewAgentDocumentStore(store)
	if err != nil {
		t.Fatalf("NewAgentDocumentStore: %v", err)
	}
	srv.d.AgentDocuments = documents
	d, err := store.Create(context.Background(), "anonymous", "Document podcast", planResponse{})
	if err != nil {
		t.Fatalf("Create discussion: %v", err)
	}
	if _, err := documents.Create(context.Background(), "anonymous", &d.ID,
		"conv", "call", "Chat brief", "# Brief"); err != nil {
		t.Fatalf("Create document: %v", err)
	}
	resp := getUIActions(t, srv, d.ID, "podcast-documents")
	action := findAction(t, resp.Items, "agent-documents")
	want := "debatepod://discussion/" + d.ID + "/sheet/documents"
	if action.Action.Link != want {
		t.Fatalf("agent-documents link=%q want=%q", action.Action.Link, want)
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

	resp := getUIActions(t, srv, d.ID, "podcast-actions&supports_create_from_plan=true&supports_points=true&supports_sign_out=true")
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
	if hasAction(resp.Items, "edit-captions") {
		t.Fatalf("edit-captions must stay hidden for non-uploaded podcasts: %+v", resp.Items)
	}
	assertActionOrder(t, resp.Items,
		"points",
		"podcast-creation-divider",
		"create-from-plan",
		"podcast-management-divider",
		"translate-podcast",
		"publish",
		"podcast-transfer-divider",
		"share-private",
		"download-captions",
		"download-podcast",
		"podcast-session-divider",
		"sign-out",
	)
	for _, id := range []string{"podcast-creation-divider", "podcast-management-divider", "podcast-transfer-divider", "podcast-session-divider"} {
		if divider := findAction(t, resp.Items, id); divider.Action.Type != "divider" {
			t.Fatalf("%s action type = %q, want divider", id, divider.Action.Type)
		}
	}
}

func TestDiscussionUIActionsOffersCaptionEditorForReadyUploadedAudio(t *testing.T) {
	srv, store := newUIActionsTestServer(t)
	ctx := context.Background()
	plan := &config.DebateTopic{
		Title:                   "Editable captions",
		Type:                    config.ContentTypeUploadedAudio,
		Language:                "en-US",
		TotalMinutes:            1,
		Channel:                 "default",
		UploadedAudioKey:        "uploads/anonymous/audio.mp3",
		UploadedAudioDurationMS: 10_000,
		TranscriptSegments: []config.TranscriptSegment{
			{Speaker: "Host", OffsetMS: 0, DurationMS: 2_000, Text: "Hello"},
		},
	}
	d, err := store.Create(ctx, "anonymous", plan.Title, planResponse{Script: plan})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.SetJob(ctx, "anonymous", d.ID, "job-edit-captions"); err != nil {
		t.Fatalf("SetJob: %v", err)
	}
	if err := store.SetJobResult(ctx, d.ID, DiscussionReady, "https://audio.example/ready.mp3"); err != nil {
		t.Fatalf("SetJobResult: %v", err)
	}

	resp := getUIActions(t, srv, d.ID, "podcast-actions")
	edit := findAction(t, resp.Items, "edit-captions")
	if edit.Action.Link != "debatepod://discussion/"+d.ID+"/sheet/caption-editor" {
		t.Fatalf("edit-captions link = %q", edit.Action.Link)
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
	if hasAction(out.Items, "chat") {
		t.Fatal("chat tab returned without Q&A and semantic search configuration")
	}
	account := findAction(t, out.Toolbars, "account")
	if len(account.Children) == 0 {
		t.Fatalf("account toolbar children missing: %+v", account)
	}
	if !hasAction(account.Children, "points") {
		t.Fatalf("points child missing when supports_points=true: %+v", account.Children)
	}
	if !hasAction(account.Children, "recordings") {
		t.Fatalf("recordings child missing from account menu: %+v", account.Children)
	}
	assertActionOrder(t, account.Children,
		"points",
		"account-content-divider",
		"documents",
		"settings",
		"recordings",
		"whats-new",
		"account-refresh-divider",
		"refresh",
		"account-session-divider",
		"sign-out",
	)
	for _, id := range []string{"account-content-divider", "account-refresh-divider", "account-session-divider"} {
		if divider := findAction(t, account.Children, id); divider.Action.Type != "divider" {
			t.Fatalf("%s action type = %q, want divider", id, divider.Action.Type)
		}
	}
	create := findAction(t, out.Toolbars, "create")
	if !hasAction(create.Children, "new-station") || !hasAction(create.Children, "new-album") {
		t.Fatalf("create children missing station/album actions: %+v", create.Children)
	}
	if !hasAction(create.Children, "record-audio") {
		t.Fatalf("record-audio child missing from create menu: %+v", create.Children)
	}
	assertActionOrder(t, create.Children, "new-album", "create-audio-divider", "upload-audio", "record-audio")
	createDivider := findAction(t, create.Children, "create-audio-divider")
	if createDivider.Action.Type != "divider" {
		t.Fatalf("create divider action type = %q, want divider", createDivider.Action.Type)
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

func TestHomeUIActionsEnableChatWhenQAIsConfigured(t *testing.T) {
	srv, store := newUIActionsTestServer(t)
	embeddings, err := NewEmbeddingStore(store, 3)
	if err != nil {
		t.Fatalf("NewEmbeddingStore: %v", err)
	}
	qa, err := NewQAStore(store)
	if err != nil {
		t.Fatalf("NewQAStore: %v", err)
	}
	srv.d.Embeddings = embeddings
	srv.d.QA = qa
	permissions, err := NewSubscriptionPermissionStore(store)
	if err != nil {
		t.Fatalf("NewSubscriptionPermissionStore: %v", err)
	}
	permission := SubscriptionPermission{
		Permissions: Permissions{Features: PermissionFeatures{CanUseChat: true}},
	}
	if err := permissions.Create(context.Background(), &permission); err != nil {
		t.Fatalf("create chat permission: %v", err)
	}
	srv.d.SubscriptionPermissions = permissions
	srv.d.Env = &config.Env{
		OpenAIBaseURL:  "https://api.example.test/v1",
		EmbeddingModel: "test-embedding",
	}

	req := httptest.NewRequest(http.MethodGet, "/api/home/ui-actions", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var out discussionUIActionsResponse
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	chat := findAction(t, out.Items, "chat")
	if !chat.Enabled {
		t.Fatal("chat tab disabled with Q&A and semantic search configured")
	}

	permission.Permissions.Features.CanUseChat = false
	if err := permissions.Update(context.Background(), permission.ID, &permission); err != nil {
		t.Fatalf("remove chat permission: %v", err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/home/ui-actions", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("locked status = %d, want 200", rec.Code)
	}
	out = discussionUIActionsResponse{}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("Decode locked response: %v", err)
	}
	chat = findAction(t, out.Items, "chat")
	if chat.Enabled {
		t.Fatal("chat tab should remain present but locked without subscription permission")
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

func assertActionOrder(t *testing.T, items []discussionUIActionItem, ids ...string) {
	t.Helper()
	positions := make(map[string]int, len(items))
	for i, item := range items {
		positions[item.ID] = i
	}
	for i := 1; i < len(ids); i++ {
		previous, previousOK := positions[ids[i-1]]
		current, currentOK := positions[ids[i]]
		if !previousOK || !currentOK || previous >= current {
			t.Fatalf("actions not in expected order %v: %+v", ids, items)
		}
	}
}
