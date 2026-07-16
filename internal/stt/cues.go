package stt

import (
	"strings"
	"unicode"

	"github.com/sirily11/debate-bot/internal/subtitleutil"
)

// Cue is one sentence-level caption segment derived from a Transcript. Text
// keeps its natural punctuation — the WebVTT writer strips it at render time
// so the plan/transcript view still reads like prose.
type Cue struct {
	Speaker int
	StartMS int64
	EndMS   int64
	Text    string
}

// cueMaxRunes forces a split inside a run of text that contains no boundary
// punctuation at all, so a single cue can't grow unbounded. Generous compared
// to the 44-rune VTT wrap because the VTT writer re-wraps at render time.
const cueMaxRunes = 80

// cueBoundary reports whether r ends a sentence-level cue. Commas deliberately
// do not count: a comma-separated sentence must remain attached to one audio
// range instead of becoming several independently timed transcript rows.
func cueBoundary(r rune) bool {
	switch r {
	case '.', '!', '?', ';',
		'。', '！', '？', '；', '…':
		return true
	}
	return false
}

// SentenceCues flattens a transcript into sentence-level cues. Phrases with
// word timings split exactly at the word whose text ends in boundary
// punctuation; phrases without word timings split textually and distribute
// the phrase duration proportionally to content-rune weight (mirroring the
// live pipeline's vttWriter). Cues never cross a phrase's speaker boundary
// and times stay monotonic within a phrase.
func SentenceCues(t *Transcript) []Cue {
	if t == nil {
		return nil
	}
	var out []Cue
	for _, p := range t.Phrases {
		if strings.TrimSpace(p.Text) == "" && len(p.Words) == 0 {
			continue
		}
		if len(p.Words) > 0 {
			out = append(out, cuesFromWords(p)...)
		} else {
			out = append(out, cuesFromText(p)...)
		}
	}
	return clampCueOverlaps(mergeSentenceContinuations(out))
}

// clampCueOverlaps trims each cue that runs past the next cue's start. Timing
// validation only requires monotonic phrase offsets, so a provider may report
// a duration that overruns the following phrase; an overlapping cue makes
// every containment-based caption lookup ambiguous. Runs after
// mergeSentenceContinuations, whose gap check must see the raw ends. A cue
// whose clamped range collapses merges its text into the successor instead of
// dropping words — these cues become the editable transcript segments.
func clampCueOverlaps(cues []Cue) []Cue {
	if len(cues) < 2 {
		return cues
	}
	work := append([]Cue(nil), cues...)
	out := make([]Cue, 0, len(work))
	for i := range work {
		cue := work[i]
		if i+1 < len(work) {
			next := &work[i+1]
			if cue.EndMS > next.StartMS {
				cue.EndMS = next.StartMS
			}
			if cue.EndMS <= cue.StartMS {
				if needsSpace(lastRune(cue.Text), firstRune(next.Text)) {
					next.Text = cue.Text + " " + next.Text
				} else {
					next.Text = cue.Text + next.Text
				}
				if cue.StartMS < next.StartMS {
					next.StartMS = cue.StartMS
				}
				continue
			}
		}
		out = append(out, cue)
	}
	return out
}

// mergeSentenceContinuations joins provider phrases that were cut at clause
// punctuation. Word-timed providers may choose phrase boundaries independently
// of sentence boundaries; keeping the clauses as separate cues recreates the
// exact subtitle drift this package is meant to prevent.
func mergeSentenceContinuations(cues []Cue) []Cue {
	out := make([]Cue, 0, len(cues))
	for _, cue := range cues {
		if n := len(out); n > 0 && out[n-1].Speaker == cue.Speaker &&
			cue.StartMS >= out[n-1].EndMS && cue.StartMS-out[n-1].EndMS <= 1500 &&
			cueContinuesSentence(out[n-1].Text) {
			if needsSpace(lastRune(out[n-1].Text), firstRune(cue.Text)) {
				out[n-1].Text += " "
			}
			out[n-1].Text += strings.TrimSpace(cue.Text)
			out[n-1].EndMS = cue.EndMS
			continue
		}
		out = append(out, cue)
	}
	return out
}

func cueContinuesSentence(text string) bool {
	switch lastRune(strings.TrimSpace(text)) {
	case ',', ':', '，', '、', '：':
		return true
	}
	return false
}

// cuesFromWords walks a phrase's word timeline, closing a cue whenever a
// word ends in sentence punctuation or the running text exceeds cueMaxRunes.
func cuesFromWords(p Phrase) []Cue {
	var (
		out     []Cue
		text    strings.Builder
		runes   int
		startMS int64 = -1
		endMS   int64
	)
	flush := func() {
		s := strings.TrimSpace(text.String())
		if s != "" && startMS >= 0 && endMS > startMS {
			out = append(out, Cue{Speaker: p.Speaker, StartMS: startMS, EndMS: endMS, Text: s})
		}
		text.Reset()
		runes = 0
		startMS = -1
	}
	for _, w := range p.Words {
		wt := strings.TrimSpace(w.Text)
		if wt == "" {
			continue
		}
		if startMS < 0 {
			startMS = w.OffsetMS
		}
		if text.Len() > 0 && needsSpace(lastRune(text.String()), firstRune(wt)) {
			text.WriteByte(' ')
		}
		text.WriteString(wt)
		runes += len([]rune(wt))
		if end := w.OffsetMS + w.DurationMS; end > endMS {
			endMS = end
		}
		if cueBoundary(lastRune(wt)) || runes >= cueMaxRunes {
			flush()
		}
	}
	flush()
	return out
}

// cuesFromText splits a phrase's text at boundary punctuation (keeping the
// punctuation on the piece it ends) and distributes the phrase duration
// across pieces proportionally to their content-rune weight, the same
// weighting the live vttWriter uses.
func cuesFromText(p Phrase) []Cue {
	pieces := splitAtBoundaries(p.Text)
	if len(pieces) == 0 {
		return nil
	}
	weights := make([]int, len(pieces))
	totalW := 0
	for i, piece := range pieces {
		n := 0
		for _, r := range piece {
			if subtitleutil.IsWordRune(r) {
				n++
			}
		}
		if n < 1 {
			n = 1
		}
		weights[i] = n
		totalW += n
	}
	var out []Cue
	cursor := p.OffsetMS
	end := p.OffsetMS + p.DurationMS
	for i, piece := range pieces {
		var pieceEnd int64
		if i == len(pieces)-1 {
			// Pin the final piece to the phrase's exact end so rounding
			// never leaves a gap before the next phrase.
			pieceEnd = end
		} else {
			pieceEnd = cursor + p.DurationMS*int64(weights[i])/int64(totalW)
		}
		if pieceEnd > cursor {
			out = append(out, Cue{Speaker: p.Speaker, StartMS: cursor, EndMS: pieceEnd, Text: piece})
			cursor = pieceEnd
		}
	}
	return out
}

// splitAtBoundaries cuts text after every boundary rune, additionally hard-
// splitting boundary-free runs at cueMaxRunes. Pieces are trimmed and empty
// pieces dropped.
func splitAtBoundaries(text string) []string {
	var (
		pieces []string
		cur    strings.Builder
		runes  int
	)
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			pieces = append(pieces, s)
		}
		cur.Reset()
		runes = 0
	}
	for _, r := range text {
		cur.WriteRune(r)
		runes++
		if cueBoundary(r) || runes >= cueMaxRunes {
			flush()
		}
	}
	flush()
	return pieces
}

// needsSpace decides whether reconstructing text from adjacent words needs a
// separating space: Latin words do, CJK glyphs (and attachment to CJK) don't.
func needsSpace(prev, next rune) bool {
	if prev == 0 || next == 0 {
		return false
	}
	if isCJK(prev) || isCJK(next) {
		return false
	}
	if unicode.IsPunct(next) {
		return false
	}
	return true
}

func isCJK(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF,
		r >= 0x3400 && r <= 0x4DBF,
		r >= 0x3000 && r <= 0x303F, // CJK punctuation
		r >= 0xFF00 && r <= 0xFFEF, // fullwidth forms
		r >= 0x3040 && r <= 0x30FF, // kana
		r >= 0xAC00 && r <= 0xD7AF: // hangul
		return true
	}
	return false
}

func firstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}

func lastRune(s string) rune {
	var last rune
	for _, r := range s {
		last = r
	}
	return last
}
