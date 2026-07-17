package server

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSearchSummaryDocumentsUsesSummaryBodyAndOwnerScope(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "summary-search.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()

	owned, err := store.Create(ctx, "owner-a", "technology", planResponse{})
	if err != nil {
		t.Fatalf("Create owned discussion: %v", err)
	}
	other, err := store.Create(ctx, "owner-b", "technology", planResponse{})
	if err != nil {
		t.Fatalf("Create other discussion: %v", err)
	}
	if err := store.SaveSummary(ctx, owned.ID, SummaryDocTypeSummary,
		"# Summary\n\nThe central conclusion concerns quantum error correction.", "model", SummaryUsage{}); err != nil {
		t.Fatalf("SaveSummary owned: %v", err)
	}
	if err := store.SaveSummary(ctx, other.ID, SummaryDocTypeSummary,
		"# Summary\n\nAnother quantum error correction result.", "model", SummaryUsage{}); err != nil {
		t.Fatalf("SaveSummary other: %v", err)
	}

	results, err := store.SearchSummaryDocuments(ctx, "owner-a", "",
		"what did the podcast say about quantum error correction?", 5)
	if err != nil {
		t.Fatalf("SearchSummaryDocuments: %v", err)
	}
	if len(results) != 1 || results[0].DiscussionID != owned.ID {
		t.Fatalf("summary results = %+v, want only owned discussion %s", results, owned.ID)
	}
}

func TestSearchSummaryDocumentsReadsKnownPodcastWithoutQuery(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "summary-by-id.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()

	discussion, err := store.Create(ctx, "owner", "topic", planResponse{})
	if err != nil {
		t.Fatalf("Create discussion: %v", err)
	}
	if err := store.SaveSummary(ctx, discussion.ID, SummaryDocTypeSummary, "Known summary", "model", SummaryUsage{}); err != nil {
		t.Fatalf("SaveSummary: %v", err)
	}
	results, err := store.SearchSummaryDocuments(ctx, "owner", discussion.ID, "", 1)
	if err != nil {
		t.Fatalf("SearchSummaryDocuments: %v", err)
	}
	if len(results) != 1 || results[0].Markdown != "Known summary" {
		t.Fatalf("summary results = %+v", results)
	}
}
