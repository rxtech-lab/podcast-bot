package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// MCPTransport names the transport flavour for an MCP server.
type MCPTransport string

const (
	// MCPTransportStdio launches a local subprocess and talks JSON-RPC over stdio.
	MCPTransportStdio MCPTransport = "stdio"
	// MCPTransportStreamableHTTP uses the modern streamable-http MCP transport.
	MCPTransportStreamableHTTP MCPTransport = "streamable-http"
	// MCPTransportSSE uses the older HTTP+SSE transport.
	MCPTransportSSE MCPTransport = "sse"
)

// MCPServerConfig matches the Claude-Desktop mcp.json shape per server,
// extended to support remote HTTP transports (streamable-http, sse).
//
// Stdio entry:
//
//	"name": { "command": "npx", "args": [...], "env": {"K":"V"} }
//
// Remote (streamable-http or sse) entry:
//
//	"name": {
//	  "url":       "https://example.com/mcp",
//	  "transport": "streamable-http",   // optional; defaults to streamable-http when url is set
//	  "headers":   { "Authorization": "Bearer ..." }
//	}
type MCPServerConfig struct {
	// Stdio fields.
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`

	// HTTP fields.
	URL       string            `json:"url,omitempty"`
	Transport MCPTransport      `json:"transport,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}

// ResolvedTransport returns the transport this entry should use, applying
// defaults: command-only → stdio; url-only → streamable-http; explicit
// transport overrides both.
func (c MCPServerConfig) ResolvedTransport() MCPTransport {
	if c.Transport != "" {
		return c.Transport
	}
	if c.URL != "" {
		return MCPTransportStreamableHTTP
	}
	return MCPTransportStdio
}

// Validate returns an error if neither a command nor a url is provided, or
// if mutually exclusive fields are mixed in confusing ways.
func (c MCPServerConfig) Validate(name string) error {
	hasCmd := c.Command != ""
	hasURL := c.URL != ""
	if !hasCmd && !hasURL {
		return fmt.Errorf("mcp server %q: either command or url is required", name)
	}
	if hasCmd && hasURL {
		return fmt.Errorf("mcp server %q: cannot set both command and url", name)
	}
	switch c.ResolvedTransport() {
	case MCPTransportStdio:
		if !hasCmd {
			return fmt.Errorf("mcp server %q: stdio transport needs command", name)
		}
	case MCPTransportStreamableHTTP, MCPTransportSSE:
		if !hasURL {
			return fmt.Errorf("mcp server %q: %s transport needs url", name, c.Transport)
		}
	default:
		return fmt.Errorf("mcp server %q: unknown transport %q", name, c.Transport)
	}
	return nil
}

// MCPConfig is the top-level mcp.json structure.
type MCPConfig struct {
	MCPServers map[string]MCPServerConfig `json:"mcpServers"`
}

// LoadMCPConfig reads an mcp.json. Returns an empty config if path is empty
// or the file does not exist.
func LoadMCPConfig(path string) (*MCPConfig, error) {
	if path == "" {
		return &MCPConfig{MCPServers: map[string]MCPServerConfig{}}, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &MCPConfig{MCPServers: map[string]MCPServerConfig{}}, nil
		}
		return nil, fmt.Errorf("read mcp.json: %w", err)
	}
	var c MCPConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse mcp.json: %w", err)
	}
	if c.MCPServers == nil {
		c.MCPServers = map[string]MCPServerConfig{}
	}
	for name, sc := range c.MCPServers {
		if err := sc.Validate(name); err != nil {
			return nil, err
		}
	}
	return &c, nil
}
