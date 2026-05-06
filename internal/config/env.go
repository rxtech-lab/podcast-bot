package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

// Env holds all process-level configuration loaded from .env / environment.
// It is treated as immutable after LoadEnv returns.
type Env struct {
	OpenAIBaseURL string
	OpenAIKey     string
	HostModel     string

	// ScenePlannerModel is the LLM used for the visual director call that
	// proposes the per-frame surface + conclusion beats. Falls back to
	// HostModel if unset. Use a higher-quality model here (e.g.
	// openai/gpt-5.4 or anthropic/claude-opus-4-7) since the plan only
	// runs once per puzzle and benefits from richer reasoning about
	// scene composition + story-beat ordering. Set via SCENE_PLANNER_MODEL.
	ScenePlannerModel string

	CompressionBaseURL string
	CompressionKey     string
	CompressionModel   string

	AzureSpeechKey    string
	AzureSpeechRegion string

	ElevenLabsAPIKey string

	// GeminiAPIKey authenticates against Google's Generative Language REST
	// endpoints (image / music generation). Required at startup so puzzle
	// asset generation can run unconditionally — debate-only deployments
	// still need it set even though they won't call the endpoint.
	GeminiAPIKey string

	OutDir string

	// PersistentRoot is the non-session base directory for cross-run
	// archives — today only the series content type uses it (every
	// episode writes its assets to
	// `<PersistentRoot>/tv-series/<show>/s<NN>/e<NN>/` so episode N+1 can
	// re-use prior images and synthesise a "previously on …" recap).
	// Defaults to OUT_DIR's value at LoadEnv time, BEFORE bootstrap appends
	// `session-<stamp>`. Override via the SERIES_ROOT env var when you
	// want the archive to live in a different location than the per-run
	// session output.
	PersistentRoot string
}

// LoadEnv reads .env (if present) then env vars, validates, and freezes config.
// Compression endpoint/key fall back to OpenAI ones when blank.
//
// Uses godotenv.Overload so values in .env take precedence over the inherited
// shell environment — otherwise a stray OPENAI_API_KEY exported in ~/.zshrc
// silently shadows the project's .env, which is a frequent footgun.
func LoadEnv() (*Env, error) {
	_ = godotenv.Overload() // .env wins over inherited shell env

	e := &Env{
		OpenAIBaseURL:      strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")),
		OpenAIKey:          strings.TrimSpace(os.Getenv("OPENAI_API_KEY")),
		HostModel:          strings.TrimSpace(os.Getenv("HOST_MODEL")),
		ScenePlannerModel:  strings.TrimSpace(os.Getenv("SCENE_PLANNER_MODEL")),
		CompressionBaseURL: strings.TrimSpace(os.Getenv("COMPRESSION_BASE_URL")),
		CompressionKey:     strings.TrimSpace(os.Getenv("COMPRESSION_API_KEY")),
		CompressionModel:   strings.TrimSpace(os.Getenv("COMPRESSION_MODEL")),
		AzureSpeechKey:     strings.TrimSpace(os.Getenv("AZURE_SPEECH_KEY")),
		AzureSpeechRegion:  strings.TrimSpace(os.Getenv("AZURE_SPEECH_REGION")),
		ElevenLabsAPIKey:   strings.TrimSpace(os.Getenv("ELEVENLABS_API_KEY")),
		GeminiAPIKey:       strings.TrimSpace(os.Getenv("GEMINI_API_KEY")),
		OutDir:             strings.TrimSpace(os.Getenv("OUT_DIR")),
		PersistentRoot:     strings.TrimSpace(os.Getenv("SERIES_ROOT")),
	}

	if e.CompressionBaseURL == "" {
		e.CompressionBaseURL = e.OpenAIBaseURL
	}
	if e.CompressionKey == "" {
		e.CompressionKey = e.OpenAIKey
	}
	if e.ScenePlannerModel == "" {
		e.ScenePlannerModel = e.HostModel
	}
	if e.OutDir == "" {
		e.OutDir = "./out"
	}
	if e.PersistentRoot == "" {
		// Default the cross-session archive root to the user's OUT_DIR so
		// out-of-the-box runs put `tv-series/...` next to `session-<stamp>/...`.
		// This is captured BEFORE bootstrap appends the session stamp so
		// archived episodes survive across runs.
		e.PersistentRoot = e.OutDir
	}

	var missing []string
	if e.OpenAIBaseURL == "" {
		missing = append(missing, "OPENAI_BASE_URL")
	}
	if e.OpenAIKey == "" {
		missing = append(missing, "OPENAI_API_KEY")
	}
	if e.HostModel == "" {
		missing = append(missing, "HOST_MODEL")
	}
	if e.CompressionModel == "" {
		missing = append(missing, "COMPRESSION_MODEL")
	}
	if e.GeminiAPIKey == "" {
		missing = append(missing, "GEMINI_API_KEY")
	}
	// Provider-specific keys (Azure, ElevenLabs) are NOT required here —
	// the orchestrator validates the credentials matching the chosen
	// `tts_provider` from topic.md, so users only need to set what they use.
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))
	}
	return e, nil
}

// ErrEnvNotLoaded is returned when an Env was expected but not initialised.
var ErrEnvNotLoaded = errors.New("env not loaded")
