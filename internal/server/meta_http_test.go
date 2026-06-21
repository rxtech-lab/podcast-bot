package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/eventbus"
)

func newMetaServer(t *testing.T, env *config.Env, mcp *config.MCPConfig) *httptest.Server {
	t.Helper()
	bus := eventbus.New(nil)
	srv := New(Deps{
		Bus:      bus,
		Sessions: NewSessionRegistry(),
		Log:      slog.Default(),
		Env:      env,
		MCPCfg:   mcp,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		bus.Close()
	})
	return ts
}

func TestHandleModels(t *testing.T) {
	env := &config.Env{HostModel: "openai/custom-host", ScenePlannerModel: "openai/custom-host", CompressionModel: "openai/gpt-4o-mini"}
	ts := newMetaServer(t, env, nil)

	resp, err := http.Get(ts.URL + "/api/models")
	if err != nil {
		t.Fatalf("get models: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out modelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Defaults.Host != "openai/custom-host" {
		t.Fatalf("defaults.host = %q, want openai/custom-host", out.Defaults.Host)
	}
	// The curated roster plus the (new) env-default host model.
	var sawEnvDefault, sawCurated bool
	for _, m := range out.Models {
		if m.ID == "openai/custom-host" {
			sawEnvDefault = true
		}
		if m.ID == "anthropic/claude-opus-4-8" {
			sawCurated = true
		}
	}
	if !sawEnvDefault {
		t.Error("env-default host model not present in models list")
	}
	if !sawCurated {
		t.Error("curated models missing from list")
	}
}

func TestHandleTools(t *testing.T) {
	mcp := &config.MCPConfig{MCPServers: map[string]config.MCPServerConfig{
		"firecrawl": {URL: "https://example.com/mcp"},
	}}
	ts := newMetaServer(t, nil, mcp)

	resp, err := http.Get(ts.URL + "/api/tools")
	if err != nil {
		t.Fatalf("get tools: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out toolsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var sawBuiltin, sawDataStore, sawMCP bool
	for _, tl := range out.Tools {
		switch tl.Name {
		case "take_note":
			sawBuiltin = true
		case "data_store":
			sawDataStore = true
		case "firecrawl":
			sawMCP = tl.Dynamic && tl.Source == "mcp"
		}
	}
	if !sawBuiltin {
		t.Error("built-in tool take_note missing")
	}
	if !sawDataStore {
		t.Error("discussion data_store tool missing")
	}
	if !sawMCP {
		t.Error("declared MCP server not listed as a dynamic tool")
	}
}
