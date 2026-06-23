package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/content_creator"
	"github.com/sirily11/debate-bot/internal/storage"
	"gorm.io/gorm"
)

func TestJobRegistryPersistsJobsAndLogs(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "jobs.db")
	jobs, err := NewJobRegistry(dbPath, "", "")
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
		j.PromptTokens = 1000
		j.CompletionTokens = 250
		j.TotalTokens = 1250
		j.LLMCostUSD = 0.00375
		j.LLMCostKnown = true
	})
	jobs.AppendLog("job-a", "status", "queued", nil)
	jobs.AppendLog("job-a", "phase", "Done", map[string]string{"phase": "ended"})

	reopened, err := NewJobRegistry(dbPath, "", "")
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
	if got.PromptTokens != 1000 || got.CompletionTokens != 250 || got.TotalTokens != 1250 ||
		got.LLMCostUSD != 0.00375 || !got.LLMCostKnown {
		t.Fatalf("usage fields = %+v", got)
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
	jobs, err := NewJobRegistry(filepath.Join(t.TempDir(), "jobs.db"), "", "")
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
	jobs, err := NewJobRegistry(filepath.Join(t.TempDir(), "jobs.db"), "", "")
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

func TestJobRegistryUsageUpdateSurvivesConcurrentProgressUpdate(t *testing.T) {
	jobs, err := NewJobRegistry(filepath.Join(t.TempDir(), "jobs.db"), "", "")
	if err != nil {
		t.Fatalf("NewJobRegistry: %v", err)
	}
	jobs.Add("job-a")

	enteredUpdate := make(chan struct{})
	releaseUpdate := make(chan struct{})
	progressDone := make(chan struct{})
	go func() {
		jobs.Update("job-a", func(j *Job) {
			close(enteredUpdate)
			<-releaseUpdate
			j.Phase = "speaking"
			j.ElapsedMS = 1234
		})
		close(progressDone)
	}()

	select {
	case <-enteredUpdate:
	case <-time.After(time.Second):
		t.Fatal("progress update did not enter mutation callback")
	}

	usageDone := make(chan struct{})
	go func() {
		jobs.UpdateUsage("job-a", 1000, 250, 1250, 0.00375, true, 0.0012, 0.16)
		close(usageDone)
	}()

	select {
	case <-usageDone:
		t.Fatal("usage update completed while whole-row progress update was still open")
	case <-time.After(20 * time.Millisecond):
	}

	close(releaseUpdate)
	select {
	case <-progressDone:
	case <-time.After(time.Second):
		t.Fatal("progress update did not finish")
	}
	select {
	case <-usageDone:
	case <-time.After(time.Second):
		t.Fatal("usage update did not finish")
	}

	got := jobs.Get("job-a")
	if got == nil {
		t.Fatal("Get(job-a) = nil")
	}
	if got.Phase != "speaking" || got.ElapsedMS != 1234 {
		t.Fatalf("progress fields = phase %q elapsed %d", got.Phase, got.ElapsedMS)
	}
	if got.PromptTokens != 1000 || got.CompletionTokens != 250 || got.TotalTokens != 1250 ||
		got.LLMCostUSD != 0.00375 || !got.LLMCostKnown ||
		got.TTSCostUSD != 0.0012 || got.MusicCostUSD != 0.16 {
		t.Fatalf("usage fields were lost: %+v", got)
	}
}

func TestJobListSanitizesUsageWhenPointsEnabled(t *testing.T) {
	jobs, err := NewJobRegistry(filepath.Join(t.TempDir(), "jobs.db"), "", "")
	if err != nil {
		t.Fatalf("NewJobRegistry: %v", err)
	}
	discussions, err := NewDiscussionStore(filepath.Join(t.TempDir(), "discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer discussions.Close()
	points, err := NewPointsStore(discussions)
	if err != nil {
		t.Fatalf("NewPointsStore: %v", err)
	}

	jobs.Add("job-a")
	jobs.UpdateUsage("job-a", 1000, 250, 1250, 0.00375, true, 0.0012, 0.16)

	srv := New(Deps{
		Mode:   ModeDashboard,
		Jobs:   jobs,
		Points: points,
		Env:    &config.Env{},
	})
	req := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	raw := rec.Body.String()
	for _, field := range []string{"prompt_tokens", "completion_tokens", "total_tokens", "llm_cost_usd", "tts_cost_usd", "music_cost_usd"} {
		if strings.Contains(raw, field) {
			t.Fatalf("job list leaked %s in body: %s", field, raw)
		}
	}
	var items []Job
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode jobs: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items len = %d, want 1", len(items))
	}
	if items[0].PromptTokens != 0 || items[0].CompletionTokens != 0 || items[0].TotalTokens != 0 ||
		items[0].LLMCostUSD != 0 || items[0].LLMCostKnown ||
		items[0].TTSCostUSD != 0 || items[0].MusicCostUSD != 0 {
		t.Fatalf("sanitized usage fields = %+v", items[0])
	}
}

func TestJobStopRequiresActiveJob(t *testing.T) {
	jobs, err := NewJobRegistry(filepath.Join(t.TempDir(), "jobs.db"), "", "")
	if err != nil {
		t.Fatalf("NewJobRegistry: %v", err)
	}
	srv := New(Deps{Mode: ModeDashboard, Jobs: jobs})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/jobs/missing/stop", "application/json", nil)
	if err != nil {
		t.Fatalf("post inactive stop: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("inactive status = %d, want 503", resp.StatusCode)
	}
}

func TestJobMessageRateLimitRejectsImmediateRepeat(t *testing.T) {
	jobs, err := NewJobRegistry(filepath.Join(t.TempDir(), "jobs.db"), "", "")
	if err != nil {
		t.Fatalf("NewJobRegistry: %v", err)
	}
	jobs.Add("job-a")
	orch := contentcreator.NewForTest(func(any) {}, nil)
	jobs.SetOrch("job-a", orch)
	defer jobs.ClearOrch("job-a")

	now := time.Unix(1000, 0)
	srv := New(Deps{Mode: ModeDashboard, Jobs: jobs})
	srv.jobMessageRateNow = func() time.Time { return now }
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	post := func() *http.Response {
		t.Helper()
		body := strings.NewReader(`{"text":"hello","username":"alice"}`)
		resp, err := http.Post(ts.URL+"/api/jobs/job-a/messages", "application/json", body)
		if err != nil {
			t.Fatalf("post message: %v", err)
		}
		return resp
	}

	resp := post()
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("first status = %d, want 204", resp.StatusCode)
	}

	resp = post()
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want 429", resp.StatusCode)
	}
	if got := resp.Header.Get("Retry-After"); got != "2" {
		t.Fatalf("Retry-After = %q, want 2", got)
	}

	now = now.Add(jobMessageMinInterval)
	resp = post()
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("third status = %d, want 204", resp.StatusCode)
	}
}

func TestJobStopRequestsActiveOrchestratorFinalization(t *testing.T) {
	jobs, err := NewJobRegistry(filepath.Join(t.TempDir(), "jobs.db"), "", "")
	if err != nil {
		t.Fatalf("NewJobRegistry: %v", err)
	}
	jobs.Add("job-a")
	orch := contentcreator.NewForTest(func(any) {}, nil)
	jobs.SetOrch("job-a", orch)
	defer jobs.ClearOrch("job-a")

	srv := New(Deps{Mode: ModeDashboard, Jobs: jobs})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/jobs/job-a/stop", "application/json", nil)
	if err != nil {
		t.Fatalf("post stop: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}

	got := jobs.Get("job-a")
	if got == nil {
		t.Fatal("Get(job-a) = nil")
	}
	if len(got.Logs) == 0 || got.Logs[len(got.Logs)-1].Text != "force stop requested - finalising generated audio..." {
		t.Fatalf("logs = %+v", got.Logs)
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

	jobs, err := NewJobRegistry(filepath.Join(root, "jobs.db"), "", "")
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

func TestRecoverJobFromLegacySessionArtifacts(t *testing.T) {
	root := t.TempDir()
	uploadRoot := filepath.Join(root, "dashboard", "uploads")
	if err := os.MkdirAll(uploadRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	legacyOut := filepath.Join(root, "session-2026-06-22_04-19-14")
	jobOut := filepath.Join(legacyOut, "jobs", "job-a")
	if err := os.MkdirAll(jobOut, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobOut, "audio.mp3"), []byte("mp3"), 0o644); err != nil {
		t.Fatal(err)
	}

	jobs, err := NewJobRegistry(filepath.Join(root, "dashboard", "jobs.db"), "", "")
	if err != nil {
		t.Fatalf("NewJobRegistry: %v", err)
	}
	s := &Server{d: Deps{
		Jobs:       jobs,
		UploadRoot: uploadRoot,
		Env:        &config.Env{OutDir: filepath.Join(root, "dashboard"), PersistentRoot: root},
	}}

	got := s.recoverJob("job-a")
	if got == nil {
		t.Fatal("recoverJob legacy session = nil")
	}
	if got.Status != JobDone || !got.HasAudio || !got.AudioOnly {
		t.Fatalf("recovered legacy job = %+v", got)
	}
	if got.AudioPath != filepath.Join(jobOut, "audio.mp3") {
		t.Fatalf("audio path = %q, want legacy path", got.AudioPath)
	}
}

func TestAudioDownloadRedirectsToCustomS3Domain(t *testing.T) {
	jobs, err := NewJobRegistry(filepath.Join(t.TempDir(), "jobs.db"), "", "")
	if err != nil {
		t.Fatalf("NewJobRegistry: %v", err)
	}
	jobs.Add("job-a")
	jobs.Update("job-a", func(j *Job) {
		j.Status = JobDone
		j.HasAudio = true
		j.AudioOnly = true
		j.AudioS3Key = "podcasts/job-a.mp3"
	})
	uploader, err := storage.New(context.Background(), storage.Config{
		Bucket:          "podcasts",
		Region:          "auto",
		DownloadBaseURL: "https://media.example.com/audio",
	})
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	srv := New(Deps{Mode: ModeDashboard, Jobs: jobs, Uploader: uploader})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	client := ts.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := client.Get(ts.URL + "/api/jobs/job-a/audio")
	if err != nil {
		t.Fatalf("get audio: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}
	if got, want := resp.Header.Get("Location"), "https://media.example.com/audio/podcasts/job-a.mp3"; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}

	resp, err = client.Get(ts.URL + "/api/jobs/job-a")
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("job status = %d, want 200", resp.StatusCode)
	}
	var got Job
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode job: %v", err)
	}
	if got.DownloadURL != "https://media.example.com/audio/podcasts/job-a.mp3" {
		t.Fatalf("download_url = %q", got.DownloadURL)
	}
}

func TestApplyDiscussionJobStatusMarksMissingGeneratingJobFailed(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "native-discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()
	jobs, err := NewJobRegistry(filepath.Join(t.TempDir(), "jobs.db"), "", "")
	if err != nil {
		t.Fatalf("NewJobRegistry: %v", err)
	}

	owner := "oauth:user-1"
	created, err := store.Create(ctx, owner, "AI safety", planResponse{
		Script:   &config.DebateTopic{Title: "AI Safety", Type: config.ContentTypeDiscussion, Language: "en-US"},
		Markdown: "# AI Safety",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	generating, err := store.SetJob(ctx, owner, created.ID, "missing-job")
	if err != nil {
		t.Fatalf("SetJob: %v", err)
	}

	s := &Server{d: Deps{
		Jobs:        jobs,
		Discussions: store,
		UploadRoot:  filepath.Join(t.TempDir(), "uploads"),
	}}
	req := httptest.NewRequest(http.MethodGet, "/api/discussions", nil)
	s.applyDiscussionJobStatus(req, generating)
	if generating.Status != DiscussionFailed {
		t.Fatalf("status = %q, want failed", generating.Status)
	}
	persisted, err := store.Get(ctx, owner, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if persisted.Status != DiscussionFailed {
		t.Fatalf("persisted status = %q, want failed", persisted.Status)
	}
}

func TestApplyDiscussionJobStatusChargesFromStoredDiscussionUsage(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "native-discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()
	points, err := NewPointsStore(store)
	if err != nil {
		t.Fatalf("NewPointsStore: %v", err)
	}
	jobs, err := NewJobRegistry(filepath.Join(t.TempDir(), "jobs.db"), "", "")
	if err != nil {
		t.Fatalf("NewJobRegistry: %v", err)
	}

	owner := "oauth:user-1"
	if _, err := points.Credit(ctx, owner, 1000, "purchase:TEST", "evt-usage-repair"); err != nil {
		t.Fatalf("Credit: %v", err)
	}
	created, err := store.Create(ctx, owner, "AI safety", planResponse{
		Script:   &config.DebateTopic{Title: "AI Safety", Type: config.ContentTypeDiscussion, Language: "en-US"},
		Markdown: "# AI Safety",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if ok, _, err := points.Reserve(ctx, owner, created.ID, 200, pointsReasonGeneration); err != nil || !ok {
		t.Fatalf("Reserve ok=%v err=%v", ok, err)
	}
	generating, err := store.SetJob(ctx, owner, created.ID, "job-a")
	if err != nil {
		t.Fatalf("SetJob: %v", err)
	}
	if err := store.SetUsage(ctx, created.ID, 1000, 250, 1250, 0.032, true, 0.10, 0); err != nil {
		t.Fatalf("SetUsage: %v", err)
	}
	// Refresh the discussion snapshot so applyDiscussionJobStatus sees the
	// stored usage. The job itself intentionally has no usage fields, matching a
	// recovered/done job that only knows about final artifacts.
	generating, err = store.Get(ctx, owner, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	jobs.Add("job-a")
	jobs.Update("job-a", func(j *Job) {
		j.Status = JobDone
		j.HasAudio = true
	})

	s := &Server{d: Deps{
		Env:         &config.Env{PointsPerUSDCost: 1000, PointsMinPerPodcast: 1},
		Jobs:        jobs,
		Discussions: store,
		Points:      points,
		UploadRoot:  filepath.Join(t.TempDir(), "uploads"),
	}}
	req := httptest.NewRequest(http.MethodGet, "/api/discussions", nil)
	s.applyDiscussionJobStatus(req, generating)
	if generating.Status != DiscussionReady {
		t.Fatalf("status = %q, want ready", generating.Status)
	}
	if generating.PointsCharged != 132 {
		t.Fatalf("points_charged = %d, want 132", generating.PointsCharged)
	}
	persisted, err := store.Get(ctx, owner, created.ID)
	if err != nil {
		t.Fatalf("Get persisted: %v", err)
	}
	if persisted.PointsCharged != 132 {
		t.Fatalf("persisted points_charged = %d, want 132", persisted.PointsCharged)
	}
}

func TestApplyDiscussionJobStatusDoesNotChargeStoredUsageWithoutReservation(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "native-discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()
	points, err := NewPointsStore(store)
	if err != nil {
		t.Fatalf("NewPointsStore: %v", err)
	}
	jobs, err := NewJobRegistry(filepath.Join(t.TempDir(), "jobs.db"), "", "")
	if err != nil {
		t.Fatalf("NewJobRegistry: %v", err)
	}

	owner := "oauth:user-1"
	if _, err := points.Credit(ctx, owner, 1000, "purchase:TEST", "evt-unreserved-usage"); err != nil {
		t.Fatalf("Credit: %v", err)
	}
	created, err := store.Create(ctx, owner, "AI safety", planResponse{
		Script:   &config.DebateTopic{Title: "AI Safety", Type: config.ContentTypeDiscussion, Language: "en-US"},
		Markdown: "# AI Safety",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.SetJob(ctx, owner, created.ID, "job-a"); err != nil {
		t.Fatalf("SetJob: %v", err)
	}
	if err := store.SetUsage(ctx, created.ID, 1000, 250, 1250, 0.032, true, 0.10, 0); err != nil {
		t.Fatalf("SetUsage: %v", err)
	}
	discussion, err := store.Get(ctx, owner, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	jobs.Add("job-a")
	jobs.Update("job-a", func(j *Job) {
		j.Status = JobDone
		j.HasAudio = true
	})

	s := &Server{d: Deps{
		Env:         &config.Env{PointsPerUSDCost: 1000, PointsMinPerPodcast: 1},
		Jobs:        jobs,
		Discussions: store,
		Points:      points,
		UploadRoot:  filepath.Join(t.TempDir(), "uploads"),
	}}
	req := httptest.NewRequest(http.MethodPatch, "/api/discussions/"+created.ID+"/visibility", nil)
	s.applyDiscussionJobStatus(req, discussion)
	if discussion.Status != DiscussionReady {
		t.Fatalf("status = %q, want ready", discussion.Status)
	}
	if discussion.PointsCharged != 0 {
		t.Fatalf("points_charged = %d, want 0", discussion.PointsCharged)
	}
	bal, err := points.Balance(ctx, owner)
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	if bal != 1000 {
		t.Fatalf("balance = %d, want 1000", bal)
	}
	history, err := points.History(ctx, owner, 10, 0)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	for _, entry := range history.Entries {
		if entry.Reason == pointsReasonGeneration {
			t.Fatalf("unexpected generation ledger entry: %+v", entry)
		}
	}
}

func TestJobTranscriptReturnsNativeDiscussionTranscriptFromDB(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "native-discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()

	owner := "oauth:user-1"
	created, err := store.Create(ctx, owner, "AI safety", planResponse{
		Script:   &config.DebateTopic{Title: "AI Safety", Type: config.ContentTypeDiscussion, Language: "en-US"},
		Markdown: "# AI Safety",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.SetJob(ctx, owner, created.ID, "job-a"); err != nil {
		t.Fatalf("SetJob: %v", err)
	}
	if err := store.ReplaceTranscript(ctx, created.ID, []agent.TranscriptLine{
		{Speaker: "Host", Role: agent.RoleHost, Text: "Welcome back to the discussion."},
		{Speaker: "Mina", Role: agent.RoleDiscussant, Side: "technical", Text: "The important part is the tradeoff."},
	}); err != nil {
		t.Fatalf("ReplaceTranscript: %v", err)
	}

	jobs, err := NewJobRegistry(filepath.Join(t.TempDir(), "jobs.db"), "", "")
	if err != nil {
		t.Fatalf("NewJobRegistry: %v", err)
	}
	jobs.Add("job-a")
	jobs.Update("job-a", func(j *Job) {
		j.Status = JobDone
		j.HasAudio = true
		j.AudioOnly = true
	})
	srv := New(Deps{Mode: ModeDashboard, Jobs: jobs, Discussions: store})
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
	if got[1].Speaker != "Mina" || got[1].Role != "discussant" ||
		got[1].Side != "technical" || got[1].Text != "The important part is the tradeoff." {
		t.Fatalf("second line = %+v", got[1])
	}
}

func TestActiveJobTranscriptUsesDiskOrderAndLiveSuffix(t *testing.T) {
	ctx := context.Background()
	store, err := NewDiscussionStore(filepath.Join(t.TempDir(), "native-discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	defer store.Close()

	owner := "oauth:user-1"
	created, err := store.Create(ctx, owner, "AI safety", planResponse{
		Script:   &config.DebateTopic{Title: "AI Safety", Type: config.ContentTypeDiscussion, Language: "en-US"},
		Markdown: "# AI Safety",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.SetJob(ctx, owner, created.ID, "job-a"); err != nil {
		t.Fatalf("SetJob: %v", err)
	}
	if err := store.AppendLine(ctx, owner, created.ID, DiscussionLine{
		Speaker: "Viewer",
		Role:    "user",
		Text:    "Can you explain the rollout risk?",
		IsUser:  true,
	}); err != nil {
		t.Fatalf("AppendLine: %v", err)
	}

	jobs, err := NewJobRegistry(filepath.Join(t.TempDir(), "jobs.db"), "", "")
	if err != nil {
		t.Fatalf("NewJobRegistry: %v", err)
	}
	root := t.TempDir()
	uploadRoot := filepath.Join(root, "uploads")
	if err := os.MkdirAll(uploadRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	jobOut := filepath.Join(root, "jobs", "job-a")
	if err := os.MkdirAll(jobOut, 0o755); err != nil {
		t.Fatal(err)
	}
	diskStore, err := contentcreator.OpenStore(filepath.Join(jobOut, "session.db"), nil)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer diskStore.Close()
	diskLines := []agent.TranscriptLine{
		{Speaker: "Host", Role: agent.RoleHost, Text: "Welcome back."},
		{Speaker: "Viewer", Role: agent.Role("user"), Text: "Can you explain the rollout risk?"},
		{Speaker: "Host", Role: agent.RoleHost, Text: "Yes."},
		{Speaker: "Host", Role: agent.RoleHost, Text: "Yes."},
	}
	for _, line := range diskLines {
		diskStore.Append(line)
	}

	jobs.Add("job-a")
	jobs.Update("job-a", func(j *Job) {
		j.Status = JobRunning
		j.HasAudio = true
		j.AudioOnly = true
	})
	orch := contentcreator.NewForTest(func(any) {}, nil)
	for _, line := range diskLines {
		orch.Transcript.AppendLine(line)
	}
	orch.Transcript.AppendLine(agent.TranscriptLine{
		Speaker: "Mina",
		Role:    agent.RoleDiscussant,
		Text:    "The rollout risk is operational drift.",
	})
	jobs.SetOrch("job-a", orch)
	defer jobs.ClearOrch("job-a")

	srv := New(Deps{Mode: ModeDashboard, Jobs: jobs, Discussions: store, UploadRoot: uploadRoot})
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
	if len(got) != 5 {
		t.Fatalf("len = %d, want 5: %+v", len(got), got)
	}
	if got[0].Speaker != "Host" || got[0].Text != "Welcome back." {
		t.Fatalf("first line = %+v", got[0])
	}
	if got[1].Speaker != "Viewer" || got[1].Role != "user" ||
		got[1].Text != "Can you explain the rollout risk?" {
		t.Fatalf("second line = %+v", got[1])
	}
	if got[2].Speaker != "Host" || got[2].Text != "Yes." ||
		got[3].Speaker != "Host" || got[3].Text != "Yes." {
		t.Fatalf("repeated lines were not preserved: %+v", got[2:4])
	}
	if got[4].Speaker != "Mina" || got[4].Role != "discussant" ||
		got[4].Text != "The rollout risk is operational drift." {
		t.Fatalf("live suffix line = %+v", got[4])
	}
}

func TestTransientDBConnectionErrorClassifier(t *testing.T) {
	err := errors.New("stream is closed: driver: bad connection")
	if !isTransientDBConnectionError(err) {
		t.Fatal("expected stream closed bad connection to be transient")
	}
	if isTransientDBConnectionError(gorm.ErrRecordNotFound) {
		t.Fatal("record-not-found must not be treated as transient")
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

	jobs, err := NewJobRegistry(filepath.Join(root, "jobs.db"), "", "")
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
