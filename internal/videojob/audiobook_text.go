package videojob

import (
	"context"
	"fmt"
	"strings"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
	contentcreator "github.com/sirily11/debate-bot/internal/content_creator"
	"github.com/sirily11/debate-bot/internal/server"
)

// generateAudioBookTextDoc builds the audiobook's "text-based content" — a
// readable book version of the narration with the generated illustrations
// inline — and stores it as the
// SummaryDocTypeText document so the client can open it from the context menu.
//
// Called from the audio-only finalisation path AFTER the audio has been
// uploaded. Best-effort: failures are logged and never fail the job.
func generateAudioBookTextDoc(ctx context.Context, deps Deps, jobID string,
	topic *config.DebateTopic, orch *contentcreator.Orchestrator, audioURL string,
) {
	if deps.Discussions == nil || deps.DiscussionID == "" || orch == nil || topic == nil {
		return
	}
	md := buildAudioBookText(topic, orch.Transcript.Snapshot(), orch.AudioBookImages(), audioURL)
	if strings.TrimSpace(md) == "" {
		return
	}
	if err := deps.Discussions.SaveSummary(ctx, deps.DiscussionID,
		server.SummaryDocTypeText, md, "", server.SummaryUsage{}); err != nil {
		if deps.Log != nil {
			deps.Log.Warn("audiobook text doc save failed",
				"discussion_id", deps.DiscussionID, "err", err)
		}
		return
	}
	// Tell connected clients the text document is ready so the menu refreshes.
	if deps.Bus != nil {
		deps.Bus.Publish(contentcreator.StampChannelID(contentcreator.SummaryReadyMsg{
			DocType: server.SummaryDocTypeText,
			Status:  "ready",
		}, jobID))
	}
}

// buildAudioBookText assembles the book-style Markdown. The narration prose is
// the consolidated transcript; each illustration is inserted just before the
// line that opens its chapter (matched by the chapter title in the caption),
// and any unmatched images are appended at the end.
func buildAudioBookText(topic *config.DebateTopic, lines []agent.TranscriptLine,
	imgs []contentcreator.AudioBookImage, audioURL string,
) string {
	var b strings.Builder
	title := strings.TrimSpace(topic.Title)
	if title == "" {
		title = "Audiobook"
	}
	fmt.Fprintf(&b, "# %s\n\n", title)
	if summary := strings.TrimSpace(topic.Background); summary != "" {
		b.WriteString(summary)
		b.WriteString("\n\n")
	}

	// Track which images still need placing. Images with no URL (upload
	// disabled) are skipped — there's nothing to embed. Repeated URLs are
	// skipped so the text document does not show the same rendered image twice.
	used := make([]bool, len(imgs))
	emittedURLs := map[string]bool{}
	emitImage := func(i int) {
		img := imgs[i]
		url := strings.TrimSpace(img.URL)
		if url == "" || emittedURLs[url] {
			used[i] = true
			return
		}
		alt := strings.TrimSpace(img.Caption)
		if alt == "" {
			alt = "illustration"
		}
		fmt.Fprintf(&b, "![%s](%s)\n\n", alt, url)
		emittedURLs[url] = true
		used[i] = true
	}

	for _, l := range lines {
		text := strings.TrimSpace(l.Text)
		if text == "" {
			continue
		}
		// Place any image whose chapter title appears in this line, before the
		// line itself, so the picture leads its chapter.
		for i, img := range imgs {
			if used[i] {
				continue
			}
			cap := strings.TrimSpace(img.Caption)
			if cap != "" && strings.Contains(text, cap) {
				emitImage(i)
			}
		}
		b.WriteString(text)
		b.WriteString("\n\n")
	}

	// Any illustrations we couldn't anchor to a chapter line go at the end so
	// none are lost.
	for i := range imgs {
		if !used[i] {
			emitImage(i)
		}
	}

	return strings.TrimSpace(b.String())
}
