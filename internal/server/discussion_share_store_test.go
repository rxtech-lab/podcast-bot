package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sirily11/debate-bot/internal/config"
)

func newShareTestDiscussion(t *testing.T, ctx context.Context, store *DiscussionStore, owner string) *Discussion {
	t.Helper()
	d, err := store.Create(ctx, owner, "Sharing topic", planResponse{
		Script: &config.DebateTopic{
			Title:    "Shareable Panel",
			Type:     config.ContentTypeDiscussion,
			Language: "en-US",
			Host:     config.AgentSpec{Name: "Host"},
		},
		Markdown: "# Shareable Panel",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return d
}

func TestShareCreateResolveRevoke(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "native-discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()

	owner := "oauth:owner"
	d := newShareTestDiscussion(t, ctx, store, owner)

	// Non-owner cannot create a share.
	if _, err := store.CreateShare(ctx, "oauth:intruder", d.ID, time.Hour); !errors.Is(err, errDiscussionForbidden) {
		t.Fatalf("CreateShare by non-owner = %v, want errDiscussionForbidden", err)
	}

	share, err := store.CreateShare(ctx, owner, d.ID, time.Hour)
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	if share.Token == "" {
		t.Fatal("CreateShare returned empty token")
	}

	gotID, err := store.ResolveShare(ctx, share.Token)
	if err != nil || gotID != d.ID {
		t.Fatalf("ResolveShare = (%q, %v), want (%q, nil)", gotID, err, d.ID)
	}

	shares, err := store.ListSharesForDiscussion(ctx, owner, d.ID)
	if err != nil || len(shares) != 1 {
		t.Fatalf("ListSharesForDiscussion = (%d, %v), want (1, nil)", len(shares), err)
	}

	ok, err := store.RevokeShare(ctx, owner, d.ID, share.Token)
	if err != nil || !ok {
		t.Fatalf("RevokeShare = (%v, %v), want (true, nil)", ok, err)
	}
	if _, err := store.ResolveShare(ctx, share.Token); !errors.Is(err, errDiscussionNotVisible) {
		t.Fatalf("ResolveShare after revoke = %v, want errDiscussionNotVisible", err)
	}
	shares, err = store.ListSharesForDiscussion(ctx, owner, d.ID)
	if err != nil || len(shares) != 0 {
		t.Fatalf("ListSharesForDiscussion after revoke = (%d, %v), want (0, nil)", len(shares), err)
	}
}

func TestShareExpiry(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "native-discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()

	owner := "oauth:owner"
	d := newShareTestDiscussion(t, ctx, store, owner)

	share, err := store.CreateShare(ctx, owner, d.ID, -time.Second) // already expired
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	if _, err := store.ResolveShare(ctx, share.Token); !errors.Is(err, errDiscussionNotVisible) {
		t.Fatalf("ResolveShare expired = %v, want errDiscussionNotVisible", err)
	}
}

func TestJoinDiscussionCap(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "native-discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()

	owner := "oauth:owner"
	d := newShareTestDiscussion(t, ctx, store, owner)

	// Owner joining is a no-op and never consumes a slot.
	for i := 0; i < 3; i++ {
		if err := store.JoinDiscussion(ctx, d.ID, owner, owner); err != nil {
			t.Fatalf("owner JoinDiscussion: %v", err)
		}
	}
	if n, _ := store.CountParticipants(ctx, d.ID); n != 0 {
		t.Fatalf("owner counted as participant: %d", n)
	}

	// Fill exactly to the cap.
	for i := 0; i < config.MaxParticipantsPerDiscussion; i++ {
		uid := fmt.Sprintf("oauth:joiner-%d", i)
		if err := store.JoinDiscussion(ctx, d.ID, owner, uid); err != nil {
			t.Fatalf("JoinDiscussion %d: %v", i, err)
		}
	}
	if n, _ := store.CountParticipants(ctx, d.ID); n != config.MaxParticipantsPerDiscussion {
		t.Fatalf("CountParticipants = %d, want %d", n, config.MaxParticipantsPerDiscussion)
	}

	// Re-joining an existing participant is idempotent and still allowed.
	if err := store.JoinDiscussion(ctx, d.ID, owner, "oauth:joiner-0"); err != nil {
		t.Fatalf("re-join existing participant: %v", err)
	}

	// The next distinct joiner is rejected.
	if err := store.JoinDiscussion(ctx, d.ID, owner, "oauth:one-too-many"); !errors.Is(err, errParticipantCapReached) {
		t.Fatalf("JoinDiscussion over cap = %v, want errParticipantCapReached", err)
	}
}

func TestAuthorizeShareParticipation(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "native-discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()

	owner := "oauth:owner"
	viewer := "oauth:viewer"
	d := newShareTestDiscussion(t, ctx, store, owner)
	if _, err := store.SetJob(ctx, owner, d.ID, "job-share"); err != nil {
		t.Fatalf("SetJob: %v", err)
	}
	share, err := store.CreateShare(ctx, owner, d.ID, time.Hour)
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}

	// Owner is allowed while the podcast is live even without a token.
	if err := store.AuthorizeShareParticipation(ctx, owner, d.ID, ""); err != nil {
		t.Fatalf("owner AuthorizeShareParticipation: %v", err)
	}
	// A token-holder who hasn't joined is still forbidden (private + not generating).
	if err := store.AuthorizeShareParticipation(ctx, viewer, d.ID, share.Token); !errors.Is(err, errDiscussionForbidden) {
		t.Fatalf("non-joined viewer = %v, want errDiscussionForbidden", err)
	}
	// After joining, the token-holder may participate.
	if err := store.JoinDiscussion(ctx, d.ID, owner, viewer); err != nil {
		t.Fatalf("JoinDiscussion: %v", err)
	}
	if err := store.AuthorizeShareParticipation(ctx, viewer, d.ID, share.Token); err != nil {
		t.Fatalf("joined viewer AuthorizeShareParticipation: %v", err)
	}
	// Without the token, the joined viewer is still forbidden (no public/generating path).
	if err := store.AuthorizeShareParticipation(ctx, viewer, d.ID, ""); !errors.Is(err, errDiscussionForbidden) {
		t.Fatalf("joined viewer without token = %v, want errDiscussionForbidden", err)
	}
	if err := store.SetJobResult(ctx, d.ID, DiscussionReady, "https://audio.example/share.mp3"); err != nil {
		t.Fatalf("SetJobResult ready: %v", err)
	}
	if err := store.AuthorizeShareParticipation(ctx, viewer, d.ID, share.Token); !errors.Is(err, errDiscussionForbidden) {
		t.Fatalf("ready joined viewer = %v, want errDiscussionForbidden", err)
	}
}

func TestShareResolvePublicMetadata(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "native-discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()

	d := newShareTestDiscussion(t, ctx, store, "oauth:owner")
	share, err := store.CreateShare(ctx, "oauth:owner", d.ID, time.Hour)
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	srv := New(Deps{
		Discussions: store,
		Env: &config.Env{
			AuthIssuer: "https://auth.example.test",
		},
		Log: slog.Default(),
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/share/" + share.Token)
	if err != nil {
		t.Fatalf("GET share resolve: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET share resolve status = %d, want 200", resp.StatusCode)
	}
	var got shareResolveResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode share resolve: %v", err)
	}
	if got.ID != d.ID || got.Title != "Shareable Panel" {
		t.Fatalf("share resolve = %+v, want id %q title Shareable Panel", got, d.ID)
	}
}
