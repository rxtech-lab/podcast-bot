package planner

import "github.com/sirily11/debate-bot/internal/config"

const DefaultTemplateID = "default"

// Template is a named JSON schema for a planner output shape. The current
// decoder/assembler still expects the default discussion shape; divergent
// future templates will need per-template decoders or a template-driven
// assembler.
type Template struct {
	ID          string         `json:"id"`
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"schema,omitempty"`
}

var templateRegistry = map[string][]Template{
	config.ContentTypeDiscussion: {
		{
			ID:          DefaultTemplateID,
			Type:        config.ContentTypeDiscussion,
			Name:        "Default",
			Description: "A balanced panel-discussion plan with a host, background, and discussants.",
			Schema:      defaultDiscussionPlanSchema(),
		},
	},
}

func defaultDiscussionPlanSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title":      map[string]any{"type": "string"},
			"background": map[string]any{"type": "string", "description": "Two to four neutral paragraphs grounding the discussion."},
			"host": map[string]any{
				"type":       "object",
				"properties": map[string]any{"name": map[string]any{"type": "string"}},
				"required":   []string{"name"},
			},
			"discussants": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name":   map[string]any{"type": "string"},
						"aspect": map[string]any{"type": "string"},
					},
					"required": []string{"name", "aspect"},
				},
			},
		},
		"required": []string{"title", "background", "host", "discussants"},
	}
}

func TemplatesByType(t string) []Template {
	templates := templateRegistry[t]
	out := make([]Template, len(templates))
	for i, tmpl := range templates {
		out[i] = cloneTemplate(tmpl)
	}
	return out
}

func TemplateByID(t, id string) (Template, bool) {
	if id == "" {
		id = DefaultTemplateID
	}
	for _, tmpl := range templateRegistry[t] {
		if tmpl.ID == id {
			return cloneTemplate(tmpl), true
		}
	}
	return Template{}, false
}

func TemplateSchema(t, id string) map[string]any {
	if tmpl, ok := TemplateByID(t, id); ok {
		return tmpl.Schema
	}
	if tmpl, ok := TemplateByID(t, DefaultTemplateID); ok {
		return tmpl.Schema
	}
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func cloneTemplate(t Template) Template {
	t.Schema = cloneMap(t.Schema)
	return t
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = cloneAny(v)
	}
	return out
}

func cloneAny(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return cloneMap(x)
	case []string:
		out := make([]string, len(x))
		copy(out, x)
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = cloneAny(item)
		}
		return out
	default:
		return v
	}
}
