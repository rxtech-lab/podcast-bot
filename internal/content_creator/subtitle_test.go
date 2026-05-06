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
	w.Append("first sentence.", 0, 2*time.Second)
	w.Append("second sentence.", 2*time.Second, 3*time.Second)
	if got, want := w.CueCount(), 2; got != want {
		t.Errorf("CueCount = %d, want %d", got, want)
	}
}

func TestVTTWriter_AppendIgnoresEmpty(t *testing.T) {
	w := newVTTWriter()
	w.Append("", 0, 5*time.Second)
	w.Append("hi", 0, 0)
	w.Append("hi", 0, -time.Second)
	if got := w.CueCount(); got != 0 {
		t.Errorf("CueCount = %d, want 0 for invalid inputs", got)
	}
}

// TestVTTWriter_StartOffsetRespected asserts the first cue lands at
// whatever offset the caller passes — important because in series
// mode the music bed pre-roll begins seconds before the first spoken
// sentence, so the first cue must NOT collapse onto 00:00. The pipeline
// computes `targetSend - LiveStream.FirstWriteAt() - subtitleClientLatency`;
// here we just assert the writer faithfully records that.
func TestVTTWriter_StartOffsetRespected(t *testing.T) {
	w := newVTTWriter()
	w.Append("opening line", 6*time.Second, 1*time.Second)
	if got, want := w.cues[0].Start, 6*time.Second; got != want {
		t.Errorf("first cue start = %v, want %v (caller-supplied offset)", got, want)
	}
	if got, want := w.cues[0].End, 7*time.Second; got != want {
		t.Errorf("first cue end = %v, want %v", got, want)
	}

	// Inter-turn silence: the next cue's start is whatever the caller
	// computes. The writer doesn't auto-extend from the previous cue.
	w.Append("after gap", 9*time.Second, 1*time.Second)
	if got, want := w.cues[1].Start, 9*time.Second; got != want {
		t.Errorf("second cue start = %v, want %v (caller-supplied offset)", got, want)
	}
	if got, want := w.cues[1].End, 10*time.Second; got != want {
		t.Errorf("second cue end = %v, want %v", got, want)
	}
}

// TestVTTWriter_NegativeStartClamped guards the edge where the
// pipeline's `targetSend - FirstWriteAt - clientLatency` evaluates
// slightly negative (e.g. the first sentence fires just after first
// write while clientLatency is still being absorbed). Clamping keeps
// the file syntactically valid.
func TestVTTWriter_NegativeStartClamped(t *testing.T) {
	w := newVTTWriter()
	w.Append("early", -200*time.Millisecond, 1*time.Second)
	if got, want := w.cues[0].Start, time.Duration(0); got != want {
		t.Errorf("clamped start = %v, want %v", got, want)
	}
	if got, want := w.cues[0].End, 1*time.Second; got != want {
		t.Errorf("end after clamped start = %v, want %v", got, want)
	}
}

func TestVTTWriter_WriteToProducesValidVTT(t *testing.T) {
	w := newVTTWriter()
	w.Append("hello world.", 0, 1500*time.Millisecond)
	w.Append("second line.", 1500*time.Millisecond, 2*time.Second)
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
	// First cue is anchored at 00:00:00.000.
	if !strings.Contains(body, "00:00:00.000 --> 00:00:01.500") {
		t.Errorf("first cue timing wrong; full body=%q", body)
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

func TestVTTWriter_CuesSnapshotAndWriteSubtitleCues(t *testing.T) {
	w := newVTTWriter()
	w.Append("hello world.", 500*time.Millisecond, 1500*time.Millisecond)
	cues := w.Cues()
	if len(cues) != 1 {
		t.Fatalf("Cues length = %d, want 1", len(cues))
	}
	cues[0].Text = "bonjour & bienvenue"

	dir := t.TempDir()
	path := filepath.Join(dir, "subtitles.fr.vtt")
	if err := WriteSubtitleCues(path, cues); err != nil {
		t.Fatalf("WriteSubtitleCues: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, "00:00:00.500 --> 00:00:02.000") {
		t.Errorf("translated cue timing wrong; body=%q", body)
	}
	if !strings.Contains(body, "bonjour &amp; bienvenue") {
		t.Errorf("translated cue text was not escaped; body=%q", body)
	}

	orig := w.Cues()
	if got := orig[0].Text; got != "hello world" {
		t.Errorf("Cues should return a copy; original text = %q", got)
	}
}

// TestVTTWriter_StripsPunct asserts that CJK punctuation (the project's
// primary content language) is removed from cue text so the sidecar
// matches what drawHBOSubtitleBodyOutlined paints on the burned-in
// frame. Mismatched soft / burned text was the original symptom.
func TestVTTWriter_StripsPunct(t *testing.T) {
	w := newVTTWriter()
	w.Append("林夕說：「夜深了，我得走。」", 0, 2*time.Second)
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

// TestVTTWriter_SplitsLongSentence verifies that a long sentence emits
// multiple cues whose summed duration equals the sentence's audio
// duration. The burned-in renderer paints one wrapped line at a time,
// so the sidecar mirrors that by chunking; durations are split in
// proportion to each chunk's content-rune count.
func TestVTTWriter_SplitsLongSentence(t *testing.T) {
	w := newVTTWriter()
	// 60-char CJK string — more than vttMaxRunesPerCue (~22) so it
	// must split into multiple cues.
	long := strings.Repeat("夜深了我得走前要把這封信交給你保管", 3)
	w.Append(long, 0, 6*time.Second)
	if got := w.CueCount(); got < 2 {
		t.Fatalf("CueCount = %d, want >= 2 for long input (input had %d runes)",
			got, len([]rune(long)))
	}
	// Cumulative span must cover the full sentence duration; the last
	// piece pins to the exact end so rounding doesn't leave a gap.
	last := w.cues[len(w.cues)-1]
	if got, want := last.End, 6*time.Second; got != want {
		t.Errorf("final cue end = %v, want %v (last piece should pin to sentence end)",
			got, want)
	}
	// Each cue's text should fit under the per-cue rune cap.
	for i, c := range w.cues {
		if got := len([]rune(c.Text)); got > vttMaxRunesPerCue {
			t.Errorf("cue %d has %d runes (cap=%d); text=%q",
				i, got, vttMaxRunesPerCue, c.Text)
		}
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
	w.Append("line one\nline two", 0, 1*time.Second)
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
