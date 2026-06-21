package config

// ModelInfo describes one selectable LLM the engine can drive an agent with.
// Models are referenced by ID (the string written into an AgentSpec.Model
// field, e.g. "anthropic/claude-opus-4-8"); Label/Provider/Capabilities are
// presentation hints for the dashboard's model pickers.
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

// capTools marks a model usable for tool-calling agents (host/discussants);
// everything in the curated list below supports it, but the field keeps the
// contract explicit for the dashboard.
var (
	capChat   = []string{"tools", "reasoning"}
	capVision = []string{"tools", "vision", "reasoning"}
)

// CuratedModels is the engine's known-good roster of OpenAI-compatible gateway
// model ids. The gateway exposes no reliable cross-provider /models listing,
// so this curated set is the source of truth for the dashboard's pickers.
// Keep ids in the provider/model form the gateway expects.
func CuratedModels() []ModelInfo {
	return []ModelInfo{
		{ID: "anthropic/claude-opus-4-8", Label: "Claude Opus 4.8", Provider: "anthropic", Capabilities: capVision},
		{ID: "anthropic/claude-sonnet-4-6", Label: "Claude Sonnet 4.6", Provider: "anthropic", Capabilities: capVision},
		{ID: "anthropic/claude-haiku-4-5", Label: "Claude Haiku 4.5", Provider: "anthropic", Capabilities: capVision},
		{ID: "openai/gpt-5.4", Label: "GPT-5.4", Provider: "openai", Capabilities: capVision},
		{ID: "openai/gpt-4o", Label: "GPT-4o", Provider: "openai", Capabilities: capVision},
		{ID: "openai/gpt-4o-mini", Label: "GPT-4o mini", Provider: "openai", Capabilities: capChat},
		{ID: "google/gemini-2.5-pro", Label: "Gemini 2.5 Pro", Provider: "google", Capabilities: capVision},
		{ID: "google/gemini-2.5-flash", Label: "Gemini 2.5 Flash", Provider: "google", Capabilities: capChat},
	}
}

// ModelDefaults maps engine roles to the configured default model ids.
type ModelDefaults struct {
	Host         string `json:"host"`
	ScenePlanner string `json:"scene_planner"`
	Compression  string `json:"compression"`
}

// ModelsForEnv returns the curated roster augmented with any env-configured
// default models that aren't already present (marked provider "env-default"),
// and stamps DefaultFor on the models the env points at. The returned defaults
// let the dashboard preselect sensible models for each role.
func ModelsForEnv(e *Env) ([]ModelInfo, ModelDefaults) {
	models := CuratedModels()
	defaults := ModelDefaults{}
	if e == nil {
		return models, defaults
	}
	defaults = ModelDefaults{
		Host:         e.HostModel,
		ScenePlanner: e.ScenePlannerModel,
		Compression:  e.CompressionModel,
	}

	index := func(id string) int {
		for i := range models {
			if models[i].ID == id {
				return i
			}
		}
		return -1
	}
	ensure := func(id, role string) {
		if id == "" {
			return
		}
		i := index(id)
		if i < 0 {
			models = append(models, ModelInfo{
				ID: id, Label: id, Provider: "env-default", Capabilities: capChat,
				DefaultFor: []string{role},
			})
			return
		}
		models[i].DefaultFor = append(models[i].DefaultFor, role)
	}
	ensure(e.HostModel, "host")
	ensure(e.ScenePlannerModel, "scene_planner")
	ensure(e.CompressionModel, "compression")
	return models, defaults
}
