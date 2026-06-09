package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// DataStoreTool is the plain-text research scratchpad handed to discussion
// participants when topic storage is "plaintext". Each calling agent gets its
// own JSON file under Dir (keyed by the agent's name) holding a flat
// key→value map, so a discussant can stash findings from firecrawl research
// and recall them on a later turn. When topic storage is "mongodb" this tool
// is NOT registered — discussants use the MongoDB MCP server instead.
//
// The tool is a single shared instance in the registry; per-agent isolation
// comes from AgentContext.AgentName(). Calls from one agent are serialized by
// the agent's own turn loop; the mutex guards the rare cross-agent race on the
// shared Dir.
type DataStoreTool struct {
	Dir string
	mu  sync.Mutex
}

// NewDataStoreTool creates a file-backed data store rooted at dir.
func NewDataStoreTool(dir string) *DataStoreTool { return &DataStoreTool{Dir: dir} }

func (t *DataStoreTool) Name() string { return "data_store" }

func (t *DataStoreTool) Description() string {
	return "Your private research scratchpad. Use op=\"save\" with key+value to store a finding, op=\"load\" with key to recall one, or op=\"list\" to see all your saved keys."
}

func (t *DataStoreTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"op": map[string]any{
				"type":        "string",
				"enum":        []string{"save", "load", "list"},
				"description": "The operation: save, load, or list.",
			},
			"key": map[string]any{
				"type":        "string",
				"description": "Identifier for the entry (required for save and load).",
			},
			"value": map[string]any{
				"type":        "string",
				"description": "The text to store (required for save).",
			},
		},
		"required": []string{"op"},
	}
}

func (t *DataStoreTool) Call(_ context.Context, args map[string]any, ag AgentContext) (string, error) {
	op, _ := args["op"].(string)
	key, _ := args["key"].(string)
	value, _ := args["value"].(string)
	op = strings.TrimSpace(op)
	key = strings.TrimSpace(key)

	t.mu.Lock()
	defer t.mu.Unlock()

	path := t.pathFor(ag.AgentName())
	store, err := t.read(path)
	if err != nil {
		return "", err
	}

	switch op {
	case "save":
		if key == "" || strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("save requires both key and value")
		}
		store[key] = value
		if err := t.write(path, store); err != nil {
			return "", err
		}
		return fmt.Sprintf("saved %q", key), nil
	case "load":
		if key == "" {
			return "", fmt.Errorf("load requires key")
		}
		v, ok := store[key]
		if !ok {
			return fmt.Sprintf("no entry for %q", key), nil
		}
		return v, nil
	case "list":
		if len(store) == 0 {
			return "no saved entries", nil
		}
		keys := make([]string, 0, len(store))
		for k := range store {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return strings.Join(keys, "\n"), nil
	default:
		return "", fmt.Errorf("unknown op %q (want save, load, or list)", op)
	}
}

func (t *DataStoreTool) pathFor(agentName string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, agentName)
	if safe == "" {
		safe = "agent"
	}
	return filepath.Join(t.Dir, safe+".json")
}

func (t *DataStoreTool) read(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read data store: %w", err)
	}
	store := map[string]string{}
	if len(raw) == 0 {
		return store, nil
	}
	if err := json.Unmarshal(raw, &store); err != nil {
		return nil, fmt.Errorf("parse data store: %w", err)
	}
	return store, nil
}

func (t *DataStoreTool) write(path string, store map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create data store dir: %w", err)
	}
	raw, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return fmt.Errorf("write data store: %w", err)
	}
	return nil
}
