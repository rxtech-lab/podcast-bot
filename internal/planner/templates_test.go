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

func TestConversationToolsUseDefaultTemplateSchema(t *testing.T) {
	want, err := json.Marshal(TemplateSchema(config.ContentTypeDiscussion, DefaultTemplateID))
	if err != nil {
		t.Fatalf("marshal want schema: %v", err)
	}
	for _, tool := range conversationTools(DefaultTemplateID) {
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
	for _, tool := range conversationTools(ResearchTemplateID) {
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
