package server

import "testing"

func TestSynthesizeIllustrationTimelineUsesTimedRows(t *testing.T) {
	lines := []DiscussionLine{
		{Speaker: "N", Role: "series-host", Text: "opening", ImageURL: "https://img/0.png", StartMS: 0},
		{Speaker: "N", Role: "series-host", Text: "hello there"},
		{Speaker: "N", Role: "series-host", Text: "bar scene", ImageURL: "https://img/1.png", StartMS: 36406},
		{Speaker: "N", Role: "series-host", Text: "red star", ImageURL: "https://img/2.png", StartMS: 78694},
	}
	cues := synthesizeIllustrationTimeline(lines, 0)
	if len(cues) != 3 {
		t.Fatalf("cues = %d, want 3", len(cues))
	}
	// The transcript's first image has no recorded offset (0 = column
	// default) and must be anchored to the start of the timeline.
	if cues[0].StartMS != 0 || cues[0].ImageURL != "https://img/0.png" {
		t.Fatalf("cues[0] = %+v, want opening image at 0", cues[0])
	}
	if cues[1].StartMS != 36406 || cues[2].StartMS != 78694 {
		t.Fatalf("timed cues wrong: %+v", cues[1:])
	}
	if cues[2].Caption != "red star" {
		t.Fatalf("caption = %q", cues[2].Caption)
	}
}

func TestSynthesizeIllustrationTimelineEvenSplitFallback(t *testing.T) {
	lines := []DiscussionLine{
		{ImageURL: "https://img/a.png"},
		{ImageURL: "https://img/b.png"},
		{ImageURL: "https://img/c.png"},
	}
	cues := synthesizeIllustrationTimeline(lines, 90_000)
	if len(cues) != 3 {
		t.Fatalf("cues = %d, want 3", len(cues))
	}
	for i, want := range []int64{0, 30_000, 60_000} {
		if cues[i].StartMS != want {
			t.Fatalf("cues[%d].StartMS = %d, want %d", i, cues[i].StartMS, want)
		}
	}
	// Without a duration only the first image is returned, anchored at 0.
	cues = synthesizeIllustrationTimeline(lines, 0)
	if len(cues) != 1 || cues[0].StartMS != 0 || cues[0].ImageURL != "https://img/a.png" {
		t.Fatalf("no-duration cues = %+v, want single opening cue", cues)
	}
}

func TestSynthesizeIllustrationTimelineDedupsAndSkipsEmpty(t *testing.T) {
	lines := []DiscussionLine{
		{Text: "no image"},
		{ImageURL: "https://img/a.png", StartMS: 5_000},
		{ImageURL: "https://img/a.png", StartMS: 40_000},
		{ImageURL: "https://img/b.png", StartMS: 60_000},
	}
	cues := synthesizeIllustrationTimeline(lines, 0)
	if len(cues) != 2 {
		t.Fatalf("cues = %d, want 2", len(cues))
	}
	if cues[0].StartMS != 5_000 || cues[1].StartMS != 60_000 {
		t.Fatalf("cues = %+v", cues)
	}
	if got := synthesizeIllustrationTimeline(nil, 10_000); got != nil {
		t.Fatalf("empty lines should yield nil, got %+v", got)
	}
}
