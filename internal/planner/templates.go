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

// News-podcast template IDs. The roundup template is the type default
// (DefaultTemplateID) so the shared default-fallback lookups resolve.
const (
	NewsDeepDiveTemplateID   = "deep-dive"
	NewsCommentaryTemplateID = "commentary"
	NewsBreakingTemplateID   = "breaking"
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
	config.ContentTypeUploadedAudio: {
		{
			ID:          DefaultTemplateID,
			Type:        config.ContentTypeUploadedAudio,
			Name:        "Transcript review",
			Description: "Proofread the transcript of an uploaded audio recording.",
			Schema:      uploadedAudioPlanSchema(),
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
	config.ContentTypeNews: {
		{
			ID:          DefaultTemplateID,
			Type:        config.ContentTypeNews,
			Name:        "Morning News Roundup",
			Description: "Several headlines at a brisk pace with quick commentator reactions.",
			Schema:      newsPlanSchema(),
		},
		{
			ID:          NewsDeepDiveTemplateID,
			Type:        config.ContentTypeNews,
			Name:        "Deep Dive Single Story",
			Description: "One story broken into segments with extended analyst commentary.",
			Schema:      newsPlanSchema(),
		},
		{
			ID:          NewsCommentaryTemplateID,
			Type:        config.ContentTypeNews,
			Name:        "News + Commentary",
			Description: "The anchor reads each report, then the desk discusses opinions and implications.",
			Schema:      newsPlanSchema(),
		},
		{
			ID:          NewsBreakingTemplateID,
			Type:        config.ContentTypeNews,
			Name:        "Breaking News Special",
			Description: "Urgent single-event coverage: what we know, what is unconfirmed, what's next.",
			Schema:      newsPlanSchema(),
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

// NewsTemplateInstructions returns per-template planning guidance for the news
// content type. Kept separate from TemplateInstructions because the news
// default template shares the "default" ID with the discussion default.
func NewsTemplateInstructions(id string) string {
	shared := `
- Prefer sources published within the last 72 hours; include publication dates and outlet attributions in summaries and key_facts.
- Give each commentator a distinct beat (focus) that maps onto the rundown.`
	switch strings.TrimSpace(id) {
	case "", DefaultTemplateID:
		return strings.TrimSpace(`Template: Morning News Roundup
- Plan 6-10 short stories covering today's most relevant headlines for the topic.
- Keep summaries brisk — two or three sentences each; the pace is a fast morning show with roughly one commentator reaction per story.`) + shared
	case NewsDeepDiveTemplateID:
		return strings.TrimSpace(`Template: Deep Dive Single Story
- Plan EXACTLY ONE story, but make it segment-sized: split its key_facts into 3-5 coherent groups (timeline, background, stakeholders, implications) so the anchor can present it in parts while the analysts go deep between reads.`) + shared
	case NewsCommentaryTemplateID:
		return strings.TrimSpace(`Template: News + Commentary
- Plan 3-5 stories with meaty summaries. The format is talk-radio: the anchor reads the full report, then the desk debates opinions and implications back and forth before the next item — so pick stories with genuine room for disagreement and give commentators opinionated but distinct beats.`) + shared
	case NewsBreakingTemplateID:
		return strings.TrimSpace(`Template: Breaking News Special
- Plan EXACTLY ONE developing event with an urgent, live-updates feel.
- Explicitly separate confirmed key_facts from unconfirmed reports — prefix unconfirmed items with "Unconfirmed:" so the anchor can flag them on air.
- Structure the summary as: what happened, what we know, what remains unclear, what to watch next.`) + shared
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
			"title":      map[string]any{"type": "string", "description": "Concise, engaging discussion title; fewer than 10 words."},
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

func newsPlanSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title":      map[string]any{"type": "string", "description": "Concise broadcast title; fewer than 10 words."},
			"background": map[string]any{"type": "string", "description": "One to three neutral paragraphs of shared context for today's broadcast."},
			"anchor": map[string]any{
				"type":       "object",
				"properties": map[string]any{"name": map[string]any{"type": "string"}},
				"required":   []string{"name"},
			},
			"commentators": map[string]any{
				"type":        "array",
				"description": "One to four commentators, each with a distinct beat that maps onto the rundown.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name":  map[string]any{"type": "string"},
						"focus": map[string]any{"type": "string", "description": "The commentator's beat or analytical angle (e.g. economics, on-the-ground, policy)."},
					},
					"required": []string{"name", "focus"},
				},
			},
			"stories": map[string]any{
				"type":        "array",
				"description": "The ordered rundown. Each story drives one on-air segment: the anchor reads it, then commentators react.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"headline": map[string]any{"type": "string", "description": "The on-air headline for this story."},
						"summary":  map[string]any{"type": "string", "description": "Two to four sentences the anchor reads from; include publication dates and outlet attributions."},
						"key_facts": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "string"},
							"description": "Concrete, dated, attributed facts the anchor works into the read.",
						},
					},
					"required": []string{"headline", "summary"},
				},
			},
		},
		"required": []string{"title", "background", "anchor", "commentators", "stories"},
	}
}

func defaultAudioBookPlanSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title":           map[string]any{"type": "string", "description": "Concise audiobook title; fewer than 10 words."},
			"style":           map[string]any{"type": "string", "enum": []string{config.AudioBookStyleNews, config.AudioBookStyleConversational, config.AudioBookStyleAudioBook, config.AudioBookStylePodcast, config.AudioBookStyleMeeting}, "description": "The high-level production style the agent selected for this audiobook."},
			"overall_summary": map[string]any{"type": "string", "description": "A concise Markdown summary of the full source material and audiobook direction."},
			"narrator": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":   map[string]any{"type": "string"},
					"gender": map[string]any{"type": "string", "enum": []string{"male", "female"}, "description": "REQUIRED voice gender for TTS casting."},
				},
				"required": []string{"name", "gender"},
			},
			"speakers": map[string]any{
				"type":        "array",
				"description": "Source-cast voice roster. Include most of the book/source's speaking cast: all central and recurring voices plus chapter-critical one-off voices. Omit only unnamed, background, or truly incidental speakers. Use one dedicated entry per included character or guest; never fold two characters into one shared voice, and never repeat the narrator here.",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name":        map[string]any{"type": "string"},
						"gender":      map[string]any{"type": "string", "enum": []string{"male", "female"}, "description": "REQUIRED voice gender for TTS casting; infer from the source, never leave it out."},
						"description": map[string]any{"type": "string", "description": "Concrete voice-casting brief: approximate age, vocal tone and register, personality, speaking energy."},
					},
					"required": []string{"name", "gender", "description"},
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
						"mode":    map[string]any{"type": "string", "enum": []string{config.AudioBookModeNarration, config.AudioBookModeDialogue}, "description": "narration = the narrator reads alone; dialogue = a real exchange with the listed speakers."},
						"speakers": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "string"},
							"description": "Names from the top-level speakers list who talk in this chapter; empty for narration chapters. Never list the narrator.",
						},
					},
					"required": []string{"title", "summary"},
				},
			},
		},
		"required": []string{"title", "style", "overall_summary", "narrator", "speakers", "chapters"},
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
