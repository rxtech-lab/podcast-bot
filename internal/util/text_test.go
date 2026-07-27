package util

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRuneBoundarySnapsBackwards(t *testing.T) {
	// "a" + "，" (U+FF0C, EF BC 8C) + "b" -> bytes 1,2,3 are the comma.
	s := "a，b"
	for i, want := range map[int]int{0: 0, 1: 1, 2: 1, 3: 1, 4: 4, 5: 5} {
		if got := RuneBoundary(s, i); got != want {
			t.Fatalf("RuneBoundary(%q, %d) = %d, want %d", s, i, got, want)
		}
	}
	if got := RuneBoundary(s, -3); got != 0 {
		t.Fatalf("negative offset = %d, want 0", got)
	}
	if got := RuneBoundary(s, 99); got != len(s) {
		t.Fatalf("past-end offset = %d, want %d", got, len(s))
	}
}

func TestTruncateBytesKeepsValidUTF8(t *testing.T) {
	s := strings.Repeat("中文内容", 50)
	for limit := 0; limit < len(s); limit++ {
		out := TruncateBytes(s, limit)
		if !utf8.ValidString(out) {
			t.Fatalf("TruncateBytes(limit=%d) produced invalid UTF-8", limit)
		}
		if len(out) > limit {
			t.Fatalf("TruncateBytes(limit=%d) returned %d bytes", limit, len(out))
		}
		if !strings.HasPrefix(s, out) {
			t.Fatalf("TruncateBytes(limit=%d) is not a prefix of the input", limit)
		}
	}
}
