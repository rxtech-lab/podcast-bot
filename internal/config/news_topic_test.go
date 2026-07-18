package config

import (
	"os"
	"strings"
	"testing"
)

func validNewsTopic() *DebateTopic {
	return &DebateTopic{
		Title:    "Morning Brief",
		Type:     ContentTypeNews,
		Language: "en-US",
		Channel:  "default",
		Host:     AgentSpec{Name: "Dana", Model: "m"},
		Discussants: []AgentSpec{
			{Name: "Ravi", Model: "m", Aspect: "markets"},
		},
		Commander: AgentSpec{Model: "m"},
		NewsStories: []NewsStory{
			{Headline: "Chips rally", Summary: "Chip stocks rallied.", KeyFacts: []string{"Index up 4%"}},
			{Headline: "New AI rules", Summary: "Regulators proposed rules."},
		},
		Background: "Shared context.",
	}
}

func TestValidateNews(t *testing.T) {
	if err := ValidateTopic(validNewsTopic()); err != nil {
		t.Fatalf("valid news topic rejected: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*DebateTopic)
		want   string
	}{
		{"missing stories", func(tp *DebateTopic) { tp.NewsStories = nil }, "news_stories"},
		{"story missing summary", func(tp *DebateTopic) { tp.NewsStories[0].Summary = " " }, "headline and summary"},
		{"missing anchor", func(tp *DebateTopic) { tp.Host = AgentSpec{} }, "host.model"},
		{"missing commander", func(tp *DebateTopic) { tp.Commander = AgentSpec{} }, "commander.model"},
		{"missing commentators", func(tp *DebateTopic) { tp.Discussants = nil }, "at least one commentator"},
		{"foreign debate roster", func(tp *DebateTopic) { tp.Judge = AgentSpec{Model: "m"} }, "must not declare"},
		{"foreign puzzle roster", func(tp *DebateTopic) { tp.PuzzleHost = AgentSpec{Model: "m"} }, "must not declare"},
		{"foreign audiobook roster", func(tp *DebateTopic) { tp.AudioBookHost = AgentSpec{Model: "m"} }, "must not declare"},
		{"bad storage", func(tp *DebateTopic) { tp.Storage = "sqlite" }, "storage"},
	}
	for _, tc := range cases {
		tp := validNewsTopic()
		tc.mutate(tp)
		err := ValidateTopic(tp)
		if err == nil {
			t.Fatalf("%s: expected validation error", tc.name)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err = %v, want it to mention %q", tc.name, err, tc.want)
		}
	}
}

func TestNewsRenderMarkdownRoundTrip(t *testing.T) {
	tp := validNewsTopic()
	tp.TotalMinutes = 30
	tp.SegmentMaxSeconds = 60
	tp.Storage = StoragePlaintext
	md, err := tp.RenderMarkdown()
	if err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	if !strings.Contains(md, "news_stories:") {
		t.Fatalf("rendered markdown missing news_stories:\n%s", md)
	}

	front, body, err := splitFrontmatter(md)
	if err != nil {
		t.Fatalf("splitFrontmatter: %v", err)
	}
	_ = body
	if !strings.Contains(front, "type: news") {
		t.Fatalf("frontmatter missing type: news:\n%s", front)
	}

	// Full round trip through the same parse path LoadTopic uses.
	dir := t.TempDir()
	path := dir + "/topic.md"
	if err := os.WriteFile(path, []byte(md), 0o644); err != nil {
		t.Fatalf("write temp topic: %v", err)
	}
	got, err := LoadTopic(path)
	if err != nil {
		t.Fatalf("LoadTopic on rendered news markdown: %v", err)
	}
	if got.Type != ContentTypeNews {
		t.Fatalf("round-trip type = %q, want news", got.Type)
	}
	if len(got.NewsStories) != 2 || got.NewsStories[0].Headline != "Chips rally" ||
		len(got.NewsStories[0].KeyFacts) != 1 {
		t.Fatalf("round-trip stories = %+v, want originals", got.NewsStories)
	}
	if got.Storage != StoragePlaintext {
		t.Fatalf("round-trip storage = %q, want plaintext default", got.Storage)
	}
	if got.Background != "Shared context." {
		t.Fatalf("round-trip background = %q", got.Background)
	}
}
