package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sirily11/debate-bot/internal/llm"
)

// Commander is the silent visual/music director of a panel discussion. It
// never takes a spoken turn — instead a background loop (see
// content_creator/discussion_director.go) periodically calls Direct with the
// recent transcript and acts on the returned cue: generating a fresh
// background image on the fly and/or crossfading the music bed to match the
// mood of the conversation. Think of it as a VJ/DJ working the room.
type Commander struct {
	*Base
	topicTitle string
	// musicMoods describes the pre-generated music beds available to
	// crossfade between, in index order. len(musicMoods) == number of beds.
	musicMoods []string
}

// NewCommander constructs the silent director. musicMoods are short
// descriptions of the pre-generated beds (index-aligned) the commander may
// crossfade to; pass nil if music is unavailable.
func NewCommander(b *Base, topicTitle string, musicMoods []string) *Commander {
	return &Commander{Base: b, topicTitle: topicTitle, musicMoods: musicMoods}
}

// Speak is never called — the commander is silent and the planner never
// schedules it as a turn. It exists only to satisfy the Agent interface.
func (c *Commander) Speak(ctx context.Context, p SpeakPrompt) (*llm.Stream, error) {
	return nil, fmt.Errorf("commander is a silent director and does not speak")
}

// DirectorCue is the JSON-mode decision the commander returns each tick.
type DirectorCue struct {
	// Action is "keep" (leave the background as-is) or "generate" (produce a
	// fresh background image from ScenePrompt).
	Action string `json:"action"`
	// ScenePrompt is the image-generation prompt used when Action=="generate".
	// It should describe a mood-appropriate, text-free background scene with a
	// calm lower third (the subtitle sits there).
	ScenePrompt string `json:"scene_prompt"`
	// MusicIndex selects a pre-generated bed to crossfade to. -1 keeps the
	// current bed. Out-of-range values are ignored by the caller.
	MusicIndex int `json:"music_index"`
	// Reason is a one-line rationale, surfaced to logs only.
	Reason string `json:"reason"`
}

// Direct asks the commander's LLM whether to change the visuals/music given
// the recent discussion. Uses JSON mode (same pattern as Viewer.WantsToAsk).
func (c *Commander) Direct(ctx context.Context, recent []TranscriptLine) (DirectorCue, error) {
	var musicBlock string
	if len(c.musicMoods) > 0 {
		var sb strings.Builder
		sb.WriteString("Available music beds (set music_index to crossfade, -1 to keep current):\n")
		for i, m := range c.musicMoods {
			fmt.Fprintf(&sb, "  %d: %s\n", i, strings.TrimSpace(m))
		}
		musicBlock = sb.String()
	} else {
		musicBlock = "No music beds available — always set music_index to -1.\n"
	}

	system := fmt.Sprintf(`You are the silent visual + music director of a live panel discussion titled %q.
You never speak. Your only job is to keep the background image and music matching the mood of the conversation, like a VJ/DJ. Change things only when the mood or subject genuinely shifts — gratuitous switching is distracting.
Reply STRICTLY as JSON: {"action": "keep"|"generate", "scene_prompt": "<image prompt, empty when action=keep>", "music_index": <int>, "reason": "<one line>"}.
When action="generate", scene_prompt must describe a cinematic, photographic or painterly BACKGROUND scene with NO people front-and-center and NO text, evoking the current topic/mood, with a calm, darker lower third so white subtitles stay legible.
%s`, c.topicTitle, musicBlock)

	user := "Recent discussion:\n" + formatRecent(recent)
	raw, err := c.llmC.JSON(ctx, system, user)
	if err != nil {
		return DirectorCue{}, err
	}
	cue := DirectorCue{MusicIndex: -1}
	trimmed := cleanDirectorJSON(string(raw))
	if err := json.Unmarshal([]byte(trimmed), &cue); err != nil {
		return DirectorCue{}, fmt.Errorf("decode director cue: %w (raw=%s)", err, trimmed)
	}
	return cue, nil
}

func cleanDirectorJSON(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}
	trimmed = strings.TrimPrefix(trimmed, "```")
	if i := strings.IndexByte(trimmed, '\n'); i >= 0 {
		trimmed = trimmed[i+1:]
	}
	trimmed = strings.TrimSpace(trimmed)
	trimmed = strings.TrimSuffix(trimmed, "```")
	return strings.TrimSpace(trimmed)
}
