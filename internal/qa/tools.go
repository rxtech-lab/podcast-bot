package qa

import "github.com/openai/openai-go"

func toolDef(name, description string, schema map[string]any) openai.ChatCompletionToolParam {
	return openai.ChatCompletionToolParam{
		Function: openai.FunctionDefinitionParam{
			Name:        name,
			Description: openai.String(description),
			Parameters:  schema,
		},
	}
}

// toolSet returns the scope's tool roster. Podcast scope omits discussion_id
// everywhere (the conversation is bound to one podcast); global scope
// requires it on the per-podcast tools.
func toolSet(scope string) []openai.ChatCompletionToolParam {
	if scope == ScopeGlobal {
		return globalTools()
	}
	return podcastTools()
}

func podcastTools() []openai.ChatCompletionToolParam {
	return []openai.ChatCompletionToolParam{
		toolDef("search_summary", "Read this podcast's generated summary. Use this first for general questions about the episode's content, themes, arguments, or conclusions; use search_content only when the summary lacks the needed detail.", map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}),
		toolDef("search_content", "Semantic search over this podcast's transcript and research source material. Returns relevant passages with speakers, timestamps, and similarity scores. Use for exact details, quotes, or facts not covered by search_summary.", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "What to look for, phrased as a natural question or topic."},
			},
			"required": []string{"query"},
		}),
		toolDef("get_sources", "List the research sources (title, URL, snippet) behind this podcast.", map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}),
		toolDef("show_highlight_lines", "Display several exact quotes from this podcast together. Use one call after search_content and copy each quote from the retrieved transcript text. This is a terminal presentation tool.", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"highlights": map[string]any{
					"type":        "array",
					"items":       highlightSchema(false),
					"description": "Every quote to display together in one card.",
				},
			},
			"required": []string{"highlights"},
		}),
		toolDef("show_transcript", "Display a transcript excerpt card to the user covering the given time range (milliseconds). Use when the user asks to see what was said.", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"start_ms": map[string]any{"type": "integer", "description": "Range start in milliseconds."},
				"end_ms":   map[string]any{"type": "integer", "description": "Range end in milliseconds."},
			},
			"required": []string{"start_ms", "end_ms"},
		}),
		toolDef("show_sources", "Display this podcast's source cards to the user. Optionally restrict to specific source URLs.", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"urls": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Subset of source URLs to display; omit to show all.",
				},
			},
		}),
		documentTool("display_mindmap", "Display this podcast's generated mindmap. Use when the user asks to view or open the mindmap.", false),
		documentTool("display_ppt", "Display this podcast's generated slide deck. Use when the user asks to view or open the PPT or presentation.", false),
		writeDocumentTool(false),
	}
}

func globalTools() []openai.ChatCompletionToolParam {
	return []openai.ChatCompletionToolParam{
		toolDef("search_podcasts", "Find the user's podcasts by title/topic keywords. Returns discussion_id, title, and topic for each match.", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "Title or topic keywords."},
			},
			"required": []string{"query"},
		}),
		toolDef("search_summary", "Search the generated summaries in the user's podcast library. Use this first for general content, theme, argument, comparison, or conclusion questions; optionally restrict it to one known podcast.", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":         map[string]any{"type": "string", "description": "Keywords or a natural-language description of what to find in podcast summaries."},
				"discussion_id": map[string]any{"type": "string", "description": "Optional: read the summary of one known podcast."},
			},
			"required": []string{"query"},
		}),
		toolDef("search_content", "Semantic search over the transcripts and source material of every podcast in the user's library. Use for exact details, quotes, speakers, timestamps, or information not covered by search_summary.", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":         map[string]any{"type": "string", "description": "What to look for, phrased as a natural question or topic."},
				"discussion_id": map[string]any{"type": "string", "description": "Optional: restrict the search to one podcast."},
			},
			"required": []string{"query"},
		}),
		toolDef("get_sources", "List one podcast's research sources (title, URL, snippet).", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"discussion_id": map[string]any{"type": "string", "description": "The podcast to list sources for."},
			},
			"required": []string{"discussion_id"},
		}),
		toolDef("display_podcasts", "Display all selected podcasts together in one tappable cover-art grid. Use one call for the complete search/list result. This is a terminal presentation tool.", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"discussion_ids": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "All podcast discussion IDs to display, in desired order.",
				},
			},
			"required": []string{"discussion_ids"},
		}),
		toolDef("show_podcasts", "Display grounded highlights for one or more podcasts together. Use one call containing every podcast; never call once per podcast. Quotes must come from search_content. This is a terminal presentation tool.", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"podcasts": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"discussion_id": map[string]any{"type": "string", "description": "Podcast discussion ID."},
							"highlights": map[string]any{
								"type":        "array",
								"items":       highlightSchema(false),
								"description": "Grounded quotes to show under this podcast.",
							},
						},
						"required": []string{"discussion_id", "highlights"},
					},
				},
			},
			"required": []string{"podcasts"},
		}),
		toolDef("show_highlight_lines", "Display several exact transcript quotes, potentially from several podcasts, grouped together in one card. Use one call containing every quote. This is a terminal presentation tool.", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"highlights": map[string]any{
					"type":        "array",
					"items":       highlightSchema(true),
					"description": "Every quote to display together.",
				},
			},
			"required": []string{"highlights"},
		}),
		toolDef("show_transcript", "Display a transcript excerpt card for one podcast covering the given time range (milliseconds).", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"discussion_id": map[string]any{"type": "string", "description": "The podcast the excerpt is from."},
				"start_ms":      map[string]any{"type": "integer", "description": "Range start in milliseconds."},
				"end_ms":        map[string]any{"type": "integer", "description": "Range end in milliseconds."},
			},
			"required": []string{"discussion_id", "start_ms", "end_ms"},
		}),
		toolDef("show_sources", "Display one podcast's source cards to the user. Optionally restrict to specific source URLs.", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"discussion_id": map[string]any{"type": "string", "description": "The podcast whose sources to display."},
				"urls": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Subset of source URLs to display; omit to show all.",
				},
			},
			"required": []string{"discussion_id"},
		}),
		documentTool("display_mindmap", "Display one podcast's generated mindmap. Use when the user asks to view or open its mindmap.", true),
		documentTool("display_ppt", "Display one podcast's generated slide deck. Use when the user asks to view or open its PPT or presentation.", true),
		writeDocumentTool(true),
	}
}

func writeDocumentTool(allowsDiscussionID bool) openai.ChatCompletionToolParam {
	properties := map[string]any{
		"title": map[string]any{"type": "string", "description": "A concise document title."},
		"markdown": map[string]any{
			"type":        "string",
			"description": "The complete document in Markdown. Use a fenced mermaid block when a diagram materially improves understanding.",
		},
	}
	if allowsDiscussionID {
		properties["discussion_id"] = map[string]any{
			"type":        "string",
			"description": "Optional podcast to link when this document belongs to exactly one verified podcast. Omit for a global or multi-podcast document.",
		}
	}
	return toolDef("write_document", "Write and save a persistent document for the user. This is a terminal presentation tool.", map[string]any{
		"type": "object", "properties": properties, "required": []string{"title", "markdown"},
	})
}

func documentTool(name, description string, requireDiscussionID bool) openai.ChatCompletionToolParam {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
	if requireDiscussionID {
		schema["properties"] = map[string]any{
			"discussion_id": map[string]any{"type": "string", "description": "The podcast whose document should be displayed."},
		}
		schema["required"] = []string{"discussion_id"}
	}
	return toolDef(name, description, schema)
}

func highlightSchema(includeDiscussionID bool) map[string]any {
	properties := map[string]any{
		"start_ms": map[string]any{"type": "integer", "description": "Start of the retrieved transcript range in milliseconds."},
		"end_ms":   map[string]any{"type": "integer", "description": "End of the retrieved transcript range in milliseconds."},
		"quote":    map[string]any{"type": "string", "description": "Exact quote copied from the retrieved transcript text."},
	}
	required := []string{"start_ms", "end_ms", "quote"}
	if includeDiscussionID {
		properties["discussion_id"] = map[string]any{"type": "string", "description": "Podcast discussion ID."}
		required = append([]string{"discussion_id"}, required...)
	}
	return map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   required,
	}
}
