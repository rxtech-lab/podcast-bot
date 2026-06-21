package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/content_creator"
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

func TestJobRegistryRecreatesMissingTables(t *testing.T) {
	jobs, err := NewJobRegistry(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("NewJobRegistry: %v", err)
	}
	if err := jobs.db.Exec("DROP TABLE video_jobs").Error; err != nil {
		t.Fatalf("drop video_jobs: %v", err)
	}

	jobs.Add("job-a")
	got := jobs.Get("job-a")
	if got == nil {
		t.Fatal("Get(job-a) after table recreation = nil")
	}
	if got.Status != JobPending {
		t.Fatalf("status = %q, want pending", got.Status)
	}
}

func TestRecoverJobFromArtifacts(t *testing.T) {
	root := t.TempDir()
	uploadRoot := filepath.Join(root, "uploads")
	if err := os.MkdirAll(filepath.Join(uploadRoot, "job-a"), 0o755); err != nil {
		t.Fatal(err)
	}
	jobOut := filepath.Join(root, "jobs", "job-a")
	if err := os.MkdirAll(jobOut, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobOut, "video.mp4"), []byte("mp4"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobOut, "archive.zip"), []byte("zip"), 0o644); err != nil {
		t.Fatal(err)
	}

	jobs, err := NewJobRegistry(filepath.Join(root, "jobs.db"))
	if err != nil {
		t.Fatalf("NewJobRegistry: %v", err)
	}
	s := &Server{d: Deps{Jobs: jobs, UploadRoot: uploadRoot}}

	got := s.recoverJob("job-a")
	if got == nil {
		t.Fatal("recoverJob = nil")
	}
	if got.Status != JobDone || !got.HasVideo || !got.HasArchive {
		t.Fatalf("recovered job = %+v", got)
	}
	if len(got.Logs) == 0 || got.Logs[len(got.Logs)-1].Text != "done" {
		t.Fatalf("recovered logs = %+v", got.Logs)
	}
}

func TestJobTranscriptReturnsPersistedJobTranscript(t *testing.T) {
	root := t.TempDir()
	uploadRoot := filepath.Join(root, "uploads")
	if err := os.MkdirAll(filepath.Join(uploadRoot, "job-a"), 0o755); err != nil {
		t.Fatal(err)
	}
	jobOut := filepath.Join(root, "jobs", "job-a")
	if err := os.MkdirAll(jobOut, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobOut, "audio.mp3"), []byte("mp3"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := contentcreator.OpenStore(filepath.Join(jobOut, "session.db"), nil)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()
	store.Append(agent.TranscriptLine{
		Speaker: "Host",
		Role:    agent.RoleHost,
		Text:    "Welcome back to the discussion.",
		At:      time.Now(),
	})
	store.Append(agent.TranscriptLine{
		Speaker: "Mina",
		Role:    agent.RoleDiscussant,
		Text:    "The important part is the tradeoff.",
		At:      time.Now(),
	})

	jobs, err := NewJobRegistry(filepath.Join(root, "jobs.db"))
	if err != nil {
		t.Fatalf("NewJobRegistry: %v", err)
	}
	jobs.Add("job-a")
	jobs.Update("job-a", func(j *Job) {
		j.Status = JobDone
		j.AudioPath = filepath.Join(jobOut, "audio.mp3")
		j.HasAudio = true
		j.AudioOnly = true
	})
	srv := New(Deps{Mode: ModeDashboard, Jobs: jobs, UploadRoot: uploadRoot})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/jobs/job-a/transcript")
	if err != nil {
		t.Fatalf("get transcript: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got []transcriptDTO
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode transcript: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %+v", len(got), got)
	}
	if got[0].Speaker != "Host" || got[0].Role != "host" || got[0].Text != "Welcome back to the discussion." {
		t.Fatalf("first line = %+v", got[0])
	}
	if got[1].Speaker != "Mina" || got[1].Role != "discussant" || got[1].Text != "The important part is the tradeoff." {
		t.Fatalf("second line = %+v", got[1])
	}
}
