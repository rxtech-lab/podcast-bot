package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	mcptypes "github.com/mark3labs/mcp-go/mcp"

	debatemcp "github.com/sirily11/debate-bot/internal/mcp"
)

// MCPToolAdapter exposes one MCP tool through the unified Tool interface.
type MCPToolAdapter struct {
	srv  *debatemcp.Server
	tool mcptypes.Tool
}

// Name returns "<server>__<tool>" so we never collide across servers.
func (a *MCPToolAdapter) Name() string {
	return a.srv.Name + "__" + a.tool.Name
}

func (a *MCPToolAdapter) Description() string {
	if a.tool.Description == "" {
		return "MCP tool from " + a.srv.Name
	}
	return a.tool.Description
}

func (a *MCPToolAdapter) Schema() map[string]any {
	if a.tool.RawInputSchema != nil {
		var m map[string]any
		if err := json.Unmarshal(a.tool.RawInputSchema, &m); err == nil {
			return m
		}
	}
	props := map[string]any{}
	for k, v := range a.tool.InputSchema.Properties {
		props[k] = v
	}
	out := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(a.tool.InputSchema.Required) > 0 {
		out["required"] = a.tool.InputSchema.Required
	}
	return out
}

func (a *MCPToolAdapter) Call(ctx context.Context, args map[string]any, _ AgentContext) (string, error) {
	req := mcptypes.CallToolRequest{}
	req.Params.Name = a.tool.Name
	req.Params.Arguments = args
	res, err := a.srv.Client.CallTool(ctx, req)
	if err != nil {
		return "", fmt.Errorf("mcp call %s: %w", a.Name(), err)
	}
	if res.IsError {
		return "", fmt.Errorf("mcp tool %s reported error", a.Name())
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(mcptypes.TextContent); ok {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(tc.Text)
		}
	}
	if b.Len() == 0 && res.StructuredContent != nil {
		if data, err := json.Marshal(res.StructuredContent); err == nil {
			b.Write(data)
		}
	}
	return b.String(), nil
}

// RegisterMCPTools wraps every discovered MCP tool in an adapter and registers it.
func RegisterMCPTools(reg *Registry, mcpTools []debatemcp.ToolWithServer) {
	for _, t := range mcpTools {
		reg.Register(&MCPToolAdapter{srv: t.Server, tool: t.Tool})
	}
}
