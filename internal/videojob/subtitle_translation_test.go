package videojob

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contentcreator "github.com/sirily11/debate-bot/internal/content_creator"
)

type fakeSubtitleJSONClient struct {
	raw []byte
	err error
}

func (f fakeSubtitleJSONClient) JSON(context.Context, string, string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.raw, nil
}

func TestTranslateSubtitleCuesValidatesCount(t *testing.T) {
	source := []contentcreator.SubtitleCue{
		{Start: 0, End: time.Second, Text: "hello"},
		{Start: time.Second, End: 2 * time.Second, Text: "world"},
	}
	_, err := translateSubtitleCues(context.Background(),
		fakeSubtitleJSONClient{raw: []byte(`{"translations":["hola"]}`)},
		source, subtitleLanguage{Code: "es", Name: "Spanish"})
	if err == nil || !strings.Contains(err.Error(), "got 1 cues, want 2") {
		t.Fatalf("expected cue count error, got %v", err)
	}
}

func TestTranslateSubtitleCuesRejectsBadJSON(t *testing.T) {
	source := []contentcreator.SubtitleCue{{Start: 0, End: time.Second, Text: "hello"}}
	_, err := translateSubtitleCues(context.Background(),
		fakeSubtitleJSONClient{raw: []byte(`not json`)},
		source, subtitleLanguage{Code: "fr", Name: "French"})
	if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("expected invalid JSON error, got %v", err)
	}
}

func TestSubtitleTracksForJobWritesDistinctLanguageFiles(t *testing.T) {
	source := []contentcreator.SubtitleCue{
		{Start: 0, End: time.Second, Text: "hello"},
	}
	dir := t.TempDir()
	client := fakeSubtitleJSONClient{raw: []byte(`{"translations":["hola"]}`)}
	tracks, err := subtitleTracksForJob(context.Background(), client, dir,
		"zh-CN", source, []string{"es", "es", "zh-CN"})
	if err != nil {
		t.Fatalf("subtitleTracksForJob: %v", err)
	}
	if len(tracks) != 1 {
		t.Fatalf("track count = %d, want 1", len(tracks))
	}
	wantPath := filepath.Join(dir, "subtitles.es.vtt")
	if tracks[0].Path != wantPath {
		t.Fatalf("track path = %q, want %q", tracks[0].Path, wantPath)
	}
	data, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read translated vtt: %v", err)
	}
	if !strings.Contains(string(data), "hola") {
		t.Fatalf("translated VTT missing text; body=%q", string(data))
	}
}

func TestSubtitleTracksForJobPropagatesTranslationErrors(t *testing.T) {
	source := []contentcreator.SubtitleCue{{Start: 0, End: time.Second, Text: "hello"}}
	_, err := subtitleTracksForJob(context.Background(),
		fakeSubtitleJSONClient{err: errors.New("network down")},
		t.TempDir(), "zh-CN", source, []string{"en"})
	if err == nil || !strings.Contains(err.Error(), "network down") {
		t.Fatalf("expected translation error, got %v", err)
	}
}

func TestNormalizeRequestedSubtitleLanguages(t *testing.T) {
	langs, err := normalizeRequestedSubtitleLanguages("en-US",
		[]string{"zh-Hans", "zh-Hant", "eng", "fr", "fra", "de"})
	if err != nil {
		t.Fatalf("normalizeRequestedSubtitleLanguages: %v", err)
	}
	var got []string
	for _, lang := range langs {
		got = append(got, lang.Code)
	}
	want := strings.Join([]string{"zh-Hans", "zh-Hant", "fr", "de"}, ",")
	if strings.Join(got, ",") != want {
		t.Fatalf("languages = %v, want %s", got, want)
	}

	langs, err = normalizeRequestedSubtitleLanguages("zh-CN",
		[]string{"zh-Hans", "zh-Hant"})
	if err != nil {
		t.Fatalf("normalizeRequestedSubtitleLanguages zh variants: %v", err)
	}
	if len(langs) != 1 || langs[0].Code != "zh-Hant" {
		t.Fatalf("zh-CN should skip Simplified and keep Traditional; got %#v", langs)
	}

	if _, err := normalizeRequestedSubtitleLanguages("en", []string{"it"}); err == nil {
		t.Fatal("expected unsupported language error")
	}
}
