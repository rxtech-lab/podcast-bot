package planner

import (
	"context"
	"strings"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/llm"
)

func TestNewsTemplatesRegistered(t *testing.T) {
	templates := TemplatesByType(config.ContentTypeNews)
	if len(templates) != 4 {
		t.Fatalf("news templates = %d, want 4", len(templates))
	}
	if templates[0].ID != DefaultTemplateID {
		t.Fatalf("first news template ID = %q, want %q (default-fallback lookups depend on it)", templates[0].ID, DefaultTemplateID)
	}
	wantIDs := []string{DefaultTemplateID, NewsDeepDiveTemplateID, NewsCommentaryTemplateID, NewsBreakingTemplateID}
	for i, id := range wantIDs {
		if templates[i].ID != id {
			t.Fatalf("news template %d ID = %q, want %q", i, templates[i].ID, id)
		}
		if templates[i].Schema == nil {
			t.Fatalf("news template %q has no schema", id)
		}
	}
	schema := TemplateSchema(config.ContentTypeNews, "")
	props, _ := schema["properties"].(map[string]any)
	for _, key := range []string{"title", "background", "anchor", "commentators", "stories"} {
		if _, ok := props[key]; !ok {
			t.Fatalf("news plan schema missing property %q", key)
		}
	}
}

func TestNewsTemplateInstructionsPerTemplate(t *testing.T) {
	for id, marker := range map[string]string{
		"":                       "Morning News Roundup",
		DefaultTemplateID:        "Morning News Roundup",
		NewsDeepDiveTemplateID:   "Deep Dive Single Story",
		NewsCommentaryTemplateID: "News + Commentary",
		NewsBreakingTemplateID:   "Breaking News Special",
	} {
		got := NewsTemplateInstructions(id)
		if !strings.Contains(got, marker) {
			t.Fatalf("NewsTemplateInstructions(%q) = %q, want it to mention %q", id, got, marker)
		}
		if !strings.Contains(got, "72 hours") {
			t.Fatalf("NewsTemplateInstructions(%q) should carry the shared recency guidance", id)
		}
	}
}

func TestConversationDispatchNewsWritePlan(t *testing.T) {
	s := testConversationSession()
	s.opts.Type = config.ContentTypeNews
	draft := `{
		"title": "Morning Tech Brief",
		"background": "Shared context for today's broadcast.",
		"anchor": {"name": "Dana"},
		"commentators": [
			{"name": "Ravi", "focus": "markets"},
			{"name": "Mia", "focus": "policy"}
		],
		"stories": [
			{"headline": "Chips rally", "summary": "Chip stocks rallied on July 17, per Reuters.", "key_facts": ["Index up 4% (Reuters, 2026-07-17)"]},
			{"headline": "New AI rules", "summary": "Regulators proposed new AI rules this week."}
		]
	}`
	output, kind, res, _, isErr := s.dispatch(context.Background(), llm.ToolCall{ID: "c-news", Name: "write_plan", Arguments: draft})
	if isErr {
		t.Fatalf("write_plan errored: %q", output)
	}
	if kind != dispatchTool {
		t.Fatalf("expected hidden dispatchTool, got %v", kind)
	}
	if res == nil || res.Script == nil {
		t.Fatalf("write_plan should produce an assembled news plan")
	}
	script := res.Script
	if script.Type != config.ContentTypeNews {
		t.Fatalf("script type = %q, want news", script.Type)
	}
	if script.Host.Name != "Dana" {
		t.Fatalf("anchor = %q, want Dana", script.Host.Name)
	}
	if len(script.Discussants) != 2 || script.Discussants[0].Aspect != "markets" {
		t.Fatalf("commentators = %+v, want 2 with focus mapped to Aspect", script.Discussants)
	}
	if len(script.NewsStories) != 2 || script.NewsStories[0].Headline != "Chips rally" {
		t.Fatalf("stories = %+v, want the 2 drafted stories", script.NewsStories)
	}
	if script.Commander.Model == "" {
		t.Fatalf("news plan should carry a commander model for the silent director")
	}
	if !strings.Contains(res.Markdown, "news_stories") {
		t.Fatalf("rendered markdown should serialize news_stories, got:\n%s", res.Markdown)
	}
}

func TestDecodeNewsDraftRejectsIncomplete(t *testing.T) {
	if _, err := decodeNewsDraft(`{"title":"x","commentators":[],"stories":[]}`); err == nil {
		t.Fatalf("incomplete news draft should be rejected")
	}
	if _, err := decodeNewsDraft(`not json`); err == nil {
		t.Fatalf("malformed news draft should be rejected")
	}
}

func TestConversationInitialTextNews(t *testing.T) {
	text := ConversationInitialText(PlanRequest{
		Type:        config.ContentTypeNews,
		Topic:       "semiconductor supply chains",
		Discussants: 2,
	})
	if !strings.Contains(text, "Content type: "+config.ContentTypeNews) {
		t.Fatalf("initial text must carry the content-type marker (planningContentType depends on it):\n%s", text)
	}
	if !strings.Contains(text, "radio-news broadcast") {
		t.Fatalf("initial text should describe a news broadcast:\n%s", text)
	}
	if !strings.Contains(text, "Number of commentators: 2") {
		t.Fatalf("initial text should carry the commentator count:\n%s", text)
	}
}
