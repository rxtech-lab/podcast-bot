package server

import (
	"strings"
	"testing"
)

func TestStationCoverGenerationPromptForcesImageOutput(t *testing.T) {
	got := stationCoverGenerationPrompt("灵感即代码：Vibe Coding 时代的 IDE 演进与开发者转型")
	for _, want := range []string{
		"Create a square podcast cover image",
		"simple, flat podcast cover artwork",
		"minimal visual elements",
		"little to no shadows",
		"Avoid busy scenes",
		"Do not write an essay",
		"Return only the generated cover image",
		"灵感即代码",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt missing %q: %s", want, got)
		}
	}
}
