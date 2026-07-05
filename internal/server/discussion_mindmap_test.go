package server

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
	contentcreator "github.com/sirily11/debate-bot/internal/content_creator"
)

func newMindmapTestStore(t *testing.T) *DiscussionStore {
	t.Helper()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "mindmaps.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func createMindmapTestDiscussion(t *testing.T, store *DiscussionStore, contentType string) *Discussion {
	t.Helper()
	ctx := context.Background()
	created, err := store.Create(ctx, "owner", "AI safety", planResponse{
		Script: &config.DebateTopic{Type: contentType, Title: "AI safety", Language: "en-US"},
	})
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
	return ready
}

func TestApplyDiscussionMindmapMetaOffersGenerationForDiscussion(t *testing.T) {
	ctx := context.Background()
	store := newMindmapTestStore(t)
	ready := createMindmapTestDiscussion(t, store, config.ContentTypeDiscussion)

	s := &Server{d: Deps{Discussions: store}}
	s.applyDiscussionMindmapMeta(ctx, ready)

	if ready.Mindmap == nil {
		t.Fatal("mindmap meta is nil, want generation-capable descriptor")
	}
	if !ready.Mindmap.Generation {
		t.Fatalf("mindmap generation = false, want true: %+v", ready.Mindmap)
	}
	if ready.Mindmap.Pending || ready.Mindmap.Available {
		t.Fatalf("mindmap meta = %+v, want not pending and not available", ready.Mindmap)
	}
}

func TestApplyDiscussionMindmapMetaSkipsOtherTypes(t *testing.T) {
	ctx := context.Background()
	store := newMindmapTestStore(t)
	s := &Server{d: Deps{Discussions: store}}

	for _, contentType := range []string{config.ContentTypeDebate, config.ContentTypeAudioBook, config.ContentTypeSeries, config.ContentTypeSituationPuzzle} {
		ready := createMindmapTestDiscussion(t, store, contentType)
		s.applyDiscussionMindmapMeta(ctx, ready)
		if ready.Mindmap != nil {
			t.Fatalf("type %q: mindmap meta = %+v, want nil", contentType, ready.Mindmap)
		}
	}
}

func TestApplyDiscussionMindmapMetaReportsPending(t *testing.T) {
	ctx := context.Background()
	store := newMindmapTestStore(t)
	ready := createMindmapTestDiscussion(t, store, config.ContentTypeDiscussion)

	if err := store.BeginSummary(ctx, ready.ID, SummaryDocTypeMindmap, "test-model"); err != nil {
		t.Fatalf("BeginSummary: %v", err)
	}
	s := &Server{d: Deps{Discussions: store}}
	s.applyDiscussionMindmapMeta(ctx, ready)

	if ready.Mindmap == nil || !ready.Mindmap.Pending || ready.Mindmap.Status != SummaryGenerating {
		t.Fatalf("mindmap meta = %+v, want generating pending", ready.Mindmap)
	}
	if ready.Mindmap.Generation || ready.Mindmap.Available {
		t.Fatalf("mindmap meta = %+v, want no generation action and not available", ready.Mindmap)
	}
}

func TestPodcastDocumentActionsMindmapGating(t *testing.T) {
	ctx := context.Background()
	store := newMindmapTestStore(t)
	s := &Server{d: Deps{Discussions: store}}

	hasItem := func(items []discussionUIActionItem, id string) bool {
		for _, item := range items {
			if item.ID == id {
				return true
			}
		}
		return false
	}
	actionsFor := func(d *Discussion) []discussionUIActionItem {
		s.applyDiscussionMindmapMeta(ctx, d)
		return s.podcastDocumentActions(nil, d, contentcreator.Lang(0))
	}

	discussion := createMindmapTestDiscussion(t, store, config.ContentTypeDiscussion)
	items := actionsFor(discussion)
	if !hasItem(items, "generate-mindmap") {
		t.Fatalf("discussion without mindmap: items = %+v, want generate-mindmap", items)
	}

	if err := store.BeginSummary(ctx, discussion.ID, SummaryDocTypeMindmap, "m"); err != nil {
		t.Fatalf("BeginSummary: %v", err)
	}
	items = actionsFor(discussion)
	if !hasItem(items, "mindmap-pending") || hasItem(items, "generate-mindmap") {
		t.Fatalf("generating mindmap: items = %+v, want mindmap-pending only", items)
	}

	if err := store.SaveSummary(ctx, discussion.ID, SummaryDocTypeMindmap, `{"version":1,"root":{"id":"root","title":"t"}}`, "m", SummaryUsage{}); err != nil {
		t.Fatalf("SaveSummary: %v", err)
	}
	items = actionsFor(discussion)
	if !hasItem(items, "open-mindmap") {
		t.Fatalf("ready mindmap: items = %+v, want open-mindmap", items)
	}

	debate := createMindmapTestDiscussion(t, store, config.ContentTypeDebate)
	items = actionsFor(debate)
	for _, id := range []string{"open-mindmap", "mindmap-pending", "generate-mindmap"} {
		if hasItem(items, id) {
			t.Fatalf("debate podcast: items = %+v, must not contain %s", items, id)
		}
	}
}

func TestUpdateSummaryMarkdownRequiresReadyRow(t *testing.T) {
	ctx := context.Background()
	store := newMindmapTestStore(t)
	discussion := createMindmapTestDiscussion(t, store, config.ContentTypeDiscussion)

	if err := store.UpdateSummaryMarkdown(ctx, discussion.ID, SummaryDocTypeMindmap, "{}"); err == nil {
		t.Fatal("expected error updating a missing mindmap row")
	}
	if err := store.BeginSummary(ctx, discussion.ID, SummaryDocTypeMindmap, "m"); err != nil {
		t.Fatalf("BeginSummary: %v", err)
	}
	if err := store.UpdateSummaryMarkdown(ctx, discussion.ID, SummaryDocTypeMindmap, "{}"); err == nil {
		t.Fatal("expected error updating a generating mindmap row")
	}
	if err := store.SaveSummary(ctx, discussion.ID, SummaryDocTypeMindmap, `{"version":1,"root":{"id":"root","title":"t"}}`, "m", SummaryUsage{}); err != nil {
		t.Fatalf("SaveSummary: %v", err)
	}
	edited := `{"version":1,"root":{"id":"root","title":"edited"}}`
	if err := store.UpdateSummaryMarkdown(ctx, discussion.ID, SummaryDocTypeMindmap, edited); err != nil {
		t.Fatalf("UpdateSummaryMarkdown: %v", err)
	}
	doc, err := store.SummaryDocumentFor(ctx, discussion.ID, SummaryDocTypeMindmap)
	if err != nil || doc == nil {
		t.Fatalf("SummaryDocumentFor: %v, %v", doc, err)
	}
	if doc.Markdown != edited {
		t.Fatalf("markdown = %q, want %q", doc.Markdown, edited)
	}
	if doc.Status != SummaryReadyState {
		t.Fatalf("status = %q, want ready", doc.Status)
	}
}
