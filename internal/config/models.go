package config

import "strings"

// ModelInfo describes one selectable LLM the engine can drive an agent with.
// Models are referenced by ID (the string written into an AgentSpec.Model
// field, e.g. "anthropic/claude-opus-4-8"); Label/Provider/Capabilities are
// presentation hints for the dashboard's model pickers. The roster is fetched
// live from the OpenAI-compatible gateway (see llm.ListModels) — there is no
// curated list — so Label defaults to the raw id and Provider is derived from
// the "provider/model" id prefix.
type ModelInfo struct {
	ID           string   `json:"id"`
	Label        string   `json:"label"`
	Provider     string   `json:"provider"`
	Capabilities []string `json:"capabilities,omitempty"`
	// DefaultFor lists the engine roles this model is the env-configured
	// default for (any of "host", "scene_planner", "compression"). Empty for
	// models that are merely available but not a default.
	DefaultFor []string `json:"default_for,omitempty"`
}

// ModelDefaults maps engine roles to the configured default model ids.
type ModelDefaults struct {
	Host         string `json:"host"`
	ScenePlanner string `json:"scene_planner"`
	Compression  string `json:"compression"`
}

// DefaultsForEnv reports the env-configured default model id for each engine
// role so the dashboard/app can preselect sensible models.
func DefaultsForEnv(e *Env) ModelDefaults {
	if e == nil {
		return ModelDefaults{}
	}
	return ModelDefaults{
		Host:         e.HostModel,
		ScenePlanner: e.ScenePlannerModel,
		Compression:  e.CompressionModel,
	}
}

// ModelsFromIDs turns a flat list of gateway model ids into ModelInfo entries,
// deriving Provider from the "provider/model" id prefix and stamping DefaultFor
// for any id the env points at as a role default. Blank/duplicate ids are
// dropped so the resulting roster is clean for the pickers.
func ModelsFromIDs(ids []string, defaults ModelDefaults) []ModelInfo {
	out := make([]ModelInfo, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		provider := "openai"
		if i := strings.Index(id, "/"); i > 0 {
			provider = id[:i]
		}
		info := ModelInfo{ID: id, Label: id, Provider: provider}
		if id == defaults.Host {
			info.DefaultFor = append(info.DefaultFor, "host")
		}
		if id == defaults.ScenePlanner {
			info.DefaultFor = append(info.DefaultFor, "scene_planner")
		}
		if id == defaults.Compression {
			info.DefaultFor = append(info.DefaultFor, "compression")
		}
		out = append(out, info)
	}
	return out
}
