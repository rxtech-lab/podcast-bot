package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgentDocumentHTTPListsScopesAndAddsPodcastLink(t *testing.T) {
	discussions, documents := newAgentDocumentTestStores(t)
	ctx := context.Background()
	podcast, err := discussions.Create(ctx, "anonymous", "HTTP podcast", planResponse{})
	if err != nil {
		t.Fatalf("Create discussion: %v", err)
	}
	global, _ := documents.Create(ctx, "anonymous", nil, "global", "global-call", "Global", "# Global")
	linked, _ := documents.Create(ctx, "anonymous", &podcast.ID, "local", "local-call", "Linked", "# Linked")
	_, _ = documents.Create(ctx, "other", nil, "other", "other-call", "Private", "# Private")

	srv := New(Deps{
		Discussions: discussions, AgentDocuments: documents,
		WebsiteBaseURL: "https://podcast.test",
	})

	globalRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(globalRec, httptest.NewRequest(http.MethodGet, "/api/documents", nil))
	if globalRec.Code != http.StatusOK {
		t.Fatalf("global list status=%d body=%s", globalRec.Code, globalRec.Body.String())
	}
	var globalResp agentDocumentListResponse
	if err := json.Unmarshal(globalRec.Body.Bytes(), &globalResp); err != nil || len(globalResp.Documents) != 1 || globalResp.Documents[0].ID != global.ID {
		t.Fatalf("global response=%+v err=%v", globalResp, err)
	}

	allFirstRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(allFirstRec, httptest.NewRequest(http.MethodGet,
		"/api/documents?scope=all&limit=1&offset=0", nil))
	if allFirstRec.Code != http.StatusOK {
		t.Fatalf("all-documents first page status=%d body=%s", allFirstRec.Code, allFirstRec.Body.String())
	}
	var allFirst agentDocumentListResponse
	if err := json.Unmarshal(allFirstRec.Body.Bytes(), &allFirst); err != nil ||
		len(allFirst.Documents) != 1 || allFirst.Documents[0].ID != global.ID || !allFirst.HasMore {
		t.Fatalf("all-documents first page=%+v err=%v", allFirst, err)
	}

	allSecondRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(allSecondRec, httptest.NewRequest(http.MethodGet,
		"/api/documents?scope=all&limit=1&offset=1", nil))
	if allSecondRec.Code != http.StatusOK {
		t.Fatalf("all-documents second page status=%d body=%s", allSecondRec.Code, allSecondRec.Body.String())
	}
	var allSecond agentDocumentListResponse
	if err := json.Unmarshal(allSecondRec.Body.Bytes(), &allSecond); err != nil ||
		len(allSecond.Documents) != 1 || allSecond.Documents[0].ID != linked.ID || allSecond.HasMore {
		t.Fatalf("all-documents second page=%+v err=%v", allSecond, err)
	}

	searchRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(searchRec, httptest.NewRequest(http.MethodGet,
		"/api/documents?scope=all&q=Linked&limit=20", nil))
	if searchRec.Code != http.StatusOK {
		t.Fatalf("all-documents search status=%d body=%s", searchRec.Code, searchRec.Body.String())
	}
	var searchResp agentDocumentListResponse
	if err := json.Unmarshal(searchRec.Body.Bytes(), &searchResp); err != nil ||
		len(searchResp.Documents) != 1 || searchResp.Documents[0].ID != linked.ID {
		t.Fatalf("all-documents search=%+v err=%v", searchResp, err)
	}

	linkedRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(linkedRec, httptest.NewRequest(http.MethodGet,
		"/api/discussions/"+podcast.ID+"/documents", nil))
	if linkedRec.Code != http.StatusOK {
		t.Fatalf("linked list status=%d body=%s", linkedRec.Code, linkedRec.Body.String())
	}
	var linkedResp agentDocumentListResponse
	if err := json.Unmarshal(linkedRec.Body.Bytes(), &linkedResp); err != nil || len(linkedResp.Documents) != 1 || linkedResp.Documents[0].ID != linked.ID {
		t.Fatalf("linked response=%+v err=%v", linkedResp, err)
	}

	detailRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(detailRec, httptest.NewRequest(http.MethodGet, "/api/documents/"+linked.ID, nil))
	if detailRec.Code != http.StatusOK || !strings.Contains(detailRec.Body.String(), "https://podcast.test/p/"+podcast.ID) {
		t.Fatalf("detail status=%d body=%s", detailRec.Code, detailRec.Body.String())
	}

	actionsRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(actionsRec, httptest.NewRequest(http.MethodGet,
		"/api/documents/"+linked.ID+"/ui-actions", nil))
	if actionsRec.Code != http.StatusOK {
		t.Fatalf("ui-actions status=%d body=%s", actionsRec.Code, actionsRec.Body.String())
	}
	var actions discussionUIActionsResponse
	if err := json.Unmarshal(actionsRec.Body.Bytes(), &actions); err != nil {
		t.Fatalf("decode ui-actions: %v", err)
	}
	for _, tc := range []struct {
		id   string
		link string
	}{
		{id: "download-pdf", link: "debatepod://document/" + linked.ID + "/export/pdf"},
		{id: "download-markdown", link: "debatepod://document/" + linked.ID + "/export/markdown"},
		{id: "export-notion", link: "debatepod://document/" + linked.ID + "/sheet/notion"},
	} {
		item := findAction(t, actions.Items, tc.id)
		if item.Action.Link != tc.link {
			t.Fatalf("%s link=%q want %q", tc.id, item.Action.Link, tc.link)
		}
	}

	deleteRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(deleteRec, httptest.NewRequest(http.MethodDelete,
		"/api/documents/"+linked.ID, nil))
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	if deleted, err := documents.Get(ctx, "anonymous", linked.ID); err != nil || deleted != nil {
		t.Fatalf("deleted document=%+v err=%v", deleted, err)
	}
}

func TestAgentDocumentHTTPDoesNotExposeAnotherOwner(t *testing.T) {
	discussions, documents := newAgentDocumentTestStores(t)
	other, err := documents.Create(context.Background(), "other", nil, "conv", "call", "Private", "secret")
	if err != nil {
		t.Fatalf("Create document: %v", err)
	}
	srv := New(Deps{Discussions: discussions, AgentDocuments: documents})
	for _, request := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/api/documents/" + other.ID},
		{method: http.MethodGet, path: "/api/documents/" + other.ID + "/ui-actions"},
		{method: http.MethodDelete, path: "/api/documents/" + other.ID},
	} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(request.method, request.path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("cross-owner %s %s status=%d body=%s",
				request.method, request.path, rec.Code, rec.Body.String())
		}
	}
	if retained, err := documents.Get(context.Background(), "other", other.ID); err != nil || retained == nil {
		t.Fatalf("cross-owner delete changed document: %+v err=%v", retained, err)
	}
}
