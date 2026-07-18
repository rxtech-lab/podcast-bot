package video

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestBuildStitchArgs_DefaultAudioCopyPath(t *testing.T) {
	dir := t.TempDir()
	hlsDir, _ := writeStitchInputs(t, dir)

	args, err := buildStitchArgs(hlsDir, filepath.Join(dir, "video.mp4"), StitchOpts{})
	if err != nil {
		t.Fatalf("buildStitchArgs: %v", err)
	}

	if !containsAdjacent(args, "-c:v", "copy") {
		t.Fatalf("video stream-copy missing; args=%v", args)
	}
	if !containsAdjacent(args, "-c:a", "copy") {
		t.Fatalf("audio stream-copy missing on default path; args=%v", args)
	}
	if contains(args, "-af") {
		t.Fatalf("default path should not apply an audio filter; args=%v", args)
	}
}

func TestBuildStitchArgs_AudioFadeOutReencodesAudio(t *testing.T) {
	dir := t.TempDir()
	hlsDir, _ := writeStitchInputs(t, dir)

	args, err := buildStitchArgs(hlsDir, filepath.Join(dir, "video.mp4"), StitchOpts{
		AudioFadeOut: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("buildStitchArgs: %v", err)
	}

	for _, want := range [][]string{
		{"-c:v", "copy"},
		{"-af", "areverse,afade=t=in:st=0:d=5.000,areverse"},
		{"-c:a", "aac"},
		{"-b:a", "160k"},
		{"-ar", "48000"},
		{"-ac", "2"},
	} {
		if !containsAdjacent(args, want[0], want[1]) {
			t.Fatalf("args missing %v; args=%v", want, args)
		}
	}
	if containsAdjacent(args, "-c:a", "copy") {
		t.Fatalf("fade path should not stream-copy audio; args=%v", args)
	}
}

func TestBuildStitchArgs_AudioFadeOutWithSoftSubs(t *testing.T) {
	dir := t.TempDir()
	hlsDir, playlist := writeStitchInputs(t, dir)
	subtitles := filepath.Join(dir, "subtitles.vtt")
	if err := os.WriteFile(subtitles, []byte("WEBVTT\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	args, err := buildStitchArgs(hlsDir, filepath.Join(dir, "video.mp4"), StitchOpts{
		SoftSubs:     true,
		AudioFadeOut: 5 * time.Second,
		SubtitleTracks: []SubtitleTrack{{
			Path:     subtitles,
			Language: "en",
			Default:  true,
		}},
	})
	if err != nil {
		t.Fatalf("buildStitchArgs: %v", err)
	}

	for _, want := range [][]string{
		{"-i", playlist},
		{"-i", subtitles},
		{"-af", "areverse,afade=t=in:st=0:d=5.000,areverse"},
		{"-c:a", "aac"},
		{"-map", "1:s"},
		{"-c:s", "mov_text"},
		{"-metadata:s:s:0", "language=eng"},
		{"-disposition:s:0", "default"},
	} {
		if !containsAdjacent(args, want[0], want[1]) {
			t.Fatalf("args missing %v; args=%v", want, args)
		}
	}
}

func TestBuildStitchArgs_MultipleSubtitleTracks(t *testing.T) {
	dir := t.TempDir()
	hlsDir, playlist := writeStitchInputs(t, dir)
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
	hlsDir, _ := writeStitchInputs(t, dir)
	subtitles := filepath.Join(dir, "subtitles.vtt")
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

func writeStitchInputs(t *testing.T, dir string) (hlsDir, playlist string) {
	t.Helper()
	hlsDir = filepath.Join(dir, "hls")
	if err := os.MkdirAll(hlsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	playlist = filepath.Join(hlsDir, "stream.m3u8")
	if err := os.WriteFile(playlist, []byte("#EXTM3U\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return hlsDir, playlist
}

func containsAdjacent(args []string, k, v string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == k && args[i+1] == v {
			return true
		}
	}
	return false
}

func contains(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
