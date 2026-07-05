package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
)

func TestAudioBookIllustrationPathsNumericSort(t *testing.T) {
	dir := t.TempDir()
	scenes := filepath.Join(dir, "audiobook", "scenes")
	if err := os.MkdirAll(scenes, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create out of order, including double-digit beats that break a plain
	// string sort (v10 < v2 lexicographically).
	for _, n := range []string{"narration-v10.png", "narration-v2.png", "narration-v0.png", "narration-v1.png"} {
		if err := os.WriteFile(filepath.Join(scenes, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := audioBookIllustrationPaths(dir)
	want := []string{"narration-v0.png", "narration-v1.png", "narration-v2.png", "narration-v10.png"}
	if len(got) != len(want) {
		t.Fatalf("got %d paths, want %d", len(got), len(want))
	}
	for i := range want {
		if filepath.Base(got[i]) != want[i] {
			t.Fatalf("order mismatch at %d: got %s, want %s (all: %v)", i, filepath.Base(got[i]), want[i], got)
		}
	}
}

func TestAudioBookVideoTimings(t *testing.T) {
	dir := t.TempDir()
	scenes := filepath.Join(dir, "audiobook", "scenes")
	if err := os.MkdirAll(scenes, 0o755); err != nil {
		t.Fatal(err)
	}

	// No sidecars → empty metadata (legacy compat path).
	anims, offsets, beats := audioBookVideoTimings(dir, 3)
	if len(anims) != 0 || len(offsets) != 0 || len(beats) != 0 {
		t.Errorf("expected empty metadata without sidecars, got %v %v %v", anims, offsets, beats)
	}

	// plan.json alone supplies animations only.
	plan := map[string]any{"narration_animations": []string{"zoomin", "panleft", "stall"}}
	writeSidecarJSON(t, filepath.Join(scenes, "plan.json"), plan)
	anims, offsets, beats = audioBookVideoTimings(dir, 3)
	if len(anims) != 3 || anims[0] != "zoomin" || len(offsets) != 0 || len(beats) != 0 {
		t.Errorf("plan.json animations not loaded: %v %v %v", anims, offsets, beats)
	}

	// timings.json wins and supplies offsets when the count matches.
	timings := map[string]any{
		"animations":    []string{"panright", "zoomout", "stall"},
		"image_offsets": []float64{0, 12.5, 40},
	}
	writeSidecarJSON(t, filepath.Join(scenes, "timings.json"), timings)
	anims, offsets, beats = audioBookVideoTimings(dir, 3)
	if len(anims) != 3 || anims[0] != "panright" {
		t.Errorf("timings.json animations not preferred: %v", anims)
	}
	if len(offsets) != 3 || offsets[1] != 12.5 {
		t.Errorf("timings.json offsets not loaded: %v", offsets)
	}
	if len(beats) != 0 {
		t.Errorf("legacy sidecar should have no beats, got %v", beats)
	}

	// Offset count mismatch with the image list → offsets dropped.
	_, offsets, _ = audioBookVideoTimings(dir, 5)
	if len(offsets) != 0 {
		t.Errorf("mismatched offset count should be dropped, got %v", offsets)
	}

	// A beats field pins offsets to specific beats regardless of how many
	// PNGs the glob finds (a filtered snapshot from a chapter-limited run).
	timings = map[string]any{
		"animations":    []string{"panright", "zoomout"},
		"image_offsets": []float64{0, 30.5},
		"beats":         []int{0, 1},
	}
	writeSidecarJSON(t, filepath.Join(scenes, "timings.json"), timings)
	anims, offsets, beats = audioBookVideoTimings(dir, 5)
	if len(beats) != 2 || beats[1] != 1 {
		t.Errorf("beats not loaded: %v", beats)
	}
	if len(offsets) != 2 || offsets[1] != 30.5 || len(anims) != 2 {
		t.Errorf("beat-scoped timings not loaded: %v %v", anims, offsets)
	}
}

func TestApplyAudioBookTimingBeats(t *testing.T) {
	paths := []string{
		"/x/narration-v0.png", "/x/narration-v1.png",
		"/x/narration-v2.png", "/x/narration-v3.png",
	}
	anims := []string{"stall", "zoomin"}
	offsets := []float64{0, 41.5}

	// nil beats (legacy) → unchanged.
	p, a, o := applyAudioBookTimingBeats(paths, anims, offsets, nil)
	if len(p) != 4 || len(a) != 2 || len(o) != 2 {
		t.Fatalf("legacy passthrough mangled inputs: %v %v %v", p, a, o)
	}

	// beats narrow the glob to the narrated images, timings stay parallel.
	p, a, o = applyAudioBookTimingBeats(paths, anims, offsets, []int{0, 2})
	if len(p) != 2 || p[1] != "/x/narration-v2.png" {
		t.Fatalf("paths not narrowed to beats: %v", p)
	}
	if len(a) != 2 || a[1] != "zoomin" || len(o) != 2 || o[1] != 41.5 {
		t.Fatalf("timings desynced from narrowed paths: %v %v", a, o)
	}

	// A beat whose PNG vanished drops its timing entries too.
	p, _, o = applyAudioBookTimingBeats(paths[:1], anims, offsets, []int{0, 2})
	if len(p) != 1 || len(o) != 1 || o[0] != 0 {
		t.Fatalf("missing PNG should drop its beat: %v %v", p, o)
	}

	// beats without offsets (even-split snapshot) → offsets stay nil.
	p, _, o = applyAudioBookTimingBeats(paths, anims, nil, []int{0, 2})
	if len(p) != 2 || o != nil {
		t.Fatalf("even-split snapshot grew offsets: %v %v", p, o)
	}
}

func TestAudioBookOffsetsFromIllustrations(t *testing.T) {
	dir := t.TempDir()
	// Missing sidecar → nils.
	if offsets, beats := audioBookOffsetsFromIllustrations(dir); offsets != nil || beats != nil {
		t.Fatalf("missing sidecar should return nils, got %v %v", offsets, beats)
	}

	audioDir := filepath.Join(dir, PodcastAudioDir)
	if err := os.MkdirAll(audioDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A chapter-limited run: beats 0, 1 and 3 fired (image-K.webp is beat K-1).
	sidecar := map[string]any{"illustrations": []map[string]any{
		{"start_ms": 0, "image_key": "podcasts/audiobooks/x/image-1.webp"},
		{"start_ms": 36718, "image_key": "podcasts/audiobooks/x/image-2.webp"},
		{"start_ms": 81200, "image_url": "https://cdn.example.com/x/image-4.webp"},
		{"start_ms": 90000, "image_key": "not-a-beat.webp"},
	}}
	writeSidecarJSON(t, filepath.Join(audioDir, PodcastIllustrationsFilename), sidecar)
	offsets, beats := audioBookOffsetsFromIllustrations(dir)
	if len(beats) != 3 || beats[0] != 0 || beats[1] != 1 || beats[2] != 3 {
		t.Fatalf("beats = %v, want [0 1 3]", beats)
	}
	if len(offsets) != 3 || offsets[1] != 36.718 || offsets[2] != 81.2 {
		t.Fatalf("offsets = %v", offsets)
	}
}

func TestDiscussionAudioBookVideoOptionsCarryPodcastLanguage(t *testing.T) {
	opts := discussionAudioBookVideoOptions(&config.DebateTopic{
		Title:    "History",
		Language: "zh-CN",
	}, nil, nil)
	if opts.Language != "zh-CN" {
		t.Fatalf("Language = %q, want zh-CN", opts.Language)
	}
}

func writeSidecarJSON(t *testing.T, path string, v any) {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}
