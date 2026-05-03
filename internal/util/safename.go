package util

import (
	"strings"
	"unicode"
)

// Safe lowercases name, ASCII-folds, and replaces non-alnum runes with '_'.
// Used for memory file prefixes and audio paths.
func Safe(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	out = strings.Trim(out, "_")
	if out == "" {
		return "agent"
	}
	return out
}
