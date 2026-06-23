package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestHealthzOKWithoutAuthCookie(t *testing.T) {
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	srv := New(Deps{
		Discussions: store,
		Password:    "s3cret",
		Log:         slog.Default(),
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body healthResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Status != "ok" || body.Checks["discussions_db"].Status != "ok" {
		t.Fatalf("body = %+v, want healthy discussions_db", body)
	}
}

func TestHealthzFailsWhenDBClosed(t *testing.T) {
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	srv := New(Deps{Discussions: store, Log: slog.Default()})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
	var body healthResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Status != "error" || body.Checks["discussions_db"].Status != "error" {
		t.Fatalf("body = %+v, want failed discussions_db", body)
	}
}

func TestHealthzFailsWhenRedisInvalid(t *testing.T) {
	progress := NewDiscussionProgressStore(":", slog.Default())

	srv := New(Deps{Progress: progress, Log: slog.Default()})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
	var body healthResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Status != "error" || body.Checks["redis"].Status != "error" {
		t.Fatalf("body = %+v, want failed redis", body)
	}
}
