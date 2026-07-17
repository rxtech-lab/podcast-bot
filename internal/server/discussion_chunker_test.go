package server

import (
	"strings"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
)

func TestChunkTranscriptGroupsLinesWithOverlap(t *testing.T) {
	lines := make([]DiscussionLine, 0, 30)
	for i := 0; i < 30; i++ {
		lines = append(lines, DiscussionLine{
			Speaker: "Alice",
			Role:    "host",
			Text:    strings.Repeat("word ", 40), // ~200 chars per line
			StartMS: int64(i * 10_000),
		})
	}
	chunks := chunkTranscript(lines)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if c.Kind != ChunkKindTranscript {
			t.Fatalf("chunk %d kind = %q", i, c.Kind)
		}
		if c.ChunkIndex != i {
			t.Fatalf("chunk %d index = %d", i, c.ChunkIndex)
		}
		if len(c.Text) > transcriptChunkMax+transcriptChunkTarget {
			t.Fatalf("chunk %d too large: %d chars", i, len(c.Text))
		}
		if !strings.Contains(c.Text, "Alice (host):") {
			t.Fatalf("chunk %d missing speaker prefix", i)
		}
		if len(c.Meta.Speakers) != 1 || c.Meta.Speakers[0] != "Alice" {
			t.Fatalf("chunk %d speakers = %v", i, c.Meta.Speakers)
		}
	}
	// Overlap: each chunk after the first repeats the previous chunk's last line.
	for i := 1; i < len(chunks); i++ {
		firstLine := strings.SplitN(chunks[i].Text, "\n", 2)[0]
		if !strings.HasSuffix(chunks[i-1].Text, firstLine) {
			t.Fatalf("chunk %d does not start with chunk %d's last line", i, i-1)
		}
	}
	// Time ranges must be monotonic and anchored to the source lines.
	if chunks[0].Meta.StartMS != 0 {
		t.Fatalf("first chunk start = %d", chunks[0].Meta.StartMS)
	}
	last := chunks[len(chunks)-1]
	if last.Meta.EndMS != lines[len(lines)-1].StartMS {
		t.Fatalf("last chunk end = %d, want %d", last.Meta.EndMS, lines[len(lines)-1].StartMS)
	}
}

func TestChunkTranscriptSkipsEmptyLines(t *testing.T) {
	lines := []DiscussionLine{
		{Speaker: "Alice", Text: "Hello there."},
		{Speaker: "Bob", Text: "   "},
		{Speaker: "Carol", Text: "Goodbye."},
	}
	chunks := chunkTranscript(lines)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if strings.Contains(chunks[0].Text, "Bob") {
		t.Fatalf("empty line leaked into chunk: %q", chunks[0].Text)
	}
	if chunks[0].Meta.LineStart != 0 || chunks[0].Meta.LineEnd != 2 {
		t.Fatalf("line range = %d..%d", chunks[0].Meta.LineStart, chunks[0].Meta.LineEnd)
	}
}

func TestChunkSourcesSplitsMarkdownWithTitlePrefix(t *testing.T) {
	long := strings.Repeat("This is a paragraph about the topic.\n\n", 200) // ~7.6k chars
	sources := []config.Source{
		{Title: "Long Doc", URL: "https://example.com/a", Markdown: long},
		{Title: "Snippet Only", URL: "https://example.com/b", Snippet: "just a snippet"},
		{Title: "Empty", URL: "https://example.com/c"},
	}
	chunks := chunkSources(sources, 5)
	if len(chunks) < 4 {
		t.Fatalf("expected several chunks, got %d", len(chunks))
	}
	if chunks[0].ChunkIndex != 5 {
		t.Fatalf("start index = %d, want 5", chunks[0].ChunkIndex)
	}
	longChunks := 0
	snippetChunks := 0
	for _, c := range chunks {
		switch c.Meta.SourceURL {
		case "https://example.com/a":
			longChunks++
			if !strings.HasPrefix(c.Text, "# Long Doc\n") {
				t.Fatalf("source chunk missing title prefix: %q", c.Text[:40])
			}
		case "https://example.com/b":
			snippetChunks++
			if !strings.Contains(c.Text, "just a snippet") {
				t.Fatalf("snippet fallback missing: %q", c.Text)
			}
		case "https://example.com/c":
			t.Fatalf("empty source produced a chunk")
		}
	}
	if longChunks < 3 {
		t.Fatalf("long markdown produced %d chunks", longChunks)
	}
	if snippetChunks != 1 {
		t.Fatalf("snippet source produced %d chunks", snippetChunks)
	}
}

func TestSplitTextChunksOverlap(t *testing.T) {
	text := strings.Repeat("alpha beta gamma delta. ", 400) // ~9.6k chars
	parts := splitTextChunks(text, 2000, 200)
	if len(parts) < 4 {
		t.Fatalf("expected >=4 parts, got %d", len(parts))
	}
	for i, p := range parts {
		if len(p) > 2000+200 {
			t.Fatalf("part %d too long: %d", i, len(p))
		}
	}
}

func TestDiscussionContentHashChangesOnEdit(t *testing.T) {
	lines := []DiscussionLine{{Speaker: "Alice", Text: "Hello.", StartMS: 100}}
	sources := []config.Source{{Title: "Doc", URL: "https://example.com", Markdown: "body"}}
	base := discussionContentHash(lines, sources)
	if base != discussionContentHash(lines, sources) {
		t.Fatal("hash is not deterministic")
	}
	edited := []DiscussionLine{{Speaker: "Alice", Text: "Hello!", StartMS: 100}}
	if base == discussionContentHash(edited, sources) {
		t.Fatal("hash unchanged after transcript edit")
	}
	moreSources := append(sources, config.Source{Title: "Doc2", URL: "https://example.com/2", Markdown: "more"})
	if base == discussionContentHash(lines, moreSources) {
		t.Fatal("hash unchanged after source addition")
	}
}
