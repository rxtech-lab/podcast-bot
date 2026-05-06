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
	// Punctuation is stripped to align with burned-in captions, so the
	// trailing periods on the input cues should not appear in the file.
	if !strings.Contains(body, "hello world") || !strings.Contains(body, "second line") {
		t.Errorf("cue text missing; full body=%q", body)
	}
	if strings.Contains(body, "hello world.") || strings.Contains(body, "second line.") {
		t.Errorf("punctuation should be stripped from cue text; full body=%q", body)
	}
}

// TestVTTWriter_StripsPunct asserts that CJK punctuation (the project's
// primary content language) is removed from cue text so the sidecar
// matches what drawHBOSubtitleBodyOutlined paints on the burned-in
// frame. Mismatched soft / burned text was the original symptom.
func TestVTTWriter_StripsPunct(t *testing.T) {
	w := newVTTWriter()
	w.Append("林夕說：「夜深了，我得走。」", 2*time.Second)
	dir := t.TempDir()
	path := filepath.Join(dir, "subtitles.vtt")
	if err := w.WriteTo(path); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	body, _ := os.ReadFile(path)
	got := string(body)
	for _, p := range []string{"。", "，", "「", "」"} {
		if strings.Contains(got, p) {
			t.Errorf("expected punctuation %q stripped; body=%q", p, got)
		}
	}
	// Colons (both ASCII and fullwidth) are deliberately preserved by
	// the strip rules — they're structural in lines like "林夕說：…".
	if !strings.Contains(got, "林夕說：") {
		t.Errorf("colon should be preserved; body=%q", got)
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
	// `<` / `>` / `&` are not word runes, so the punctuation strip drops
	// them before they reach escapeVTT. Use an input that exercises the
	// newline collapse without relying on the now-stripped html-like
	// chars; the html escape path is covered directly by escapeVTT's
	// behaviour and unaffected by the strip.
	w.Append("line one\nline two", 1*time.Second)
	dir := t.TempDir()
	path := filepath.Join(dir, "x.vtt")
	if err := w.WriteTo(path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	body := string(data)
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
