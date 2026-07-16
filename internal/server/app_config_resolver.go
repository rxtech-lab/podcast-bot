package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/llm"
	"github.com/sirily11/debate-bot/internal/stt"
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

func (s *Server) resolvedTranslationModel(ctx context.Context) string {
	model := ""
	if s.d.Env != nil {
		model = strings.TrimSpace(s.d.Env.PodcastTranslationModel)
		if model == "" {
			model = strings.TrimSpace(s.d.Env.PodcastSummaryModel)
		}
		if model == "" {
			model = strings.TrimSpace(s.d.Env.HostModel)
		}
	}
	if s.d.AppConfig != nil {
		if v, ok, err := s.d.AppConfig.Get(ctx, appConfigKeyTranslationModel); err == nil && ok && strings.TrimSpace(v) != "" {
			model = strings.TrimSpace(v)
		}
	}
	return model
}

// resolvedSTTProvider returns the effective speech-to-text provider id: the
// env default (STT_PROVIDER, "gemini" when unset) with the admin App Config
// override overlaid.
func (s *Server) resolvedSTTProvider(ctx context.Context) string {
	provider := stt.ProviderGemini
	if s.d.Env != nil && s.d.Env.STTProvider != "" {
		provider = s.d.Env.STTProvider
	}
	if s.d.AppConfig != nil {
		if v, ok, err := s.d.AppConfig.Get(ctx, appConfigKeySTTProvider); err == nil && ok && v != "" {
			provider = strings.ToLower(strings.TrimSpace(v))
		}
	}
	return provider
}

// resolvedSTTGeminiModel returns the Gemini model used for uploaded-audio
// transcription: the env transcribe model with the admin App Config override
// overlaid.
func (s *Server) resolvedSTTGeminiModel(ctx context.Context) string {
	model := ""
	if s.d.Env != nil {
		model = strings.TrimSpace(s.d.Env.TranscribeModel)
	}
	if s.d.AppConfig != nil {
		if v, ok, err := s.d.AppConfig.Get(ctx, appConfigKeySTTGeminiModel); err == nil && ok && strings.TrimSpace(v) != "" {
			model = strings.TrimSpace(v)
		}
	}
	return model
}

// sttProvider constructs the effective STT provider from the resolved id and
// the env credentials, mirroring content_creator's buildTTSProvider shape.
func (s *Server) sttProvider(ctx context.Context) (stt.Provider, error) {
	if s.d.Env == nil {
		return nil, fmt.Errorf("stt: env not configured")
	}
	switch id := s.resolvedSTTProvider(ctx); id {
	case stt.ProviderAzure:
		return s.azureSTTProvider()
	case stt.ProviderGemini:
		if s.d.Env.GeminiAPIKey == "" {
			return nil, fmt.Errorf("stt: gemini selected but GEMINI_API_KEY not set")
		}
		return stt.NewGemini(s.d.Env.GeminiAPIKey, s.resolvedSTTGeminiModel(ctx)), nil
	default:
		return nil, fmt.Errorf("stt: unknown provider %q", id)
	}
}

func (s *Server) azureSTTProvider() (stt.Provider, error) {
	if s.d.Env == nil || s.d.Env.AzureSpeechKey == "" ||
		(s.d.Env.AzureSpeechEndpoint == "" && s.d.Env.AzureSpeechRegion == "") {
		return nil, fmt.Errorf("stt: azure selected but AZURE_SPEECH_KEY / AZURE_SPEECH_ENDPOINT (or _REGION) not set")
	}
	return stt.NewAzureFast(s.d.Env.AzureSpeechEndpoint, s.d.Env.AzureSpeechRegion, s.d.Env.AzureSpeechKey), nil
}

// geminiSTTModelOptions fetches the audio-capable Gemini model catalog for the
// admin dropdown. Best-effort: an unreachable catalog returns nil so the form
// still renders (with a free-form field semantics on save).
func (s *Server) geminiSTTModelOptions(ctx context.Context) []stt.GeminiModel {
	if s.d.Env == nil || s.d.Env.GeminiAPIKey == "" {
		return nil
	}
	models, err := stt.ListGeminiAudioModels(ctx, s.d.Env.GeminiAPIKey)
	if err != nil {
		if s.d.Log != nil {
			s.d.Log.Warn("admin: list gemini audio models", "err", err)
		}
		return nil
	}
	return models
}

// sttCostPerHour returns the configured per-hour transcription price for the
// given provider id.
func (s *Server) sttCostPerHour(provider string) float64 {
	if s.d.Env == nil {
		return 0
	}
	if provider == stt.ProviderAzure {
		return s.d.Env.AzureSTTCostPerHour
	}
	return s.d.Env.GeminiSTTCostPerHour
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
