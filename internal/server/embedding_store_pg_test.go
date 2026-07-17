package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestEmbeddingStorePGVector exercises the Postgres/pgvector code path
// (extension + vector column + HNSW index + SQL KNN) against a real database.
// Skipped unless EMBEDDING_PG_TEST_URL points at a pgvector-enabled Postgres,
// e.g. the docker-compose `postgres` service:
//
//	EMBEDDING_PG_TEST_URL=postgres://debate:debate@127.0.0.1:5432/debate go test ./internal/server/ -run TestEmbeddingStorePGVector
func TestEmbeddingStorePGVector(t *testing.T) {
	pgURL := os.Getenv("EMBEDDING_PG_TEST_URL")
	if pgURL == "" {
		t.Skip("EMBEDDING_PG_TEST_URL not set")
	}
	ctx := context.Background()
	ds, err := NewDiscussionStore(filepath.Join(t.TempDir(), "unused.db"), pgURL, "")
	if err != nil {
		t.Fatalf("NewDiscussionStore(pg): %v", err)
	}
	es, err := NewEmbeddingStore(ds, 4)
	if err != nil {
		t.Fatalf("NewEmbeddingStore(pg): %v", err)
	}
	if !es.pgvectorReady {
		t.Fatal("pgvector not ready — extension or column/index creation failed")
	}
	if !es.SemanticEnabled() {
		t.Fatal("SemanticEnabled false on pgvector-ready store")
	}

	// The HNSW index must exist.
	var indexCount int
	if err := ds.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM pg_indexes
		WHERE tablename = 'discussion_chunks' AND indexname = 'discussion_chunks_embedding_idx'`).Scan(&indexCount); err != nil {
		t.Fatalf("pg_indexes lookup: %v", err)
	}
	if indexCount != 1 {
		t.Fatalf("HNSW index missing (count=%d)", indexCount)
	}

	d := newReadyEmbeddingDiscussion(t, ctx, ds, "pg-test-owner", "PGVector Roundtrip")
	t.Cleanup(func() {
		_, _ = ds.db.ExecContext(ctx, `DELETE FROM native_discussions WHERE id = ?`, d.ID)
	})

	chunks := []ChunkInput{
		{Kind: ChunkKindTranscript, ChunkIndex: 0, Text: "pg exact", Embedding: []float32{1, 0, 0, 0},
			Meta: ChunkMeta{Speakers: []string{"Alice"}, StartMS: 500, EndMS: 900}},
		{Kind: ChunkKindTranscript, ChunkIndex: 1, Text: "pg partial", Embedding: []float32{0.7, 0.7, 0, 0}},
		{Kind: ChunkKindSource, ChunkIndex: 2, Text: "pg unrelated", Embedding: []float32{0, 0, 1, 0}},
	}
	if err := es.ReplaceChunks(ctx, d.ID, "pg-model", "pg-hash", chunks); err != nil {
		t.Fatalf("ReplaceChunks(pg): %v", err)
	}
	hits, err := es.SearchGlobal(ctx, "pg-test-owner", []float32{1, 0, 0, 0}, "pg-model", 10)
	if err != nil {
		t.Fatalf("SearchGlobal(pg): %v", err)
	}
	if len(hits) != 3 || hits[0].Text != "pg exact" || hits[0].Similarity < 0.99 {
		t.Fatalf("pg KNN wrong: %+v", hits)
	}
	if hits[0].Meta.StartMS != 500 {
		t.Fatalf("pg meta not round-tripped: %+v", hits[0].Meta)
	}
	scoped, err := es.SearchDiscussion(ctx, "pg-test-owner", d.ID, []float32{0, 0, 1, 0}, "pg-model", 1)
	if err != nil || len(scoped) != 1 || scoped[0].Text != "pg unrelated" {
		t.Fatalf("pg scoped search: hits=%+v err=%v", scoped, err)
	}
}
