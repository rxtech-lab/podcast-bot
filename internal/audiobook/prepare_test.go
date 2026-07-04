package audiobook

import (
	"strings"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
)

func TestBuildAudioBookCueSpecsAddsReplacementOptionsAfterOpening(t *testing.T) {
	topic := &config.DebateTopic{
		AudioBookChapters: []config.AudioBookChapter{
			{Title: "Opening", Summary: "Set the scene."},
			{Title: "The Turn", Summary: "The mood changes.", Mode: config.AudioBookModeDialogue, Speakers: []string{"Guest"}},
			{Title: "Resolution", Summary: "The story settles."},
		},
	}

	specs := buildAudioBookCueSpecs(topic)
	if got, want := len(specs), 5; got != want {
		t.Fatalf("cue spec count = %d, want %d", got, want)
	}

	if specs[0].mode != "overlap" || specs[0].anchor != "Opening" || specs[0].durationSeconds != stingerSeconds {
		t.Fatalf("opening cue = %+v, want overlap stinger anchored to Opening", specs[0])
	}
	if strings.Contains(specs[0].cacheLabel, "replace") {
		t.Fatalf("opening cue cache label = %q, should not be replacement", specs[0].cacheLabel)
	}

	assertPair := func(t *testing.T, overlap, replace audioBookCueSpec, title string) {
		t.Helper()
		if overlap.mode != "overlap" || replace.mode != "replace" {
			t.Fatalf("%s modes = %q/%q, want overlap/replace", title, overlap.mode, replace.mode)
		}
		if overlap.anchor != title || replace.anchor != title {
			t.Fatalf("%s anchors = %q/%q, want both %q", title, overlap.anchor, replace.anchor, title)
		}
		if overlap.durationSeconds != stingerSeconds {
			t.Fatalf("%s overlap duration = %d, want %d", title, overlap.durationSeconds, stingerSeconds)
		}
		if replace.durationSeconds != replacementCueSeconds {
			t.Fatalf("%s replace duration = %d, want %d", title, replace.durationSeconds, replacementCueSeconds)
		}
		if !strings.Contains(replace.prompt, "Sustained instrumental background bed") {
			t.Fatalf("%s replace prompt = %q, want sustained bed prompt", title, replace.prompt)
		}
	}

	assertPair(t, specs[1], specs[2], "The Turn")
	assertPair(t, specs[3], specs[4], "Resolution")
}

func TestAudioBookIllustrationPromptKeepsAnimatedFilmContinuity(t *testing.T) {
	topic := &config.DebateTopic{
		Title:         "The Memory Palace",
		AudioBookHost: config.AgentSpec{Name: "Mina"},
		AudioBookSpeakers: []config.AudioBookSpeaker{
			{Name: "Jordan", Gender: "neutral", Description: "a curious guest speaker"},
		},
	}
	ch := config.AudioBookChapter{
		Title:    "Opening the Archive",
		Summary:  "Mina and Jordan enter a quiet archive and discover the central question.",
		Mode:     config.AudioBookModeDialogue,
		Speakers: []string{"Jordan"},
	}

	visualGuide := audioBookIllustrationVisualGuide(topic)
	prompt := audioBookIllustrationPrompt(topic,
		"Mina and Jordan step into the dim archive as dust drifts through a shaft of light.",
		ch, 0, 3, visualGuide)

	for _, want := range []string{
		"animated-film illustration",
		"same animated feature film",
		"Keep the main character's face, hair, wardrobe, silhouette, proportions, and color palette exactly the same",
		"Main character: Mina",
		"Recurring speaker: Jordan",
		"Featured voices: Jordan",
		"no photorealism",
		"Scene direction: Mina and Jordan step into the dim archive",
		"No text of any kind",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("illustration prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "unique composition, subject, setting, and color accents") {
		t.Fatalf("illustration prompt kept old anti-continuity wording:\n%s", prompt)
	}
	if strings.Contains(prompt, "Leave the lower third calm enough for subtitles") {
		t.Fatalf("illustration prompt kept the subtitle lower-third wording:\n%s", prompt)
	}

	avatars := audioBookAvatarSpeakers(topic)
	if len(avatars) != 2 {
		t.Fatalf("avatar speakers = %d, want 2", len(avatars))
	}
	hostAvatarPrompt := audioBookAvatarPrompt(topic, avatars[0])
	for _, want := range []string{
		"animated-film style speaker avatar",
		"Character continuity: use this exact character design",
		avatars[0].Look,
	} {
		if !strings.Contains(hostAvatarPrompt, want) {
			t.Fatalf("avatar prompt missing %q:\n%s", want, hostAvatarPrompt)
		}
	}
	if !strings.Contains(prompt, avatars[0].Look) {
		t.Fatalf("chapter prompt does not share host avatar look %q:\n%s", avatars[0].Look, prompt)
	}
}

func TestAudioBookIllustrationObjectNameUsesPerAudioBookWebPPath(t *testing.T) {
	got := audioBookIllustrationObjectName("DISCUSSION-123", 0)
	if want := "audiobooks/discussion-123/image-1.webp"; got != want {
		t.Fatalf("object name = %q, want %q", got, want)
	}

	got = audioBookIllustrationObjectName("../same title/a", 2)
	if want := "audiobooks/same-title-a/image-3.webp"; got != want {
		t.Fatalf("sanitized object name = %q, want %q", got, want)
	}

	got = audioBookIllustrationObjectName("", -1)
	if want := "audiobooks/audiobook/image-1.webp"; got != want {
		t.Fatalf("fallback object name = %q, want %q", got, want)
	}
}
