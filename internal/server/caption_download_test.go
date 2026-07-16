package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
