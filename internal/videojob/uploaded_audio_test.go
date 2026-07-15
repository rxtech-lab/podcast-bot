package videojob

import (
	"strings"
	"testing"
	"time"

	"github.com/sirily11/debate-bot/internal/config"
	contentcreator "github.com/sirily11/debate-bot/internal/content_creator"
)

func uploadedSegments() []config.TranscriptSegment {
	return []config.TranscriptSegment{
		{Speaker: "Speaker 1", OffsetMS: 0, DurationMS: 2000, Text: "Hello there,"},
		{Speaker: "Speaker 1", OffsetMS: 2000, DurationMS: 1000, Text: "welcome back."},
		{Speaker: "Speaker 2", OffsetMS: 3000, DurationMS: 2500, Text: "很高兴来到这里，"},
		{Speaker: "Speaker 2", OffsetMS: 5500, DurationMS: 1500, Text: "谢谢邀请。"},
	}
}

func TestUploadedAudioSubtitleCues(t *testing.T) {
	cues := uploadedAudioSubtitleCues(uploadedSegments())
	if len(cues) != 4 {
		t.Fatalf("cues = %d, want 4", len(cues))
	}
	if cues[0].Start != 0 || cues[0].End != 2*time.Second {
		t.Fatalf("cue 0 timing wrong: %+v", cues[0])
	}
	// Punctuation is stripped to match the app's caption style.
	if strings.ContainsAny(cues[0].Text, ",.") || strings.ContainsAny(cues[2].Text, "，。") {
		t.Fatalf("cue text should be punctuation-free: %q / %q", cues[0].Text, cues[2].Text)
	}
	vtt := contentcreator.FormatSubtitleCues(cues)
	if !strings.HasPrefix(vtt, "WEBVTT") || !strings.Contains(vtt, "00:00:03.000 --> 00:00:05.500") {
		t.Fatalf("vtt output wrong:\n%s", vtt)
	}
}

func TestUploadedAudioTranscriptLinesMergeSpeakerTurns(t *testing.T) {
	lines := uploadedAudioTranscriptLines(uploadedSegments())
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2 (one per speaker turn)", len(lines))
	}
	if lines[0].Speaker != "Speaker 1" || lines[0].AudioOffsetMS != 0 {
		t.Fatalf("line 0 wrong: %+v", lines[0])
	}
	if lines[0].Text != "Hello there, welcome back." {
		t.Fatalf("latin merge should be space-joined: %q", lines[0].Text)
	}
	if lines[1].Text != "很高兴来到这里，谢谢邀请。" {
		t.Fatalf("cjk merge should join directly: %q", lines[1].Text)
	}
	if lines[1].AudioOffsetMS != 3000 {
		t.Fatalf("line 1 offset = %d, want 3000", lines[1].AudioOffsetMS)
	}
	if !lines[1].At.After(lines[0].At) {
		t.Fatal("line times must follow playback order")
	}
}
