package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sirily11/debate-bot/internal/planner"
)

func attachmentsRow(t *testing.T, role string, attachments []planner.Attachment) planningTurnRow {
	t.Helper()
	raw, err := json.Marshal(attachments)
	if err != nil {
		t.Fatalf("marshal attachments: %v", err)
	}
	return planningTurnRow{Role: role, AttachmentsJSON: string(raw)}
}

func TestPlanningSourceDocsDigest(t *testing.T) {
	turns := []planningTurnRow{
		attachmentsRow(t, "user", []planner.Attachment{
			{Filename: "report.pdf", Markdown: "## Findings\nRemote workers are happier.", Key: "uploads/u/1.pdf"},
			{Filename: "photo.png", URL: "https://example.com/p.png", MIMEType: "image/png", Key: "uploads/u/2.png"},
			{Filename: "note.m4a", URL: "https://example.com/a.m4a", MIMEType: "audio/mp4", Key: "uploads/u/3.m4a"},
			{Filename: "site", URL: "https://example.com/article", MIMEType: "text/html", Key: "uploads/u/4"},
		}),
		// Same document attached again on a later turn — deduped by key.
		attachmentsRow(t, "user", []planner.Attachment{
			{Filename: "report.pdf", Markdown: "## Findings\nRemote workers are happier.", Key: "uploads/u/1.pdf"},
		}),
		// Assistant turns never contribute.
		attachmentsRow(t, "assistant", []planner.Attachment{
			{Filename: "assistant.md", Markdown: "should not appear", Key: "uploads/u/5.md"},
		}),
	}

	got := planningSourceDocsDigest(turns)
	if got == "" {
		t.Fatalf("digest is empty")
	}
	if n := strings.Count(got, "--- report.pdf ---"); n != 1 {
		t.Fatalf("report.pdf appears %d times, want 1 (dedup by key)\n%s", n, got)
	}
	if strings.Contains(got, "photo.png") || strings.Contains(got, "note.m4a") {
		t.Fatalf("image/audio attachments must be skipped:\n%s", got)
	}
	if !strings.Contains(got, "Shared webpage: https://example.com/article") {
		t.Fatalf("URL-only webpage attachment missing:\n%s", got)
	}
	if strings.Contains(got, "assistant.md") {
		t.Fatalf("assistant-turn attachments must be skipped:\n%s", got)
	}
	// Heading lines are neutralized so the digest can't terminate the
	// `## Source Documents` section when the script markdown is re-parsed.
	if strings.Contains(got, "\n## Findings") || strings.HasPrefix(got, "## ") {
		t.Fatalf("digest contains an unsanitized heading line:\n%s", got)
	}
	if !strings.Contains(got, "> ## Findings") {
		t.Fatalf("heading line should be blockquoted:\n%s", got)
	}
}

func TestPlanningSourceDocsDigestCaps(t *testing.T) {
	long := strings.Repeat("word ", 3000) // ~15k chars, beyond the per-doc cap
	turns := []planningTurnRow{
		attachmentsRow(t, "user", []planner.Attachment{
			{Filename: "a.pdf", Markdown: long, Key: "k1"},
			{Filename: "b.pdf", Markdown: long, Key: "k2"},
			{Filename: "c.pdf", Markdown: long, Key: "k3"},
		}),
	}
	got := planningSourceDocsDigest(turns)
	if !strings.Contains(got, "[...truncated]") {
		t.Fatalf("per-doc truncation marker missing")
	}
	if strings.Contains(got, "--- c.pdf ---") {
		t.Fatalf("total cap should have dropped the third document")
	}
	// Roughly bounded: total cap + one per-doc entry of slack.
	if len(got) > sourceDocsTotalLimit+sourceDocCharLimit {
		t.Fatalf("digest length %d exceeds bound", len(got))
	}
}

func TestPlanningSourceDocsDigestEmpty(t *testing.T) {
	if got := planningSourceDocsDigest(nil); got != "" {
		t.Fatalf("nil turns should produce empty digest, got %q", got)
	}
	turns := []planningTurnRow{
		attachmentsRow(t, "user", []planner.Attachment{
			{Filename: "photo.png", URL: "https://example.com/p.png", MIMEType: "image/png"},
		}),
	}
	if got := planningSourceDocsDigest(turns); got != "" {
		t.Fatalf("image-only attachments should produce empty digest, got %q", got)
	}
}
