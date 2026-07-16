package planner

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sirily11/debate-bot/internal/config"
)

// uploadedAudioMaxTitleRunes bounds a proposed title so a runaway model output
// can't be persisted as the discussion title.
const uploadedAudioMaxTitleRunes = 200

// uploadedAudioSystemContract is appended to the base planning system prompt
// for uploaded-audio conversations: the agent proofreads a real transcript, it
// never authors content.
const uploadedAudioSystemContract = `
Uploaded-audio transcript review contract:
- The plan is a transcript of the user's own uploaded audio recording, split into indexed segments with fixed timings. Your job is proofreading, NOT authoring.
- Fix transcription errors only: misheard words, wrong homophones, garbled names or terms, missing or wrong punctuation, and segments attributed to the wrong speaker.
- Never invent, add, remove, reorder, merge, or split segments, and never change what was actually said. Timings are server-owned and cannot be edited.
- update_plan takes only the segments you change (by their index), an optional corrected title, and optional speaker renames (e.g. "Speaker 1" → a real name the audio reveals). Unlisted segments stay exactly as they are.
- You may rename speakers only when the audio content makes the mapping obvious, or when the user asks.
- Reply in the same language as the transcript.`

// uploadedAudioPlanSchema is the write_plan/update_plan argument schema for
// transcript review. Segment edits are keyed by index so the model submits only
// what changed; the assembler merges them onto the stored plan and keeps every
// server-owned field (audio key, timings) intact.
func uploadedAudioPlanSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title": map[string]any{"type": "string", "description": "Optional corrected episode title. Omit to keep the current title."},
			"speaker_renames": map[string]any{
				"type":        "array",
				"description": "Optional speaker renames applied across the whole transcript.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"from": map[string]any{"type": "string", "description": "The current speaker label, e.g. \"Speaker 1\"."},
						"to":   map[string]any{"type": "string", "description": "The new display name."},
					},
					"required": []string{"from", "to"},
				},
			},
			"segments": map[string]any{
				"type":        "array",
				"description": "ONLY the transcript segments being corrected, identified by their zero-based index. Unlisted segments are kept unchanged.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"index":   map[string]any{"type": "integer", "description": "Zero-based index of the segment in the transcript."},
						"text":    map[string]any{"type": "string", "description": "Corrected text for this segment. Omit to keep the text."},
						"speaker": map[string]any{"type": "string", "description": "Corrected speaker label for this segment. Omit to keep the speaker."},
					},
					"required": []string{"index"},
				},
			},
		},
	}
}

// renderUploadedAudioTranscript renders the stored transcript as the indexed
// listing the review agent works from. It is appended to the system prompt
// every turn so the model always proofreads the CURRENT saved segments
// (including its own earlier corrections), never a stale copy baked into an
// old conversation turn.
func renderUploadedAudioTranscript(t *config.DebateTopic) string {
	if t == nil || len(t.TranscriptSegments) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Current transcript of %q (%d segments; format: index | speaker | start | text):\n",
		t.Title, len(t.TranscriptSegments)))
	for i, seg := range t.TranscriptSegments {
		total := seg.OffsetMS / 1000
		sb.WriteString(fmt.Sprintf("%d | %s | %d:%02d:%02d | %s\n",
			i, seg.Speaker, total/3600, (total%3600)/60, total%60, seg.Text))
	}
	return sb.String()
}

// uploadedAudioDraft mirrors uploadedAudioPlanSchema.
type uploadedAudioDraft struct {
	Title          string `json:"title"`
	SpeakerRenames []struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"speaker_renames"`
	Segments []struct {
		Index   *int    `json:"index"`
		Text    *string `json:"text"`
		Speaker *string `json:"speaker"`
	} `json:"segments"`
}

func decodeUploadedAudioDraft(raw string) (*uploadedAudioDraft, error) {
	var d uploadedAudioDraft
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return nil, fmt.Errorf("decode transcript edits: %w", err)
	}
	return &d, nil
}

// assembleUploadedAudioPlan merges a review draft onto the existing transcript
// plan. Server-owned fields (audio key, durations, offsets, segment count and
// order) always come from the existing plan; the draft contributes only text
// and speaker corrections plus an optional title.
func assembleUploadedAudioPlan(existing *config.DebateTopic, d *uploadedAudioDraft) (*Result, error) {
	if existing == nil || existing.Type != config.ContentTypeUploadedAudio || len(existing.TranscriptSegments) == 0 {
		return nil, fmt.Errorf("no uploaded-audio transcript exists to edit")
	}
	if d == nil {
		return nil, fmt.Errorf("transcript edits are required")
	}
	merged := *existing
	merged.TranscriptSegments = append([]config.TranscriptSegment(nil), existing.TranscriptSegments...)

	if title := strings.TrimSpace(d.Title); title != "" {
		if runes := []rune(title); len(runes) > uploadedAudioMaxTitleRunes {
			title = string(runes[:uploadedAudioMaxTitleRunes])
		}
		merged.Title = title
	}
	for _, seg := range d.Segments {
		if seg.Index == nil {
			return nil, fmt.Errorf("segment edit is missing its index")
		}
		i := *seg.Index
		if i < 0 || i >= len(merged.TranscriptSegments) {
			return nil, fmt.Errorf("segment index %d is out of range (transcript has %d segments)", i, len(merged.TranscriptSegments))
		}
		if seg.Text != nil {
			text := strings.TrimSpace(*seg.Text)
			if text == "" {
				return nil, fmt.Errorf("segment %d: corrected text must not be empty", i)
			}
			merged.TranscriptSegments[i].Text = text
		}
		if seg.Speaker != nil {
			speaker := strings.TrimSpace(*seg.Speaker)
			if speaker == "" {
				return nil, fmt.Errorf("segment %d: corrected speaker must not be empty", i)
			}
			merged.TranscriptSegments[i].Speaker = speaker
		}
	}
	for _, r := range d.SpeakerRenames {
		from, to := strings.TrimSpace(r.From), strings.TrimSpace(r.To)
		if from == "" || to == "" {
			return nil, fmt.Errorf("speaker renames need both from and to")
		}
		for i := range merged.TranscriptSegments {
			if strings.EqualFold(strings.TrimSpace(merged.TranscriptSegments[i].Speaker), from) {
				merged.TranscriptSegments[i].Speaker = to
			}
		}
		for i := range merged.UploadedAudioSpeakers {
			if strings.EqualFold(strings.TrimSpace(merged.UploadedAudioSpeakers[i]), from) {
				merged.UploadedAudioSpeakers[i] = to
			}
		}
	}
	merged.UploadedAudioSpeakers = config.UploadedAudioSpeakerNames(&merged)
	if err := config.ValidateTopic(&merged); err != nil {
		return nil, err
	}
	return &Result{Script: &merged}, nil
}
