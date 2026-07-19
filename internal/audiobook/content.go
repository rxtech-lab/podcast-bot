package audiobook

import (
	"context"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/storage"
)

// maxChapterTextChars caps a single chapter's injected source text so one
// oversized chapter cannot blow the narrator's per-turn prompt. The stored
// slice keeps its full length (AudioBookChapter.ContentChars records it), so
// truncation here is observable in logs.
const maxChapterTextChars = 150_000

// FetchChapterTexts downloads each chapter's sliced source markdown from
// object storage, keyed by GLOBAL chapter number: for batch scripts the i-th
// chapter maps to AudioBookChapterIndices[i], for root scripts to i+1 — the
// same numbering the outline and chapter-boundary machinery use. Missing keys
// and failed downloads are logged and omitted. Returns nil when storage is
// disabled or no chapter carries a content key (legacy plans), which keeps
// generation on the outline-only path.
func FetchChapterTexts(ctx context.Context, log *slog.Logger, up *storage.Uploader, topic *config.DebateTopic) map[int]string {
	if up == nil || !up.Enabled() || topic == nil {
		return nil
	}
	indexed := len(topic.AudioBookChapterIndices) == len(topic.AudioBookChapters)
	out := make(map[int]string)
	for i, ch := range topic.AudioBookChapters {
		key := strings.TrimSpace(ch.ContentKey)
		if key == "" {
			continue
		}
		number := i + 1
		if indexed && topic.AudioBookChapterIndices[i] > 0 {
			number = topic.AudioBookChapterIndices[i]
		}
		data, err := up.Download(ctx, key)
		if err != nil {
			log.Warn("audiobook chapter text fetch failed", "chapter", number, "key", key, "err", err)
			continue
		}
		text := strings.TrimSpace(string(data))
		if text == "" {
			continue
		}
		if len(text) > maxChapterTextChars {
			log.Warn("audiobook chapter text truncated",
				"chapter", number, "chars", len(text), "cap", maxChapterTextChars)
			text = truncateAtRune(text, maxChapterTextChars)
		}
		out[number] = text
	}
	if len(out) == 0 {
		return nil
	}
	log.Info("audiobook chapter texts fetched", "chapters", len(out))
	return out
}

// truncateAtRune cuts s to at most limit bytes without splitting a UTF-8 rune.
func truncateAtRune(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
