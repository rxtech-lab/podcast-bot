package planner

import (
	"strings"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
)

func uploadedAudioFixture() *config.DebateTopic {
	return &config.DebateTopic{
		Title:                   "Raw upload",
		Type:                    config.ContentTypeUploadedAudio,
		Language:                "en-US",
		Channel:                 "default",
		UploadedAudioKey:        "uploads/u/abc.mp3",
		UploadedAudioDurationMS: 60_000,
		TranscriptSegments: []config.TranscriptSegment{
			{Speaker: "Speaker 1", OffsetMS: 0, DurationMS: 2000, Text: "Hello their, welcome."},
			{Speaker: "Speaker 2", OffsetMS: 2000, DurationMS: 3000, Text: "Thanks for having me."},
			{Speaker: "Speaker 1", OffsetMS: 5000, DurationMS: 1500, Text: "Let's begin."},
		},
	}
}

func mustDraft(t *testing.T, raw string) *uploadedAudioDraft {
	t.Helper()
	d, err := decodeUploadedAudioDraft(raw)
	if err != nil {
		t.Fatalf("decode draft: %v", err)
	}
	return d
}

func TestAssembleUploadedAudioPlanMergesByIndex(t *testing.T) {
	existing := uploadedAudioFixture()
	draft := mustDraft(t, `{
		"title": "Corrected Show",
		"language": "zh-CN",
		"segments": [{"index": 0, "text": "Hello there, welcome."}],
		"speaker_renames": [{"from": "Speaker 2", "to": "Alice"}]
	}`)
	res, err := assembleUploadedAudioPlan(existing, draft)
	if err != nil {
		t.Fatal(err)
	}
	got := res.Script
	if got.Title != "Corrected Show" {
		t.Fatalf("title = %q", got.Title)
	}
	if got.Language != "zh-CN" {
		t.Fatalf("language = %q", got.Language)
	}
	if got.TranscriptSegments[0].Text != "Hello there, welcome." {
		t.Fatalf("segment 0 text = %q", got.TranscriptSegments[0].Text)
	}
	if got.TranscriptSegments[1].Speaker != "Alice" {
		t.Fatalf("segment 1 speaker = %q", got.TranscriptSegments[1].Speaker)
	}
	// Server-owned fields survive untouched.
	if got.UploadedAudioKey != existing.UploadedAudioKey ||
		got.TranscriptSegments[0].OffsetMS != 0 ||
		got.TranscriptSegments[1].OffsetMS != 2000 ||
		got.TranscriptSegments[1].DurationMS != 3000 ||
		len(got.TranscriptSegments) != 3 {
		t.Fatalf("server-owned fields changed: %+v", got)
	}
	// The stored plan is never mutated in place.
	if existing.Title != "Raw upload" || existing.TranscriptSegments[0].Text != "Hello their, welcome." ||
		existing.TranscriptSegments[1].Speaker != "Speaker 2" {
		t.Fatalf("existing plan was mutated: %+v", existing)
	}
}

func TestAssembleUploadedAudioPlanRejectsBadEdits(t *testing.T) {
	existing := uploadedAudioFixture()
	if _, err := assembleUploadedAudioPlan(existing, mustDraft(t, `{"segments":[{"index":99,"text":"out of range"}]}`)); err == nil {
		t.Fatal("out-of-range index must be rejected")
	}
	if _, err := assembleUploadedAudioPlan(existing, mustDraft(t, `{"segments":[{"index":0,"text":"   "}]}`)); err == nil {
		t.Fatal("blank corrected text must be rejected")
	}
	if _, err := assembleUploadedAudioPlan(existing, mustDraft(t, `{"segments":[{"text":"missing index"}]}`)); err == nil {
		t.Fatal("segment edit without an index must be rejected")
	}
	if _, err := assembleUploadedAudioPlan(&config.DebateTopic{Type: config.ContentTypeDiscussion}, mustDraft(t, `{}`)); err == nil {
		t.Fatal("non-uploaded-audio plan must be rejected")
	}
	if _, err := assembleUploadedAudioPlan(existing, mustDraft(t, `{"language":"Chinese (Mandarin)"}`)); err == nil {
		t.Fatal("non-BCP-47 language must be rejected")
	}
	if res, err := assembleUploadedAudioPlan(existing, mustDraft(t, `{"language":"  "}`)); err != nil {
		t.Fatalf("blank language must keep the current one: %v", err)
	} else if res.Script.Language != "en-US" {
		t.Fatalf("language = %q, want existing en-US", res.Script.Language)
	}
}

func TestRenderUploadedAudioTranscript(t *testing.T) {
	listing := renderUploadedAudioTranscript(uploadedAudioFixture())
	if listing == "" {
		t.Fatal("listing must not be empty")
	}
	for _, want := range []string{
		"language: en-US",
		"0 | Speaker 1 | 0:00:00.000-0:00:02.000 | Hello their, welcome.",
		"1 | Speaker 2 | 0:00:02.000-0:00:05.000 | Thanks for having me.",
	} {
		if !strings.Contains(listing, want) {
			t.Fatalf("listing missing %q:\n%s", want, listing)
		}
	}
}

func TestUploadedAudioConversationToolsSkipResearch(t *testing.T) {
	tools := conversationTools(config.ContentTypeUploadedAudio, DefaultTemplateID)
	for _, tool := range tools {
		name := tool.Function.Name
		if name == "search_sources" || name == "crawl_sources" || name == "search_research_papers" {
			t.Fatalf("uploaded-audio conversations must not offer research tool %q", name)
		}
	}
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Function.Name] = true
	}
	for _, want := range []string{"write_plan", "update_plan", "show_plan", "ask_question"} {
		if !names[want] {
			t.Fatalf("missing tool %q", want)
		}
	}
}
