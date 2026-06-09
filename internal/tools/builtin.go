package tools

import (
	"context"
	"fmt"
	"strings"
)

// TakeNoteTool appends a note to the calling agent's memory file.
type TakeNoteTool struct{}

func (TakeNoteTool) Name() string        { return "take_note" }
func (TakeNoteTool) Description() string { return "Append a personal note to your private memory file." }
func (TakeNoteTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text": map[string]any{
				"type":        "string",
				"description": "The note text to append.",
			},
		},
		"required": []string{"text"},
	}
}
func (TakeNoteTool) Call(_ context.Context, args map[string]any, ag AgentContext) (string, error) {
	text, _ := args["text"].(string)
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("text is empty")
	}
	if err := ag.AppendMemory("- " + text); err != nil {
		return "", err
	}
	return "noted", nil
}

// LookUpQuoteTool searches the running transcript for substring matches.
type LookUpQuoteTool struct{}

func (LookUpQuoteTool) Name() string { return "look_up_quote" }
func (LookUpQuoteTool) Description() string {
	return "Search the running debate transcript for quotes matching a substring. Returns up to 3 attributed hits."
}
func (LookUpQuoteTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Substring to search for (case-insensitive).",
			},
		},
		"required": []string{"query"},
	}
}
func (LookUpQuoteTool) Call(_ context.Context, args map[string]any, ag AgentContext) (string, error) {
	q, _ := args["query"].(string)
	q = strings.TrimSpace(q)
	if q == "" {
		return "", fmt.Errorf("query is empty")
	}
	needle := strings.ToLower(q)
	hits := 0
	var b strings.Builder
	for _, line := range ag.Transcript() {
		if strings.Contains(strings.ToLower(line.Text), needle) {
			fmt.Fprintf(&b, "- %s: %q\n", line.Speaker, line.Text)
			hits++
			if hits >= 3 {
				break
			}
		}
	}
	if hits == 0 {
		return "no matches", nil
	}
	return b.String(), nil
}

// RegisterBuiltins registers all built-in tools.
func RegisterBuiltins(r *Registry) {
	r.Register(TakeNoteTool{})
	r.Register(LookUpQuoteTool{})
}

// RegisterDataStore registers the plain-text research scratchpad rooted at
// dir. Called only for discussion topics whose storage is "plaintext"; the
// MongoDB backend is provided by an MCP server instead, so nothing is
// registered here in that case.
func RegisterDataStore(r *Registry, dir string) {
	r.Register(NewDataStoreTool(dir))
}
