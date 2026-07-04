package server

import (
	"context"
	"path/filepath"
	"testing"
)

// TestSeedE2EAudioBookFixtures verifies the seeded audiobook chain the iOS
// XCUITest suite (AudiobookAlbumTests) relies on: a 12-chapter root with
// chapters 1-3 generated, a batch child covering 4-5, and both grouped into
// the auto album, with chapter progress computed accordingly.
func TestSeedE2EAudioBookFixtures(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()
	points, err := NewPointsStore(store)
	if err != nil {
		t.Fatalf("NewPointsStore: %v", err)
	}
	if err := store.SeedE2E(ctx, points); err != nil {
		t.Fatalf("SeedE2E: %v", err)
	}
	// Idempotence: seeding twice must not fail or duplicate.
	if err := store.SeedE2E(ctx, points); err != nil {
		t.Fatalf("SeedE2E second run: %v", err)
	}

	root, err := store.Get(ctx, "test", "test-audiobook")
	if err != nil || root == nil {
		t.Fatalf("load seeded root: %v", err)
	}
	if !discussionIsAudioBook(root) || len(root.Script.AudioBookChapters) != 12 {
		t.Fatalf("seeded root script = %+v, want 12-chapter audiobook", root.Script)
	}
	if root.AlbumID != "test-album" {
		t.Fatalf("root album = %q, want test-album", root.AlbumID)
	}
	child, err := store.Get(ctx, "test", "test-audiobook-part2")
	if err != nil || child == nil {
		t.Fatalf("load seeded batch child: %v", err)
	}
	if child.ReferenceDiscussionID != "test-audiobook" || child.AlbumID != "test-album" {
		t.Fatalf("batch child linkage = ref %q album %q", child.ReferenceDiscussionID, child.AlbumID)
	}

	album, err := store.GetAlbum(ctx, "test", "test-album")
	if err != nil || album == nil {
		t.Fatalf("load seeded album: %v", err)
	}
	if album.EpisodeCount != 2 || album.Kind != albumKindAuto {
		t.Fatalf("seeded album = %+v, want 2 episodes / auto", album)
	}
	episodes, err := store.AlbumEpisodes(ctx, "test", "test-album")
	if err != nil || len(episodes) != 2 {
		t.Fatalf("album episodes = %d (%v), want 2", len(episodes), err)
	}
	if episodes[0].ID != "test-audiobook" || episodes[1].ID != "test-audiobook-part2" {
		t.Fatalf("album episode order = %s, %s; want root then batch", episodes[0].ID, episodes[1].ID)
	}

	srv := New(Deps{Discussions: store})
	states, err := srv.audioBookChapterStates(ctx, "test", root, "")
	if err != nil {
		t.Fatalf("chapter states: %v", err)
	}
	if len(states) != 12 {
		t.Fatalf("chapter states = %d, want 12", len(states))
	}
	for _, st := range states {
		want := chapterStatusPending
		if st.Index <= 5 {
			want = chapterStatusDone
		}
		if st.Status != want {
			t.Fatalf("chapter %d status = %q, want %q", st.Index, st.Status, want)
		}
	}
}
