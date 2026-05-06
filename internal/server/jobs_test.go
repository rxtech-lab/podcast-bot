package server

import (
	"path/filepath"
	"testing"
)

func TestJobRegistryPersistsJobsAndLogs(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "jobs.db")
	jobs, err := NewJobRegistry(dbPath)
	if err != nil {
		t.Fatalf("NewJobRegistry: %v", err)
	}

	jobs.Add("job-a")
	jobs.Update("job-a", func(j *Job) {
		j.Status = JobDone
		j.Title = "Finished Job"
		j.VideoPath = "/tmp/video.mp4"
		j.HasVideo = true
		j.ElapsedMS = 1234
		j.Phase = "ended"
		j.PhaseLabel = "Done"
	})
	jobs.AppendLog("job-a", "status", "queued", nil)
	jobs.AppendLog("job-a", "phase", "Done", map[string]string{"phase": "ended"})

	reopened, err := NewJobRegistry(dbPath)
	if err != nil {
		t.Fatalf("reopen NewJobRegistry: %v", err)
	}
	got := reopened.Get("job-a")
	if got == nil {
		t.Fatal("Get(job-a) = nil")
	}
	if got.Status != JobDone || got.Title != "Finished Job" || !got.HasVideo {
		t.Fatalf("job snapshot = %+v", got)
	}
	if got.ElapsedMS != 1234 || got.Phase != "ended" || got.PhaseLabel != "Done" {
		t.Fatalf("progress fields = elapsed %d phase %q label %q",
			got.ElapsedMS, got.Phase, got.PhaseLabel)
	}
	if len(got.Logs) != 2 {
		t.Fatalf("logs len = %d, want 2", len(got.Logs))
	}
	if got.Logs[0].Kind != "status" || got.Logs[0].Text != "queued" {
		t.Fatalf("first log = %+v", got.Logs[0])
	}
	if got.Logs[1].Kind != "phase" || got.Logs[1].Text != "Done" {
		t.Fatalf("second log = %+v", got.Logs[1])
	}
}

func TestJobRegistryListNewestFirst(t *testing.T) {
	jobs, err := NewJobRegistry(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("NewJobRegistry: %v", err)
	}

	jobs.Add("old")
	jobs.Add("new")

	got := jobs.List()
	if len(got) != 2 {
		t.Fatalf("List len = %d, want 2", len(got))
	}
	if got[0].ID != "new" || got[1].ID != "old" {
		t.Fatalf("List order = %s, %s; want new, old", got[0].ID, got[1].ID)
	}
	if len(got[0].Logs) != 0 {
		t.Fatalf("List should not hydrate logs, got %+v", got[0].Logs)
	}
}
