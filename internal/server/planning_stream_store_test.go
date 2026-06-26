package server

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/sirily11/debate-bot/internal/planner"
)

func TestPlanningStreamStoreNilSafeWhenUnconfigured(t *testing.T) {
	store := NewPlanningStreamStore("", slog.Default())
	if store != nil {
		t.Fatalf("NewPlanningStreamStore empty url = %#v, want nil", store)
	}

	var nilStore *PlanningStreamStore
	if nilStore.Enabled() {
		t.Fatal("nil PlanningStreamStore should be disabled")
	}
	if active, ok := nilStore.Active(context.Background(), "conv"); ok || active != nil {
		t.Fatalf("nil Active = (%#v, %v), want nil,false", active, ok)
	}
}

func TestPlanningStreamResumeNoActiveReturns204(t *testing.T) {
	ds, err := NewDiscussionStore(filepath.Join(t.TempDir(), "planning-stream.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	t.Cleanup(func() { _ = ds.Close() })
	ps, err := NewPlanningStore(ds)
	if err != nil {
		t.Fatalf("NewPlanningStore: %v", err)
	}
	disc, err := ds.CreatePlaceholder(context.Background(), "anonymous", "stream recovery", "en-US", planner.DefaultTemplateID)
	if err != nil {
		t.Fatalf("CreatePlaceholder: %v", err)
	}
	if _, err := ps.EnsureConversation(context.Background(), "anonymous", disc.ID); err != nil {
		t.Fatalf("EnsureConversation: %v", err)
	}

	srv := New(Deps{Discussions: ds, Planning: ps, Log: slog.Default()})
	req := httptest.NewRequest(http.MethodGet, "/api/discussions/"+disc.ID+"/planning/stream", nil)
	resp := httptest.NewRecorder()
	srv.Handler().ServeHTTP(resp, req)
	if resp.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%q", resp.Code, resp.Body.String())
	}
}
