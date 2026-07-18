package agent

import (
	"strings"
	"testing"
)

func TestNewsClosingPromptDoesNotRecapSingleStory(t *testing.T) {
	w := &NewsScriptWriter{
		anchor:     "Dana",
		headlines:  []string{"Only headline"},
		rundown:    "1. Only headline\n   Summary: Full story detail",
		background: "Shared background detail",
		sourceDocs: "Original source detail",
	}

	prompt := w.segmentPrompt(NewsSegmentRequest{Kind: NewsSegmentClosing, TargetSeconds: 40})
	for _, forbidden := range []string{"Only headline", "Full story detail", "Shared background detail", "Original source detail"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("single-story closing prompt contains %q: %s", forbidden, prompt)
		}
	}
	if !strings.Contains(prompt, "Do NOT recap or repeat") {
		t.Fatalf("single-story closing prompt does not prohibit a recap: %s", prompt)
	}
}

func TestNewsClosingPromptUsesHeadlineNamesOnlyForMultipleStories(t *testing.T) {
	w := &NewsScriptWriter{
		anchor:     "Dana",
		headlines:  []string{"First headline", "Second headline"},
		rundown:    "1. First headline\n   Summary: Full story detail",
		background: "Shared background detail",
		sourceDocs: "Original source detail",
	}

	prompt := w.segmentPrompt(NewsSegmentRequest{Kind: NewsSegmentClosing, TargetSeconds: 40})
	for _, headline := range w.headlines {
		if !strings.Contains(prompt, headline) {
			t.Fatalf("multi-story closing prompt is missing %q: %s", headline, prompt)
		}
	}
	for _, forbidden := range []string{"Full story detail", "Shared background detail", "Original source detail"} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("multi-story closing prompt contains %q: %s", forbidden, prompt)
		}
	}
	if !strings.Contains(prompt, "Do NOT repeat any story details") {
		t.Fatalf("multi-story closing prompt does not prohibit story details: %s", prompt)
	}
}

func TestNewsScriptWriterAddsEveryMissingRequiredCoHost(t *testing.T) {
	w := &NewsScriptWriter{
		anchor: "Dana",
		commentators: []NewsBeat{
			{Name: "Ravi"},
			{Name: "Mia"},
		},
	}
	req := NewsSegmentRequest{
		Kind:          NewsSegmentStory,
		Headline:      "Chips rally",
		Summary:       "Chip shares rose.",
		KeyFacts:      []string{"Index gained four percent.", "Volume doubled."},
		AddOnSpeakers: []string{"Ravi", "Mia"},
	}

	lines := w.ensureRequiredAddOns(req, []NewsScriptLine{{Speaker: "Dana", Text: "The anchor report."}})
	if len(lines) != 3 {
		t.Fatalf("lines = %+v, want anchor plus both required co-hosts", lines)
	}
	if lines[1] != (NewsScriptLine{Speaker: "Ravi", Text: "Index gained four percent."}) {
		t.Fatalf("first fallback add-on = %+v", lines[1])
	}
	if lines[2] != (NewsScriptLine{Speaker: "Mia", Text: "Volume doubled."}) {
		t.Fatalf("second fallback add-on = %+v", lines[2])
	}
}

func TestNewsScriptWriterDoesNotDuplicatePresentRequiredCoHost(t *testing.T) {
	w := &NewsScriptWriter{}
	req := NewsSegmentRequest{
		Kind:          NewsSegmentStory,
		Summary:       "Chip shares rose.",
		AddOnSpeakers: []string{"Ravi"},
	}
	written := []NewsScriptLine{
		{Speaker: "Dana", Text: "The anchor report."},
		{Speaker: "Ravi", Text: "A fresh add-on."},
	}

	lines := w.ensureRequiredAddOns(req, written)
	if len(lines) != len(written) {
		t.Fatalf("lines = %+v, want the existing required co-host line unchanged", lines)
	}
}
