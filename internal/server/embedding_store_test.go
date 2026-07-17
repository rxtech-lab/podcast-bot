package server

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
)

func newEmbeddingTestStores(t *testing.T) (*DiscussionStore, *EmbeddingStore) {
	t.Helper()
	ds, err := NewDiscussionStore(filepath.Join(t.TempDir(), "embeddings.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	es, err := NewEmbeddingStore(ds, 4)
	if err != nil {
		t.Fatalf("NewEmbeddingStore: %v", err)
	}
	return ds, es
}

func newReadyEmbeddingDiscussion(t *testing.T, ctx context.Context, ds *DiscussionStore, owner, title string) *Discussion {
	t.Helper()
	d, err := ds.Create(ctx, owner, title, planResponse{
		Script: &config.DebateTopic{
			Title:    title,
			Type:     config.ContentTypeDiscussion,
			Language: "en-US",
			Host:     config.AgentSpec{Name: "Host"},
		},
		Markdown: "# " + title,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := ds.db.ExecContext(ctx, `UPDATE native_discussions SET status = ? WHERE id = ?`,
		string(DiscussionReady), d.ID); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	return d
}

func TestEmbeddingStoreSearchOrdersBySimilarity(t *testing.T) {
	ctx := context.Background()
	ds, es := newEmbeddingTestStores(t)
	d := newReadyEmbeddingDiscussion(t, ctx, ds, "owner", "Vectors")

	chunks := []ChunkInput{
		{Kind: ChunkKindTranscript, ChunkIndex: 0, Text: "exact match", Embedding: []float32{1, 0, 0, 0},
			Meta: ChunkMeta{Speakers: []string{"Alice"}, StartMS: 1000, EndMS: 2000}},
		{Kind: ChunkKindTranscript, ChunkIndex: 1, Text: "partial match", Embedding: []float32{0.7, 0.7, 0, 0}},
		{Kind: ChunkKindSource, ChunkIndex: 2, Text: "unrelated", Embedding: []float32{0, 0, 1, 0},
			Meta: ChunkMeta{SourceURL: "https://example.com", SourceTitle: "Doc"}},
	}
	if err := es.ReplaceChunks(ctx, d.ID, "model-a", "hash-1", chunks); err != nil {
		t.Fatalf("ReplaceChunks: %v", err)
	}

	hits, err := es.SearchGlobal(ctx, "owner", []float32{1, 0, 0, 0}, "model-a", 10)
	if err != nil {
		t.Fatalf("SearchGlobal: %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("got %d hits, want 3", len(hits))
	}
	if hits[0].Text != "exact match" || hits[1].Text != "partial match" || hits[2].Text != "unrelated" {
		t.Fatalf("wrong order: %q, %q, %q", hits[0].Text, hits[1].Text, hits[2].Text)
	}
	if hits[0].Similarity < 0.99 {
		t.Fatalf("exact match similarity = %f", hits[0].Similarity)
	}
	if hits[0].Meta.StartMS != 1000 || len(hits[0].Meta.Speakers) != 1 {
		t.Fatalf("meta not round-tripped: %+v", hits[0].Meta)
	}

	// Model filter: chunks embedded with a different model are invisible.
	if hits, err := es.SearchGlobal(ctx, "owner", []float32{1, 0, 0, 0}, "model-b", 10); err != nil || len(hits) != 0 {
		t.Fatalf("model-b search: hits=%d err=%v", len(hits), err)
	}
	// Ownership: another user sees nothing.
	if hits, err := es.SearchGlobal(ctx, "intruder", []float32{1, 0, 0, 0}, "model-a", 10); err != nil || len(hits) != 0 {
		t.Fatalf("intruder search: hits=%d err=%v", len(hits), err)
	}

	// Per-discussion search scopes to the one podcast.
	other := newReadyEmbeddingDiscussion(t, ctx, ds, "owner", "Other")
	if err := es.ReplaceChunks(ctx, other.ID, "model-a", "hash-2", []ChunkInput{
		{Kind: ChunkKindTranscript, ChunkIndex: 0, Text: "other podcast", Embedding: []float32{1, 0, 0, 0}},
	}); err != nil {
		t.Fatalf("ReplaceChunks other: %v", err)
	}
	scoped, err := es.SearchDiscussion(ctx, "owner", d.ID, []float32{1, 0, 0, 0}, "model-a", 10)
	if err != nil {
		t.Fatalf("SearchDiscussion: %v", err)
	}
	for _, h := range scoped {
		if h.DiscussionID != d.ID {
			t.Fatalf("scoped search leaked discussion %s", h.DiscussionID)
		}
	}
}

func TestEmbeddingStoreReplaceChunksSwapsAtomically(t *testing.T) {
	ctx := context.Background()
	ds, es := newEmbeddingTestStores(t)
	d := newReadyEmbeddingDiscussion(t, ctx, ds, "owner", "Swap")

	first := []ChunkInput{{Kind: ChunkKindTranscript, ChunkIndex: 0, Text: "old", Embedding: []float32{1, 0, 0, 0}}}
	if err := es.ReplaceChunks(ctx, d.ID, "model-a", "h1", first); err != nil {
		t.Fatalf("ReplaceChunks: %v", err)
	}
	second := []ChunkInput{
		{Kind: ChunkKindTranscript, ChunkIndex: 0, Text: "new one", Embedding: []float32{1, 0, 0, 0}},
		{Kind: ChunkKindTranscript, ChunkIndex: 1, Text: "new two", Embedding: []float32{0, 1, 0, 0}},
	}
	if err := es.ReplaceChunks(ctx, d.ID, "model-b", "h2", second); err != nil {
		t.Fatalf("ReplaceChunks 2: %v", err)
	}
	hits, err := es.SearchGlobal(ctx, "owner", []float32{1, 0, 0, 0}, "model-b", 10)
	if err != nil {
		t.Fatalf("SearchGlobal: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("got %d hits after swap, want 2", len(hits))
	}
	for _, h := range hits {
		if h.Text == "old" {
			t.Fatal("old chunk survived the swap")
		}
	}
}

func TestEmbeddingStoreIndexStatusAndStaleness(t *testing.T) {
	ctx := context.Background()
	ds, es := newEmbeddingTestStores(t)
	d := newReadyEmbeddingDiscussion(t, ctx, ds, "owner", "Stale")

	// No status row: stale.
	ids, err := es.StaleDiscussions(ctx, "owner", "model-a", 10)
	if err != nil || len(ids) != 1 || ids[0] != d.ID {
		t.Fatalf("StaleDiscussions (missing row): ids=%v err=%v", ids, err)
	}

	if err := es.MarkReady(ctx, d.ID, "model-a", "hash-1"); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	st, err := es.IndexStatus(ctx, d.ID)
	if err != nil || st == nil || st.Status != DiscussionIndexReady || st.EmbeddingModel != "model-a" {
		t.Fatalf("IndexStatus after ready: %+v err=%v", st, err)
	}

	// Ready with the current model: not stale.
	if ids, _ := es.StaleDiscussions(ctx, "owner", "model-a", 10); len(ids) != 0 {
		t.Fatalf("ready discussion reported stale: %v", ids)
	}
	// Admin switches the embedding model: stale again.
	if ids, _ := es.StaleDiscussions(ctx, "owner", "model-b", 10); len(ids) != 1 {
		t.Fatalf("model switch did not mark stale")
	}
	// Failed: stale.
	if err := es.MarkFailed(ctx, d.ID, "model-a", "boom"); err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if ids, _ := es.StaleDiscussions(ctx, "owner", "model-a", 10); len(ids) != 1 {
		t.Fatalf("failed discussion not reported stale")
	}
	// Fresh pending (just enqueued): not stale.
	if err := es.MarkPending(ctx, d.ID, "model-a"); err != nil {
		t.Fatalf("MarkPending: %v", err)
	}
	if ids, _ := es.StaleDiscussions(ctx, "owner", "model-a", 10); len(ids) != 0 {
		t.Fatalf("fresh pending discussion reported stale")
	}
}
