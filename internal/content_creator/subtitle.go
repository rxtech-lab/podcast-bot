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

// vttCue is one rendered subtitle line in the sidecar WebVTT file. Start
// and End are offsets from the FIRST cue's wall-clock moment — i.e. the
// same timeline the encoder records when its audio pump first sees real
// (non-silent) bytes from the LiveStream. Stitch trims the silent prep
// prefix off the front of the mp4 to that same anchor, so cue offsets
// land on the audio they describe instead of leading by however much
// inter-turn silence the run accumulated. Text is the already-cleaned
// chunk (punctuation stripped, scene/sound markers removed).
type vttCue struct {
	Start time.Duration
	End   time.Duration
	Text  string
}

// SubtitleCue is the exported, immutable view of a generated subtitle cue.
// It lets job-level post-processing, such as translation, reuse the exact
// timings the live pipeline computed without parsing WebVTT text back from
// disk.
type SubtitleCue struct {
	Start time.Duration
	End   time.Duration
	Text  string
}

// vttMaxRunesPerCue caps the visible text per cue. The burned-in
// renderer paints one wrapped line at a time (puzzleSubtitleMaxLines = 1
// in internal/video) and scrolls overflow lines through it weighted by
// audio duration. The sidecar mirrors that one-line-at-a-time feel by
// splitting a long sentence into multiple cues, each holding a single
// readable chunk. ~22 runes is the sweet spot for CJK content (the
// project's primary language) — long enough to fit a full clause,
// short enough that a player without auto-scroll doesn't show a wall of
// text.
const vttMaxRunesPerCue = 22

// vttWriter accumulates one cue per synthesised sentence and writes a
// sidecar .vtt file at the end of the run. Toggling captions off in the
// player just means ignoring this file; the burn-in subtitle layer the
// renderer paints is untouched, so disabling CC is a player-side
// concern.
//
// The writer is timeline-agnostic — the caller passes a `start
// time.Duration` already expressed in the trimmed-mp4 timeline. The
// pipeline computes that as `targetSend - LiveStream.FirstWriteAt() -
// subtitleClientLatency`, which lines cues up with the same instant
// the encoder pump observed first real audio (= mp4 t=0 after
// StartOffset trim). Centralising that math in the pipeline avoids
// the writer needing to know about LiveStream, the music bed pre-roll,
// or the player-side latency constant.
type vttWriter struct {
	mu   sync.Mutex
	cues []vttCue
}

// newVTTWriter constructs an empty writer. Pipeline holds one per Run.
func newVTTWriter() *vttWriter { return &vttWriter{} }

// Append records cue(s) covering this sentence's spoken audio. start
// is the offset from mp4 t=0 (already adjusted for the music-bed
// pre-roll and player latency by the caller); dur is the audio
// duration. Empty text or non-positive durations are skipped — both
// would produce a syntactically-invalid (or invisible) cue, and a
// missing cue is preferable to a malformed file that breaks the
// player's parser. Negative starts are clamped to zero.
//
// Punctuation is stripped via subtitleutil.StripPunct so the sidecar's
// visible text matches the burned-in caption byte-for-byte. Long
// sentences are split into multiple sequential cues whose durations are
// proportional to their content-rune count, mirroring the burned-in
// renderer's weighted scroll across wrapped lines.
func (w *vttWriter) Append(text string, start, dur time.Duration) {
	if w == nil {
		return
	}
	clean := strings.TrimSpace(text)
	if clean == "" || dur <= 0 {
		return
	}
	clean = subtitleutil.StripPunct(clean)
	if clean == "" {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if start < 0 {
		start = 0
	}

	pieces := subtitleutil.WrapByRunes(clean, vttMaxRunesPerCue)
	if len(pieces) <= 1 {
		w.cues = append(w.cues, vttCue{Start: start, End: start + dur, Text: clean})
		return
	}

	// Distribute the sentence's audio duration across pieces in
	// proportion to each piece's content-rune count. A piece that's
	// only filler (rare after StripPunct, but possible with all-symbol
	// fragments) gets a min weight of 1 so it still dwells briefly.
	weights := make([]int, len(pieces))
	totalW := 0
	for i, p := range pieces {
		count := 0
		for _, r := range p {
			if subtitleutil.IsWordRune(r) {
				count++
			}
		}
		if count < 1 {
			count = 1
		}
		weights[i] = count
		totalW += count
	}

	cursor := start
	for i, p := range pieces {
		var chunkDur time.Duration
		if i == len(pieces)-1 {
			// Pin the final piece to the sentence's exact end so
			// rounding errors in the proportional split don't leave a
			// 1-2 ms gap between sentences.
			chunkDur = (start + dur) - cursor
		} else {
			chunkDur = time.Duration(int64(dur) * int64(weights[i]) / int64(totalW))
		}
		if chunkDur <= 0 {
			continue
		}
		end := cursor + chunkDur
		w.cues = append(w.cues, vttCue{Start: cursor, End: end, Text: p})
		cursor = end
	}
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

// Cues returns a stable snapshot of the writer's timed cue list.
func (w *vttWriter) Cues() []SubtitleCue {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return exportVTTCues(w.cues)
}

// WriteTo emits the WebVTT file at path. No-op when the writer holds
// zero cues — an empty .vtt confuses some players and a missing file is
// the "no captions available" signal we want there.
func (w *vttWriter) WriteTo(path string) error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	cues := exportVTTCues(w.cues)
	w.mu.Unlock()
	return WriteSubtitleCues(path, cues)
}

// WriteSubtitleCues emits cues as a WebVTT file. It is shared by the live
// writer and translated sidecar generation so all subtitle tracks keep the
// same escaping and timestamp format.
func WriteSubtitleCues(path string, cues []SubtitleCue) error {
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

func exportVTTCues(cues []vttCue) []SubtitleCue {
	out := make([]SubtitleCue, len(cues))
	for i, c := range cues {
		out[i] = SubtitleCue{Start: c.Start, End: c.End, Text: c.Text}
	}
	return out
}

// formatVTT renders a duration as HH:MM:SS.mmm — the canonical WebVTT
// timestamp form. Negative durations are clamped to zero (shouldn't
// happen in normal flow, but guards against a clock-skew edge case
// during testing where a synthetic Append fires with a slightly-
// negative dur).
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

// escapeVTT applies the minimal WebVTT escaping the spec requires.
// Newlines are normalised — multi-line cue text is allowed by the
// format but every upstream caller currently passes a single sentence,
// so collapsing internal newlines keeps the output compact and avoids
// accidentally splitting one cue into two.
func escapeVTT(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return strings.TrimSpace(s)
}
