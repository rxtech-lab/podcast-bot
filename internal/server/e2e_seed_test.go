package server

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
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

// TestSeedE2EUploadedAudioFixture verifies the uploaded-audio fixture the
// transcript-editor UI tests (TranscriptRetimeTests) rely on: a planning-stage
// discussion whose script validates as type=uploaded-audio and carries the
// five caption segments the tests retime and assert against.
func TestSeedE2EUploadedAudioFixture(t *testing.T) {
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

	d, err := store.Get(ctx, "test", "test-uploaded-audio")
	if err != nil || d == nil {
		t.Fatalf("load uploaded-audio fixture: %v", err)
	}
	if d.Status != DiscussionPlanning {
		t.Fatalf("fixture status = %q, want planning (segment edits require it)", d.Status)
	}
	if d.Script == nil || d.Script.Type != config.ContentTypeUploadedAudio {
		t.Fatalf("fixture script = %+v, want type uploaded-audio", d.Script)
	}
	if err := config.ValidateTopic(d.Script); err != nil {
		t.Fatalf("fixture script does not validate: %v", err)
	}
	if got := len(d.Script.TranscriptSegments); got != 5 {
		t.Fatalf("fixture segments = %d, want 5", got)
	}
	if d.Script.UploadedAudioDurationMS != 60_000 {
		t.Fatalf("fixture audio duration = %d, want 60000", d.Script.UploadedAudioDurationMS)
	}
	for i, seg := range d.Script.TranscriptSegments {
		if seg.OffsetMS != int64(i)*5000 || seg.DurationMS != 4000 {
			t.Fatalf("segment %d timing = %d+%d, want %d+4000", i, seg.OffsetMS, seg.DurationMS, int64(i)*5000)
		}
		end := seg.OffsetMS + seg.DurationMS
		if end > d.Script.UploadedAudioDurationMS {
			t.Fatalf("segment %d ends at %d, past the audio duration", i, end)
		}
	}
}

func TestSeedE2EPlanningShortfallFixture(t *testing.T) {
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

	shortfall, err := store.Get(ctx, "test", "test-plan-shortfall")
	if err != nil || shortfall == nil {
		t.Fatalf("load shortfall plan fixture: %v", err)
	}
	if shortfall.ID == "test-plan" || shortfall.Status != DiscussionPlanning {
		t.Fatalf("shortfall fixture = id %q status %q, want dedicated planning discussion", shortfall.ID, shortfall.Status)
	}
}
