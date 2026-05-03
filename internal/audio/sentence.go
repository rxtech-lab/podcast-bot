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
type SentenceSplitter struct {
	buf strings.Builder
}

// Push adds a chunk and returns any complete sentences it produced.
func (s *SentenceSplitter) Push(chunk string) []string {
	s.buf.WriteString(chunk)
	cur := s.buf.String()
	out, rest := extractSentences(cur, false)
	s.buf.Reset()
	s.buf.WriteString(rest)
	return out
}

// Flush returns any remaining buffered text as a final sentence (if non-empty).
func (s *SentenceSplitter) Flush() []string {
	cur := s.buf.String()
	s.buf.Reset()
	out, rest := extractSentences(cur, true)
	if rest = strings.TrimSpace(rest); rest != "" {
		out = append(out, rest)
	}
	return out
}

// extractSentences splits text into complete sentences, returning leftover.
// When forceFinal is true, treat end-of-input as a boundary even without
// trailing whitespace after a terminator.
func extractSentences(text string, forceFinal bool) (sentences []string, rest string) {
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
