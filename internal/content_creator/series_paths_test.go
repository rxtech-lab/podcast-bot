package contentcreator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEpisodeDirAndShowDir(t *testing.T) {
	dir := t.TempDir()
	got := EpisodeDir(dir, "Night Walk", 1, 3)
	want := filepath.Join(dir, "tv-series", "night-walk", "s01", "e03")
	if got != want {
		t.Errorf("EpisodeDir = %q, want %q", got, want)
	}
	show := ShowDir(dir, "Night Walk")
	if !strings.HasSuffix(show, filepath.Join("tv-series", "night-walk")) {
		t.Errorf("ShowDir = %q", show)
	}
}

func TestEnsureEpisodeDirCreatesSubdirs(t *testing.T) {
	root := t.TempDir()
	dir, err := EnsureEpisodeDir(root, "Show A", 2, 5)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	for _, sub := range []string{"scenes", "music", "sounds"} {
		path := filepath.Join(dir, sub)
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("subdir %s: %v", sub, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("subdir %s is not a dir", sub)
		}
	}
}

func TestSiblingEpisodeDirs(t *testing.T) {
	root := t.TempDir()
	for _, e := range [][2]int{{1, 1}, {1, 2}, {1, 3}, {2, 1}} {
		if _, err := EnsureEpisodeDir(root, "Show", e[0], e[1]); err != nil {
			t.Fatal(err)
		}
	}
	priors, err := SiblingEpisodeDirs(root, "Show", 2, 1)
	if err != nil {
		t.Fatalf("siblings: %v", err)
	}
	if len(priors) != 3 {
		t.Errorf("siblings before s2e1 = %d, want 3 (s1e1..s1e3); priors=%v", len(priors), priors)
	}
	// Order should be (1,1), (1,2), (1,3).
	for i, want := range [][2]int{{1, 1}, {1, 2}, {1, 3}} {
		if priors[i].Season != want[0] || priors[i].Episode != want[1] {
			t.Errorf("prior[%d] = (s%d,e%d), want (s%d,e%d)",
				i, priors[i].Season, priors[i].Episode, want[0], want[1])
		}
	}
}

func TestSiblingEpisodeDirsExcludesCurrent(t *testing.T) {
	root := t.TempDir()
	for _, e := range [][2]int{{1, 1}, {1, 2}, {1, 3}} {
		if _, err := EnsureEpisodeDir(root, "S", e[0], e[1]); err != nil {
			t.Fatal(err)
		}
	}
	priors, err := SiblingEpisodeDirs(root, "S", 1, 2)
	if err != nil {
		t.Fatalf("siblings: %v", err)
	}
	if len(priors) != 1 {
		t.Errorf("priors len = %d, want 1 (only s1e1); priors=%v", len(priors), priors)
	}
	if len(priors) > 0 && (priors[0].Season != 1 || priors[0].Episode != 1) {
		t.Errorf("prior = (s%d,e%d), want (s1,e1)", priors[0].Season, priors[0].Episode)
	}
}

func TestSiblingEpisodeDirsNoShow(t *testing.T) {
	root := t.TempDir()
	priors, err := SiblingEpisodeDirs(root, "Nonexistent", 1, 1)
	if err != nil {
		t.Fatalf("missing-show: %v", err)
	}
	if len(priors) != 0 {
		t.Errorf("expected no priors for missing show; got %d", len(priors))
	}
}

func TestImageRefKeyAndMarker(t *testing.T) {
	if got, want := ImageRefKey(2, 5, 7), "s2e5i7"; got != want {
		t.Errorf("ImageRefKey = %q, want %q", got, want)
	}
	if got, want := FormatImageRefMarker(2, 5, 7), "<season-2-episode-5-image-7/>"; got != want {
		t.Errorf("marker = %q, want %q", got, want)
	}
}

func TestSlugifyShow(t *testing.T) {
	cases := map[string]string{
		"Night Walk":  "night-walk",
		"  spaces  ":  "spaces",
		"夜行 — 記":     "show", // CJK + dashes get stripped → fallback
		"Mixed1Case2": "mixed1case2",
		"":            "show",
	}
	for in, want := range cases {
		got := SlugifyShow(in)
		if got != want {
			t.Errorf("SlugifyShow(%q) = %q, want %q", in, got, want)
		}
	}
}
