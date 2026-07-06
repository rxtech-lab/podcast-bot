package server

import (
	"context"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/llm"
)

// resolvedModelDefaults returns the effective generation model defaults: the
// env-configured defaults with any admin overrides from AppConfig overlaid on
// top. ENV remains the default when no override row exists (or AppConfig is
// nil), so behavior is unchanged unless an admin sets a value in the UI.
func (s *Server) resolvedModelDefaults(ctx context.Context) config.ModelDefaults {
	defaults := config.DefaultsForEnv(s.d.Env)
	if s.d.AppConfig == nil {
		return defaults
	}
	if v, ok, err := s.d.AppConfig.Get(ctx, appConfigKeyDefaultHostModel); err == nil && ok && v != "" {
		defaults.Host = v
		// ScenePlanner falls back to the host model when unset in env; keep that
		// relationship so overriding the host default also moves the planner
		// unless the planner had its own explicit env default.
		if s.d.Env != nil && s.d.Env.ScenePlannerModel == s.d.Env.HostModel {
			defaults.ScenePlanner = v
		}
	}
	return defaults
}

// plannerEnv returns the Env the in-server planner should run with: a shallow
// copy of the configured Env with the admin-overridden default generation model
// applied. The planner bakes this default into the planned agent roster
// (Planner.agentModel/scriptModel), so overriding here is what makes the admin
// "default model" setting take effect for newly generated content. When no
// override is set it returns the unmodified Env.
func (s *Server) plannerEnv() *config.Env {
	if s.d.Env == nil {
		return nil
	}
	if s.d.AppConfig == nil {
		return s.d.Env
	}
	defaults := s.resolvedModelDefaults(context.Background())
	if defaults.Host == s.d.Env.HostModel && defaults.ScenePlanner == s.d.Env.ScenePlannerModel {
		return s.d.Env
	}
	envCopy := *s.d.Env
	envCopy.HostModel = defaults.Host
	envCopy.ScenePlannerModel = defaults.ScenePlanner
	return &envCopy
}

// modelCatalog returns the roster of selectable models, preferring the Redis
// cache and falling back to a live gateway fetch (which it then caches). It
// mirrors handleModels but is reusable by the admin app-config dropdown.
func (s *Server) modelCatalog(ctx context.Context) []config.ModelInfo {
	defaults := s.resolvedModelDefaults(ctx)
	if s.d.ModelCatalog != nil {
		if cached, ok := s.d.ModelCatalog.Get(ctx); ok {
			return config.ModelsFromIDs(modelIDs(cached), defaults)
		}
	}
	if s.d.Env == nil {
		return nil
	}
	ids, err := llm.ListModels(ctx, s.d.Env.OpenAIBaseURL, s.d.Env.OpenAIKey)
	if err != nil {
		if s.d.Log != nil {
			s.d.Log.Warn("admin: list gateway models", "err", err)
		}
		return nil
	}
	models := config.ModelsFromIDs(ids, defaults)
	if s.d.ModelCatalog != nil {
		s.d.ModelCatalog.Set(ctx, models)
	}
	return models
}

// modelIDs projects a ModelInfo slice down to its ids.
func modelIDs(models []config.ModelInfo) []string {
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	return ids
}
