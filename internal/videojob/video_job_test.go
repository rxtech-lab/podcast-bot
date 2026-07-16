package videojob

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/server"
)

func TestPodcastAudioObjectNames(t *testing.T) {
	if got, want := podcastAudioObjectName("job-a", "mp3"), "podcast-audio/job-a.mp3"; got != want {
		t.Fatalf("podcastAudioObjectName mp3 = %q, want %q", got, want)
	}
	if got, want := podcastAudioObjectName("job-a", ".vtt"), "podcast-audio/job-a.vtt"; got != want {
		t.Fatalf("podcastAudioObjectName vtt = %q, want %q", got, want)
	}
	if got, want := rawMusicObjectName("job-a", "/tmp/raw-music/discussion/calm-abc123.mp3"), "raw-music/job-a/calm-abc123.mp3"; got != want {
		t.Fatalf("rawMusicObjectName = %q, want %q", got, want)
	}
}

func TestStagePodcastSubtitlesMovesLegacySidecar(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, server.PodcastSubtitlesFilename)
	if err := os.WriteFile(legacy, []byte("WEBVTT\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := stagePodcastSubtitles(dir, testLogger())
	want := filepath.Join(dir, server.PodcastAudioDir, server.PodcastSubtitlesFilename)
	if got != want {
		t.Fatalf("stagePodcastSubtitles path = %q, want %q", got, want)
	}
	if data, err := os.ReadFile(want); err != nil || string(data) != "WEBVTT\n\n" {
		t.Fatalf("staged subtitles = %q, %v", data, err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy subtitles still present: %v", err)
	}
}

func TestSupportsSoftSubtitles(t *testing.T) {
	for _, contentType := range []string{
		config.ContentTypeSeries,
		config.ContentTypeDiscussion,
		config.ContentTypeAudioBook,
		config.ContentTypeUploadedAudio,
	} {
		if !supportsSoftSubtitles(contentType) {
			t.Errorf("supportsSoftSubtitles(%q) = false, want true", contentType)
		}
	}
	if supportsSoftSubtitles(config.ContentTypeDebate) {
		t.Error("supportsSoftSubtitles(debate) = true, want false")
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
