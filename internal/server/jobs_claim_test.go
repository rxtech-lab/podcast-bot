package server

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sirily11/debate-bot/internal/config"
)

func newClaimTestRegistry(t *testing.T) *JobRegistry {
	t.Helper()
	jobs, err := NewJobRegistry(filepath.Join(t.TempDir(), "jobs.db"), "", "")
	if err != nil {
		t.Fatalf("NewJobRegistry: %v", err)
	}
	return jobs
}

func TestClaimRunAttemptLifecycle(t *testing.T) {
	jobs := newClaimTestRegistry(t)
	jobs.Add("job-1")

	if !jobs.ClaimRun("job-1", 1, time.Hour) {
		t.Fatal("first delivery of attempt 1 should claim")
	}
	if jobs.ClaimRun("job-1", 1, time.Hour) {
		t.Fatal("duplicate delivery of attempt 1 must not re-claim a fresh running job")
	}

	// Failed attempt: dispatch resets to pending, then attempt 2 arrives.
	jobs.Update("job-1", func(j *Job) { j.Status = JobPending })
	if !jobs.ClaimRun("job-1", 2, time.Hour) {
		t.Fatal("attempt 2 should claim a pending job with attempts=1")
	}
	if got := jobs.Get("job-1"); got.Status != JobRunning || got.Attempts != 2 {
		t.Fatalf("claimed job = status %s attempts %d, want running/2", got.Status, got.Attempts)
	}
}

func TestClaimRunStaleTakeover(t *testing.T) {
	jobs := newClaimTestRegistry(t)
	jobs.Add("job-2")
	if !jobs.ClaimRun("job-2", 1, time.Hour) {
		t.Fatal("initial claim failed")
	}
	// Same-attempt redelivery while the claim is fresh: consumer presumed
	// alive, do not double-run.
	if jobs.ClaimRun("job-2", 1, time.Hour) {
		t.Fatal("fresh claim must block same-attempt redelivery")
	}
	// Once the claim goes stale (consumer died mid-run), the redelivered
	// attempt may take over.
	if !jobs.ClaimRun("job-2", 1, 0) {
		t.Fatal("stale running claim should allow takeover")
	}
}

func TestClaimRunRespectsTerminalStates(t *testing.T) {
	jobs := newClaimTestRegistry(t)
	jobs.Add("job-3")
	jobs.Update("job-3", func(j *Job) { j.Status = JobError; j.Error = "stopped" })
	if jobs.ClaimRun("job-3", 1, time.Hour) {
		t.Fatal("errored job must not be claimable")
	}
	jobs.Update("job-3", func(j *Job) { j.Status = JobDone; j.Error = "" })
	if jobs.ClaimRun("job-3", 2, time.Hour) {
		t.Fatal("done job must not be claimable")
	}
}

func TestClaimSummaryRunLifecycle(t *testing.T) {
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "d.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()
	ctx := t.Context()
	d, err := store.Create(ctx, "user-1", "AI safety", planResponse{
		Script: &config.DebateTopic{Type: config.ContentTypeDiscussion, Title: "AI safety", Language: "en-US"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.BeginSummary(ctx, d.ID, SummaryDocTypeSummary, "model-x"); err != nil {
		t.Fatalf("BeginSummary: %v", err)
	}

	if ok, _ := store.ClaimSummaryRun(ctx, d.ID, SummaryDocTypeSummary, 1, time.Hour); !ok {
		t.Fatal("attempt 1 should claim a fresh generating doc")
	}
	if ok, _ := store.ClaimSummaryRun(ctx, d.ID, SummaryDocTypeSummary, 1, time.Hour); ok {
		t.Fatal("duplicate delivery of attempt 1 must not re-claim while fresh")
	}
	// Crash takeover: same attempt, stale claim. claimed_at has millisecond
	// precision, so let a couple of ms pass before the zero-threshold check.
	time.Sleep(5 * time.Millisecond)
	if ok, _ := store.ClaimSummaryRun(ctx, d.ID, SummaryDocTypeSummary, 1, 0); !ok {
		t.Fatal("stale claim should allow same-attempt takeover")
	}
	// Retry attempt.
	if ok, _ := store.ClaimSummaryRun(ctx, d.ID, SummaryDocTypeSummary, 2, time.Hour); !ok {
		t.Fatal("attempt 2 should claim after attempt 1")
	}
	// Terminal states block claims.
	if err := store.FailSummary(ctx, d.ID, SummaryDocTypeSummary, "boom"); err != nil {
		t.Fatalf("FailSummary: %v", err)
	}
	if ok, _ := store.ClaimSummaryRun(ctx, d.ID, SummaryDocTypeSummary, 3, time.Hour); ok {
		t.Fatal("failed doc must not be claimable")
	}
	// A fresh Begin (manual regenerate) resets the attempt counter.
	if err := store.BeginSummary(ctx, d.ID, SummaryDocTypeSummary, "model-x"); err != nil {
		t.Fatalf("BeginSummary again: %v", err)
	}
	if ok, _ := store.ClaimSummaryRun(ctx, d.ID, SummaryDocTypeSummary, 1, time.Hour); !ok {
		t.Fatal("attempt 1 should claim again after BeginSummary reset")
	}
}

func TestClaimRunStampsOwnerPod(t *testing.T) {
	jobs := newClaimTestRegistry(t)
	jobs.SetPodName("pod-a")
	jobs.Add("job-4")
	jobs.SetPodName("pod-b") // simulate the consuming pod differing from the accepting pod
	if !jobs.ClaimRun("job-4", 1, time.Hour) {
		t.Fatal("claim failed")
	}
	if got := jobs.Get("job-4"); got.OwnerPod != "pod-b" {
		t.Fatalf("OwnerPod = %q, want the consuming pod (pod-b)", got.OwnerPod)
	}
}
