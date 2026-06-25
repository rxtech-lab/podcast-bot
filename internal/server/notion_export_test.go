package server

import (
	"strings"
	"testing"
)

func TestMarkdownToNotionBlocks(t *testing.T) {
	md := strings.Join([]string{
		"# Title",
		"",
		"A paragraph with a [link](https://podcast.rxlab.app/p/abc) and **bold**.",
		"",
		"## Section",
		"- first",
		"- second",
		"1. one",
		"2. two",
		"> a quote",
		"```mermaid",
		"graph TD; A-->B;",
		"```",
		"---",
	}, "\n")

	blocks := markdownToNotionBlocks(md)
	if len(blocks) == 0 {
		t.Fatal("expected blocks, got none")
	}

	types := make([]string, 0, len(blocks))
	for _, b := range blocks {
		types = append(types, b["type"].(string))
	}
	want := []string{
		"heading_1", "paragraph", "heading_2",
		"bulleted_list_item", "bulleted_list_item",
		"numbered_list_item", "numbered_list_item",
		"quote", "code", "divider",
	}
	if len(types) != len(want) {
		t.Fatalf("block count = %d (%v), want %d (%v)", len(types), types, len(want), want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Errorf("block[%d] type = %q, want %q", i, types[i], want[i])
		}
	}

	// Code block keeps mermaid language and verbatim content.
	code := blocks[8]["code"].(map[string]any)
	if code["language"] != "mermaid" {
		t.Errorf("code language = %v, want mermaid", code["language"])
	}

	// The paragraph's rich text splits into plain + link + plain + bold runs.
	para := blocks[1]["paragraph"].(map[string]any)
	rich := para["rich_text"].([]map[string]any)
	var sawLink, sawBold bool
	for _, rt := range rich {
		text := rt["text"].(map[string]any)
		if link, ok := text["link"].(map[string]any); ok && link["url"] == "https://podcast.rxlab.app/p/abc" {
			sawLink = true
		}
		if ann, ok := rt["annotations"].(map[string]any); ok && ann["bold"] == true {
			sawBold = true
		}
	}
	if !sawLink {
		t.Error("expected a rich-text run with the link annotation")
	}
	if !sawBold {
		t.Error("expected a rich-text run with the bold annotation")
	}
}

func TestNotionRichTextChunks2000(t *testing.T) {
	long := strings.Repeat("x", 4500)
	rich := notionRichText(long)
	if len(rich) != 3 {
		t.Fatalf("expected 3 chunks for 4500 chars, got %d", len(rich))
	}
	for i, rt := range rich {
		content := rt["text"].(map[string]any)["content"].(string)
		if len([]rune(content)) > 2000 {
			t.Errorf("chunk %d exceeds 2000 runes: %d", i, len([]rune(content)))
		}
	}
}
