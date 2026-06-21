package storage

import (
	"context"
	"testing"
	"time"
)

func TestDownloadURLUsesCustomBaseURL(t *testing.T) {
	uploader, err := New(context.Background(), Config{
		Bucket:          "podcasts",
		Region:          "auto",
		DownloadBaseURL: "https://media.example.com/audio/",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got, err := uploader.DownloadURL(context.Background(), "jobs/job-a.mp3", time.Hour)
	if err != nil {
		t.Fatalf("DownloadURL: %v", err)
	}
	if want := "https://media.example.com/audio/jobs/job-a.mp3"; got != want {
		t.Fatalf("DownloadURL = %q, want %q", got, want)
	}
}

func TestDownloadURLAddsBucketForRootCustomBaseURL(t *testing.T) {
	uploader, err := New(context.Background(), Config{
		Bucket:          "podcast",
		Region:          "auto",
		DownloadBaseURL: "https://s3podcast.rxlab.app",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got, err := uploader.DownloadURL(context.Background(), "podcasts/job-a.mp3", time.Hour)
	if err != nil {
		t.Fatalf("DownloadURL: %v", err)
	}
	if want := "https://s3podcast.rxlab.app/podcast/podcasts/job-a.mp3"; got != want {
		t.Fatalf("DownloadURL = %q, want %q", got, want)
	}
}

func TestNewRejectsPartialExplicitCredentials(t *testing.T) {
	_, err := New(context.Background(), Config{
		Bucket:      "podcasts",
		Region:      "auto",
		AccessKeyID: "key-only",
	})
	if err == nil {
		t.Fatal("New succeeded with partial credentials, want error")
	}
}
