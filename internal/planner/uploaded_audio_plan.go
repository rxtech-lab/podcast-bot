package planner

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/sirily11/debate-bot/internal/config"
)

// uploadedAudioMaxTitleRunes bounds a proposed title so a runaway model output
// can't be persisted as the discussion title.
const uploadedAudioMaxTitleRunes = 200

// uploadedAudioLanguageRe accepts BCP-47-shaped tags ("en", "zh-CN",
// "zh-Hant-TW") so an arbitrary model string can't be persisted as the plan
// language.
var uploadedAudioLanguageRe = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8}){0,3}$`)

// uploadedAudioSystemContract is appended to the base planning system prompt
// for uploaded-audio conversations: the agent proofreads a real transcript, it
// never authors content.
const uploadedAudioSystemContract = `
Uploaded-audio transcript review contract:
- The plan is a transcript of the user's own uploaded audio recording, split into indexed segments with fixed timings. Your job is proofreading, NOT authoring.
- Fix transcription errors only: misheard words, wrong homophones, garbled names or terms, missing or wrong punctuation, and segments attributed to the wrong speaker.
- Never invent, add, remove, reorder, merge, or split segments, and never change what was actually said. Timings are server-owned and cannot be edited.
- Every text correction must stay inside its existing segment's displayed start-end time range. Never move a clause, sentence, or other words into a neighboring segment, and never shorten one segment by redistributing the rest of its text across later indices.
- If a correction would substantially rewrite a segment or change which words belong to its time range, leave that segment unchanged and tell the user to use the transcript editor.
- update_plan takes only the segments you change (by their index), an optional corrected title, and optional speaker renames (e.g. "Speaker 1" → a real name the audio reveals). Unlisted segments stay exactly as they are.
- The stored title was auto-derived from the uploaded file's name, so it is usually meaningless. On your first review, always call update_plan with a generated episode title that describes what the audio is actually about — short and punchy, FEWER THAN 10 WORDS, in the transcript's language. Never keep a filename-shaped title.
- The plan's language must be the audio's spoken language as a BCP-47 tag (e.g. "en-US", "zh-CN", "ja-JP"). When the current language shown with the transcript is empty or does not match what the speakers actually speak, set "language" in update_plan.
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
			"title": map[string]any{"type": "string", "description": "Generated episode title describing the audio content: fewer than 10 words, in the transcript's language. Always provide one on the first review (the stored title is just the uploaded filename); afterwards omit to keep the current title."},
			"language": map[string]any{"type": "string", "description": "BCP-47 tag of the audio's spoken language, e.g. \"en-US\" or \"zh-CN\". Set it when the plan's current language is empty or does not match the transcript; omit to keep it."},
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
				"description": "ONLY transcript segments receiving small proofreading corrections, identified by zero-based index. Keep every correction within that segment's existing time range; never redistribute text between indices. Unlisted segments stay unchanged.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"index":   map[string]any{"type": "integer", "description": "Zero-based index of the segment in the transcript."},
						"text":    map[string]any{"type": "string", "description": "A small in-segment proofreading correction. It must retain all speech belonging to this segment's fixed time range and must not borrow from or move text to adjacent segments. Omit to keep the text."},
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
	lang := strings.TrimSpace(t.Language)
	if lang == "" {
		lang = "not set"
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Current transcript of %q (language: %s; %d segments; format: index | speaker | fixed start-end | text):\n",
		t.Title, lang, len(t.TranscriptSegments)))
	for i, seg := range t.TranscriptSegments {
		sb.WriteString(fmt.Sprintf("%d | %s | %s-%s | %s\n",
			i, seg.Speaker, uploadedAudioTimestamp(seg.OffsetMS),
			uploadedAudioTimestamp(seg.OffsetMS+seg.DurationMS), seg.Text))
	}
	return sb.String()
}

func uploadedAudioTimestamp(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	totalSeconds := ms / 1000
	return fmt.Sprintf("%d:%02d:%02d.%03d", totalSeconds/3600,
		(totalSeconds%3600)/60, totalSeconds%60, ms%1000)
}

// uploadedAudioDraft mirrors uploadedAudioPlanSchema.
type uploadedAudioDraft struct {
	Title          string `json:"title"`
	Language       string `json:"language"`
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
	if lang := strings.TrimSpace(d.Language); lang != "" {
		if !uploadedAudioLanguageRe.MatchString(lang) {
			return nil, fmt.Errorf("language %q is not a BCP-47 tag such as \"en-US\" or \"zh-CN\"", lang)
		}
		merged.Language = lang
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
