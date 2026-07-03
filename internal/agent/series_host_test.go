package agent

import (
	"fmt"
	"strings"
	"testing"
)

func TestAudioBookPromptRequiresImmediateEndToolAtCompletion(t *testing.T) {
	system := fmt.Sprintf(audioBookHostSystemTemplate,
		"Rain Notes",
		"Chapter 1: The path\nChapter 2: The clinic",
		"",
		"",
		"",
		"",
	)

	required := []string{
		"the next action must be end_audio_book",
		"Do not add encouragement, filler, \"next chapter\" teasers",
		"call end_audio_book immediately with no spoken text",
		"After the end_audio_book tool result, stop",
	}
	for _, want := range required {
		if !strings.Contains(system, want) {
			t.Fatalf("audiobook system prompt missing %q:\n%s", want, system)
		}
	}
}
