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
		"do not call end_audio_book until you have emitted the final required scene marker",
		"call end_audio_book immediately with no spoken text",
		"After the end_audio_book tool result, stop",
	}
	for _, want := range required {
		if !strings.Contains(system, want) {
			t.Fatalf("audiobook system prompt missing %q:\n%s", want, system)
		}
	}
}

func TestAudioBookLengthContractIncludesConcreteDensity(t *testing.T) {
	contract := audioBookLengthContract(SpeakPrompt{
		SecondsBudget: 600,
		Instructions:  "narrate",
	})
	required := []string{
		"Target duration: at least 10 minute(s)",
		"at least about 2400 CJK characters",
		"target around 3000 CJK characters",
		"Do not collapse chapters into a short summary",
	}
	for _, want := range required {
		if !strings.Contains(contract, want) {
			t.Fatalf("audiobook length contract missing %q:\n%s", want, contract)
		}
	}
}
