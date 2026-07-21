package planner

import (
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
)

func TestAssembleAudioBookNormalizesGenders(t *testing.T) {
	d := &audioBookDraft{
		Title:          "The Test Book",
		Style:          "audiobook",
		OverallSummary: "A summary.",
	}
	d.Narrator.Name = "Evelyn"
	d.Narrator.Gender = " Female "
	d.Speakers = []struct {
		Name        string `json:"name"`
		Gender      string `json:"gender"`
		Description string `json:"description"`
	}{
		{Name: "Captain Reyes", Gender: " Male ", Description: "gruff sea captain"},
		{Name: "The Oracle", Gender: "unknown", Description: "mysterious voice"},
		{Name: "Mira", Gender: "WOMAN", Description: "young apprentice"},
	}
	d.Chapters = []audioBookDraftChapter{
		{Title: "One", Summary: "The beginning.", Mode: "narration"},
	}

	p := &Planner{env: &config.Env{}}
	res, err := p.assembleAudioBookWithModel(d, "en-US", "", nil, "test-model")
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	topic := res.Script
	if got := topic.AudioBookHost.Gender; got != "female" {
		t.Fatalf("narrator gender = %q, want female", got)
	}
	wantGenders := map[string]string{
		"Captain Reyes": "male",
		"The Oracle":    "",
		"Mira":          "female",
	}
	if len(topic.AudioBookSpeakers) != len(wantGenders) {
		t.Fatalf("speakers = %d, want %d", len(topic.AudioBookSpeakers), len(wantGenders))
	}
	for _, s := range topic.AudioBookSpeakers {
		want, ok := wantGenders[s.Name]
		if !ok {
			t.Fatalf("unexpected speaker %q", s.Name)
		}
		if s.Gender != want {
			t.Fatalf("speaker %q gender = %q, want %q", s.Name, s.Gender, want)
		}
	}
}

func TestAssembleAudioBookSingleChapterUsesChapterTitle(t *testing.T) {
	d := &audioBookDraft{
		Title:          "The Whole Book",
		Style:          "audiobook",
		OverallSummary: "A summary.",
	}
	d.Narrator.Name = "Narrator"
	d.Narrator.Gender = "female"
	d.Chapters = []audioBookDraftChapter{
		{Title: "The First Door", Summary: "The opening chapter.", Mode: "narration"},
	}

	p := &Planner{env: &config.Env{}}
	res, err := p.assembleAudioBookWithModel(d, "en-US", "", nil, "test-model")
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if got := res.Script.Title; got != "The First Door" {
		t.Fatalf("single-chapter audiobook title = %q, want The First Door", got)
	}
}
