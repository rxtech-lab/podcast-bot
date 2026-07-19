package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writeAndLoad renders t to markdown, writes it to a temp file, and loads it
// back through the real LoadTopic so the test exercises the exact parse path.
func writeAndLoad(t *testing.T, topic *DebateTopic) *DebateTopic {
	t.Helper()
	md, err := topic.RenderMarkdown()
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	path := filepath.Join(t.TempDir(), "script.md")
	if err := os.WriteFile(path, []byte(md), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	loaded, err := LoadTopic(path)
	if err != nil {
		t.Fatalf("LoadTopic of rendered markdown failed: %v\n---\n%s", err, md)
	}
	return loaded
}

func TestRenderMarkdownRoundTripDiscussion(t *testing.T) {
	in := &DebateTopic{
		Title:             "The Future of Remote Work",
		Type:              ContentTypeDiscussion,
		Language:          "en-US",
		TotalMinutes:      30,
		SegmentMaxSeconds: 60,
		TTSProvider:       TTSProviderAzure,
		Resolution:        Resolution1080p,
		Channel:           "default",
		Host:              AgentSpec{Name: "Mira", Model: "openai/gpt-4o"},
		Discussants: []AgentSpec{
			{Name: "Diego", Model: "openai/gpt-4o", Aspect: "economic"},
			{Name: "Priya", Model: "openai/gpt-4o", Aspect: "cultural"},
		},
		Commander:       AgentSpec{Model: "openai/gpt-4o"},
		Storage:         StoragePlaintext,
		Background:      "Remote work reshaped the economy.\n\nThis panel explores what comes next.",
		SourceDocuments: "--- report.pdf ---\nRemote workers report higher satisfaction.\n\n> ## An embedded heading, sanitized upstream\nMore content after the heading line.",
	}
	got := writeAndLoad(t, in)
	if !reflect.DeepEqual(in, got) {
		t.Fatalf("round-trip mismatch:\nwant %+v\n got %+v", in, got)
	}
}

func TestRenderMarkdownRoundTripDebate(t *testing.T) {
	in := &DebateTopic{
		Title:             "AI Regulation",
		Type:              ContentTypeDebate,
		Language:          "en-US",
		TotalMinutes:      30,
		SegmentMaxSeconds: 60,
		TTSProvider:       TTSProviderAzure,
		Resolution:        Resolution1080p,
		Channel:           "tech",
		Affirmative:       []AgentSpec{{Name: "Ada", Model: "openai/gpt-4o"}},
		Negative:          []AgentSpec{{Name: "Bram", Model: "openai/gpt-4o"}},
		Judge:             AgentSpec{Model: "openai/gpt-4o"},
		Background:        "Should AI be regulated?",
		AffirmativePos:    "Yes, strongly.",
		NegativePos:       "No, it stifles innovation.",
		Rules:             "Be civil.",
	}
	got := writeAndLoad(t, in)
	if !reflect.DeepEqual(in, got) {
		t.Fatalf("round-trip mismatch:\nwant %+v\n got %+v", in, got)
	}
}

func TestRenderMarkdownRoundTripPuzzle(t *testing.T) {
	in := &DebateTopic{
		Title:             "The Locked Room",
		Type:              ContentTypeSituationPuzzle,
		Language:          "zh-CN",
		TotalMinutes:      30,
		SegmentMaxSeconds: 60,
		TTSProvider:       TTSProviderAzure,
		Resolution:        Resolution1080p,
		Channel:           "puzzles",
		PuzzleHost:        AgentSpec{Name: "Host", Model: "openai/gpt-4o"},
		Players:           []AgentSpec{{Name: "P1", Model: "openai/gpt-4o"}},
		Surface:           "A man is found dead in a locked room.",
		Truth:             "He was never inside.",
	}
	got := writeAndLoad(t, in)
	if !reflect.DeepEqual(in, got) {
		t.Fatalf("round-trip mismatch:\nwant %+v\n got %+v", in, got)
	}
}

func TestRenderMarkdownRoundTripSeries(t *testing.T) {
	in := &DebateTopic{
		Title:             "Pilot",
		Type:              ContentTypeSeries,
		Language:          "en-US",
		TotalMinutes:      30,
		SegmentMaxSeconds: 60,
		TTSProvider:       TTSProviderAzure,
		Resolution:        Resolution1080p,
		Channel:           "series",
		Show:              "Mystery Lane",
		Season:            1,
		Episode:           1,
		SeriesHost:        AgentSpec{Name: "Narrator", Model: "openai/gpt-4o"},
		Surface:           "The story begins on a quiet street.",
	}
	got := writeAndLoad(t, in)
	if !reflect.DeepEqual(in, got) {
		t.Fatalf("round-trip mismatch:\nwant %+v\n got %+v", in, got)
	}
}

func TestRenderMarkdownRoundTripAudioBook(t *testing.T) {
	in := &DebateTopic{
		Title:             "A Long Book",
		Type:              ContentTypeAudioBook,
		Language:          "en-US",
		TotalMinutes:      20,
		SegmentMaxSeconds: 60,
		TTSProvider:       TTSProviderAzure,
		Resolution:        Resolution1080p,
		Channel:           "default",
		AudioBookHost:     AgentSpec{Name: "Narrator", Model: "openai/gpt-4o"},
		AudioBookStyle:    AudioBookStylePodcast,
		AudioBookSpeakers: []AudioBookSpeaker{{Name: "Author", Gender: "neutral", Description: "quoted source passages"}},
		AudioBookChapters: []AudioBookChapter{
			{Title: "Origins", Summary: "How the story begins.", StartMarker: "# Origins", ContentKey: "audiobooks/d1/chapters/01-abcd1234.md", ContentChars: 5200},
			{Title: "Consequences", Summary: "What follows from the opening ideas."},
		},
		AudioBookChapterIndices: []int{6, 7},
		Background:              "A concise overall summary.",
		Surface:                 "### Chapter 1: Origins\n\nHow the story begins.\n\n### Chapter 2: Consequences\n\nWhat follows.",
	}
	got := writeAndLoad(t, in)
	if !reflect.DeepEqual(in, got) {
		t.Fatalf("round-trip mismatch:\nwant %+v\n got %+v", in, got)
	}
}

func TestRenderMarkdownRoundTripUploadedAudio(t *testing.T) {
	in := &DebateTopic{
		Title:                    "My Uploaded Show",
		Type:                     ContentTypeUploadedAudio,
		Language:                 "zh-CN",
		TotalMinutes:             12,
		SegmentMaxSeconds:        60,
		TTSProvider:              TTSProviderAzure,
		Resolution:               Resolution1080p,
		Channel:                  "default",
		UploadedAudioKey:         "uploads/user-1/abc123.mp3",
		UploadedAudioDurationMS:  695000,
		UploadedAudioMaxSpeakers: 3,
		UploadedAudioSpeakers:    []string{"Speaker 1", "Speaker 2", "Guest"},
		TranscriptSegments: []TranscriptSegment{
			{Speaker: "Speaker 1", OffsetMS: 10080, DurationMS: 24920, Text: "各位听众朋友们，"},
			{Speaker: "Speaker 2", OffsetMS: 35000, DurationMS: 4000, Text: "Welcome to the show."},
		},
	}
	got := writeAndLoad(t, in)
	if !reflect.DeepEqual(in, got) {
		t.Fatalf("round-trip mismatch:\nwant %+v\n got %+v", in, got)
	}
}
