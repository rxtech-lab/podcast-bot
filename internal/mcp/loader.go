package mcp

import (
	"context"
	"fmt"
	"log/slog"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcptypes "github.com/mark3labs/mcp-go/mcp"

	"github.com/sirily11/debate-bot/internal/config"
)

// Server wraps an MCP stdio subprocess client.
type Server struct {
	Name   string
	Client *mcpclient.Client
}

// StartAll boots every server in cfg using the transport declared per entry
// (stdio | streamable-http | sse) and runs MCP initialize. Failures are
// logged and skipped (not fatal) so the debate can still run.
func StartAll(ctx context.Context, cfg *config.MCPConfig, log *slog.Logger) ([]*Server, error) {
	if cfg == nil || len(cfg.MCPServers) == 0 {
		return nil, nil
	}
	var out []*Server
	for name, sc := range cfg.MCPServers {
		c, err := newClient(ctx, sc)
		if err != nil {
			log.Warn("mcp server start failed", "name", name, "transport", sc.ResolvedTransport(), "err", err)
			continue
		}
		init := mcptypes.InitializeRequest{}
		init.Params.ProtocolVersion = mcptypes.LATEST_PROTOCOL_VERSION
		init.Params.ClientInfo = mcptypes.Implementation{Name: "debate-bot", Version: "0.1"}
		if _, err := c.Initialize(ctx, init); err != nil {
			log.Warn("mcp initialize failed", "name", name, "err", err)
			_ = c.Close()
			continue
		}
		out = append(out, &Server{Name: name, Client: c})
		log.Info("mcp server ready", "name", name, "transport", sc.ResolvedTransport())
	}
	return out, nil
}

// newClient instantiates the right transport for sc.
func newClient(ctx context.Context, sc config.MCPServerConfig) (*mcpclient.Client, error) {
	switch sc.ResolvedTransport() {
	case config.MCPTransportStdio:
		envSlice := make([]string, 0, len(sc.Env))
		for k, v := range sc.Env {
			envSlice = append(envSlice, k+"="+v)
		}
		return mcpclient.NewStdioMCPClient(sc.Command, envSlice, sc.Args...)
	case config.MCPTransportStreamableHTTP:
		var opts []transport.StreamableHTTPCOption
		if len(sc.Headers) > 0 {
			opts = append(opts, transport.WithHTTPHeaders(sc.Headers))
		}
		c, err := mcpclient.NewStreamableHttpClient(sc.URL, opts...)
		if err != nil {
			return nil, err
		}
		if err := c.Start(ctx); err != nil {
			_ = c.Close()
			return nil, err
		}
		return c, nil
	case config.MCPTransportSSE:
		var opts []transport.ClientOption
		if len(sc.Headers) > 0 {
			opts = append(opts, transport.WithHeaders(sc.Headers))
		}
		c, err := mcpclient.NewSSEMCPClient(sc.URL, opts...)
		if err != nil {
			return nil, err
		}
		if err := c.Start(ctx); err != nil {
			_ = c.Close()
			return nil, err
		}
		return c, nil
	default:
		return nil, fmt.Errorf("unsupported transport %q", sc.ResolvedTransport())
	}
}

// StopAll closes every server (kills subprocesses).
func StopAll(_ context.Context, srvs []*Server) {
	for _, s := range srvs {
		if s.Client != nil {
			_ = s.Client.Close()
		}
	}
}

// ListAllTools returns every tool exposed by any server, paired with its server.
func ListAllTools(ctx context.Context, srvs []*Server) ([]ToolWithServer, error) {
	var out []ToolWithServer
	for _, s := range srvs {
		res, err := s.Client.ListTools(ctx, mcptypes.ListToolsRequest{})
		if err != nil {
			return nil, fmt.Errorf("list tools (%s): %w", s.Name, err)
		}
		for _, t := range res.Tools {
			out = append(out, ToolWithServer{Server: s, Tool: t})
		}
	}
	return out, nil
}

// ToolWithServer pairs a discovered tool with the server hosting it.
type ToolWithServer struct {
	Server *Server
	Tool   mcptypes.Tool
}
