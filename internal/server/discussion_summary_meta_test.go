package server

import (
	"context"
	"path/filepath"
	"testing"
)

func TestApplyDiscussionSummaryMetaOffersGenerationForReadyOwner(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "summaries.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	created, err := store.Create(ctx, "owner", "AI safety", planResponse{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.SetJobResult(ctx, created.ID, DiscussionReady, ""); err != nil {
		t.Fatalf("SetJobResult: %v", err)
	}
	ready, err := store.Get(ctx, "owner", created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	s := &Server{d: Deps{Discussions: store}}
	s.applyDiscussionSummaryMeta(ctx, ready)

	if ready.Summary == nil {
		t.Fatal("ready summary meta is nil, want generation-capable descriptor")
	}
	if !ready.Summary.Generation {
		t.Fatalf("summary generation = false, want true: %+v", ready.Summary)
	}
	if ready.Summary.Pending || ready.Summary.Available {
		t.Fatalf("summary meta = %+v, want not pending and not available", ready.Summary)
	}
}

func TestApplyDiscussionSummaryMetaReportsPending(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "summaries.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	created, err := store.Create(ctx, "owner", "AI safety", planResponse{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.SetJobResult(ctx, created.ID, DiscussionReady, ""); err != nil {
		t.Fatalf("SetJobResult: %v", err)
	}
	if err := store.BeginSummary(ctx, created.ID, SummaryDocTypeSummary, "test-model"); err != nil {
		t.Fatalf("BeginSummary: %v", err)
	}
	ready, err := store.Get(ctx, "owner", created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	s := &Server{d: Deps{Discussions: store}}
	s.applyDiscussionSummaryMeta(ctx, ready)

	if ready.Summary == nil {
		t.Fatal("ready summary meta is nil, want pending descriptor")
	}
	if ready.Summary.Status != SummaryGenerating || !ready.Summary.Pending {
		t.Fatalf("summary meta = %+v, want generating pending", ready.Summary)
	}
	if ready.Summary.Generation || ready.Summary.Available {
		t.Fatalf("summary meta = %+v, want no generation action and not available", ready.Summary)
	}
}
