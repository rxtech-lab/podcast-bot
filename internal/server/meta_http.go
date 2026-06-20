package server

import (
	"net/http"
	"sort"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/tools"
)

// modelsResponse is the body of GET /api/models.
type modelsResponse struct {
	Defaults config.ModelDefaults `json:"defaults"`
	Models   []config.ModelInfo   `json:"models"`
}

// handleModels enumerates the LLM models the engine can drive agents with, so
// the dashboard can populate its per-agent model pickers. The list is the
// curated roster augmented with any env-configured defaults.
func (s *Server) handleModels(w http.ResponseWriter, _ *http.Request) {
	models, defaults := config.ModelsForEnv(s.d.Env)
	writeJSON(w, modelsResponse{Defaults: defaults, Models: models})
}

// toolMeta is the dashboard-facing description of one tool.
type toolMeta struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	Schema       map[string]any `json:"schema,omitempty"`
	Source       string         `json:"source"` // builtin | datastore | mcp
	ContentTypes []string       `json:"content_types,omitempty"`
	Roles        []string       `json:"roles,omitempty"`
	// Dynamic marks tools (MCP-provided) whose concrete schema is only known
	// at runtime after a live handshake; the snapshot lists the declaring
	// server but cannot enumerate individual tool schemas.
	Dynamic bool `json:"dynamic,omitempty"`
}

// toolsResponse is the body of GET /api/tools.
type toolsResponse struct {
	Tools []toolMeta `json:"tools"`
}

// toolProfile maps a known tool name to the content types + agent roles it
// applies to. Tools not listed fall back to "all content types / speaking
// roles" so a newly-added built-in still surfaces sensibly.
var toolProfiles = map[string]struct {
	contentTypes []string
	roles        []string
	source       string
}{
	"take_note":     {nil, []string{"host", "discussant", "affirmative", "negative", "judge", "player", "puzzle_host", "series_host"}, "builtin"},
	"look_up_quote": {nil, []string{"host", "discussant", "affirmative", "negative", "judge", "player", "puzzle_host"}, "builtin"},
	"data_store":    {[]string{config.ContentTypeDiscussion}, []string{"discussant"}, "datastore"},
}

// handleTools enumerates the engine's statically-known tools plus the MCP
// servers declared in mcp.json (whose individual tools are dynamic). The
// dashboard uses this to annotate agent nodes with their available tools.
func (s *Server) handleTools(w http.ResponseWriter, _ *http.Request) {
	out := make([]toolMeta, 0, 8)

	// Built-ins + the discussion data-store scratchpad.
	for _, t := range tools.Snapshot(true) {
		prof := toolProfiles[t.Name()]
		source := prof.source
		if source == "" {
			source = "builtin"
		}
		out = append(out, toolMeta{
			Name:         t.Name(),
			Description:  t.Description(),
			Schema:       t.Schema(),
			Source:       source,
			ContentTypes: prof.contentTypes,
			Roles:        prof.roles,
		})
	}

	// MCP servers declared in mcp.json — listed by server name; their
	// concrete tools are resolved only at orchestrator boot.
	if s.d.MCPCfg != nil {
		names := make([]string, 0, len(s.d.MCPCfg.MCPServers))
		for name := range s.d.MCPCfg.MCPServers {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			out = append(out, toolMeta{
				Name:         name,
				Description:  "MCP server (tools resolved at runtime).",
				Source:       "mcp",
				ContentTypes: []string{config.ContentTypeDiscussion},
				Roles:        []string{"discussant"},
				Dynamic:      true,
			})
		}
	}

	writeJSON(w, toolsResponse{Tools: out})
}
