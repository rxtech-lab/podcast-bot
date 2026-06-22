package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
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

	// Optional pricing override for OpenAI-compatible chat calls. Values are
	// dollars per million tokens and are used only when the provider response
	// does not include a cost field in the usage payload.
	LLMInputCostPerMillion  float64
	LLMOutputCostPerMillion float64

	// AzureTTSCostPerMillionChars is the Azure neural TTS price in dollars per
	// one million synthesised characters. Used to fold speech-synthesis cost
	// into the per-run total. Defaults to $15/1M (Azure prebuilt neural,
	// pay-as-you-go); override with AZURE_TTS_COST_PER_MILLION_CHARS (e.g. set
	// to a commitment-tier rate, or 22 for Neural HD voices).
	AzureTTSCostPerMillionChars float64

	// LyriaCostPerGeneration is the Google Lyria 3 Pro price in dollars per
	// successful music-generation API call (one returned clip). Defaults to
	// $0.08/generation; override with LYRIA_COST_PER_GENERATION. Cache hits do
	// not hit the API and are not billed.
	LyriaCostPerGeneration float64

	AzureSpeechKey    string
	AzureSpeechRegion string

	ElevenLabsAPIKey string

	// GeminiAPIKey authenticates against Google's Generative Language REST
	// endpoints (image / music generation). Required at startup so puzzle
	// asset generation can run unconditionally — debate-only deployments
	// still need it set even though they won't call the endpoint.
	GeminiAPIKey string

	OutDir string

	// DashboardOrigins lists the browser origins permitted to call the API
	// cross-origin (the separately-hosted dashboard). Comma-separated in the
	// DASHBOARD_ORIGINS env var; empty leaves CORS disabled.
	DashboardOrigins []string

	// DashboardServiceToken, when set (DASHBOARD_SERVICE_TOKEN), lets the
	// dashboard's backend authenticate to the API with a bearer token instead
	// of the human password cookie. Never exposed to browsers.
	DashboardServiceToken string

	// AuthIssuer, when set (AUTH_ISSUER, e.g. https://auth.rxlab.app), enables
	// per-user rxlab OAuth authentication: a request carrying
	// `Authorization: Bearer <access token>` is validated by calling the
	// issuer's OIDC userinfo endpoint, so native apps (iOS) can authenticate
	// directly with the access token from RxAuthSwift. Empty disables it.
	AuthIssuer string

	// SearchAPIKey / SearchAPIURL configure the web-search backend the planner
	// uses to ground a discussion plan in real sources when research is
	// requested. SearchAPIKey comes from SEARCH_API_KEY; SearchAPIURL
	// (SEARCH_API_URL) defaults to Tavily's search endpoint. When the key is
	// empty, planning still works but returns researched=false (no sources).
	SearchAPIKey string
	SearchAPIURL string

	// S3 settings for uploading finished media. When S3Bucket is empty the
	// engine keeps serving media from local disk (no upload). S3Endpoint is
	// for S3-compatible APIs such as R2; S3DownloadBaseURL is an optional
	// public/custom download domain used instead of presigned URLs.
	S3Bucket          string
	S3Region          string
	S3Endpoint        string
	S3Prefix          string
	S3DownloadBaseURL string
	S3AccessKeyID     string
	S3SecretAccessKey string

	// Turso/libSQL database for durable native-client discussion storage.
	// TURSO_CONNECTION_URL is the primary database URL. TURSO_AUTH_TOKEN is
	// required for remote Turso databases and may be empty for local testing.
	TursoConnectionURL string
	TursoAuthToken     string

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
		LLMInputCostPerMillion: parseFloatEnv(
			"LLM_INPUT_COST_PER_MILLION",
		),
		LLMOutputCostPerMillion: parseFloatEnv(
			"LLM_OUTPUT_COST_PER_MILLION",
		),
		AzureTTSCostPerMillionChars: parseFloatEnvDefault(
			"AZURE_TTS_COST_PER_MILLION_CHARS", 15.0,
		),
		LyriaCostPerGeneration: parseFloatEnvDefault(
			"LYRIA_COST_PER_GENERATION", 0.08,
		),
		AzureSpeechKey:    strings.TrimSpace(os.Getenv("AZURE_SPEECH_KEY")),
		AzureSpeechRegion: strings.TrimSpace(os.Getenv("AZURE_SPEECH_REGION")),
		ElevenLabsAPIKey:  strings.TrimSpace(os.Getenv("ELEVENLABS_API_KEY")),
		GeminiAPIKey:      strings.TrimSpace(os.Getenv("GEMINI_API_KEY")),
		OutDir:            strings.TrimSpace(os.Getenv("OUT_DIR")),
		PersistentRoot:    strings.TrimSpace(os.Getenv("SERIES_ROOT")),

		DashboardOrigins:      splitCSV(os.Getenv("DASHBOARD_ORIGINS")),
		DashboardServiceToken: strings.TrimSpace(os.Getenv("DASHBOARD_SERVICE_TOKEN")),
		AuthIssuer:            strings.TrimRight(strings.TrimSpace(os.Getenv("AUTH_ISSUER")), "/"),
		SearchAPIKey:          strings.TrimSpace(os.Getenv("SEARCH_API_KEY")),
		SearchAPIURL:          strings.TrimSpace(os.Getenv("SEARCH_API_URL")),

		S3Bucket:          strings.TrimSpace(os.Getenv("S3_BUCKET")),
		S3Region:          strings.TrimSpace(os.Getenv("S3_REGION")),
		S3Endpoint:        strings.TrimSpace(os.Getenv("S3_ENDPOINT")),
		S3Prefix:          strings.TrimSpace(os.Getenv("S3_PREFIX")),
		S3DownloadBaseURL: strings.TrimSpace(os.Getenv("S3_DOWNLOAD_BASE_URL")),
		S3AccessKeyID:     strings.TrimSpace(os.Getenv("S3_ACCESS_KEY_ID")),
		S3SecretAccessKey: strings.TrimSpace(os.Getenv("S3_SECRET_ACCESS_KEY")),

		TursoConnectionURL: strings.TrimSpace(os.Getenv("TURSO_CONNECTION_URL")),
		TursoAuthToken:     strings.TrimSpace(os.Getenv("TURSO_AUTH_TOKEN")),
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

// splitCSV parses a comma-separated env value into a trimmed, non-empty slice.
func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func parseFloatEnv(key string) float64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return 0
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		return 0
	}
	return f
}

// parseFloatEnvDefault is parseFloatEnv but returns def when the var is unset
// or invalid, so a sensible non-zero default (e.g. a published price) applies
// unless the operator explicitly overrides it. Set the var to "0" to disable.
func parseFloatEnvDefault(key string, def float64) float64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		return def
	}
	return f
}

// ErrEnvNotLoaded is returned when an Env was expected but not initialised.
var ErrEnvNotLoaded = errors.New("env not loaded")
