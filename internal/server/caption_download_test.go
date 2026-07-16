package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
)

func TestCaptionDownloadFormatsAreBackendOwned(t *testing.T) {
	jobs, err := NewJobRegistry(filepath.Join(t.TempDir(), "jobs.db"), "", "")
	if err != nil {
		t.Fatalf("NewJobRegistry: %v", err)
	}
	srv := New(Deps{Mode: ModeDashboard, Jobs: jobs})
	req := httptest.NewRequest(http.MethodGet, "/api/caption-formats", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	var response captionDownloadFormatsResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode formats: %v", err)
	}
	if len(response.Formats) != 2 {
		t.Fatalf("formats = %+v, want vtt and srt", response.Formats)
	}
	if response.Formats[0].ID != "vtt" || response.Formats[0].FileExtension != "vtt" ||
		response.Formats[1].ID != "srt" || response.Formats[1].FileExtension != "srt" {
		t.Fatalf("formats = %+v, want backend descriptors for vtt and srt", response.Formats)
	}
}

func TestWebVTTToSRT(t *testing.T) {
	vtt := []byte("WEBVTT\n\nfirst-cue\n00:01.250 --> 00:03.500 position:50%\nHello\nworld\n\n2\n00:00:04.000 --> 00:00:05.750\nNext line\n")
	got, err := webVTTToSRT(vtt)
	if err != nil {
		t.Fatalf("webVTTToSRT: %v", err)
	}
	want := "1\r\n00:00:01,250 --> 00:00:03,500\r\nHello\r\nworld\r\n\r\n" +
		"2\r\n00:00:04,000 --> 00:00:05,750\r\nNext line\r\n\r\n"
	if string(got) != want {
		t.Fatalf("SRT mismatch\n got: %q\nwant: %q", string(got), want)
	}
}

func TestJobCaptionDownloadRendersSRT(t *testing.T) {
	root := t.TempDir()
	uploadRoot := filepath.Join(root, "uploads")
	jobs, err := NewJobRegistry(filepath.Join(root, "jobs.db"), "", "")
	if err != nil {
		t.Fatalf("NewJobRegistry: %v", err)
	}
	const jobID = "job-caption-download"
	jobs.Add(jobID)
	jobs.Update(jobID, func(job *Job) { job.Status = JobDone })
	artifactDir := filepath.Join(root, "jobs", jobID, PodcastAudioDir)
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(artifactDir, PodcastSubtitlesFilename),
		[]byte("WEBVTT\n\n1\n00:00:01.000 --> 00:00:02.500\nHello captions\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	srv := New(Deps{Mode: ModeDashboard, Jobs: jobs, UploadRoot: uploadRoot})
	req := httptest.NewRequest(http.MethodGet, "/api/jobs/"+jobID+"/captions/srt", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/x-subrip") {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, jobID+".srt") {
		t.Fatalf("Content-Disposition = %q", got)
	}
	if got := rec.Body.String(); !strings.Contains(got, "00:00:01,000 --> 00:00:02,500") ||
		!strings.Contains(got, "Hello captions") {
		t.Fatalf("body = %q", got)
	}
}

// seedTranslatedJob builds a done job with an original English VTT on disk plus
// a ready-status discussion (linked to the job) carrying an original transcript
// line and a ready zh-CN translation with translated captions and lines.
func seedTranslatedJob(t *testing.T, jobID string) (*JobRegistry, *DiscussionStore, string) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	jobs, err := NewJobRegistry(filepath.Join(root, "jobs.db"), "", "")
	if err != nil {
		t.Fatalf("NewJobRegistry: %v", err)
	}
	jobs.Add(jobID)
	jobs.Update(jobID, func(job *Job) { job.Status = JobDone })
	artifactDir := filepath.Join(root, "jobs", jobID, PodcastAudioDir)
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(artifactDir, PodcastSubtitlesFilename),
		[]byte("WEBVTT\n\n1\n00:00:01.000 --> 00:00:02.500\nHello captions\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	store, err := NewDiscussionStore(filepath.Join(root, "discussions.db"), "", "")
	if err != nil {
		t.Fatalf("NewDiscussionStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	const owner = "cookie:Owner"
	d, err := store.Create(ctx, owner, "Source topic", planResponse{
		Script:   &config.DebateTopic{Title: "Source title", Type: config.ContentTypeDiscussion, Language: "en-US"},
		Markdown: "Source plan",
	})
	if err != nil {
		t.Fatalf("create discussion: %v", err)
	}
	if _, err := store.SetJob(ctx, owner, d.ID, jobID); err != nil {
		t.Fatalf("set job: %v", err)
	}
	if err := store.SetJobResult(ctx, d.ID, DiscussionReady, "https://audio.example/source.mp3"); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	if err := store.AppendLine(ctx, owner, d.ID, DiscussionLine{
		Speaker: "Alice", Role: "discussant", Text: "Original line",
	}); err != nil {
		t.Fatalf("append line: %v", err)
	}
	if err := store.BeginTranslation(ctx, d.ID, "zh-CN", "test/model"); err != nil {
		t.Fatalf("begin translation: %v", err)
	}
	if err := store.SaveTranslation(ctx, d.ID, "zh-CN", DiscussionTranslationBundle{
		Language:    "zh-CN",
		Title:       "翻译标题",
		CaptionsVTT: "WEBVTT\n\n1\n00:00:01.000 --> 00:00:02.500\n翻译字幕\n",
		Lines:       []DiscussionLine{{Speaker: "爱丽丝", Role: "discussant", Text: "翻译行"}},
	}, "test/model", SummaryUsage{}); err != nil {
		t.Fatalf("save translation: %v", err)
	}
	return jobs, store, filepath.Join(root, "uploads")
}

func TestJobCaptionDownloadServesTranslatedVTTAndSRT(t *testing.T) {
	const jobID = "job-caption-translated"
	jobs, store, uploadRoot := seedTranslatedJob(t, jobID)
	srv := New(Deps{Mode: ModeDashboard, Jobs: jobs, Discussions: store, UploadRoot: uploadRoot})
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d body=%s", path, rec.Code, rec.Body.String())
		}
		return rec
	}

	if got := get("/api/jobs/" + jobID + "/captions/vtt?language=zh-CN").Body.String(); !strings.Contains(got, "翻译字幕") || strings.Contains(got, "Hello captions") {
		t.Fatalf("translated vtt body = %q", got)
	}
	if got := get("/api/jobs/" + jobID + "/captions/srt?language=zh-CN").Body.String(); !strings.Contains(got, "00:00:01,000 --> 00:00:02,500") ||
		!strings.Contains(got, "翻译字幕") || strings.Contains(got, "Hello captions") {
		t.Fatalf("translated srt body = %q", got)
	}
	if got := get("/api/jobs/" + jobID + "/captions/vtt").Body.String(); !strings.Contains(got, "Hello captions") || strings.Contains(got, "翻译字幕") {
		t.Fatalf("original vtt body = %q", got)
	}
	if got := get("/api/jobs/" + jobID + "/captions/vtt?language=ja-JP").Body.String(); !strings.Contains(got, "Hello captions") || strings.Contains(got, "翻译字幕") {
		t.Fatalf("vtt without ready translation body = %q", got)
	}
}

// A ready translation must serve the download even when the job registry has
// no row for the job id (e.g. seeded/e2e discussions, or registries rebuilt
// after the job aged out) — parity with the subtitle endpoints.
func TestJobCaptionDownloadServesTranslationWithoutJobRow(t *testing.T) {
	const jobID = "job-caption-translated-no-row"
	jobs, store, uploadRoot := seedTranslatedJob(t, jobID+"-other")
	ctx := context.Background()
	const owner = "cookie:Owner"
	d, err := store.Create(ctx, owner, "No-row topic", planResponse{
		Script:   &config.DebateTopic{Title: "No-row title", Type: config.ContentTypeDiscussion, Language: "en-US"},
		Markdown: "No-row plan",
	})
	if err != nil {
		t.Fatalf("create discussion: %v", err)
	}
	if _, err := store.SetJob(ctx, owner, d.ID, jobID); err != nil {
		t.Fatalf("set job: %v", err)
	}
	if err := store.BeginTranslation(ctx, d.ID, "zh-CN", "test/model"); err != nil {
		t.Fatalf("begin translation: %v", err)
	}
	if err := store.SaveTranslation(ctx, d.ID, "zh-CN", DiscussionTranslationBundle{
		Language:    "zh-CN",
		CaptionsVTT: "WEBVTT\n\n1\n00:00:01.000 --> 00:00:02.500\n无任务翻译字幕\n",
	}, "test/model", SummaryUsage{}); err != nil {
		t.Fatalf("save translation: %v", err)
	}

	srv := New(Deps{Mode: ModeDashboard, Jobs: jobs, Discussions: store, UploadRoot: uploadRoot})
	req := httptest.NewRequest(http.MethodGet, "/api/jobs/"+jobID+"/captions/srt?language=zh-CN", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); !strings.Contains(got, "无任务翻译字幕") {
		t.Fatalf("body = %q", got)
	}
	// Without a ready translation the missing job row still 404s.
	req = httptest.NewRequest(http.MethodGet, "/api/jobs/"+jobID+"/captions/srt", nil)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status without translation = %d, want 404", rec.Code)
	}
}

func TestJobTranscriptServesTranslatedLines(t *testing.T) {
	const jobID = "job-transcript-translated"
	jobs, store, uploadRoot := seedTranslatedJob(t, jobID)
	srv := New(Deps{Mode: ModeDashboard, Jobs: jobs, Discussions: store, UploadRoot: uploadRoot})
	get := func(path string) []transcriptDTO {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d body=%s", path, rec.Code, rec.Body.String())
		}
		var lines []transcriptDTO
		if err := json.NewDecoder(rec.Body).Decode(&lines); err != nil {
			t.Fatalf("decode transcript: %v", err)
		}
		return lines
	}

	translated := get("/api/jobs/" + jobID + "/transcript?language=zh-CN")
	if len(translated) != 1 || translated[0].Speaker != "爱丽丝" || translated[0].Text != "翻译行" {
		t.Fatalf("translated transcript = %+v", translated)
	}
	original := get("/api/jobs/" + jobID + "/transcript")
	if len(original) != 1 || original[0].Speaker != "Alice" || original[0].Text != "Original line" {
		t.Fatalf("original transcript = %+v", original)
	}
	fallback := get("/api/jobs/" + jobID + "/transcript?language=ja-JP")
	if len(fallback) != 1 || fallback[0].Speaker != "Alice" {
		t.Fatalf("fallback transcript = %+v", fallback)
	}
}
