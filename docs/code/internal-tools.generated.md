---
slug: code/internal/tools
title: Package internal/tools
description: Auto-generated go doc reference for the internal/tools package.
---

# Package `internal/tools`

_Generated with `go doc -all ./internal/tools`. Regenerate with `scripts/gen_go_docs.sh`._

```text
package tools // import "github.com/sirily11/debate-bot/internal/tools"


FUNCTIONS

func RegisterBuiltins(r *Registry)
    RegisterBuiltins registers all built-in tools.

func RegisterMCPTools(reg *Registry, mcpTools []debatemcp.ToolWithServer)
    RegisterMCPTools wraps every discovered MCP tool in an adapter and registers
    it.


TYPES

type AgentContext interface {
	AgentName() string
	AppendMemory(text string) error
	Transcript() []TranscriptLine
}
    AgentContext is what a tool sees when it runs. It is the bridge between
    stateless Tool implementations and the calling agent.

type LookUpQuoteTool struct{}
    LookUpQuoteTool searches the running transcript for substring matches.

func (LookUpQuoteTool) Call(_ context.Context, args map[string]any, ag AgentContext) (string, error)

func (LookUpQuoteTool) Description() string

func (LookUpQuoteTool) Name() string

func (LookUpQuoteTool) Schema() map[string]any

type MCPToolAdapter struct {
	// Has unexported fields.
}
    MCPToolAdapter exposes one MCP tool through the unified Tool interface.

func (a *MCPToolAdapter) Call(ctx context.Context, args map[string]any, _ AgentContext) (string, error)

func (a *MCPToolAdapter) Description() string

func (a *MCPToolAdapter) Name() string
    Name returns "<server>__<tool>" so we never collide across servers.

func (a *MCPToolAdapter) Schema() map[string]any

type Registry struct {
	// Has unexported fields.
}
    Registry holds all tools available to agents (built-ins + MCP-bridged).

func New() *Registry
    New creates an empty Registry.

func (r *Registry) All() []Tool
    All returns a snapshot of registered tools.

func (r *Registry) AsOpenAIParams() []openai.ChatCompletionToolParam
    AsOpenAIParams converts every registered tool into an OpenAI tool param.

func (r *Registry) Dispatch(ctx context.Context, name, jsonArgs string, ag AgentContext) (string, error)
    Dispatch calls the named tool with the given JSON-decoded arguments.

func (r *Registry) Register(t Tool)
    Register adds a tool, replacing any prior tool with the same name.

type TakeNoteTool struct{}
    TakeNoteTool appends a note to the calling agent's memory file.

func (TakeNoteTool) Call(_ context.Context, args map[string]any, ag AgentContext) (string, error)

func (TakeNoteTool) Description() string

func (TakeNoteTool) Name() string

func (TakeNoteTool) Schema() map[string]any

type Tool interface {
	Name() string
	Description() string
	Schema() map[string]any // JSON Schema for parameters
	Call(ctx context.Context, args map[string]any, ag AgentContext) (string, error)
}
    Tool is the unified contract used by every agent in the debate.

type TranscriptLine struct {
	Speaker string
	Role    string
	Side    string
	Text    string
	At      time.Time
}
    TranscriptLine mirrors the orchestrator's transcript entry, copied here so
    the tools package has no upward dependencies. Fields kept loose.
```
