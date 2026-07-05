package planner

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
)

func TestTemplateRegistryDefaultLookup(t *testing.T) {
	templates := TemplatesByType(config.ContentTypeDiscussion)
	if len(templates) != 2 {
		t.Fatalf("templates length = %d, want 2", len(templates))
	}
	if templates[0].ID != DefaultTemplateID {
		t.Fatalf("template id = %q, want %q", templates[0].ID, DefaultTemplateID)
	}
	if templates[1].ID != ResearchTemplateID {
		t.Fatalf("research template id = %q, want %q", templates[1].ID, ResearchTemplateID)
	}

	if tmpl, ok := TemplateByID(config.ContentTypeDiscussion, ""); !ok || tmpl.ID != DefaultTemplateID {
		t.Fatalf("empty id lookup = %+v, %v; want default", tmpl, ok)
	}
	if tmpl, ok := TemplateByID(config.ContentTypeDiscussion, ResearchTemplateID); !ok || tmpl.ID != ResearchTemplateID {
		t.Fatalf("research template lookup = %+v, %v; want research", tmpl, ok)
	}
	if _, ok := TemplateByID(config.ContentTypeDiscussion, "missing"); ok {
		t.Fatal("missing template lookup succeeded")
	}
	if schema := TemplateSchema(config.ContentTypeDiscussion, "missing"); schema == nil || schema["type"] != "object" {
		t.Fatalf("fallback schema = %+v, want default object schema", schema)
	}
}

func TestResearchTemplateUsesDefaultPlanSchemaWithInstructions(t *testing.T) {
	defaultSchema, err := json.Marshal(TemplateSchema(config.ContentTypeDiscussion, DefaultTemplateID))
	if err != nil {
		t.Fatalf("marshal default schema: %v", err)
	}
	researchSchema, err := json.Marshal(TemplateSchema(config.ContentTypeDiscussion, ResearchTemplateID))
	if err != nil {
		t.Fatalf("marshal research schema: %v", err)
	}
	if string(researchSchema) != string(defaultSchema) {
		t.Fatalf("research schema = %s, want default schema %s", researchSchema, defaultSchema)
	}
	if !strings.Contains(TemplateInstructions(ResearchTemplateID), "school-style") {
		t.Fatalf("research template instructions missing school-style guidance: %q", TemplateInstructions(ResearchTemplateID))
	}
}

func TestDefaultTemplateSchemaAvoidsGatewayUnsupportedKeywords(t *testing.T) {
	raw, err := json.Marshal(TemplateSchema(config.ContentTypeDiscussion, DefaultTemplateID))
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	text := string(raw)
	for _, keyword := range []string{"minItems", "maxItems"} {
		if strings.Contains(text, keyword) {
			t.Fatalf("default schema contains %s: %s", keyword, text)
		}
	}
}

func TestAudioBookTemplateSchema(t *testing.T) {
	templates := TemplatesByType(config.ContentTypeAudioBook)
	if len(templates) != 6 {
		t.Fatalf("audio-book templates length = %d, want 6", len(templates))
	}
	wantIDs := []string{
		DefaultTemplateID,
		AudioBookNewsTemplateID,
		AudioBookConversationalTemplateID,
		AudioBookAudioBookTemplateID,
		AudioBookPodcastTemplateID,
		AudioBookMeetingTemplateID,
	}
	for i, want := range wantIDs {
		if templates[i].ID != want {
			t.Fatalf("audio-book template %d id = %q, want %q", i, templates[i].ID, want)
		}
	}
	raw, err := json.Marshal(TemplateSchema(config.ContentTypeAudioBook, DefaultTemplateID))
	if err != nil {
		t.Fatalf("marshal audio-book schema: %v", err)
	}
	text := string(raw)
	for _, field := range []string{"style", "overall_summary", "narrator", "speakers", "chapters"} {
		if !strings.Contains(text, field) {
			t.Fatalf("audio-book schema missing %s: %s", field, text)
		}
	}
	for _, style := range []string{"news", "conversational", "audiobook", "podcast", "meeting"} {
		if !strings.Contains(text, style) {
			t.Fatalf("audio-book schema missing style %q: %s", style, text)
		}
	}
	for _, expected := range []string{"Prefer 3-5 for short sources", "up to 40", "do not duplicate chapters in overall_summary"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("audio-book schema missing chapter guidance %q: %s", expected, text)
		}
	}
	for _, expected := range []string{`"gender"`, "REQUIRED voice gender", `"mode"`, "narration", "dialogue"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("audio-book schema missing voice-casting field %q: %s", expected, text)
		}
	}
	for _, expected := range []string{"Source-cast voice roster", "Include most of the book/source's speaking cast", "chapter-critical one-off voices", "never fold two characters"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("audio-book schema missing source-cast guidance %q: %s", expected, text)
		}
	}
}

func TestAudioBookTemplateInstructionsSetStyle(t *testing.T) {
	got := TemplateInstructions(AudioBookPodcastTemplateID)
	if !strings.Contains(got, "`style` to `podcast`") {
		t.Fatalf("podcast template instructions missing style guidance: %q", got)
	}
}

func TestConversationInitialTextConstrainsAudioBookChapters(t *testing.T) {
	got := ConversationInitialText(PlanRequest{
		Type:     config.ContentTypeAudioBook,
		Topic:    "Turn this document into an audiobook",
		Language: "zh-Hans",
	})
	for _, expected := range []string{
		"dedicated ordered chapter sections in `chapters`",
		"Style must be one of news, conversational, audiobook, podcast, or meeting",
		"prefer 3-5 chapters for short sources",
		"up to 40",
		"do not repeat the chapter list in the summary",
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("audio-book initial prompt missing %q: %s", expected, got)
		}
	}
}

func TestAudioBookAttachmentPromptUsesBoundedDigest(t *testing.T) {
	long := "# Chapter One\n\n" + strings.Repeat("very long source text ", 1000)
	got := audioBookAttachmentsPrompt([]Attachment{{Filename: "book.pdf", Markdown: long}})
	if len(got) > 3500 {
		t.Fatalf("audio-book prompt too large: %d chars", len(got))
	}
	if !strings.Contains(got, "Converted length:") || !strings.Contains(got, "# Chapter One") {
		t.Fatalf("audio-book prompt missing digest metadata: %s", got)
	}
}

func TestConversationToolsUseDefaultTemplateSchema(t *testing.T) {
	want, err := json.Marshal(TemplateSchema(config.ContentTypeDiscussion, DefaultTemplateID))
	if err != nil {
		t.Fatalf("marshal want schema: %v", err)
	}
	for _, tool := range conversationTools(config.ContentTypeDiscussion, DefaultTemplateID) {
		if tool.Function.Name != "write_plan" {
			continue
		}
		got, err := json.Marshal(tool.Function.Parameters)
		if err != nil {
			t.Fatalf("marshal write_plan schema: %v", err)
		}
		if string(got) != string(want) {
			t.Fatalf("write_plan schema = %s, want %s", got, want)
		}
		return
	}
	t.Fatal("write_plan tool not found")
}

func TestConversationToolsResearchTemplateAddsPaperTools(t *testing.T) {
	var sawSearchPapers, sawReadPaper, sawWritePlan bool
	for _, tool := range conversationTools(config.ContentTypeDiscussion, ResearchTemplateID) {
		switch tool.Function.Name {
		case "search_research_papers":
			sawSearchPapers = true
		case "read_research_paper":
			sawReadPaper = true
		case "write_plan":
			sawWritePlan = true
			want, _ := json.Marshal(TemplateSchema(config.ContentTypeDiscussion, ResearchTemplateID))
			got, _ := json.Marshal(tool.Function.Parameters)
			if string(got) != string(want) {
				t.Fatalf("research write_plan schema = %s, want %s", got, want)
			}
		}
	}
	if !sawSearchPapers || !sawReadPaper || !sawWritePlan {
		t.Fatalf("research tools missing: search=%v read=%v write=%v", sawSearchPapers, sawReadPaper, sawWritePlan)
	}
}
