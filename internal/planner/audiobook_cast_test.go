package planner

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChunkTextSplitsOnParagraphBoundaries(t *testing.T) {
	para := strings.Repeat("This is a sentence about the chapter. ", 20) + "\n\n"
	text := strings.Repeat(para, 20)
	chunks := chunkText(text, 2000)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > 2000 {
			t.Fatalf("chunk %d is %d bytes, over the limit", i, len(c))
		}
		if strings.TrimSpace(c) == "" {
			t.Fatalf("chunk %d is blank", i)
		}
	}
}

func TestChunkTextKeepsMultibyteCharactersIntact(t *testing.T) {
	// No paragraph break anywhere, so every cut takes the fallback path.
	text := strings.Repeat("第一章的内容在这里，没有任何段落分隔符。", 200)
	for limit := 300; limit < 340; limit++ {
		chunks := chunkText(text, limit)
		if len(chunks) < 2 {
			t.Fatalf("limit=%d produced %d chunks, expected a split", limit, len(chunks))
		}
		for i, c := range chunks {
			if !utf8.ValidString(c) {
				t.Fatalf("limit=%d chunk %d is not valid UTF-8: %q", limit, i, c)
			}
		}
		if joined := strings.Join(chunks, ""); joined != text {
			t.Fatalf("limit=%d lost or duplicated content (%d bytes vs %d)", limit, len(joined), len(text))
		}
	}
}

func TestChunkTextShortInputIsOneChunk(t *testing.T) {
	chunks := chunkText("  短文本  ", 2000)
	if len(chunks) != 1 || chunks[0] != "短文本" {
		t.Fatalf("chunks = %q", chunks)
	}
}
