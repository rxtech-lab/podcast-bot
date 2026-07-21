package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sirily11/debate-bot/internal/llm"
)

// AudioBookBoundaryJudge is a silent reviewer for audiobook narration. It is
// not scheduled as a speaker; the content pipeline asks it after each completed
// narration loop whether the audiobook should continue.
type AudioBookBoundaryJudge struct{ *Base }

func NewAudioBookBoundaryJudge(b *Base) *AudioBookBoundaryJudge {
	return &AudioBookBoundaryJudge{Base: b}
}

func (j *AudioBookBoundaryJudge) Speak(ctx context.Context, p SpeakPrompt) (*llm.Stream, error) {
	return nil, fmt.Errorf("audiobook boundary judge is silent and does not speak")
}

type AudioBookBoundaryDecision struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
	// ChapterComplete reports whether the latest loop finished narrating the
	// current chapter (the one whose source text the narrator was given).
	// Only meaningful when a current chapter was supplied to Review.
	ChapterComplete bool `json:"chapter_complete"`
}

func (j *AudioBookBoundaryJudge) Review(ctx context.Context, selectedRange, currentChapter, outline, accepted, candidate string) (AudioBookBoundaryDecision, error) {
	if j == nil || j.llmC == nil {
		return AudioBookBoundaryDecision{}, fmt.Errorf("audiobook boundary judge has no llm client")
	}
	system := `You are a fast audiobook boundary judge.
Review the completed narration from one audiobook generation loop.
Return strict JSON only: {"action":"continue"|"stop","reason":"<one short sentence>","chapter_complete":true|false}.
Use action=continue when another narration loop is still needed for the selected audiobook chapters.
Use action=stop when the selected audiobook chapters appear complete or the latest loop has moved beyond the selected chapter range.
"Current chapter" is the chapter the narrator was working on this loop. Set chapter_complete=true when the loop clearly finished narrating that chapter (reached its final content or moved into the next chapter's material); otherwise set it to false. When no current chapter is given, set chapter_complete=false.`
	user := strings.Join([]string{
		"Selected chapter range:",
		selectedRange,
		"",
		"Current chapter being narrated:",
		fallback(currentChapter, "(not tracked)"),
		"",
		"Selected chapter outline:",
		fallback(outline, "(none)"),
		"",
		"Previously accepted context:",
		fallback(accepted, "(none)"),
		"",
		"Latest completed narration loop:",
		candidate,
	}, "\n")
	raw, err := j.llmC.JSON(ctx, system, user)
	if err != nil {
		return AudioBookBoundaryDecision{}, err
	}
	var out AudioBookBoundaryDecision
	if err := json.Unmarshal(raw, &out); err != nil {
		return AudioBookBoundaryDecision{}, fmt.Errorf("decode audiobook boundary decision: %w", err)
	}
	out.Action = strings.ToLower(strings.TrimSpace(out.Action))
	out.Reason = strings.TrimSpace(out.Reason)
	switch out.Action {
	case "stop":
	case "continue":
		out.Action = "keep"
	default:
		out.Action = "keep"
	}
	return out, nil
}
