package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/llm"
	"github.com/sirily11/debate-bot/internal/planner"
	"github.com/sirily11/debate-bot/internal/stt"
)

// appConfigValue returns one admin-owned value without supplying a default.
// It is used by admin/meta surfaces that must remain renderable while the app
// is being configured.
func (s *Server) appConfigValue(ctx context.Context, key string) string {
	if s.d.AppConfig == nil {
		return ""
	}
	v, ok, err := s.d.AppConfig.Get(ctx, key)
	if err != nil {
		if s.d.Log != nil {
			s.d.Log.Warn("read app model config", "key", key, "err", err)
		}
		return ""
	}
	if !ok {
		return ""
	}
	return strings.TrimSpace(v)
}

func (s *Server) requiredModel(ctx context.Context, key, label string) (string, error) {
	if s.d.AppConfig == nil {
		return "", fmt.Errorf("%s model is not configured: admin App Config store unavailable", label)
	}
	v, ok, err := s.d.AppConfig.Get(ctx, key)
	if err != nil {
		return "", fmt.Errorf("read %s model from admin App Config: %w", label, err)
	}
	v = strings.TrimSpace(v)
	if !ok || v == "" {
		return "", fmt.Errorf("%s model is not configured in admin App Config", label)
	}
	return v, nil
}

// resolvedModels returns the admin values exactly as stored. Empty means the
// role is unconfigured; runtime entry points validate the roles they use and
// return an explicit error.
func (s *Server) resolvedModels(ctx context.Context) config.ModelConfig {
	return config.ModelConfig{
		Host:               s.appConfigValue(ctx, appConfigKeyDefaultHostModel),
		ScenePlanner:       s.appConfigValue(ctx, appConfigKeyScenePlannerModel),
		Compression:        s.appConfigValue(ctx, appConfigKeyCompressionModel),
		PodcastSummary:     s.appConfigValue(ctx, appConfigKeySummaryModel),
		PodcastTranslation: s.appConfigValue(ctx, appConfigKeyTranslationModel),
		Judgement:          s.appConfigValue(ctx, appConfigKeyJudgementModel),
		PodcastSummaryPPT:  s.appConfigValue(ctx, appConfigKeySummaryPPTModel),
		QA:                 s.appConfigValue(ctx, appConfigKeyQAModel),
		Embedding:          s.appConfigValue(ctx, appConfigKeyEmbeddingModel),
		Transcription:      s.appConfigValue(ctx, appConfigKeySTTGeminiModel),
	}
}

// resolvedModelDefaults reports admin-configured client defaults. Missing
// values stay empty; they are never filled from another role or the env.
func (s *Server) resolvedModelDefaults(ctx context.Context) config.ModelDefaults {
	return s.resolvedModels(ctx).Defaults()
}

func (s *Server) resolvedTranslationModel(ctx context.Context) (string, error) {
	return s.requiredModel(ctx, appConfigKeyTranslationModel, "podcast translation")
}

// resolvedQAModel returns the explicit admin-owned Q&A / global-chat model.
func (s *Server) resolvedQAModel(ctx context.Context) (string, error) {
	return s.requiredModel(ctx, appConfigKeyQAModel, "Q&A")
}

// resolvedEmbeddingModel returns the explicit admin-owned embedding model.
func (s *Server) resolvedEmbeddingModel(ctx context.Context) (string, error) {
	return s.requiredModel(ctx, appConfigKeyEmbeddingModel, "embedding")
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

// resolvedSTTGeminiModel returns the explicit admin-owned Gemini transcription
// model.
func (s *Server) resolvedSTTGeminiModel(ctx context.Context) (string, error) {
	return s.requiredModel(ctx, appConfigKeySTTGeminiModel, "Gemini transcription")
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
		model, err := s.resolvedSTTGeminiModel(ctx)
		if err != nil {
			return nil, err
		}
		return stt.NewGemini(s.d.Env.GeminiAPIKey, model), nil
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

// ConfiguredEnv returns a shallow Env copy populated with the current
// admin-owned model set. Runtime components validate the roles they use.
func (s *Server) ConfiguredEnv(ctx context.Context) (*config.Env, error) {
	if s.d.Env == nil {
		return nil, fmt.Errorf("engine env is not configured")
	}
	if s.d.AppConfig == nil {
		return nil, fmt.Errorf("admin App Config store is unavailable")
	}
	read := func(key string) (string, error) {
		value, _, err := s.d.AppConfig.Get(ctx, key)
		if err != nil {
			return "", fmt.Errorf("read admin App Config %q: %w", key, err)
		}
		return strings.TrimSpace(value), nil
	}
	var models config.ModelConfig
	fields := []struct {
		key string
		set func(string)
	}{
		{appConfigKeyDefaultHostModel, func(v string) { models.Host = v }},
		{appConfigKeyScenePlannerModel, func(v string) { models.ScenePlanner = v }},
		{appConfigKeyCompressionModel, func(v string) { models.Compression = v }},
		{appConfigKeySummaryModel, func(v string) { models.PodcastSummary = v }},
		{appConfigKeyTranslationModel, func(v string) { models.PodcastTranslation = v }},
		{appConfigKeyJudgementModel, func(v string) { models.Judgement = v }},
		{appConfigKeySummaryPPTModel, func(v string) { models.PodcastSummaryPPT = v }},
		{appConfigKeyQAModel, func(v string) { models.QA = v }},
		{appConfigKeyEmbeddingModel, func(v string) { models.Embedding = v }},
		{appConfigKeySTTGeminiModel, func(v string) { models.Transcription = v }},
	}
	for _, field := range fields {
		value, err := read(field.key)
		if err != nil {
			return nil, err
		}
		field.set(value)
	}
	envCopy := *s.d.Env
	envCopy.Models = models
	return &envCopy, nil
}

func (s *Server) plannerEnv(ctx context.Context) (*config.Env, error) {
	return s.ConfiguredEnv(ctx)
}

func (s *Server) newPlanner(ctx context.Context) (*planner.Planner, error) {
	env, err := s.plannerEnv(ctx)
	if err != nil {
		return nil, err
	}
	return planner.New(env)
}

// modelCatalog returns the full roster of gateway models (all types),
// preferring the Redis cache and falling back to a live gateway fetch (which
// it then caches). Most callers want a type-filtered view — see
// languageModelCatalog / embeddingModelCatalog.
func (s *Server) modelCatalog(ctx context.Context) []config.ModelInfo {
	defaults := s.resolvedModelDefaults(ctx)
	if s.d.ModelCatalog != nil {
		if cached, ok := s.d.ModelCatalog.Get(ctx); ok {
			return config.AnnotateModelDefaults(cached, defaults)
		}
	}
	if s.d.Env == nil {
		return nil
	}
	entries, err := llm.ListModelEntries(ctx, s.d.Env.OpenAIBaseURL, s.d.Env.OpenAIKey)
	if err != nil {
		if s.d.Log != nil {
			s.d.Log.Warn("admin: list gateway models", "err", err)
		}
		return nil
	}
	descriptors := make([]config.ModelDescriptor, 0, len(entries))
	for _, e := range entries {
		descriptors = append(descriptors, config.ModelDescriptor{ID: e.ID, Type: e.Type})
	}
	models := config.ModelsFromDescriptors(descriptors, defaults)
	if s.d.ModelCatalog != nil {
		s.d.ModelCatalog.Set(ctx, models)
	}
	return models
}

// languageModelCatalog filters the catalog to chat-capable models for the
// generation/translation/Q&A pickers. Untyped entries (plain OpenAI-compatible
// gateways don't type their models) are kept so those setups keep a full
// picker.
func (s *Server) languageModelCatalog(ctx context.Context) []config.ModelInfo {
	return filterModelsByType(s.modelCatalog(ctx), func(t string) bool {
		return t == "" || t == config.ModelTypeLanguage
	})
}

// embeddingModelCatalog filters the catalog to embedding models for the
// semantic-search model picker. When the gateway doesn't type its models,
// falls back to ids containing "embed" (e.g. openai's text-embedding-*) so a
// plain OpenAI endpoint still yields options.
func (s *Server) embeddingModelCatalog(ctx context.Context) []config.ModelInfo {
	all := s.modelCatalog(ctx)
	typed := filterModelsByType(all, func(t string) bool { return t == config.ModelTypeEmbedding })
	if len(typed) > 0 {
		return typed
	}
	var out []config.ModelInfo
	for _, m := range all {
		if m.Type == "" && strings.Contains(strings.ToLower(m.ID), "embed") {
			out = append(out, m)
		}
	}
	return out
}

func filterModelsByType(models []config.ModelInfo, keep func(string) bool) []config.ModelInfo {
	out := make([]config.ModelInfo, 0, len(models))
	for _, m := range models {
		if keep(m.Type) {
			out = append(out, m)
		}
	}
	return out
}
