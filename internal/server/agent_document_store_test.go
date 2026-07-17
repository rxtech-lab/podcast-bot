package server

import (
	"context"
	"path/filepath"
	"testing"
)

func newAgentDocumentTestStores(t *testing.T) (*DiscussionStore, *AgentDocumentStore) {
	t.Helper()
	discussions, err := NewDiscussionStore(filepath.Join(t.TempDir(), "agent-documents.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	documents, err := NewAgentDocumentStore(discussions)
	if err != nil {
		t.Fatalf("NewAgentDocumentStore: %v", err)
	}
	t.Cleanup(func() { _ = discussions.Close() })
	return discussions, documents
}

func TestAgentDocumentStoreScopesOwnershipAndIdempotency(t *testing.T) {
	ctx := context.Background()
	discussions, documents := newAgentDocumentTestStores(t)
	podcast, err := discussions.Create(ctx, "owner", "Linked podcast", planResponse{})
	if err != nil {
		t.Fatalf("Create discussion: %v", err)
	}

	global, err := documents.Create(ctx, "owner", nil, "global-conv", "call-global", "Global brief", "# Global")
	if err != nil {
		t.Fatalf("Create global document: %v", err)
	}
	linked, err := documents.Create(ctx, "owner", &podcast.ID, "podcast-conv", "call-linked", "Podcast brief", "# Podcast")
	if err != nil {
		t.Fatalf("Create linked document: %v", err)
	}
	replayed, err := documents.Create(ctx, "owner", &podcast.ID, "podcast-conv", "call-linked", "Duplicate", "# Duplicate")
	if err != nil {
		t.Fatalf("Replay document create: %v", err)
	}
	if replayed.ID != linked.ID || replayed.Title != linked.Title {
		t.Fatalf("replay created a duplicate: first=%+v replay=%+v", linked, replayed)
	}

	globals, err := documents.List(ctx, "owner", nil)
	if err != nil || len(globals) != 1 || globals[0].ID != global.ID || globals[0].Markdown != "" {
		t.Fatalf("global list = %+v err=%v", globals, err)
	}
	linkedList, err := documents.List(ctx, "owner", &podcast.ID)
	if err != nil || len(linkedList) != 1 || linkedList[0].ID != linked.ID || linkedList[0].PodcastTitle != podcast.Title {
		t.Fatalf("linked list = %+v err=%v", linkedList, err)
	}
	if leaked, err := documents.Get(ctx, "other", linked.ID); err != nil || leaked != nil {
		t.Fatalf("document leaked across owners: %+v err=%v", leaked, err)
	}
}

func TestAgentDocumentStoreCascadesLinkedDocuments(t *testing.T) {
	ctx := context.Background()
	discussions, documents := newAgentDocumentTestStores(t)
	podcast, err := discussions.Create(ctx, "owner", "Disposable", planResponse{})
	if err != nil {
		t.Fatalf("Create discussion: %v", err)
	}
	doc, err := documents.Create(ctx, "owner", &podcast.ID, "conv", "call", "Linked", "body")
	if err != nil {
		t.Fatalf("Create document: %v", err)
	}
	if ok, err := discussions.Delete(ctx, "owner", podcast.ID); err != nil || !ok {
		t.Fatalf("Delete discussion: ok=%v err=%v", ok, err)
	}
	if got, err := documents.Get(ctx, "owner", doc.ID); err != nil || got != nil {
		t.Fatalf("linked document survived discussion deletion: %+v err=%v", got, err)
	}
}
