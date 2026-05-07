package videojob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contentcreator "github.com/sirily11/debate-bot/internal/content_creator"
)

type fakeSubtitleJSONClient struct {
	raws  [][]byte
	raw   []byte
	errs  []error
	err   error
	calls int
}

func (f *fakeSubtitleJSONClient) JSON(context.Context, string, string) ([]byte, error) {
	call := f.calls
	f.calls++
	if call < len(f.errs) && f.errs[call] != nil {
		return nil, f.errs[call]
	}
	if call < len(f.raws) {
		return f.raws[call], nil
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.raw, nil
}

func TestTranslateSubtitleCuesTranslatesInChunks(t *testing.T) {
	source := make([]contentcreator.SubtitleCue, subtitleTranslationChunkSize+1)
	for i := range source {
		source[i] = contentcreator.SubtitleCue{
			Start: time.Duration(i) * time.Second,
			End:   time.Duration(i+1) * time.Second,
			Text:  fmt.Sprintf("cue %d", i+1),
		}
	}
	client := &fakeSubtitleJSONClient{raws: [][]byte{
		subtitleTranslationJSON(t, subtitleTranslationChunkSize, "chunk-a"),
		subtitleTranslationJSON(t, 1, "chunk-b"),
	}}

	cues, err := translateSubtitleCues(context.Background(),
		client, source, subtitleLanguage{Code: "en", Name: "English"})
	if err != nil {
		t.Fatalf("translateSubtitleCues: %v", err)
	}
	if client.calls != 2 {
		t.Fatalf("calls = %d, want 2", client.calls)
	}
	if got, want := len(cues), subtitleTranslationChunkSize+1; got != want {
		t.Fatalf("translated cues = %d, want %d", got, want)
	}
	if got, want := cues[subtitleTranslationChunkSize].Text, "chunk b 1"; got != want {
		t.Fatalf("last translated cue = %q, want %q", got, want)
	}
}

func TestTranslateSubtitleCuesValidatesCount(t *testing.T) {
	source := []contentcreator.SubtitleCue{
		{Start: 0, End: time.Second, Text: "hello"},
		{Start: time.Second, End: 2 * time.Second, Text: "world"},
	}
	restore := stubSubtitleTranslationBackoff()
	defer restore()

	_, err := translateSubtitleCues(context.Background(),
		&fakeSubtitleJSONClient{raw: []byte(`{"translations":["hola"]}`)},
		source, subtitleLanguage{Code: "es", Name: "Spanish"})
	if err == nil || !strings.Contains(err.Error(), "got 1 cues, want 2") {
		t.Fatalf("expected cue count error, got %v", err)
	}
}

func TestTranslateSubtitleCuesRetriesValidationErrors(t *testing.T) {
	source := []contentcreator.SubtitleCue{
		{Start: 0, End: time.Second, Text: "hello"},
		{Start: time.Second, End: 2 * time.Second, Text: "world"},
	}
	client := &fakeSubtitleJSONClient{raws: [][]byte{
		[]byte(`{"translations":["hola"]}`),
		[]byte(`{"translations":["hola","mundo"]}`),
	}}
	restore := stubSubtitleTranslationBackoff()
	defer restore()

	cues, err := translateSubtitleCues(context.Background(),
		client, source, subtitleLanguage{Code: "es", Name: "Spanish"})
	if err != nil {
		t.Fatalf("translateSubtitleCues: %v", err)
	}
	if client.calls != 2 {
		t.Fatalf("calls = %d, want 2", client.calls)
	}
	if got, want := cues[1].Text, "mundo"; got != want {
		t.Fatalf("translated text = %q, want %q", got, want)
	}
}

func TestTranslateSubtitleCuesRejectsBadJSON(t *testing.T) {
	source := []contentcreator.SubtitleCue{{Start: 0, End: time.Second, Text: "hello"}}
	restore := stubSubtitleTranslationBackoff()
	defer restore()

	_, err := translateSubtitleCues(context.Background(),
		&fakeSubtitleJSONClient{raw: []byte(`not json`)},
		source, subtitleLanguage{Code: "fr", Name: "French"})
	if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("expected invalid JSON error, got %v", err)
	}
}

func TestTranslateSubtitleCuesStripsPunctuation(t *testing.T) {
	source := []contentcreator.SubtitleCue{{Start: 0, End: time.Second, Text: "hello"}}
	cues, err := translateSubtitleCues(context.Background(),
		&fakeSubtitleJSONClient{raw: []byte(`{"translations":["Hello, world!"]}`)},
		source, subtitleLanguage{Code: "en", Name: "English"})
	if err != nil {
		t.Fatalf("translateSubtitleCues: %v", err)
	}
	if got, want := cues[0].Text, "Hello world"; got != want {
		t.Fatalf("translated text = %q, want %q", got, want)
	}
}

func TestSubtitleTracksForJobWritesDistinctLanguageFiles(t *testing.T) {
	source := []contentcreator.SubtitleCue{
		{Start: 0, End: time.Second, Text: "hello"},
	}
	dir := t.TempDir()
	client := &fakeSubtitleJSONClient{raw: []byte(`{"translations":["hola"]}`)}
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
	restore := stubSubtitleTranslationBackoff()
	defer restore()

	_, err := subtitleTracksForJob(context.Background(),
		&fakeSubtitleJSONClient{err: errors.New("network down")},
		t.TempDir(), "zh-CN", source, []string{"en"})
	if err == nil || !strings.Contains(err.Error(), "network down") {
		t.Fatalf("expected translation error, got %v", err)
	}
}

func stubSubtitleTranslationBackoff() func() {
	old := subtitleTranslationRetryBackoff
	subtitleTranslationRetryBackoff = func(int) time.Duration { return time.Millisecond }
	return func() { subtitleTranslationRetryBackoff = old }
}

func subtitleTranslationJSON(t *testing.T, n int, prefix string) []byte {
	t.Helper()
	translations := make([]string, n)
	for i := range translations {
		translations[i] = fmt.Sprintf("%s %d", prefix, i+1)
	}
	data, err := json.Marshal(subtitleTranslationResponse{Translations: translations})
	if err != nil {
		t.Fatal(err)
	}
	return data
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
