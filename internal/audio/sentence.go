package audio

import (
	"strings"
	"unicode"
)

// Sentence terminators we recognise (ASCII + CJK + ellipsis).
// CJK terminators don't need trailing whitespace to be a boundary.
var sentenceEnders = map[rune]bool{
	'.': true, '!': true, '?': true,
	'。': true, '！': true, '？': true,
	'…': true,
}

// cjkEnder reports whether r is a CJK-style sentence terminator (always a
// boundary; no trailing whitespace required).
func cjkEnder(r rune) bool {
	return r == '。' || r == '！' || r == '？'
}

// SentenceSplitter accumulates streamed text deltas and emits complete
// sentences. A sentence ends when a terminator is followed by whitespace,
// newline, or end-of-stream. Terminator clusters (e.g. "...") are kept together.
//
// MinChars (when > 0) coalesces sentences below the threshold with the
// following sentence so the consumer (typically TTS) doesn't get a stream
// of single-character clips like "是。" / "不是。" — those would synthesize
// into ~0.5s audio bursts whose subtitle flickers past before viewers can
// read it. Setting MinChars to a small rune count (e.g. 6) keeps the
// host's "是。 <clarifying clause>。" pattern in one clip while still
// breaking up genuinely long prose.
type SentenceSplitter struct {
	buf      strings.Builder
	MinChars int
}

// Push adds a chunk and returns any complete sentences it produced.
func (s *SentenceSplitter) Push(chunk string) []string {
	s.buf.WriteString(chunk)
	cur := s.buf.String()
	out, rest := extractSentences(cur, false, s.MinChars)
	s.buf.Reset()
	s.buf.WriteString(rest)
	return out
}

// Flush returns any remaining buffered text as a final sentence (if non-empty).
func (s *SentenceSplitter) Flush() []string {
	cur := s.buf.String()
	s.buf.Reset()
	out, rest := extractSentences(cur, true, s.MinChars)
	if rest = strings.TrimSpace(rest); rest != "" {
		out = append(out, rest)
	}
	return out
}

// extractSentences splits text into complete sentences, returning leftover.
// When forceFinal is true, treat end-of-input as a boundary even without
// trailing whitespace after a terminator. minChars (>0) skips emission at
// a boundary when the accumulated sentence is below the threshold and
// there is more input ahead — the short fragment merges into the next
// sentence. At forceFinal end-of-input the threshold is bypassed so no
// text is lost.
func extractSentences(text string, forceFinal bool, minChars int) (sentences []string, rest string) {
	runes := []rune(text)
	start := 0
	i := 0
	for i < len(runes) {
		if !sentenceEnders[runes[i]] {
			i++
			continue
		}
		// Walk through any run of consecutive enders so "..." or "?!" stay together.
		j := i
		for j < len(runes) && sentenceEnders[runes[j]] {
			j++
		}
		// Boundary if the next rune is whitespace/EOL, this is end-of-input + forceFinal,
		// or any of the cluster runes is a CJK terminator (always a boundary).
		boundary := j == len(runes) && forceFinal
		if j < len(runes) && unicode.IsSpace(runes[j]) {
			boundary = true
		}
		if !boundary {
			for k := i; k < j; k++ {
				if cjkEnder(runes[k]) {
					boundary = true
					break
				}
			}
		}
		if boundary {
			// Hold short fragments back so they merge with the next
			// sentence — avoids "是。" being its own audio clip.
			// Bypass when this is end-of-input under forceFinal so
			// trailing fragments still get flushed instead of dropped.
			atEOF := j == len(runes) && forceFinal
			if minChars > 0 && (j-start) < minChars && !atEOF {
				i = j
				continue
			}
			sent := strings.TrimSpace(string(runes[start:j]))
			if sent != "" {
				sentences = append(sentences, sent)
			}
			// Skip trailing whitespace.
			k := j
			for k < len(runes) && unicode.IsSpace(runes[k]) {
				k++
			}
			start = k
			i = k
			continue
		}
		i = j
	}
	rest = string(runes[start:])
	return sentences, rest
}
