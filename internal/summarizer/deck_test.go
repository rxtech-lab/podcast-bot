package summarizer

import (
	"strings"
	"testing"
)

func TestParseDeckSpecFromFencedJSON(t *testing.T) {
	raw := "```json\n" + `{
		"title": "",
		"subtitle": "A short deck",
		"slides": [
			{"title": "Overview", "kicker": " Context ", "summary": "` + strings.Repeat("s", 260) + `", "bullets": [" First point ", "", "Second point"], "takeaway": "` + strings.Repeat("t", 220) + `", "speakerOpinions": [{"speaker": " Alice ", "opinion": "` + strings.Repeat("o", 180) + `", "evidence": "` + strings.Repeat("e", 150) + `"}, {"speaker": "", "opinion": "missing speaker"}], "visual": {"kind": " compare ", "title": "` + strings.Repeat("v", 120) + `", "data": [" First ", "", "` + strings.Repeat("d", 90) + `", "Third", "Fourth", "Fifth"]}},
			{"title": "Trim", "bullets": ["` + strings.Repeat("x", 220) + `"], "notes": "` + strings.Repeat("n", 360) + `"}
		]
	}` + "\n```"

	spec, err := parseDeckSpec(raw)
	if err != nil {
		t.Fatalf("parseDeckSpec returned error: %v", err)
	}
	spec.normalize("Fallback Title")
	if err := spec.validate(); err != nil {
		t.Fatalf("normalized deck did not validate: %v", err)
	}
	if spec.Title != "Fallback Title" {
		t.Fatalf("title = %q, want fallback", spec.Title)
	}
	if got := len(spec.Slides[0].Bullets); got != 2 {
		t.Fatalf("first slide bullets = %d, want 2", got)
	}
	if got := len(spec.Slides[0].Summary); got > 220 {
		t.Fatalf("long summary length = %d, want <= 220", got)
	}
	if got := len(spec.Slides[0].Takeaway); got > 180 {
		t.Fatalf("long takeaway length = %d, want <= 180", got)
	}
	if got := len(spec.Slides[0].SpeakerOpinions); got != 1 {
		t.Fatalf("speaker opinions = %d, want 1", got)
	}
	if got := len(spec.Slides[0].SpeakerOpinions[0].Opinion); got > 140 {
		t.Fatalf("long opinion length = %d, want <= 140", got)
	}
	if got := len(spec.Slides[0].SpeakerOpinions[0].Evidence); got > 110 {
		t.Fatalf("long evidence length = %d, want <= 110", got)
	}
	if got := len(spec.Slides[0].Visual.Title); got > 90 {
		t.Fatalf("long visual title length = %d, want <= 90", got)
	}
	if got := len(spec.Slides[0].Visual.Data); got != 4 {
		t.Fatalf("visual data = %d, want 4", got)
	}
	if got := len(spec.Slides[0].Visual.Data[1]); got > 64 {
		t.Fatalf("long visual data length = %d, want <= 64", got)
	}
	if got := len(spec.Slides[1].Bullets[0]); got > 170 {
		t.Fatalf("long bullet length = %d, want <= 170", got)
	}
	if got := len(spec.Slides[1].Notes); got > 320 {
		t.Fatalf("long notes length = %d, want <= 320", got)
	}
}

func TestDeckSpecValidateRejectsEmptySlides(t *testing.T) {
	spec := &DeckSpec{Title: "No Slides"}
	if err := spec.validate(); err == nil {
		t.Fatal("validate succeeded, want error")
	}
}
