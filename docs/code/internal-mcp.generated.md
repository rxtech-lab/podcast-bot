---
slug: code/internal/mcp
title: Package internal/mcp
description: Auto-generated go doc reference for the internal/mcp package.
---

# Package `internal/mcp`

_Generated with `go doc -all ./internal/mcp`. Regenerate with `scripts/gen_go_docs.sh`._

```text
package mcp // import "github.com/sirily11/debate-bot/internal/mcp"


FUNCTIONS

func StopAll(_ context.Context, srvs []*Server)
    StopAll closes every server (kills subprocesses).


TYPES

type Server struct {
	Name   string
	Client *mcpclient.Client
}
    Server wraps an MCP stdio subprocess client.

func StartAll(ctx context.Context, cfg *config.MCPConfig, log *slog.Logger) ([]*Server, error)
    StartAll boots every server in cfg using the transport declared per entry
    (stdio | streamable-http | sse) and runs MCP initialize. Failures are logged
    and skipped (not fatal) so the debate can still run.

type ToolWithServer struct {
	Server *Server
	Tool   mcptypes.Tool
}
    ToolWithServer pairs a discovered tool with the server hosting it.

func ListAllTools(ctx context.Context, srvs []*Server) ([]ToolWithServer, error)
    ListAllTools returns every tool exposed by any server, paired with its
    server.
```
