package planner

import (
	"strings"
	"testing"
)

func TestAttachmentsPromptIncludesSharedWebpage(t *testing.T) {
	prompt := attachmentsPrompt([]Attachment{{
		Filename: "Example page",
		URL:      "https://example.com/article",
		MIMEType: "text/uri-list",
	}})
	if !strings.Contains(prompt, "https://example.com/article") {
		t.Fatalf("prompt does not contain shared URL: %q", prompt)
	}
	if !strings.Contains(prompt, "Read this URL before writing the plan") {
		t.Fatalf("prompt does not require reading the shared URL: %q", prompt)
	}
}

func TestAttachmentsPromptKeepsDocumentMarkdown(t *testing.T) {
	prompt := attachmentsPrompt([]Attachment{{
		Filename: "brief.pdf",
		Markdown: "# Brief\nImportant evidence",
		URL:      "https://example.com/brief.pdf",
		MIMEType: "application/pdf",
	}})
	if !strings.Contains(prompt, "Important evidence") {
		t.Fatalf("prompt lost converted document markdown: %q", prompt)
	}
}
