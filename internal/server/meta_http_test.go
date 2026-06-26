package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/eventbus"
	"github.com/sirily11/debate-bot/internal/planner"
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
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("unexpected upstream path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"openai/custom-host","object":"model","created":0,"owned_by":"test"},{"id":"anthropic/claude-sonnet-4-5","object":"model","created":0,"owned_by":"test"}]}`))
	}))
	t.Cleanup(upstream.Close)

	env := &config.Env{
		OpenAIBaseURL:     upstream.URL,
		OpenAIKey:         "test-key",
		HostModel:         "openai/custom-host",
		ScenePlannerModel: "openai/custom-host",
		CompressionModel:  "openai/gpt-4o-mini",
	}
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
	var sawEnvDefault, sawUpstream bool
	for _, m := range out.Models {
		if m.ID == "openai/custom-host" {
			sawEnvDefault = len(m.DefaultFor) == 2 && m.Provider == "openai"
		}
		if m.ID == "anthropic/claude-sonnet-4-5" {
			sawUpstream = m.Provider == "anthropic"
		}
	}
	if !sawEnvDefault {
		t.Error("env-default host model not present in upstream models list")
	}
	if !sawUpstream {
		t.Error("upstream model missing from models list")
	}
}

func TestHandleDiscussionTypes(t *testing.T) {
	ts := newMetaServer(t, nil, nil)

	resp, err := http.Get(ts.URL + "/api/discussion-types")
	if err != nil {
		t.Fatalf("get discussion types: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out discussionTypesResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Types) != 1 {
		t.Fatalf("types length = %d, want 1", len(out.Types))
	}
	if out.Types[0].ID != config.ContentTypeDiscussion {
		t.Fatalf("type id = %q, want %q", out.Types[0].ID, config.ContentTypeDiscussion)
	}
}

func TestHandleTemplates(t *testing.T) {
	ts := newMetaServer(t, nil, nil)

	resp, err := http.Get(ts.URL + "/api/templates?type=discussion")
	if err != nil {
		t.Fatalf("get templates: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out templatesResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Templates) != 1 {
		t.Fatalf("templates length = %d, want 1", len(out.Templates))
	}
	if out.Templates[0].ID != planner.DefaultTemplateID {
		t.Fatalf("template id = %q, want %q", out.Templates[0].ID, planner.DefaultTemplateID)
	}
	if out.Templates[0].Type != config.ContentTypeDiscussion {
		t.Fatalf("template type = %q, want %q", out.Templates[0].Type, config.ContentTypeDiscussion)
	}
	if out.Templates[0].Schema == nil {
		t.Fatal("template schema is nil")
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
