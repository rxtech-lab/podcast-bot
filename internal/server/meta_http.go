package server

import (
	"net/http"
	"sort"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/llm"
	"github.com/sirily11/debate-bot/internal/planner"
	"github.com/sirily11/debate-bot/internal/tools"
)

// modelsResponse is the body of GET /api/models.
type modelsResponse struct {
	Defaults config.ModelDefaults `json:"defaults"`
	Models   []config.ModelInfo   `json:"models"`
}

// discussionTypeMeta describes a plan type the native clients can offer in the
// new-discussion sheet.
type discussionTypeMeta struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type discussionTypesResponse struct {
	Types []discussionTypeMeta `json:"types"`
}

type templatesResponse struct {
	Templates []planner.Template `json:"templates"`
}

func (s *Server) handleDiscussionTypes(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, discussionTypesResponse{Types: []discussionTypeMeta{
		{ID: config.ContentTypeDiscussion, Label: "Discussion"},
		{ID: config.ContentTypeAudioBook, Label: "Audio Book"},
	}})
}

func (s *Server) handleTemplates(w http.ResponseWriter, r *http.Request) {
	contentType := r.URL.Query().Get("type")
	if contentType == "" {
		contentType = config.ContentTypeDiscussion
	}
	writeJSON(w, templatesResponse{Templates: planner.TemplatesByType(contentType)})
}

// handleModels enumerates the LLM models the engine can drive agents with, so
// the dashboard and app can populate their per-speaker model pickers. The list
// is fetched live from the OpenAI-compatible gateway (GET /models) and cached
// in Redis for 24h; a fetch failure degrades to an empty roster (the picker
// keeps whatever model the speaker already has) rather than erroring.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	defaults := config.DefaultsForEnv(s.d.Env)

	if cached, ok := s.d.ModelCatalog.Get(r.Context()); ok {
		writeJSON(w, modelsResponse{Defaults: defaults, Models: cached})
		return
	}

	var models []config.ModelInfo
	if s.d.Env != nil {
		ids, err := llm.ListModels(r.Context(), s.d.Env.OpenAIBaseURL, s.d.Env.OpenAIKey)
		if err != nil {
			if s.d.Log != nil {
				s.d.Log.Warn("list gateway models", "err", err)
			}
		} else {
			models = config.ModelsFromIDs(ids, defaults)
			s.d.ModelCatalog.Set(r.Context(), models)
		}
	}
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
