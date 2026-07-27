package util

import "unicode/utf8"

// RuneBoundary returns the largest offset <= i that starts a UTF-8 rune in s,
// clamped to [0, len(s)]. Slicing a string at an arbitrary byte offset can cut
// a multi-byte character in half; Go tolerates the resulting bytes but
// Postgres rejects them ("invalid byte sequence for encoding UTF8", SQLSTATE
// 22021), so any computed cut point must be snapped here first.
func RuneBoundary(s string, i int) int {
	if i >= len(s) {
		return len(s)
	}
	if i <= 0 {
		return 0
	}
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}

// TruncateBytes returns at most limit bytes of s, cut on a rune boundary.
func TruncateBytes(s string, limit int) string {
	return s[:RuneBoundary(s, limit)]
}
