// Package subtitleutil holds rendering-agnostic helpers shared between
// the in-frame caption renderer (internal/video) and the sidecar WebVTT
// writer (internal/content_creator). Living in its own leaf package
// avoids the cycle that would arise if either of those imported the
// other.
package subtitleutil

import (
	"strings"
	"unicode"
)

// StripPunct removes punctuation, pause indicators and stray symbols
// from a subtitle body so the rendered caption shows only the readable
// words. Targets:
//   - CJK fullstop / comma / pauses: 。 ， 、 ； ： ！ ？ 「 」 『 』 （ ）《 》 【 】
//   - CJK pause/ellipsis sequences: …… —— ···
//   - Latin punctuation: . , ; : ! ? — - … " ' ( ) [ ] { }
//
// Stripping happens before line wrapping so a residue line that would
// otherwise contain only "。" or ", " disappears from the display
// entirely. Letters / digits / CJK glyphs are kept verbatim. Whitespace
// is collapsed to a single space so wrappers still have word boundaries
// for Latin text.
func StripPunct(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if isWordRune(r) {
			b.WriteRune(r)
			prevSpace = false
			continue
		}
		// Map any non-word rune to a single collapsed space so Latin
		// "Hello, world" doesn't become "Helloworld" while CJK still
		// reads cleanly (CJK has no inter-glyph spaces, so the trim at
		// the end strips trailing/leading spaces and consecutive spaces
		// inside CJK runs are collapsed to one each).
		if !prevSpace {
			b.WriteByte(' ')
			prevSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

// isWordRune reports whether r is a content rune that should appear in
// a subtitle caption — letter, digit, or any glyph in the CJK Unified
// Ideographs (and adjacent) blocks. Colons (`:` / `：`) are also kept
// because they're typically structural (e.g. "今晚的题目：归途" or
// "Alice: …") and dropping them collapses otherwise-meaningful labels.
// Everything else (sentence-final punctuation, pause indicators,
// symbols, control, whitespace) is dropped or mapped to a separator by
// StripPunct.
func isWordRune(r rune) bool {
	if unicode.IsLetter(r) || unicode.IsDigit(r) {
		return true
	}
	// Keep colons — both ASCII and the fullwidth CJK variant.
	if r == ':' || r == '：' {
		return true
	}
	// IsLetter already covers Han ideographs in modern Go, but be
	// explicit so a future stdlib quirk doesn't silently drop them.
	switch {
	case r >= 0x4E00 && r <= 0x9FFF, // CJK Unified Ideographs
		r >= 0x3400 && r <= 0x4DBF,   // CJK Unified Ext A
		r >= 0x20000 && r <= 0x2A6DF, // CJK Unified Ext B
		r >= 0x3040 && r <= 0x309F,   // Hiragana
		r >= 0x30A0 && r <= 0x30FF,   // Katakana
		r >= 0xAC00 && r <= 0xD7AF:   // Hangul syllables
		return true
	}
	return false
}

// IsWordRune is the exported form of isWordRune. Re-exported because
// internal/video's caption layout uses the same predicate to compute
// per-line "weight" (count of content runes) for weighted scrolling,
// and that path needs to stay byte-identical with the strip pass to
// keep the rendered caption layout aligned with the stripped text.
func IsWordRune(r rune) bool { return isWordRune(r) }

// WrapByRunes splits text into chunks of at most maxRunes runes,
// preferring a break at the last whitespace seen on the current chunk
// (matching wrapLines' Latin-friendly behavior in internal/video). When
// no whitespace is available — typical for CJK passages whose
// punctuation has already been stripped to spaces by StripPunct, then
// trimmed — the split falls back to a hard rune-count cut so a single
// long Chinese sentence still gets broken into chunks instead of
// overflowing one cue.
//
// Used by the WebVTT writer to mirror what the burned-in caption
// renderer does (one wrapped line at a time, scrolled in lockstep with
// the spoken audio): each chunk becomes its own cue with a slice of the
// total audio duration weighted by its rune count.
func WrapByRunes(text string, maxRunes int) []string {
	if maxRunes <= 0 {
		return []string{text}
	}
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= maxRunes {
		if len(runes) == 0 {
			return nil
		}
		return []string{string(runes)}
	}
	var out []string
	start := 0
	for start < len(runes) {
		// Skip leading spaces left over from the previous break.
		for start < len(runes) && runes[start] == ' ' {
			start++
		}
		if start >= len(runes) {
			break
		}
		end := start + maxRunes
		if end >= len(runes) {
			chunk := strings.TrimSpace(string(runes[start:]))
			if chunk != "" {
				out = append(out, chunk)
			}
			break
		}
		// Look for a space within [start, end] to break on.
		breakAt := end
		for i := end; i > start; i-- {
			if runes[i] == ' ' {
				breakAt = i
				break
			}
		}
		chunk := strings.TrimSpace(string(runes[start:breakAt]))
		if chunk != "" {
			out = append(out, chunk)
		}
		start = breakAt
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
