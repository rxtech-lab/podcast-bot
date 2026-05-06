package video

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestBuildStitchArgs_MultipleSubtitleTracks(t *testing.T) {
	dir := t.TempDir()
	hlsDir := filepath.Join(dir, "hls")
	if err := os.MkdirAll(hlsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	playlist := filepath.Join(hlsDir, "stream.m3u8")
	if err := os.WriteFile(playlist, []byte("#EXTM3U\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := filepath.Join(dir, "subtitles.vtt")
	en := filepath.Join(dir, "subtitles.en.vtt")
	fr := filepath.Join(dir, "subtitles.fr.vtt")
	for _, path := range []string{orig, en, fr} {
		if err := os.WriteFile(path, []byte("WEBVTT\n\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	args, err := buildStitchArgs(hlsDir, filepath.Join(dir, "video.mp4"), StitchOpts{
		SoftSubs: true,
		SubtitleTracks: []SubtitleTrack{
			{Path: orig, Language: "zh-CN", Default: true},
			{Path: en, Language: "en"},
			{Path: fr, Language: "fr"},
		},
	})
	if err != nil {
		t.Fatalf("buildStitchArgs: %v", err)
	}

	for _, want := range [][]string{
		{"-i", playlist},
		{"-i", orig},
		{"-i", en},
		{"-i", fr},
		{"-map", "1:s"},
		{"-map", "2:s"},
		{"-map", "3:s"},
		{"-metadata:s:s:0", "language=zho"},
		{"-metadata:s:s:1", "language=eng"},
		{"-metadata:s:s:2", "language=fra"},
		{"-disposition:s:0", "default"},
		{"-disposition:s:1", "0"},
		{"-disposition:s:2", "0"},
	} {
		if !containsAdjacent(args, want[0], want[1]) {
			t.Fatalf("args missing %v; args=%v", want, args)
		}
	}
}

func TestBuildStitchArgs_LegacySubtitleFields(t *testing.T) {
	dir := t.TempDir()
	hlsDir := filepath.Join(dir, "hls")
	if err := os.MkdirAll(hlsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	playlist := filepath.Join(hlsDir, "stream.m3u8")
	subtitles := filepath.Join(dir, "subtitles.vtt")
	if err := os.WriteFile(playlist, []byte("#EXTM3U\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(subtitles, []byte("WEBVTT\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	args, err := buildStitchArgs(hlsDir, filepath.Join(dir, "video.mp4"), StitchOpts{
		SoftSubs:      true,
		SubtitlesPath: subtitles,
		Language:      "ko",
	})
	if err != nil {
		t.Fatalf("buildStitchArgs: %v", err)
	}
	if !containsAdjacent(args, "-map", "1:s") {
		t.Fatalf("legacy subtitle track not mapped; args=%v", args)
	}
	if !containsAdjacent(args, "-metadata:s:s:0", "language=kor") {
		t.Fatalf("legacy subtitle language missing; args=%v", args)
	}
}

func TestNormalizeSubtitleLang_CommonDropdownLanguages(t *testing.T) {
	cases := map[string][]string{
		"zh-Hans": []string{"zho", "Simplified Chinese"},
		"zh-Hant": []string{"zho", "Traditional Chinese"},
		"en":      []string{"eng", "English"},
		"ja":      []string{"jpn", "Japanese"},
		"ko":      []string{"kor", "Korean"},
		"es":      []string{"spa", "Spanish"},
		"fr":      []string{"fra", "French"},
		"de":      []string{"deu", "German"},
	}
	for raw, want := range cases {
		iso, title := normalizeSubtitleLang(raw)
		got := []string{iso, title}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("normalizeSubtitleLang(%q) = %v, want %v", raw, got, want)
		}
	}
}

func containsAdjacent(args []string, k, v string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == k && args[i+1] == v {
			return true
		}
	}
	return false
}
