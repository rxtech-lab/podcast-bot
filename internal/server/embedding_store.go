package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DiscussionIndexState tracks the vectorization lifecycle of one discussion.
type DiscussionIndexState string

const (
	DiscussionIndexPending  DiscussionIndexState = "pending"
	DiscussionIndexIndexing DiscussionIndexState = "indexing"
	DiscussionIndexReady    DiscussionIndexState = "ready"
	DiscussionIndexFailed   DiscussionIndexState = "failed"
)

// Chunk kinds distinguish transcript passages from source-document passages.
const (
	ChunkKindTranscript = "transcript"
	ChunkKindSource     = "source"
)

// ChunkMeta anchors a chunk back to its origin: transcript chunks carry the
// speaker roster and time/line range (so search results and Q&A citations can
// deep-link into the player), source chunks carry the source identity.
type ChunkMeta struct {
	Speakers  []string `json:"speakers,omitempty"`
	StartMS   int64    `json:"start_ms,omitempty"`
	EndMS     int64    `json:"end_ms,omitempty"`
	LineStart int      `json:"line_start,omitempty"`
	LineEnd   int      `json:"line_end,omitempty"`

	SourceURL   string `json:"source_url,omitempty"`
	SourceTitle string `json:"source_title,omitempty"`
}

// ChunkInput is one chunk ready for persistence.
type ChunkInput struct {
	Kind       string
	ChunkIndex int
	Text       string
	Meta       ChunkMeta
	Embedding  []float32
}

// ChunkHit is one retrieval result, with cosine similarity in [0, 1]-ish
// (1 = identical direction).
type ChunkHit struct {
	ID           int64
	DiscussionID string
	Kind         string
	ChunkIndex   int
	Text         string
	Meta         ChunkMeta
	Similarity   float64
}

// DiscussionIndexStatus is one row of the per-discussion index bookkeeping.
type DiscussionIndexStatus struct {
	DiscussionID   string
	Status         DiscussionIndexState
	EmbeddingModel string
	ContentHash    string
	Error          string
	UpdatedAt      time.Time
}

// EmbeddingStore owns the vectorized podcast content: discussion_chunks (the
// chunk texts + embeddings) and discussion_index_status (per-discussion
// bookkeeping). It shares the DiscussionStore's handle like the other stores.
//
// The embedding vector lives in two dialect-dependent places: on Postgres a
// pgvector `embedding vector(N)` column with an HNSW index serves KNN in SQL;
// on SQLite/Turso (dev, E2E) the JSON text column is scanned and cosine
// similarity is computed in Go. Chunks are keyed by embedding_model so an
// admin model switch just makes old chunks invisible to retrieval until the
// backfill re-indexes them.
type EmbeddingStore struct {
	db         *sqlDB
	dimensions int
	// pgvectorReady is true when the vector extension + column + index exist.
	// False on Postgres means CREATE EXTENSION failed (insufficient privileges):
	// semantic features are disabled rather than degrading to a full-table scan.
	pgvectorReady bool
}

// NewEmbeddingStore builds the store over the discussion store's database and
// ensures its schema. dimensions fixes the pgvector column width; changing it
// later requires dropping discussion_chunks (documented destructive op).
func NewEmbeddingStore(ds *DiscussionStore, dimensions int) (*EmbeddingStore, error) {
	if ds == nil || ds.db == nil {
		return nil, errors.New("embedding store requires a discussion store")
	}
	if dimensions <= 0 {
		dimensions = 1536
	}
	s := &EmbeddingStore{db: ds.db, dimensions: dimensions}
	if err := s.ensureSchema(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

// Dimensions returns the fixed embedding vector width.
func (s *EmbeddingStore) Dimensions() int {
	if s == nil {
		return 0
	}
	return s.dimensions
}

// SemanticEnabled reports whether vector retrieval can run at all: always on
// SQLite/Turso (brute-force), and on Postgres only once pgvector is installed.
func (s *EmbeddingStore) SemanticEnabled() bool {
	if s == nil {
		return false
	}
	if s.db.kind == databasePostgres {
		return s.pgvectorReady
	}
	return true
}

func (s *EmbeddingStore) ensureSchema(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS discussion_chunks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			discussion_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			chunk_index INTEGER NOT NULL,
			text TEXT NOT NULL DEFAULT '',
			meta_json TEXT NOT NULL DEFAULT '{}',
			content_hash TEXT NOT NULL DEFAULT '',
			embedding_model TEXT NOT NULL DEFAULT '',
			embedding_json TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			FOREIGN KEY(discussion_id) REFERENCES native_discussions(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS discussion_chunks_discussion_idx
			ON discussion_chunks(discussion_id, kind, chunk_index)`,
		`CREATE INDEX IF NOT EXISTS discussion_chunks_model_idx
			ON discussion_chunks(embedding_model)`,
		`CREATE TABLE IF NOT EXISTS discussion_index_status (
			discussion_id TEXT PRIMARY KEY,
			status TEXT NOT NULL DEFAULT 'pending',
			embedding_model TEXT NOT NULL DEFAULT '',
			content_hash TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			updated_at INTEGER NOT NULL,
			FOREIGN KEY(discussion_id) REFERENCES native_discussions(id) ON DELETE CASCADE
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	if s.db.kind != databasePostgres {
		return nil
	}
	// pgvector lives only on the Postgres dialect. CREATE EXTENSION may need
	// elevated privileges on managed Postgres; failing is not fatal — semantic
	// features just report disabled until an operator installs the extension.
	if _, err := s.db.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
		return nil //nolint:nilerr // degrade to SemanticEnabled() == false
	}
	if _, err := s.db.ExecContext(ctx,
		fmt.Sprintf(`ALTER TABLE discussion_chunks ADD COLUMN IF NOT EXISTS embedding vector(%d)`, s.dimensions)); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS discussion_chunks_embedding_idx
		ON discussion_chunks USING hnsw (embedding vector_cosine_ops)`); err != nil {
		return err
	}
	s.pgvectorReady = true
	return nil
}

// encodeVector serializes a float32 vector to the pgvector text format, which
// doubles as the JSON storage format on non-Postgres dialects.
func encodeVector(vec []float32) string {
	var b strings.Builder
	b.Grow(len(vec)*10 + 2)
	b.WriteByte('[')
	for i, v := range vec {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(v), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

func decodeVector(text string) []float32 {
	var vec []float32
	if json.Unmarshal([]byte(text), &vec) != nil {
		return nil
	}
	return vec
}

func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// ReplaceChunks atomically swaps a discussion's chunks for the given model:
// prior chunks (any model) are deleted so a re-index never leaves a mix of
// vintages behind.
func (s *EmbeddingStore) ReplaceChunks(ctx context.Context, discussionID, model, contentHash string, chunks []ChunkInput) error {
	if s == nil {
		return errors.New("embedding store is not configured")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM discussion_chunks WHERE discussion_id = ?`, discussionID); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	for _, c := range chunks {
		metaJSON, err := json.Marshal(c.Meta)
		if err != nil {
			return err
		}
		vecText := encodeVector(c.Embedding)
		if s.pgvectorReady {
			if _, err := tx.ExecContext(ctx, `INSERT INTO discussion_chunks
				(discussion_id, kind, chunk_index, text, meta_json, content_hash, embedding_model, embedding_json, embedding, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, '', ?::vector, ?)`,
				discussionID, c.Kind, c.ChunkIndex, c.Text, string(metaJSON), contentHash, model, vecText, now); err != nil {
				return err
			}
		} else {
			if _, err := tx.ExecContext(ctx, `INSERT INTO discussion_chunks
				(discussion_id, kind, chunk_index, text, meta_json, content_hash, embedding_model, embedding_json, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				discussionID, c.Kind, c.ChunkIndex, c.Text, string(metaJSON), contentHash, model, vecText, now); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// SearchGlobal returns the most similar chunks across every ready discussion
// the owner can see, best first.
func (s *EmbeddingStore) SearchGlobal(ctx context.Context, owner string, queryVec []float32, model string, limit int) ([]ChunkHit, error) {
	return s.search(ctx, owner, "", queryVec, model, limit)
}

// SearchDiscussion returns the most similar chunks within one owned
// discussion, best first.
func (s *EmbeddingStore) SearchDiscussion(ctx context.Context, owner, discussionID string, queryVec []float32, model string, limit int) ([]ChunkHit, error) {
	discussionID = strings.TrimSpace(discussionID)
	if discussionID == "" {
		return nil, errors.New("discussion id is required")
	}
	return s.search(ctx, owner, discussionID, queryVec, model, limit)
}

func (s *EmbeddingStore) search(ctx context.Context, owner, discussionID string, queryVec []float32, model string, limit int) ([]ChunkHit, error) {
	if s == nil {
		return nil, errors.New("embedding store is not configured")
	}
	if !s.SemanticEnabled() {
		return nil, errors.New("semantic search is unavailable")
	}
	if limit <= 0 {
		limit = 20
	}
	if s.pgvectorReady {
		return s.searchPGVector(ctx, owner, discussionID, queryVec, model, limit)
	}
	return s.searchBruteForce(ctx, owner, discussionID, queryVec, model, limit)
}

func (s *EmbeddingStore) searchPGVector(ctx context.Context, owner, discussionID string, queryVec []float32, model string, limit int) ([]ChunkHit, error) {
	query := `SELECT c.id, c.discussion_id, c.kind, c.chunk_index, c.text, c.meta_json,
			1 - (c.embedding <=> ?::vector) AS similarity
		FROM discussion_chunks c
		JOIN native_discussions d ON d.id = c.discussion_id
		WHERE d.owner_user_id = ? AND d.status = ? AND c.embedding_model = ? AND c.embedding IS NOT NULL`
	args := []any{encodeVector(queryVec), owner, string(DiscussionReady), model}
	if discussionID != "" {
		query += ` AND c.discussion_id = ?`
		args = append(args, discussionID)
	}
	query += ` ORDER BY c.embedding <=> ?::vector ASC LIMIT ?`
	args = append(args, encodeVector(queryVec), limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChunkHits(rows)
}

func (s *EmbeddingStore) searchBruteForce(ctx context.Context, owner, discussionID string, queryVec []float32, model string, limit int) ([]ChunkHit, error) {
	query := `SELECT c.id, c.discussion_id, c.kind, c.chunk_index, c.text, c.meta_json, c.embedding_json
		FROM discussion_chunks c
		JOIN native_discussions d ON d.id = c.discussion_id
		WHERE d.owner_user_id = ? AND d.status = ? AND c.embedding_model = ?`
	args := []any{owner, string(DiscussionReady), model}
	if discussionID != "" {
		query += ` AND c.discussion_id = ?`
		args = append(args, discussionID)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hits := make([]ChunkHit, 0)
	for rows.Next() {
		var h ChunkHit
		var metaJSON, embeddingJSON string
		if err := rows.Scan(&h.ID, &h.DiscussionID, &h.Kind, &h.ChunkIndex, &h.Text, &metaJSON, &embeddingJSON); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(metaJSON), &h.Meta)
		h.Similarity = cosineSimilarity(queryVec, decodeVector(embeddingJSON))
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Similarity > hits[j].Similarity })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

func scanChunkHits(rows *sql.Rows) ([]ChunkHit, error) {
	hits := make([]ChunkHit, 0)
	for rows.Next() {
		var h ChunkHit
		var metaJSON string
		if err := rows.Scan(&h.ID, &h.DiscussionID, &h.Kind, &h.ChunkIndex, &h.Text, &metaJSON, &h.Similarity); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(metaJSON), &h.Meta)
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// ChunksForDiscussion returns a discussion's stored chunks of one kind,
// ordered by chunk_index. Used by Q&A tools (get_sources / show_transcript
// context) without re-fetching original rows.
func (s *EmbeddingStore) ChunksForDiscussion(ctx context.Context, discussionID, kind string) ([]ChunkHit, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, discussion_id, kind, chunk_index, text, meta_json, 0
		FROM discussion_chunks WHERE discussion_id = ? AND kind = ? ORDER BY chunk_index ASC`, discussionID, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChunkHits(rows)
}

// IndexStatus returns the bookkeeping row for a discussion, or nil.
func (s *EmbeddingStore) IndexStatus(ctx context.Context, discussionID string) (*DiscussionIndexStatus, error) {
	if s == nil {
		return nil, errors.New("embedding store is not configured")
	}
	row := s.db.QueryRowContext(ctx, `SELECT discussion_id, status, embedding_model, content_hash, error, updated_at
		FROM discussion_index_status WHERE discussion_id = ?`, discussionID)
	var st DiscussionIndexStatus
	var status string
	var updated int64
	if err := row.Scan(&st.DiscussionID, &status, &st.EmbeddingModel, &st.ContentHash, &st.Error, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	st.Status = DiscussionIndexState(status)
	st.UpdatedAt = time.UnixMilli(updated)
	return &st, nil
}

// setIndexStatus upserts the bookkeeping row.
func (s *EmbeddingStore) setIndexStatus(ctx context.Context, discussionID string, status DiscussionIndexState, model, contentHash, errMsg string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO discussion_index_status
		(discussion_id, status, embedding_model, content_hash, error, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(discussion_id) DO UPDATE SET
			status = excluded.status,
			embedding_model = excluded.embedding_model,
			content_hash = excluded.content_hash,
			error = excluded.error,
			updated_at = excluded.updated_at`,
		discussionID, string(status), model, contentHash, errMsg, time.Now().UnixMilli())
	return err
}

// MarkPending records that an index task has been enqueued.
func (s *EmbeddingStore) MarkPending(ctx context.Context, discussionID, model string) error {
	return s.setIndexStatus(ctx, discussionID, DiscussionIndexPending, model, "", "")
}

// MarkIndexing records that the worker started chunking/embedding.
func (s *EmbeddingStore) MarkIndexing(ctx context.Context, discussionID, model string) error {
	return s.setIndexStatus(ctx, discussionID, DiscussionIndexIndexing, model, "", "")
}

// MarkReady records a successful index of (model, contentHash).
func (s *EmbeddingStore) MarkReady(ctx context.Context, discussionID, model, contentHash string) error {
	return s.setIndexStatus(ctx, discussionID, DiscussionIndexReady, model, contentHash, "")
}

// MarkFailed records a terminal index failure.
func (s *EmbeddingStore) MarkFailed(ctx context.Context, discussionID, model, errMsg string) error {
	return s.setIndexStatus(ctx, discussionID, DiscussionIndexFailed, model, "", errMsg)
}

// StaleDiscussions returns up to limit of the owner's ready discussions whose
// index is missing, failed, stalled, or built with a different embedding
// model — the precheck backfill's work list, newest first. Content-hash
// staleness is not detectable here (it would require loading every
// transcript); the index task itself skips no-op re-indexes by hash.
func (s *EmbeddingStore) StaleDiscussions(ctx context.Context, owner, model string, limit int) ([]string, error) {
	if s == nil {
		return nil, errors.New("embedding store is not configured")
	}
	if limit <= 0 {
		limit = 25
	}
	// pending/indexing rows older than an hour are treated as stalled (server
	// restart mid-index, dropped task) and re-enqueued.
	stalledBefore := time.Now().Add(-time.Hour).UnixMilli()
	rows, err := s.db.QueryContext(ctx, `SELECT d.id
		FROM native_discussions d
		LEFT JOIN discussion_index_status st ON st.discussion_id = d.id
		WHERE d.owner_user_id = ? AND d.status = ?
			AND (st.discussion_id IS NULL
				OR st.status = 'failed'
				OR st.embedding_model != ?
				OR (st.status IN ('pending', 'indexing') AND st.updated_at < ?))
		ORDER BY d.created_at DESC
		LIMIT ?`, owner, string(DiscussionReady), model, stalledBefore, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
