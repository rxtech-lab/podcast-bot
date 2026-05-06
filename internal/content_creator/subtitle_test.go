package contentcreator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVTTWriter_Append(t *testing.T) {
	w := newVTTWriter()
	w.Append("first sentence.", 2*time.Second)
	w.Append("second sentence.", 3*time.Second)
	if got, want := w.CueCount(), 2; got != want {
		t.Errorf("CueCount = %d, want %d", got, want)
	}
	if got, want := w.Cursor(), 5*time.Second; got != want {
		t.Errorf("Cursor = %v, want %v", got, want)
	}
}

func TestVTTWriter_AppendIgnoresEmpty(t *testing.T) {
	w := newVTTWriter()
	w.Append("", 5*time.Second)
	w.Append("hi", 0)
	w.Append("hi", -time.Second)
	if got := w.CueCount(); got != 0 {
		t.Errorf("CueCount = %d, want 0 for invalid inputs", got)
	}
}

func TestVTTWriter_WriteToProducesValidVTT(t *testing.T) {
	w := newVTTWriter()
	w.Append("hello world.", 1500*time.Millisecond)
	w.Append("second line.", 2*time.Second)
	dir := t.TempDir()
	path := filepath.Join(dir, "subtitles.vtt")
	if err := w.WriteTo(path); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(data)
	if !strings.HasPrefix(body, "WEBVTT") {
		t.Errorf("file does not start with WEBVTT header; got %q", body[:min(20, len(body))])
	}
	// First cue starts at 00:00:00.000, ends at 00:00:01.500.
	if !strings.Contains(body, "00:00:00.000 --> 00:00:01.500") {
		t.Errorf("first cue timing wrong; full body=%q", body)
	}
	// Second cue starts where the first ended.
	if !strings.Contains(body, "00:00:01.500 --> 00:00:03.500") {
		t.Errorf("second cue timing wrong; full body=%q", body)
	}
	if !strings.Contains(body, "hello world.") || !strings.Contains(body, "second line.") {
		t.Errorf("cue text missing; full body=%q", body)
	}
}

func TestVTTWriter_WriteToEmptyNoOp(t *testing.T) {
	w := newVTTWriter()
	dir := t.TempDir()
	path := filepath.Join(dir, "subtitles.vtt")
	if err := w.WriteTo(path); err != nil {
		t.Fatalf("WriteTo on empty writer: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected no file written on empty writer; stat err=%v", err)
	}
}

func TestVTTWriter_EscapeAndNewlines(t *testing.T) {
	w := newVTTWriter()
	w.Append("a < b & c", 1*time.Second)
	w.Append("line one\nline two", 1*time.Second)
	dir := t.TempDir()
	path := filepath.Join(dir, "x.vtt")
	if err := w.WriteTo(path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	body := string(data)
	if !strings.Contains(body, "a &lt; b &amp; c") {
		t.Errorf("html-like chars not escaped; body=%q", body)
	}
	if strings.Contains(body, "line one\nline two") {
		t.Errorf("internal newline should be collapsed; body=%q", body)
	}
}

func TestFormatVTT(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "00:00:00.000"},
		{1500 * time.Millisecond, "00:00:01.500"},
		{(60 + 30) * time.Second, "00:01:30.000"},
		{61*time.Minute + 5*time.Second + 250*time.Millisecond, "01:01:05.250"},
		{-5 * time.Second, "00:00:00.000"},
	}
	for _, c := range cases {
		got := formatVTT(c.d)
		if got != c.want {
			t.Errorf("formatVTT(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}
