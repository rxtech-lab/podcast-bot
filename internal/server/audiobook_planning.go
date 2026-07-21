package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/sirily11/debate-bot/internal/planner"
)

// planningAudioBookSource rebuilds the full concatenated source markdown from
// the conversation's persisted user-turn attachments — the same concatenation
// the audiobook digest renders over, so the model's start_index references
// resolve against identical offsets.
func planningAudioBookSource(turns []planningTurnRow) string {
	var atts []planner.Attachment
	for _, t := range turns {
		if t.Role != "user" {
			continue
		}
		atts = append(atts, planningTurnAttachments(t)...)
	}
	return planner.AudioBookSourceFromAttachments(atts)
}

// audioBookChapterStorer returns the callback the planner uses to persist one
// sliced chapter's markdown to object storage, or nil when storage is
// disabled. Keys embed a content hash so re-splitting an unchanged plan is
// idempotent: identical bytes map to the same key, and the in-closure cache
// skips repeat uploads within one planning turn. Existing keys are never
// overwritten with different content, so batch scripts derived from an older
// plan revision keep reading their original chapter text.
func (s *Server) audioBookChapterStorer(discussionID string) func(ctx context.Context, chapterIndex int, content []byte) (string, error) {
	up := s.d.Uploader
	if up == nil || !up.Enabled() {
		return nil
	}
	uploaded := make(map[string]bool)
	return func(ctx context.Context, chapterIndex int, content []byte) (string, error) {
		sum := sha256.Sum256(content)
		key := up.Key(fmt.Sprintf("audiobooks/%s/chapters/%02d-%s.md", discussionID, chapterIndex, hex.EncodeToString(sum[:4])))
		if uploaded[key] {
			return key, nil
		}
		if err := up.UploadBytes(ctx, key, "text/markdown; charset=utf-8", content); err != nil {
			return "", err
		}
		uploaded[key] = true
		return key, nil
	}
}
