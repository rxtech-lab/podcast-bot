package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/shared"
)

// TranscriptLine mirrors the orchestrator's transcript entry, copied here
// so the tools package has no upward dependencies. Fields kept loose.
type TranscriptLine struct {
	Speaker string
	Role    string
	Side    string
	Text    string
	At      time.Time
}

// AgentContext is what a tool sees when it runs. It is the bridge between
// stateless Tool implementations and the calling agent.
type AgentContext interface {
	AgentName() string
	AppendMemory(text string) error
	Transcript() []TranscriptLine
}

// Tool is the unified contract used by every agent in the debate.
type Tool interface {
	Name() string
	Description() string
	Schema() map[string]any // JSON Schema for parameters
	Call(ctx context.Context, args map[string]any, ag AgentContext) (string, error)
}

// Registry holds all tools available to agents (built-ins + MCP-bridged).
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// New creates an empty Registry.
func New() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

// Register adds a tool, replacing any prior tool with the same name.
func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name()] = t
}

// All returns a snapshot of registered tools.
func (r *Registry) All() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out
}

// AsOpenAIParams converts every registered tool into an OpenAI tool param.
func (r *Registry) AsOpenAIParams() []openai.ChatCompletionToolParam {
	tools := r.All()
	out := make([]openai.ChatCompletionToolParam, 0, len(tools))
	for _, t := range tools {
		schema := t.Schema()
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, openai.ChatCompletionToolParam{
			Function: shared.FunctionDefinitionParam{
				Name:        t.Name(),
				Description: openai.String(t.Description()),
				Parameters:  schema,
			},
		})
	}
	return out
}

// Dispatch calls the named tool with the given JSON-decoded arguments.
func (r *Registry) Dispatch(ctx context.Context, name, jsonArgs string, ag AgentContext) (string, error) {
	r.mu.RLock()
	t, ok := r.tools[name]
	r.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", name)
	}
	var args map[string]any
	if jsonArgs == "" {
		args = map[string]any{}
	} else if err := json.Unmarshal([]byte(jsonArgs), &args); err != nil {
		return "", fmt.Errorf("decode args for %s: %w", name, err)
	}
	return t.Call(ctx, args, ag)
}
