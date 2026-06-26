package planner

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
)

func TestTemplateRegistryDefaultLookup(t *testing.T) {
	templates := TemplatesByType(config.ContentTypeDiscussion)
	if len(templates) != 1 {
		t.Fatalf("templates length = %d, want 1", len(templates))
	}
	if templates[0].ID != DefaultTemplateID {
		t.Fatalf("template id = %q, want %q", templates[0].ID, DefaultTemplateID)
	}

	if tmpl, ok := TemplateByID(config.ContentTypeDiscussion, ""); !ok || tmpl.ID != DefaultTemplateID {
		t.Fatalf("empty id lookup = %+v, %v; want default", tmpl, ok)
	}
	if _, ok := TemplateByID(config.ContentTypeDiscussion, "missing"); ok {
		t.Fatal("missing template lookup succeeded")
	}
	if schema := TemplateSchema(config.ContentTypeDiscussion, "missing"); schema == nil || schema["type"] != "object" {
		t.Fatalf("fallback schema = %+v, want default object schema", schema)
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
