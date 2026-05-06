package contentcreator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writePriorEpisode is a tiny helper that lays down the minimal archive
// files BuildRecap / LoadPriorEpisodes expect to find.
func writePriorEpisode(t *testing.T, root, show string, season, episode int, narration []string, scriptBody string) {
	t.Helper()
	dir, err := EnsureEpisodeDir(root, show, season, episode)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	plan := PriorScenePlan{Narration: narration}
	data, _ := json.MarshalIndent(plan, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "scene-plan.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "script.txt"), []byte(scriptBody), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadPriorEpisodes(t *testing.T) {
	root := t.TempDir()
	writePriorEpisode(t, root, "Show", 1, 1, []string{"beat A", "beat B"}, "ep1 script body")
	writePriorEpisode(t, root, "Show", 1, 2, []string{"beat C"}, "ep2 script body")
	priors, err := LoadPriorEpisodes(root, "Show", 1, 3)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(priors) != 2 {
		t.Fatalf("priors len = %d, want 2", len(priors))
	}
	if priors[0].Plan == nil || len(priors[0].Plan.Narration) != 2 {
		t.Errorf("ep1 plan not loaded")
	}
	if priors[0].Script == "" {
		t.Errorf("ep1 script not loaded")
	}
}

func TestBuildRecap_NilLLMReturnsEmpty(t *testing.T) {
	priors := []PriorEpisodeContent{
		{Season: 1, Episode: 1, Plan: &PriorScenePlan{Narration: []string{"x"}}, Script: "s"},
	}
	r, hl, err := BuildRecap(context.Background(), nil, priors, "Show")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if r != "" || len(hl) != 0 {
		t.Errorf("expected empty recap with nil LLM; got %q, %v", r, hl)
	}
}

func TestBuildRecap_NoPriorsReturnsEmpty(t *testing.T) {
	r, hl, err := BuildRecap(context.Background(), nil, nil, "Show")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if r != "" || len(hl) != 0 {
		t.Errorf("expected empty recap with no priors; got %q, %v", r, hl)
	}
}

func TestBuildImageRefCatalog(t *testing.T) {
	priors := []PriorEpisodeContent{
		{Season: 1, Episode: 1, Plan: &PriorScenePlan{Narration: []string{"alley", "diner"}}},
		{Season: 1, Episode: 2, Plan: &PriorScenePlan{Narration: []string{"river"}}},
	}
	// Stub frame lookup that always finds the file. We pass dir+beat
	// through to the synthesised path so we can verify mapping.
	lookup := func(dir string, beat int) string {
		return filepath.Join(dir, "scenes", "x.png")
	}
	cat, paths := BuildImageRefCatalog(priors, lookup)
	if len(cat) != 3 {
		t.Errorf("catalog len = %d, want 3", len(cat))
	}
	if got, want := paths[ImageRefKey(1, 1, 0)], filepath.Join("", "scenes", "x.png"); got == "" {
		t.Errorf("path missing for s1e1i0; got %q (want non-empty path); want suffix %q", got, want)
	}
	if paths[ImageRefKey(1, 2, 0)] == "" {
		t.Errorf("path missing for s1e2i0")
	}
	// Test descriptions flow through.
	for _, e := range cat {
		if e.Season == 1 && e.Episode == 1 && e.Beat == 0 && e.Description != "alley" {
			t.Errorf("description for s1e1i0 = %q, want %q", e.Description, "alley")
		}
	}
}

func TestBuildImageRefCatalog_SkipsMissingFrames(t *testing.T) {
	priors := []PriorEpisodeContent{
		{Season: 1, Episode: 1, Plan: &PriorScenePlan{Narration: []string{"a", "b"}}},
	}
	// Lookup returns "" for beat 1 — simulating a missing PNG.
	lookup := func(dir string, beat int) string {
		if beat == 0 {
			return filepath.Join(dir, "scenes", "x.png")
		}
		return ""
	}
	cat, paths := BuildImageRefCatalog(priors, lookup)
	if len(cat) != 1 {
		t.Errorf("catalog len = %d, want 1 (missing beat skipped)", len(cat))
	}
	if _, ok := paths[ImageRefKey(1, 1, 1)]; ok {
		t.Errorf("missing-beat key should not be in paths")
	}
}
