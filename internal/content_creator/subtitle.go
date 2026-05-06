package contentcreator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sirily11/debate-bot/internal/subtitleutil"
)

// vttCue is one rendered subtitle line in the sidecar WebVTT file. Start and
// End are offsets from the beginning of the produced episode audio (the same
// timeline as debate.mp3 / episode.mp3), not wall-clock time. Text is the
// already-cleaned sentence — scene/sound/image-ref markers have been stripped.
type vttCue struct {
	Start time.Duration
	End   time.Duration
	Text  string
}

// vttWriter accumulates one cue per synthesised sentence and writes a sidecar
// .vtt file at the end of the run. Toggling captions off in the player just
// means ignoring this file; the burn-in subtitle layer the renderer paints is
// untouched, so disabling CC is a player-side concern.
//
// The cursor is advanced by each Append in lockstep with the sentence's audio
// duration. Pipeline keeps a parallel encoded-stream cursor so this writer
// only needs to know "the previous sentence ended N seconds in". That is
// strictly the sum of audioDuration values — independent of the listener-
// clock playhead the producer also tracks, which carries client-side buffer
// adjustments we do NOT want in a static subtitle file.
type vttWriter struct {
	mu     sync.Mutex
	cues   []vttCue
	cursor time.Duration
}

// newVTTWriter constructs an empty writer. Pipeline holds one per Run.
func newVTTWriter() *vttWriter { return &vttWriter{} }

// Append records a single cue covering [cursor, cursor+dur) for text.
// Empty text or non-positive durations are skipped — both would produce a
// syntactically-invalid (or invisible) cue, and a missing cue is preferable
// to a malformed file that breaks the player's parser.
//
// Punctuation is stripped via subtitleutil.StripPunct so the sidecar's
// visible text matches the burned-in caption byte-for-byte. The cue's
// audio duration is unchanged — only the displayed string is condensed.
// The cursor still advances by `dur` even if stripping leaves an empty
// string, so subsequent cues stay aligned with the produced audio.
func (w *vttWriter) Append(text string, dur time.Duration) {
	if w == nil {
		return
	}
	clean := strings.TrimSpace(text)
	if clean == "" || dur <= 0 {
		return
	}
	clean = subtitleutil.StripPunct(clean)
	w.mu.Lock()
	defer w.mu.Unlock()
	start := w.cursor
	end := start + dur
	if clean != "" {
		w.cues = append(w.cues, vttCue{Start: start, End: end, Text: clean})
	}
	w.cursor = end
}

// Cursor reports the running encoded-audio position. Useful for tests and for
// callers that want to align other artefacts with the same timeline.
func (w *vttWriter) Cursor() time.Duration {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cursor
}

// CueCount reports how many cues have been appended.
func (w *vttWriter) CueCount() int {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.cues)
}

// WriteTo emits the WebVTT file at path. No-op when the writer holds zero
// cues — an empty .vtt confuses some players and a missing file is the
// "no captions available" signal we want there.
func (w *vttWriter) WriteTo(path string) error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	cues := append([]vttCue(nil), w.cues...)
	w.mu.Unlock()
	if len(cues) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("subtitle mkdir: %w", err)
	}
	var sb strings.Builder
	sb.WriteString("WEBVTT\n\n")
	for i, c := range cues {
		fmt.Fprintf(&sb, "%d\n%s --> %s\n%s\n\n",
			i+1, formatVTT(c.Start), formatVTT(c.End), escapeVTT(c.Text))
	}
	return os.WriteFile(path, []byte(sb.String()), 0o644)
}

// formatVTT renders a duration as HH:MM:SS.mmm — the canonical WebVTT
// timestamp form. Negative durations are clamped to zero (shouldn't happen
// in normal flow, but guards against a clock-skew edge case during testing
// where a synthetic Append fires with a slightly-negative dur).
func formatVTT(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := d.Milliseconds()
	hours := total / 3_600_000
	total -= hours * 3_600_000
	minutes := total / 60_000
	total -= minutes * 60_000
	seconds := total / 1_000
	millis := total - seconds*1_000
	return fmt.Sprintf("%02d:%02d:%02d.%03d", hours, minutes, seconds, millis)
}

// escapeVTT applies the minimal WebVTT escaping the spec requires. Newlines
// are normalised — multi-line cue text is allowed by the format but every
// upstream caller currently passes a single sentence, so collapsing internal
// newlines keeps the output compact and avoids accidentally splitting one
// cue into two.
func escapeVTT(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return strings.TrimSpace(s)
}
