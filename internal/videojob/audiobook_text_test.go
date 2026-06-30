package videojob

import (
	"strings"
	"testing"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
	contentcreator "github.com/sirily11/debate-bot/internal/content_creator"
)

func TestBuildAudioBookTextDeduplicatesImagesAndOmitsAudioLink(t *testing.T) {
	topic := &config.DebateTopic{
		Title:      "Border Lines",
		Background: "A concise setup.",
	}
	lines := []agent.TranscriptLine{
		{Speaker: "Narrator", Role: agent.RoleHost, Text: "Chapter One opens the story."},
		{Speaker: "Narrator", Role: agent.RoleHost, Text: "Chapter Two continues the argument."},
	}
	imgs := []contentcreator.AudioBookImage{
		{Caption: "Chapter One", URL: "https://cdn.example/one.png"},
		{Caption: "Chapter Two", URL: "https://cdn.example/one.png"},
		{Caption: "Chapter Three", URL: "https://cdn.example/three.png"},
	}

	md := buildAudioBookText(topic, lines, imgs, "https://cdn.example/audio.mp3")

	if count := strings.Count(md, "https://cdn.example/one.png"); count != 1 {
		t.Fatalf("duplicate image URL count = %d, want 1\n%s", count, md)
	}
	if !strings.Contains(md, "https://cdn.example/three.png") {
		t.Fatalf("unmatched image was not appended:\n%s", md)
	}
	if strings.Contains(md, "Listen to the audiobook") || strings.Contains(md, "https://cdn.example/audio.mp3") {
		t.Fatalf("audio link should not be included:\n%s", md)
	}
}
