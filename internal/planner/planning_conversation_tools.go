package planner

import "github.com/openai/openai-go"

// conversationPlanSchema is the structured plan shape shared by write_plan and
// update_plan. It mirrors create_plan (agent_loop.go) but, unlike create_plan,
// these tools are NOT terminal — the conversation continues so the user can keep
// refining the plan.
func conversationPlanSchema(discussants int) map[string]any {
	discussantsSchema := map[string]any{
		"type": "array",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":   map[string]any{"type": "string"},
				"aspect": map[string]any{"type": "string"},
			},
			"required": []string{"name", "aspect"},
		},
	}
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
			"discussants": discussantsSchema,
		},
		"required": []string{"title", "background", "host", "discussants"},
	}
}

// conversationTools is the tool set for the conversational planner loop.
func conversationTools(discussants int) []openai.ChatCompletionToolParam {
	return []openai.ChatCompletionToolParam{
		toolDef("search_sources", "Search the web through Firecrawl and return readable sources to ground the plan.", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "Search query to research the discussion topic."},
			},
			"required": []string{"query"},
		}),
		toolDef("crawl_sources", "Read one or more specific URLs and return clean markdown context.", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"urls": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "The http(s) URLs to read.",
				},
			},
			"required": []string{"urls"},
		}),
		toolDef("write_plan", "Write and save the initial panel-discussion plan internally. This does not show the plan to the user; call show_plan after this when the plan is ready to display.", conversationPlanSchema(discussants)),
		toolDef("update_plan", "Replace the current saved plan with a revised one. Provide the FULL updated plan, not a diff. This does not show the plan to the user; call show_plan after this when the revised plan is ready to display.", conversationPlanSchema(discussants)),
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
	}
}
