package planner

import (
	"github.com/openai/openai-go"

	"github.com/sirily11/debate-bot/internal/config"
)

// conversationPlanSchema is the structured plan shape shared by write_plan and
// update_plan. It mirrors create_plan (agent_loop.go) but, unlike create_plan,
// these tools are NOT terminal — the conversation continues so the user can keep
// refining the plan.
func conversationPlanSchema(template string) map[string]any {
	return TemplateSchema(config.ContentTypeDiscussion, template)
}

// conversationTools is the tool set for the conversational planner loop.
func conversationTools(template string) []openai.ChatCompletionToolParam {
	tools := []openai.ChatCompletionToolParam{}
	if IsResearchTemplate(template) {
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
		toolDef("write_plan", "Write and save the initial panel-discussion plan internally. This does not show the plan to the user; call show_plan after this when the plan is ready to display.", conversationPlanSchema(template)),
		toolDef("update_plan", "Replace the current saved plan with a revised one. Provide the FULL updated plan, not a diff. This does not show the plan to the user; call show_plan after this when the revised plan is ready to display.", conversationPlanSchema(template)),
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
