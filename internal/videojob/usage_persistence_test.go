package videojob

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sirily11/debate-bot/internal/llm"
	"github.com/sirily11/debate-bot/internal/server"
)

func TestPersistUsageSnapshotStoresMediaOnlyUsageBeforeCompletion(t *testing.T) {
	ctx := context.Background()
	jobs, err := server.NewJobRegistry(filepath.Join(t.TempDir(), "jobs.db"), "", "")
	if err != nil {
		t.Fatalf("NewJobRegistry: %v", err)
	}
	discussions, err := server.NewDiscussionStore(filepath.Join(t.TempDir(), "discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer discussions.Close()

	jobs.Add("job-a")
	disc, err := discussions.CreatePlaceholder(ctx, "user-a", "topic", "en-US")
	if err != nil {
		t.Fatalf("CreatePlaceholder: %v", err)
	}

	persistUsageSnapshot(ctx, Deps{
		Jobs:         jobs,
		Discussions:  discussions,
		DiscussionID: disc.ID,
	}, "job-a", llm.UsageSummary{
		TTSCharacters:    1234,
		TTSCostUSD:       0.02468,
		MusicGenerations: 1,
		MusicCostUSD:     0.16,
	})

	job := jobs.Get("job-a")
	if job == nil {
		t.Fatal("job disappeared")
	}
	if job.TotalTokens != 0 || job.TTSCostUSD != 0.02468 || job.MusicCostUSD != 0.16 {
		t.Fatalf("job usage = %+v, want media-only usage persisted", job)
	}
	if len(job.Logs) != 0 {
		t.Fatalf("usage snapshot should not create user-visible logs, got %+v", job.Logs)
	}

	got, err := discussions.Get(ctx, "user-a", disc.ID)
	if err != nil {
		t.Fatalf("Get discussion: %v", err)
	}
	if got == nil {
		t.Fatal("discussion disappeared")
	}
	if got.TotalTokens != 0 || got.TTSCostUSD != 0.02468 || got.MusicCostUSD != 0.16 {
		t.Fatalf("discussion usage = %+v, want media-only usage persisted", got)
	}
}
