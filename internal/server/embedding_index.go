package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/llm"
	"github.com/sirily11/debate-bot/internal/mq"
)

const (
	discussionIndexTimeout = 10 * time.Minute
	// indexBackfillTTL is how often one user's precheck may sweep for stale
	// indexes; the sweep itself is cheap but each hit costs embedding calls.
	indexBackfillTTL = 15 * time.Minute
	// indexBackfillLimit caps how many discussions one sweep enqueues.
	indexBackfillLimit = 25
)

var ErrIndexingNotConfigured = errors.New("content indexing is not configured")

// DiscussionIndexPayload is the wire payload of a queued indexing task.
type DiscussionIndexPayload struct {
	DiscussionID string `json:"discussion_id"`
}

// embeddingLLM builds an embeddings client on the shared OpenAI-compatible
// endpoint with the admin-resolved embedding model. Nil when semantic
// features are unconfigured (no model, no endpoint, or no vector storage).
func (s *Server) embeddingLLM(ctx context.Context) *llm.Client {
	if s.d.Env == nil || s.d.Embeddings == nil || !s.d.Embeddings.SemanticEnabled() {
		return nil
	}
	model := s.resolvedEmbeddingModel(ctx)
	if model == "" || s.d.Env.OpenAIBaseURL == "" {
		return nil
	}
	return llm.New(s.d.Env.OpenAIBaseURL, s.d.Env.OpenAIKey, model)
}

// SemanticSearchEnabled reports whether semantic endpoints can serve.
func (s *Server) SemanticSearchEnabled(ctx context.Context) bool {
	return s.embeddingLLM(ctx) != nil
}

// embedQuery vectorizes one search/retrieval query.
func (s *Server) embedQuery(ctx context.Context, text string) ([]float32, error) {
	client := s.embeddingLLM(ctx)
	if client == nil {
		return nil, ErrIndexingNotConfigured
	}
	vecs, _, err := client.Embed(ctx, []string{text}, s.d.Embeddings.Dimensions())
	if err != nil {
		return nil, err
	}
	if len(vecs) != 1 {
		return nil, fmt.Errorf("embed query: got %d vectors", len(vecs))
	}
	return vecs[0], nil
}

// StartDiscussionIndexing enqueues a background chunk+embed pass for one
// discussion, skipping when the stored index already matches the current
// embedding model and content hash. Callers treat this as fire-and-forget;
// indexing is a platform cost and never charges the user.
func (s *Server) StartDiscussionIndexing(ctx context.Context, discussionID string) error {
	discussionID = strings.TrimSpace(discussionID)
	if discussionID == "" || s.d.Discussions == nil || s.d.Embeddings == nil || s.d.MQ == nil {
		return ErrIndexingNotConfigured
	}
	model := s.resolvedEmbeddingModel(ctx)
	if model == "" || !s.d.Embeddings.SemanticEnabled() {
		return ErrIndexingNotConfigured
	}
	d, err := s.d.Discussions.DiscussionWithTranscript(ctx, discussionID)
	if err != nil {
		return err
	}
	if d == nil || d.Status != DiscussionReady {
		return nil
	}
	if len(d.Lines) == 0 && len(d.Sources) == 0 {
		return nil
	}
	hash := discussionContentHash(d.Lines, d.Sources)
	if st, err := s.d.Embeddings.IndexStatus(ctx, discussionID); err == nil && st != nil {
		upToDate := st.EmbeddingModel == model && st.ContentHash == hash && st.Status == DiscussionIndexReady
		inFlight := (st.Status == DiscussionIndexPending || st.Status == DiscussionIndexIndexing) &&
			time.Since(st.UpdatedAt) < time.Hour
		if upToDate || inFlight {
			return nil
		}
	}
	if err := s.d.Embeddings.MarkPending(ctx, discussionID, model); err != nil {
		return err
	}
	task, err := mq.NewTask(mq.TaskDiscussionIndex, discussionID, DiscussionIndexPayload{DiscussionID: discussionID})
	if err == nil {
		err = s.d.MQ.Publish(ctx, mq.QueueDocs, task)
	}
	if err != nil {
		_ = s.d.Embeddings.MarkFailed(ctx, discussionID, model, "failed to enqueue indexing")
		return err
	}
	return nil
}

// RunDiscussionIndexTask executes one queued indexing attempt: load the
// discussion content, chunk it, embed the chunks, and swap them into the
// store. A non-nil return is a failed attempt; the dispatch layer decides
// retry vs terminal (FailDiscussionIndexTask).
func (s *Server) RunDiscussionIndexTask(ctx context.Context, p DiscussionIndexPayload) error {
	ctx, cancel := context.WithTimeout(ctx, discussionIndexTimeout)
	defer cancel()
	model := s.resolvedEmbeddingModel(ctx)
	client := s.embeddingLLM(ctx)
	if client == nil {
		return mq.Permanent(ErrIndexingNotConfigured)
	}
	d, err := s.d.Discussions.DiscussionWithTranscript(ctx, p.DiscussionID)
	if err != nil {
		return fmt.Errorf("load discussion: %w", err)
	}
	if d == nil {
		return mq.Permanent(fmt.Errorf("discussion %s not found", p.DiscussionID))
	}
	hash := discussionContentHash(d.Lines, d.Sources)
	if st, serr := s.d.Embeddings.IndexStatus(ctx, p.DiscussionID); serr == nil && st != nil &&
		st.Status == DiscussionIndexReady && st.EmbeddingModel == model && st.ContentHash == hash {
		return nil
	}
	if err := s.d.Embeddings.MarkIndexing(ctx, p.DiscussionID, model); err != nil {
		return err
	}
	chunks := chunkTranscript(d.Lines)
	chunks = append(chunks, chunkSources(d.Sources, len(chunks))...)
	if len(chunks) == 0 {
		return s.d.Embeddings.MarkReady(ctx, p.DiscussionID, model, hash)
	}
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Text
	}
	vectors, usage, err := client.Embed(ctx, texts, s.d.Embeddings.Dimensions())
	if err != nil {
		return fmt.Errorf("embed chunks: %w", err)
	}
	for i := range chunks {
		chunks[i].Embedding = vectors[i]
	}
	if err := s.d.Embeddings.ReplaceChunks(ctx, p.DiscussionID, model, hash, chunks); err != nil {
		return fmt.Errorf("store chunks: %w", err)
	}
	if err := s.d.Embeddings.MarkReady(ctx, p.DiscussionID, model, hash); err != nil {
		return err
	}
	if s.d.Log != nil {
		s.d.Log.Info("discussion indexed",
			"discussion_id", p.DiscussionID,
			"model", model,
			"chunks", len(chunks),
			"embedding_tokens", usage.TotalTokens)
	}
	return nil
}

// FailDiscussionIndexTask records the terminal failure of a queued indexing
// task so the backfill can retry it later.
func (s *Server) FailDiscussionIndexTask(p DiscussionIndexPayload, cause error) {
	if s.d.Embeddings == nil {
		return
	}
	msg := "indexing failed"
	if cause != nil {
		msg = cause.Error()
	}
	ctx := context.Background()
	_ = s.d.Embeddings.MarkFailed(ctx, p.DiscussionID, s.resolvedEmbeddingModel(ctx), msg)
}

// SeedE2EIndexes synchronously indexes every seeded ready podcast at E2E
// boot, so semantic search and the Q&A entry points are live from the first
// UI-test interaction instead of waiting for the precheck backfill queue.
func (s *Server) SeedE2EIndexes(ctx context.Context) {
	if s.d.Embeddings == nil || !s.d.Embeddings.SemanticEnabled() {
		return
	}
	model := s.resolvedEmbeddingModel(ctx)
	if model == "" {
		return
	}
	for _, owner := range []string{"test", "test2"} {
		ids, err := s.d.Embeddings.StaleDiscussions(ctx, owner, model, 100)
		if err != nil {
			continue
		}
		for _, id := range ids {
			if err := s.RunDiscussionIndexTask(ctx, DiscussionIndexPayload{DiscussionID: id}); err != nil && s.d.Log != nil {
				s.d.Log.Warn("e2e seed index failed", "discussion_id", id, "err", err)
			}
		}
	}
}

// enqueueStaleIndexBackfill sweeps the user's ready discussions for missing,
// failed, stalled, or wrong-model indexes and enqueues them. Rate-limited per
// user; called fire-and-forget from the precheck endpoint so pre-embedding
// podcasts (and everything after an admin model switch) get indexed without
// user action.
func (s *Server) enqueueStaleIndexBackfill(userID string) {
	if userID == "" || s.d.Embeddings == nil || s.d.MQ == nil {
		return
	}
	s.indexBackfillMu.Lock()
	if s.indexBackfillLast == nil {
		s.indexBackfillLast = map[string]time.Time{}
	}
	if last, ok := s.indexBackfillLast[userID]; ok && time.Since(last) < indexBackfillTTL {
		s.indexBackfillMu.Unlock()
		return
	}
	s.indexBackfillLast[userID] = time.Now()
	s.indexBackfillMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	model := s.resolvedEmbeddingModel(ctx)
	if model == "" || !s.d.Embeddings.SemanticEnabled() {
		return
	}
	ids, err := s.d.Embeddings.StaleDiscussions(ctx, userID, model, indexBackfillLimit)
	if err != nil {
		if s.d.Log != nil {
			s.d.Log.Warn("index backfill sweep failed", "user", userID, "err", err)
		}
		return
	}
	for _, id := range ids {
		if err := s.StartDiscussionIndexing(ctx, id); err != nil && !errors.Is(err, ErrIndexingNotConfigured) {
			if s.d.Log != nil {
				s.d.Log.Warn("index backfill enqueue failed", "discussion_id", id, "err", err)
			}
		}
	}
}
