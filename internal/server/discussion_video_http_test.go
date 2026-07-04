package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
	anims, offsets := audioBookVideoTimings(dir, 3)
	if len(anims) != 0 || len(offsets) != 0 {
		t.Errorf("expected empty metadata without sidecars, got %v %v", anims, offsets)
	}

	// plan.json alone supplies animations only.
	plan := map[string]any{"narration_animations": []string{"zoomin", "panleft", "stall"}}
	writeSidecarJSON(t, filepath.Join(scenes, "plan.json"), plan)
	anims, offsets = audioBookVideoTimings(dir, 3)
	if len(anims) != 3 || anims[0] != "zoomin" || len(offsets) != 0 {
		t.Errorf("plan.json animations not loaded: %v %v", anims, offsets)
	}

	// timings.json wins and supplies offsets when the count matches.
	timings := map[string]any{
		"animations":    []string{"panright", "zoomout", "stall"},
		"image_offsets": []float64{0, 12.5, 40},
	}
	writeSidecarJSON(t, filepath.Join(scenes, "timings.json"), timings)
	anims, offsets = audioBookVideoTimings(dir, 3)
	if len(anims) != 3 || anims[0] != "panright" {
		t.Errorf("timings.json animations not preferred: %v", anims)
	}
	if len(offsets) != 3 || offsets[1] != 12.5 {
		t.Errorf("timings.json offsets not loaded: %v", offsets)
	}

	// Offset count mismatch with the image list → offsets dropped.
	_, offsets = audioBookVideoTimings(dir, 5)
	if len(offsets) != 0 {
		t.Errorf("mismatched offset count should be dropped, got %v", offsets)
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
