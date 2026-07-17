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
	// Type is the gateway's model kind (ModelTypeLanguage, ModelTypeEmbedding,
	// "image", ...). Empty when the gateway doesn't type its models; such
	// entries are treated as chat models.
	Type string `json:"type,omitempty"`
	// DefaultFor lists the engine roles this model is the env-configured
	// default for (any of "host", "scene_planner", "compression"). Empty for
	// models that are merely available but not a default.
	DefaultFor []string `json:"default_for,omitempty"`
}

// Gateway model kinds this codebase cares about (the gateway advertises more:
// "image", "video", "reranking", ...).
const (
	ModelTypeLanguage  = "language"
	ModelTypeEmbedding = "embedding"
)

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

// ModelDescriptor is the raw gateway roster entry (id + optional type) that
// ModelsFromDescriptors expands into a ModelInfo. Kept as a local mirror of
// llm.ModelEntry so config doesn't import llm.
type ModelDescriptor struct {
	ID   string
	Type string
}

// ModelsFromDescriptors turns the gateway model roster into ModelInfo entries,
// deriving Provider from the "provider/model" id prefix, carrying the gateway
// type through, and stamping DefaultFor for any id the env points at as a role
// default. Blank/duplicate ids are dropped so the resulting roster is clean
// for the pickers.
func ModelsFromDescriptors(entries []ModelDescriptor, defaults ModelDefaults) []ModelInfo {
	out := make([]ModelInfo, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		id := strings.TrimSpace(e.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		provider := "openai"
		if i := strings.Index(id, "/"); i > 0 {
			provider = id[:i]
		}
		out = append(out, ModelInfo{
			ID:         id,
			Label:      id,
			Provider:   provider,
			Type:       strings.TrimSpace(e.Type),
			DefaultFor: rolesDefaultingTo(id, defaults),
		})
	}
	return out
}

// ModelsFromIDs turns a flat list of gateway model ids into ModelInfo entries.
// See ModelsFromDescriptors; ids carry no type.
func ModelsFromIDs(ids []string, defaults ModelDefaults) []ModelInfo {
	entries := make([]ModelDescriptor, 0, len(ids))
	for _, id := range ids {
		entries = append(entries, ModelDescriptor{ID: id})
	}
	return ModelsFromDescriptors(entries, defaults)
}

// AnnotateModelDefaults re-stamps DefaultFor on an already-built roster (a
// cache hit) against the CURRENT defaults, preserving every other field —
// notably Type, which the id-only rebuild used to drop.
func AnnotateModelDefaults(models []ModelInfo, defaults ModelDefaults) []ModelInfo {
	out := make([]ModelInfo, 0, len(models))
	for _, m := range models {
		m.DefaultFor = rolesDefaultingTo(m.ID, defaults)
		out = append(out, m)
	}
	return out
}

func rolesDefaultingTo(id string, defaults ModelDefaults) []string {
	var roles []string
	if id == defaults.Host {
		roles = append(roles, "host")
	}
	if id == defaults.ScenePlanner {
		roles = append(roles, "scene_planner")
	}
	if id == defaults.Compression {
		roles = append(roles, "compression")
	}
	return roles
}
