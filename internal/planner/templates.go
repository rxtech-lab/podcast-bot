package planner

import (
	"fmt"
	"strings"

	"github.com/sirily11/debate-bot/internal/config"
)

const DefaultTemplateID = "default"
const ResearchTemplateID = "research"
const (
	AudioBookNewsTemplateID           = config.AudioBookStyleNews
	AudioBookConversationalTemplateID = config.AudioBookStyleConversational
	AudioBookAudioBookTemplateID      = config.AudioBookStyleAudioBook
	AudioBookPodcastTemplateID        = config.AudioBookStylePodcast
	AudioBookMeetingTemplateID        = config.AudioBookStyleMeeting
)

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
		{
			ID:          ResearchTemplateID,
			Type:        config.ContentTypeDiscussion,
			Name:        "Research",
			Description: "A school-style discussion plan grounded in research papers and cited evidence.",
			Schema:      defaultDiscussionPlanSchema(),
		},
	},
	config.ContentTypeAudioBook: {
		{
			ID:          DefaultTemplateID,
			Type:        config.ContentTypeAudioBook,
			Name:        "Auto",
			Description: "Let the agent choose the best audiobook style for the source.",
			Schema:      defaultAudioBookPlanSchema(),
		},
		{
			ID:          AudioBookNewsTemplateID,
			Type:        config.ContentTypeAudioBook,
			Name:        "News",
			Description: "A news-style audiobook with a lead presenter and supporting voices.",
			Schema:      defaultAudioBookPlanSchema(),
		},
		{
			ID:          AudioBookConversationalTemplateID,
			Type:        config.ContentTypeAudioBook,
			Name:        "Conversational",
			Description: "A conversational audiobook where one main voice leads and guests ask questions.",
			Schema:      defaultAudioBookPlanSchema(),
		},
		{
			ID:          AudioBookAudioBookTemplateID,
			Type:        config.ContentTypeAudioBook,
			Name:        "Audiobook",
			Description: "A classic narrated audiobook with light character or quote voices.",
			Schema:      defaultAudioBookPlanSchema(),
		},
		{
			ID:          AudioBookPodcastTemplateID,
			Type:        config.ContentTypeAudioBook,
			Name:        "Podcast",
			Description: "A podcast-style audiobook with a host-led discussion format.",
			Schema:      defaultAudioBookPlanSchema(),
		},
		{
			ID:          AudioBookMeetingTemplateID,
			Type:        config.ContentTypeAudioBook,
			Name:        "Meeting",
			Description: "A meeting-style audiobook with a facilitator and participant questions.",
			Schema:      defaultAudioBookPlanSchema(),
		},
	},
}

func IsResearchTemplate(id string) bool {
	return strings.TrimSpace(id) == ResearchTemplateID
}

func TemplateInstructions(id string) string {
	if IsResearchTemplate(id) {
		return strings.TrimSpace(`Template: Research
- Produce a school-style discussion plan suitable for students, teachers, or classroom debate.
- Prefer research papers and academic evidence over general web snippets when live research is enabled.
- Ground the background in concrete findings, methods, datasets, tradeoffs, or limitations from the sources.
- Make each discussant's aspect useful for learning: e.g. evidence, methods, ethics, policy, classroom impact, history, or counterargument.
- Keep the plan understandable for a school audience without making it casual or shallow.`)
	}
	if style := AudioBookStyleForTemplate(id); style != "" {
		return "Template: " + style + "\n- Set `style` to `" + style + "` unless the user explicitly asks for a different style.\n- Shape the narrator, speakers, chapter modes, and summaries around that style."
	}
	return ""
}

func AudioBookStyleForTemplate(id string) string {
	switch strings.TrimSpace(id) {
	case AudioBookNewsTemplateID:
		return config.AudioBookStyleNews
	case AudioBookConversationalTemplateID:
		return config.AudioBookStyleConversational
	case AudioBookAudioBookTemplateID:
		return config.AudioBookStyleAudioBook
	case AudioBookPodcastTemplateID:
		return config.AudioBookStylePodcast
	case AudioBookMeetingTemplateID:
		return config.AudioBookStyleMeeting
	default:
		return ""
	}
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

func defaultAudioBookPlanSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title":           map[string]any{"type": "string"},
			"style":           map[string]any{"type": "string", "enum": []string{config.AudioBookStyleNews, config.AudioBookStyleConversational, config.AudioBookStyleAudioBook, config.AudioBookStylePodcast, config.AudioBookStyleMeeting}, "description": "The high-level production style the agent selected for this audiobook."},
			"overall_summary": map[string]any{"type": "string", "description": "A concise Markdown summary of the full source material and audiobook direction."},
			"narrator": map[string]any{
				"type":       "object",
				"properties": map[string]any{"name": map[string]any{"type": "string"}},
				"required":   []string{"name"},
			},
			"speakers": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name":        map[string]any{"type": "string"},
						"gender":      map[string]any{"type": "string"},
						"description": map[string]any{"type": "string"},
					},
					"required": []string{"name", "description"},
				},
			},
			"chapters": map[string]any{
				"type":        "array",
				"description": fmt.Sprintf("Dedicated chapter sections for the audiobook, one per natural chapter or major section of the source. Prefer 3-5 for short sources; long books may have as many chapters as the source genuinely has, up to %d. Keep chapter summaries brief and do not duplicate chapters in overall_summary.", audioBookMaxChapters),
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"title":   map[string]any{"type": "string", "description": "Chapter title only; do not include a chapter number prefix."},
						"summary": map[string]any{"type": "string", "description": "One or two concise sentences describing what this chapter narrates."},
					},
					"required": []string{"title", "summary"},
				},
			},
		},
		"required": []string{"title", "style", "overall_summary", "narrator", "chapters"},
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
