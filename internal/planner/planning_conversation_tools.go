package planner

import (
	"github.com/openai/openai-go"

	"github.com/sirily11/debate-bot/internal/config"
)

// conversationPlanSchema is the structured plan shape shared by write_plan and
// update_plan. It mirrors create_plan (agent_loop.go) but, unlike create_plan,
// these tools are NOT terminal — the conversation continues so the user can keep
// refining the plan.
func conversationPlanSchema(contentType, template string) map[string]any {
	if contentType == "" {
		contentType = config.ContentTypeDiscussion
	}
	return TemplateSchema(contentType, template)
}

// conversationTools is the tool set for the conversational planner loop.
func conversationTools(contentType, template string) []openai.ChatCompletionToolParam {
	tools := []openai.ChatCompletionToolParam{}
	// Transcript review needs no research: the source of truth is the user's
	// own audio, so only the plan/question tools are offered.
	researchable := contentType != config.ContentTypeUploadedAudio
	if researchable && contentType != config.ContentTypeAudioBook && IsResearchTemplate(template) {
		tools = append(tools,
			toolDef("search_research_papers", "Search Firecrawl Research Index for ranked scientific or engineering papers. Use this before general web search for the research template.", map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "Research question, topic, method, benchmark, author, or category to search."},
				},
				"required": []string{"query"},
			}),
			toolDef("read_research_paper", "Read relevant full-text passages from one Firecrawl Research paper. Use after search_research_papers to verify the paper contains useful evidence.", map[string]any{
				"type": "object",
				"properties": map[string]any{
					"paper_id": map[string]any{"type": "string", "description": "The paperId or primaryId returned by search_research_papers."},
					"query":    map[string]any{"type": "string", "description": "Question to answer using passages from this paper."},
				},
				"required": []string{"paper_id"},
			}),
		)
	}
	if researchable {
		tools = append(tools,
			toolDef("search_sources", "Search the web through Firecrawl and return candidate source URLs with snippets. Use this before crawl_sources; do not treat search snippets as full source content.", map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "Search query to research the discussion topic."},
				},
				"required": []string{"query"},
			}),
			toolDef("crawl_sources", "Scrape/read one or more promising URLs and return clean markdown context. Use after search_sources when candidate sources look useful.", map[string]any{
				"type": "object",
				"properties": map[string]any{
					"urls": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "The selected http(s) URLs to read.",
					},
				},
				"required": []string{"urls"},
			}),
		)
	}
	updatePlanDesc := "Replace the current saved plan with a revised one. Provide the FULL updated plan, not a diff. This does not show the plan to the user; call show_plan after this when the revised plan is ready to display."
	if contentType == config.ContentTypeUploadedAudio {
		updatePlanDesc = "Apply transcript corrections: pass only the segments you change (by index), plus an optional corrected title and speaker renames. Unlisted segments stay unchanged. This does not show the plan to the user; call show_plan after this when the corrections are ready to display."
	}
	tools = append(tools,
		toolDef("write_plan", "Write and save the initial plan internally. This does not show the plan to the user; call show_plan after this when the plan is ready to display.", conversationPlanSchema(contentType, template)),
		toolDef("update_plan", updatePlanDesc, conversationPlanSchema(contentType, template)),
		toolDef("show_plan", "Show the current saved plan in the app. Call only after write_plan or update_plan, and only when the plan should be visible to the user. After this tool returns, acknowledge briefly instead of summarizing the plan.", map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}),
		toolDef("ask_question", "Ask the user one or more structured questions when their intent is ambiguous. Use this instead of guessing. The turn pauses until the user answers.", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"questions": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"title":       map[string]any{"type": "string", "description": "The question text."},
							"description": map[string]any{"type": "string", "description": "Optional extra context for the question."},
							"type":        map[string]any{"type": "string", "enum": []string{"boolean", "single_choice", "multiple_choice", "fill_in_blank"}},
							"options": map[string]any{
								"type":        "array",
								"description": "Options for single_choice and multiple_choice questions.",
								"items": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"title":       map[string]any{"type": "string"},
										"description": map[string]any{"type": "string"},
									},
									"required": []string{"title"},
								},
							},
						},
						"required": []string{"title", "type"},
					},
				},
			},
			"required": []string{"questions"},
		}),
	)
	return tools
}
