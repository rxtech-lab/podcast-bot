package server

import (
	"context"
	"fmt"
	"strings"
)

// Source-document digest limits: per-document mirrors the planning-time
// attachmentsPrompt truncation; the total cap protects the per-turn token
// budget since the digest is re-sent on every host/discussant turn.
const (
	sourceDocCharLimit    = 6000
	sourceDocsTotalLimit  = 12000
	sourceDocsDigestIntro = "The user uploaded these reference documents when planning this discussion. They are the original source material for the conversation."
)

// discussionSourceDocsDigest builds the Source Documents prompt digest for a
// discussion generation run from the persisted planning conversation. Best
// effort: any miss (no planning store, no conversation, storage error) yields
// "" and the run proceeds without a digest, exactly as before.
func (s *Server) discussionSourceDocsDigest(ctx context.Context, owner, discussionID string) string {
	if s.d.Planning == nil {
		return ""
	}
	ok, _, turns, err := s.d.Planning.ConversationWithTurnsByDiscussion(ctx, owner, discussionID)
	if err != nil || !ok {
		return ""
	}
	return planningSourceDocsDigest(turns)
}

// planningSourceDocsDigest renders the user's document attachments across all
// planning turns as one digest block: `--- <filename> ---` followed by the
// document markdown (truncated), or the URL for a shared webpage. Images and
// audio are skipped (no quotable text), duplicates are folded by storage key
// (falling back to filename), and heading-like lines are neutralized because
// the digest travels inside a `## Source Documents` markdown section that
// config.parseSections splits on any `## ` line.
func planningSourceDocsDigest(turns []planningTurnRow) string {
	seen := make(map[string]bool)
	var sb strings.Builder
	total := 0
	docN := 0
	for _, r := range turns {
		if r.Role != "user" {
			continue
		}
		for _, a := range planningTurnAttachments(r) {
			mime := strings.ToLower(strings.TrimSpace(a.MIMEType))
			if strings.HasPrefix(mime, "image/") || strings.HasPrefix(mime, "audio/") {
				continue
			}
			markdown := strings.TrimSpace(a.Markdown)
			url := strings.TrimSpace(a.URL)
			if markdown == "" && url == "" {
				continue
			}
			dedupKey := strings.TrimSpace(a.Key)
			if dedupKey == "" {
				dedupKey = strings.TrimSpace(a.Filename)
			}
			if dedupKey != "" {
				if seen[dedupKey] {
					continue
				}
				seen[dedupKey] = true
			}
			if total >= sourceDocsTotalLimit {
				continue
			}
			docN++
			name := strings.TrimSpace(a.Filename)
			if name == "" {
				name = fmt.Sprintf("document %d", docN)
			}
			var body string
			if markdown != "" {
				body = truncateChars(sanitizeSectionBody(markdown), sourceDocCharLimit)
			} else {
				body = "Shared webpage: " + url
			}
			entry := fmt.Sprintf("--- %s ---\n%s\n\n", name, body)
			sb.WriteString(entry)
			total += len(entry)
		}
	}
	if sb.Len() == 0 {
		return ""
	}
	return sourceDocsDigestIntro + "\n\n" + strings.TrimSpace(sb.String())
}

// sanitizeSectionBody neutralizes markdown heading lines so embedded document
// content can travel inside one `## <section>` block of the rendered script:
// config.parseSections treats any line whose trimmed form starts with "## "
// as a section boundary (an unknown heading silently drops the rest of the
// section), so heading lines are turned into blockquotes.
func sanitizeSectionBody(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			lines[i] = "> " + strings.TrimSpace(l)
		}
	}
	return strings.Join(lines, "\n")
}

func truncateChars(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := s[:n]
	// Don't split a UTF-8 rune.
	for len(cut) > 0 && cut[len(cut)-1]&0xC0 == 0x80 {
		cut = cut[:len(cut)-1]
	}
	return cut + "\n[...truncated]"
}
