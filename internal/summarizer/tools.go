package summarizer

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/shared"
)

// summarySession accumulates the document parts the agent writes across
// write_summary_chunk calls and assembles them in order on finalize.
type summarySession struct {
	parts     map[int]string
	finalized bool
}

func (s *summarySession) hasParts() bool {
	for _, p := range s.parts {
		if strings.TrimSpace(p) != "" {
			return true
		}
	}
	return false
}

// assemble joins the written parts in ascending part_index order.
func (s *summarySession) assemble() string {
	if len(s.parts) == 0 {
		return ""
	}
	idx := make([]int, 0, len(s.parts))
	for i := range s.parts {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	var sb strings.Builder
	for _, i := range idx {
		chunk := strings.TrimRight(s.parts[i], "\n")
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(chunk)
	}
	return strings.TrimSpace(sb.String())
}

// dispatch executes one tool call, returning the tool result string and whether
// it terminates the loop (finalize_summary).
func (s *summarySession) dispatch(name, jsonArgs string) (string, bool) {
	switch name {
	case "write_summary_chunk":
		var args struct {
			PartIndex int    `json:"part_index"`
			Markdown  string `json:"markdown"`
		}
		if err := json.Unmarshal([]byte(jsonArgs), &args); err != nil {
			return "error: could not parse write_summary_chunk arguments: " + err.Error(), false
		}
		if strings.TrimSpace(args.Markdown) == "" {
			return "error: markdown is empty; provide the Markdown content for this part", false
		}
		if s.parts == nil {
			s.parts = map[int]string{}
		}
		s.parts[args.PartIndex] = args.Markdown
		return fmt.Sprintf("saved part %d (%d parts written so far). Continue with the next part or call finalize_summary.", args.PartIndex, len(s.parts)), false
	case "finalize_summary":
		if !s.hasParts() {
			return "error: no chunks written yet; call write_summary_chunk before finalize_summary", false
		}
		s.finalized = true
		return "summary finalized", true
	default:
		return "error: unknown tool " + name, false
	}
}

func summaryTools() []openai.ChatCompletionToolParam {
	return []openai.ChatCompletionToolParam{
		summaryToolDef("write_summary_chunk",
			"Append one ordered part of the Markdown summary document. Call this one or more times, increasing part_index from 0, to build the document section by section so a long summary is never crammed into a single call.",
			map[string]any{
				"type": "object",
				"properties": map[string]any{
					"part_index": map[string]any{
						"type":        "integer",
						"description": "0-based position of this part in the final document. Parts are concatenated in ascending order.",
					},
					"markdown": map[string]any{
						"type":        "string",
						"description": "The Markdown content of this part. The first part must begin with the title and the Mermaid flowchart overview.",
					},
				},
				"required": []string{"part_index", "markdown"},
			}),
		summaryToolDef("finalize_summary",
			"Commit the written parts as the final summary document. Must be the last tool call, made alone after all write_summary_chunk calls.",
			map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			}),
	}
}

func summaryToolDef(name, description string, schema map[string]any) openai.ChatCompletionToolParam {
	return openai.ChatCompletionToolParam{
		Function: shared.FunctionDefinitionParam{
			Name:        name,
			Description: openai.String(description),
			Parameters:  schema,
		},
	}
}

// summarySystemPrompt is the agent's instruction set: the required professional
// structure and the Mermaid-flowchart constraint that keeps every diagram
// renderable by the native client renderer.
func summarySystemPrompt(language string) string {
	lang := strings.TrimSpace(language)
	if lang == "" {
		lang = "the podcast's language"
	}
	return `You are a meticulous analyst writing a professional summary document of a podcast/panel discussion for a reader who has NOT listened to it. The reader should grasp the whole conversation quickly: who said what, why, the evidence, and the takeaways.

Run as an agent loop using the provided tools:
- Use write_summary_chunk one or more times to write the document in ordered parts (part_index starting at 0). Split long output across multiple calls.
- After all parts are written, call finalize_summary alone to commit. Do not write the summary as plain assistant text outside the tools.

Write the document in ` + lang + ` and use clean GitHub-Flavored Markdown with this structure:

1. A level-1 title (the podcast title or a concise descriptive title).
2. "## Overview" — a short TL;DR (2-4 sentences) of what the discussion was about and its outcome.
3. A Mermaid diagram in a fenced code block tagged ` + "`mermaid`" + ` that maps the main topics, ideas, and how they connect, so the reader can on-board fast.
   - The diagram MUST be a flowchart: start it with "flowchart TD" (top-down). Do NOT use mindmap, pie, gantt, journey, or other diagram types — only flowchart is supported by the renderer.
   - Keep node labels short; quote labels with spaces, e.g. A["Main topic"] --> B["Sub-idea"]. Avoid parentheses and special characters inside labels.
4. "## Participants & Positions" — for EACH distinct speaker: a "### Name" heading, then their core opinion/stance, the key evidence or reasoning they gave, and any sources/references they cited (as a bullet list). Be faithful to what they actually argued; do not invent sources.
5. "## Points of Agreement and Disagreement" — where participants converged and where they clashed.
6. "## Conclusion" — the overall takeaway and any open questions.

Be accurate and neutral. Attribute claims to the speaker who made them. Prefer concise bullet points and short paragraphs over long prose.`
}
