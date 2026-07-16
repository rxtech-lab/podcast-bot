package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

	// PodcastSummaryModel is the LLM used by the post-generation summarizer agent
	// that writes the Markdown summary document for a finished podcast. Falls back
	// to HostModel when unset. Set via PODCAST_SUMMARY_MODEL.
	PodcastSummaryModel string
	// PodcastTranslationModel translates the durable, text-only presentation of
	// a podcast. It falls back to PodcastSummaryModel, then HostModel.
	PodcastTranslationModel string
	// JudgementModel is the silent fact-checker used during discussion
	// podcasts. Falls back to HostModel when unset. Set via
	// JUDGEMENT_MODEL_NAME.
	JudgementModel string

	// PodcastSummaryPPTModel is reserved for the future slide-deck ("PPT") summary
	// document type. Falls back to PodcastSummaryModel when unset. Set via
	// PODCAST_SUMMARY_PPT_MODEL.
	PodcastSummaryPPTModel string

	// PPTXRendererScript points at the Node/pptxgenjs renderer used to turn a
	// generated deck JSON spec into a .pptx file. Empty uses the built-in
	// tools/ppt-renderer/render.mjs path.
	PPTXRendererScript string

	// LibreOfficePath is the soffice binary used to convert generated PPTX files
	// to PDF. Empty searches PATH.
	LibreOfficePath string

	CompressionBaseURL string
	CompressionKey     string
	CompressionModel   string

	// TranscribeModel is the Gemini model used to transcribe a voice message
	// server-side when the sender's device can't do it on-device (no on-device
	// model for the language, or Speech recognition unauthorized). Transcription
	// runs through Google's generateContent (reusing GeminiAPIKey) because the
	// Vercel AI Gateway does not proxy an OpenAI /audio/transcriptions endpoint.
	// Defaults to "gemini-2.5-flash"; override with GEMINI_TRANSCRIBE_MODEL.
	TranscribeModel string

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

	// ---- Points economy ----
	//
	// PointsCostLeverage is the multiplier applied over the points sale rate
	// (1000 pts = $1.50 → ~666.7 pts/$) to derive PointsPerUSDCost. Default 3.
	// Override with POINTS_COST_LEVERAGE.
	PointsCostLeverage float64

	// PointsPerUSDCost converts a podcast's real provider cost (USD) into the
	// points charged to the user: points = ceil(costUSD * PointsPerUSDCost). The
	// markup over the raw cost is the company margin. By default this is derived
	// from POINTS_COST_LEVERAGE. Override exactly with POINTS_PER_USD_COST.
	PointsPerUSDCost float64

	// PointsEstCostPerMinuteUSD estimates a podcast's cost from its target
	// minutes for the pre-generation balance gate, so a run can never deplete
	// mid-generation. Default $0.02/min ≈ the observed $0.60 per 30-min podcast.
	// Override with POINTS_EST_COST_PER_MINUTE_USD.
	PointsEstCostPerMinuteUSD float64

	// PointsPlanGateUSD is the minimum balance (USD-equivalent) a user must hold
	// before a planning / improve / add-sources round may run, so planning is
	// never free. Default $0.05. Override with POINTS_PLAN_GATE_USD.
	PointsPlanGateUSD float64

	// PointsSummaryEstUSD is the estimated cost reserved before the post-generation
	// summary agent runs, so summary generation is gated against the creator's
	// balance and never starts uncharged. Reconciled to actual usage on completion.
	// Default $0.05. Override with POINTS_SUMMARY_EST_USD.
	PointsSummaryEstUSD float64
	// PointsTranslationEstUSD is reserved before a podcast translation and
	// reconciled to actual metered LLM usage. Default $0.10.
	PointsTranslationEstUSD float64

	// PointsMinPerPodcast is the floor charged for a generation when its real
	// cost can't be determined, so a podcast is never free. Default 1.
	// Override with POINTS_MIN_PER_PODCAST.
	PointsMinPerPodcast int64

	// PointsMinPerPlanningConversation is the one-time floor charged for a
	// conversational planning thread, applied on its first turn so planning a
	// podcast is never free even when the metered LLM cost rounds to zero.
	// Default 1. Override with POINTS_MIN_PER_PLANNING_CONVERSATION.
	PointsMinPerPlanningConversation int64

	// PointsSignupGrant is an optional free starter balance credited the first
	// time a user's balance is read. Default 0 (disabled). Override with
	// POINTS_SIGNUP_GRANT.
	PointsSignupGrant int64

	// RevenueCatWebhookAuth is the shared secret expected in the Authorization
	// header of POST /api/revenuecat/webhook. Empty disables the webhook.
	// Set via REVENUECAT_WEBHOOK_AUTH.
	RevenueCatWebhookAuth string
	// RevenueCatRESTAPIKey / ProjectID / AppID configure server-side product
	// setup against RevenueCat's REST API v2. This must be a secret v2 API key,
	// not the public iOS SDK key from Secrets.xcconfig.
	RevenueCatRESTAPIKey string
	RevenueCatProjectID  string
	RevenueCatAppID      string
	RevenueCatAPIBaseURL string

	AzureSpeechKey    string
	AzureSpeechRegion string

	// AzureSpeechEndpoint is the full Azure AI Services resource endpoint
	// (https://{resource}.cognitiveservices.azure.com) used by the fast-
	// transcription STT API. Optional: when empty the endpoint is derived from
	// AzureSpeechRegion. Set via AZURE_SPEECH_ENDPOINT.
	AzureSpeechEndpoint string

	// STTProvider is the environment default speech-to-text provider used to
	// transcribe uploaded podcast audio ("gemini" or "azure"); the admin App
	// Config override wins when set. Default "gemini". Set via STT_PROVIDER.
	STTProvider string

	// AzureSTTCostPerHour / GeminiSTTCostPerHour price one hour of transcribed
	// audio in dollars, folded into the points ledger as stt_cost_usd. Azure
	// fast transcription lists at ~$0.36/hr; the Gemini figure approximates its
	// audio-token pricing. Override with AZURE_STT_COST_PER_HOUR /
	// GEMINI_STT_COST_PER_HOUR.
	AzureSTTCostPerHour  float64
	GeminiSTTCostPerHour float64

	// MaxPodcastAudioUploadMB is the global ceiling for one uploaded podcast
	// audio file in MiB; per-subscription-tier limits apply under it. Default
	// 500 (the Azure fast-transcription max). Set via MAX_PODCAST_AUDIO_UPLOAD_MB.
	MaxPodcastAudioUploadMB int64

	ElevenLabsAPIKey string

	// GeminiAPIKey authenticates against Google's Generative Language REST
	// endpoints (image / music generation). Required at startup so puzzle
	// asset generation can run unconditionally — debate-only deployments
	// still need it set even though they won't call the endpoint.
	GeminiAPIKey string

	// CloudflareAccountID / CloudflareAPIToken configure Cloudflare Browser
	// Rendering, used to (1) render a podcast summary's Markdown into a
	// downloadable PDF (the summary's ```mermaid blocks are rendered to real
	// diagrams in Cloudflare's headless Chromium) and (2) read links the user
	// pastes or adds to a plan, via the /markdown endpoint. Set via
	// CLOUDFLARE_ACCOUNT_ID and CLOUDFLARE_API_TOKEN (token needs the "Browser
	// Rendering - Edit" permission). When either is empty, the summary-PDF
	// endpoint returns 503 and link-reading is disabled.
	CloudflareAccountID string
	CloudflareAPIToken  string

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

	// AdminAllowedClientIDs (ADMIN_ALLOWED_CLIENT_IDS, comma-separated) restricts
	// which OAuth client_ids may call the /admin API. rxlab-auth access tokens
	// carry no `aud` claim, so this is how the admin resource server ensures only
	// tokens minted for the dashboard client are accepted. Empty accepts any
	// client that presents a valid admin-role token.
	AdminAllowedClientIDs []string

	// WebsiteBaseURL (WEBSITE_BASE_URL, e.g. https://podcast.rxlab.app) is the
	// fallback public base for every shareable link (/p/, /s/, /d/) when
	// FrontendPublicURL is unset. Empty falls back to https://podcast.rxlab.app.
	WebsiteBaseURL string

	// FrontendPublicURL (FRONTEND_PUBLIC_URL) is the preferred public base for
	// every shareable link — the /p/ web player ("listen again" links in exported
	// summaries), the /s/ private share link, and the /d/ deep link — e.g.
	// http://localhost:3000 in development or https://podcast.rxlab.app in
	// production. Empty falls back to WebsiteBaseURL, then to
	// https://podcast.rxlab.app. Resolved via server.FrontendBaseURL.
	FrontendPublicURL string

	// APNs token-auth configuration for native push notifications. APNS_KEY_BASE64
	// accepts a base64-encoded .p8 private key or the raw PEM .p8 contents.
	// APNS_ENVIRONMENT is "sandbox" or "production". Empty APNs fields disable
	// push sending while still allowing clients to register tokens.
	APNSKeyID       string
	APNSTeamID      string
	APNSBundleID    string
	APNSKeyBase64   string
	APNSEnvironment string

	// SearchAPIKey / SearchAPIURL configure the web-search backend the planner
	// uses to ground a discussion plan in real sources when research is
	// requested. SearchAPIKey comes from SEARCH_API_KEY; SearchAPIURL
	// (SEARCH_API_URL) defaults to Tavily's search endpoint. When the key is
	// empty, planning still works but returns researched=false (no sources).
	SearchAPIKey string
	SearchAPIURL string

	// FirecrawlAPIKey (FIRECRAWL_API_KEY) authenticates against Firecrawl's
	// REST API. When set, the planner uses Firecrawl `/v2/search` to ground a
	// plan in live web sources. (Reading individual links the user pastes or
	// adds is handled by Cloudflare Browser Rendering — see CloudflareAPIToken.)
	// Empty disables web search (planning still works ungrounded).
	FirecrawlAPIKey string

	// MarkitdownServerURL / MarkitdownAPIKey configure the deployed markitdown
	// service that converts an uploaded file (fetched by URL) into markdown so
	// the planner can read it. MarkitdownServerURL (MARKITDOWN_SERVER_URL) is
	// the base URL with no trailing slash — the client appends `/convert`.
	// MarkitdownAPIKey (MARKITDOWN_API_KEY) is sent as the `X-API-Key` header;
	// when empty the header is omitted. Empty server URL disables file uploads.
	MarkitdownServerURL string
	MarkitdownAPIKey    string

	// Notion OAuth configuration. NOTION_OAUTH_REDIRECT_URI must match the
	// redirect URI registered in Notion (typically the API callback endpoint).
	// NOTION_APP_CALLBACK_URL is where the API sends the browser after storing
	// the workspace token, so the native app can resume the picker.
	NotionOAuthClientID     string
	NotionOAuthClientSecret string
	NotionOAuthRedirectURI  string
	NotionAppCallbackURL    string
	NotionAPIBaseURL        string

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

	// DatabaseURL is the shared metadata database for jobs, native discussions,
	// points, summaries, planning state, and push tokens. PostgreSQL URLs
	// (postgres:// or postgresql://) use Postgres; libSQL/Turso URLs use Turso;
	// empty falls back to local SQLite. DATABASE_URL is preferred, with
	// TURSO_CONNECTION_URL kept as a compatibility fallback.
	DatabaseURL        string
	TursoConnectionURL string
	TursoAuthToken     string

	// RedisURL configures cluster Redis for transient stream recovery state.
	// Persistent discussion data remains in the database; Redis only stores
	// latest progress/status so reconnecting clients can restore the visible
	// loading state. Empty disables Redis-backed recovery.
	RedisURL string

	// RabbitMQURL configures the durable generation-job queue (podcast,
	// summary, mindmap, ppt/pdf export, video render, planning turns).
	// Empty falls back to an in-process queue with the same retry
	// semantics but no durability across restarts.
	RabbitMQURL string

	// MQQueuePrefix is prepended to every queue name. E2E runs set a
	// per-run prefix so parallel/successive runs sharing one broker never
	// see each other's messages; prefixed queues auto-expire when idle.
	MQQueuePrefix string

	// PodName is this process's stable network identity in a horizontally
	// scaled deployment (the StatefulSet pod hostname, injected via the
	// downward API as POD_NAME, falling back to HOSTNAME). Empty in single-pod
	// / local runs, which disables cross-pod job routing.
	PodName string

	// PeerHostTemplate builds the host:port to dial for a peer pod given its
	// name. It must contain exactly one %s, replaced with the owner pod name —
	// e.g. "%s.debate-bot-headless.debate-bot.svc.cluster.local:3000". Empty
	// disables cross-pod job routing.
	PeerHostTemplate string

	// E2EMode, when set (E2E_MODE=true), puts the whole stack into a hermetic
	// end-to-end test configuration: an in-process fake OpenAI-compatible LLM
	// replaces the real model endpoint, a fake TTS provider emits silent audio,
	// auth is bypassed (every request resolves to the fixed user "test"), and
	// the database is seeded with known fixtures on boot. It is wired in
	// bootstrap; never enable it in production.
	E2EMode bool

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
	pointsCostLeverage, pointsPerUSDCost := loadPointsCostConfig()

	e := &Env{
		OpenAIBaseURL:           strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")),
		OpenAIKey:               strings.TrimSpace(os.Getenv("OPENAI_API_KEY")),
		HostModel:               strings.TrimSpace(os.Getenv("HOST_MODEL")),
		ScenePlannerModel:       strings.TrimSpace(os.Getenv("SCENE_PLANNER_MODEL")),
		PodcastSummaryModel:     strings.TrimSpace(os.Getenv("PODCAST_SUMMARY_MODEL")),
		PodcastTranslationModel: strings.TrimSpace(os.Getenv("PODCAST_TRANSLATION_MODEL")),
		JudgementModel:          strings.TrimSpace(os.Getenv("JUDGEMENT_MODEL_NAME")),
		PodcastSummaryPPTModel:  strings.TrimSpace(os.Getenv("PODCAST_SUMMARY_PPT_MODEL")),
		PPTXRendererScript:      strings.TrimSpace(os.Getenv("PPTX_RENDERER_SCRIPT")),
		LibreOfficePath:         strings.TrimSpace(os.Getenv("LIBREOFFICE_PATH")),
		CompressionBaseURL:      strings.TrimSpace(os.Getenv("COMPRESSION_BASE_URL")),
		CompressionKey:          strings.TrimSpace(os.Getenv("COMPRESSION_API_KEY")),
		CompressionModel:        strings.TrimSpace(os.Getenv("COMPRESSION_MODEL")),
		TranscribeModel:         strings.TrimSpace(os.Getenv("GEMINI_TRANSCRIBE_MODEL")),
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
		PointsCostLeverage:               pointsCostLeverage,
		PointsPerUSDCost:                 pointsPerUSDCost,
		PointsEstCostPerMinuteUSD:        parseFloatEnvDefault("POINTS_EST_COST_PER_MINUTE_USD", 0.02),
		PointsPlanGateUSD:                parseFloatEnvDefault("POINTS_PLAN_GATE_USD", 0.05),
		PointsSummaryEstUSD:              parseFloatEnvDefault("POINTS_SUMMARY_EST_USD", 0.05),
		PointsTranslationEstUSD:          parseFloatEnvDefault("POINTS_TRANSLATION_EST_USD", 0.10),
		PointsMinPerPodcast:              parseIntEnvDefault("POINTS_MIN_PER_PODCAST", 1),
		PointsMinPerPlanningConversation: parseIntEnvDefault("POINTS_MIN_PER_PLANNING_CONVERSATION", 1),
		PointsSignupGrant:                parseIntEnvDefault("POINTS_SIGNUP_GRANT", 0),
		RevenueCatWebhookAuth:            strings.TrimSpace(os.Getenv("REVENUECAT_WEBHOOK_AUTH")),
		RevenueCatRESTAPIKey:             strings.TrimSpace(os.Getenv("REVENUECAT_REST_API_KEY")),
		RevenueCatProjectID:              strings.TrimSpace(os.Getenv("REVENUECAT_PROJECT_ID")),
		RevenueCatAppID:                  strings.TrimSpace(os.Getenv("REVENUECAT_APP_ID")),
		RevenueCatAPIBaseURL:             strings.TrimRight(strings.TrimSpace(os.Getenv("REVENUECAT_API_BASE_URL")), "/"),
		AzureSpeechKey:                   strings.TrimSpace(os.Getenv("AZURE_SPEECH_KEY")),
		AzureSpeechRegion:                strings.TrimSpace(os.Getenv("AZURE_SPEECH_REGION")),
		AzureSpeechEndpoint:              strings.TrimRight(strings.TrimSpace(os.Getenv("AZURE_SPEECH_ENDPOINT")), "/"),
		STTProvider:                      strings.ToLower(strings.TrimSpace(os.Getenv("STT_PROVIDER"))),
		AzureSTTCostPerHour:              parseFloatEnvDefault("AZURE_STT_COST_PER_HOUR", 0.36),
		GeminiSTTCostPerHour:             parseFloatEnvDefault("GEMINI_STT_COST_PER_HOUR", 0.10),
		MaxPodcastAudioUploadMB:          parseIntEnvDefault("MAX_PODCAST_AUDIO_UPLOAD_MB", 500),
		ElevenLabsAPIKey:                 strings.TrimSpace(os.Getenv("ELEVENLABS_API_KEY")),
		GeminiAPIKey:                     strings.TrimSpace(os.Getenv("GEMINI_API_KEY")),
		CloudflareAccountID:              strings.TrimSpace(os.Getenv("CLOUDFLARE_ACCOUNT_ID")),
		CloudflareAPIToken:               strings.TrimSpace(os.Getenv("CLOUDFLARE_API_TOKEN")),
		OutDir:                           strings.TrimSpace(os.Getenv("OUT_DIR")),
		PersistentRoot:                   strings.TrimSpace(os.Getenv("SERIES_ROOT")),

		DashboardOrigins:        splitCSV(os.Getenv("DASHBOARD_ORIGINS")),
		DashboardServiceToken:   strings.TrimSpace(os.Getenv("DASHBOARD_SERVICE_TOKEN")),
		AuthIssuer:              strings.TrimRight(strings.TrimSpace(os.Getenv("AUTH_ISSUER")), "/"),
		AdminAllowedClientIDs:   splitCSV(os.Getenv("ADMIN_ALLOWED_CLIENT_IDS")),
		WebsiteBaseURL:          strings.TrimRight(strings.TrimSpace(os.Getenv("WEBSITE_BASE_URL")), "/"),
		FrontendPublicURL:       strings.TrimRight(strings.TrimSpace(os.Getenv("FRONTEND_PUBLIC_URL")), "/"),
		APNSKeyID:               strings.TrimSpace(os.Getenv("APNS_KEY_ID")),
		APNSTeamID:              strings.TrimSpace(os.Getenv("APNS_TEAM_ID")),
		APNSBundleID:            strings.TrimSpace(os.Getenv("APNS_BUNDLE_ID")),
		APNSKeyBase64:           strings.TrimSpace(os.Getenv("APNS_KEY_BASE64")),
		APNSEnvironment:         strings.TrimSpace(os.Getenv("APNS_ENVIRONMENT")),
		SearchAPIKey:            strings.TrimSpace(os.Getenv("SEARCH_API_KEY")),
		SearchAPIURL:            strings.TrimSpace(os.Getenv("SEARCH_API_URL")),
		FirecrawlAPIKey:         strings.TrimSpace(os.Getenv("FIRECRAWL_API_KEY")),
		MarkitdownServerURL:     strings.TrimRight(strings.TrimSpace(os.Getenv("MARKITDOWN_SERVER_URL")), "/"),
		MarkitdownAPIKey:        strings.TrimSpace(os.Getenv("MARKITDOWN_API_KEY")),
		NotionOAuthClientID:     strings.TrimSpace(os.Getenv("NOTION_OAUTH_CLIENT_ID")),
		NotionOAuthClientSecret: strings.TrimSpace(os.Getenv("NOTION_OAUTH_CLIENT_SECRET")),
		NotionOAuthRedirectURI:  strings.TrimSpace(os.Getenv("NOTION_OAUTH_REDIRECT_URI")),
		NotionAppCallbackURL:    strings.TrimSpace(os.Getenv("NOTION_APP_CALLBACK_URL")),
		NotionAPIBaseURL:        strings.TrimRight(strings.TrimSpace(os.Getenv("NOTION_API_BASE_URL")), "/"),

		S3Bucket:          strings.TrimSpace(os.Getenv("S3_BUCKET")),
		S3Region:          strings.TrimSpace(os.Getenv("S3_REGION")),
		S3Endpoint:        strings.TrimSpace(os.Getenv("S3_ENDPOINT")),
		S3Prefix:          strings.TrimSpace(os.Getenv("S3_PREFIX")),
		S3DownloadBaseURL: strings.TrimSpace(os.Getenv("S3_DOWNLOAD_BASE_URL")),
		S3AccessKeyID:     strings.TrimSpace(os.Getenv("S3_ACCESS_KEY_ID")),
		S3SecretAccessKey: strings.TrimSpace(os.Getenv("S3_SECRET_ACCESS_KEY")),

		DatabaseURL:        firstNonEmpty(os.Getenv("DATABASE_URL"), os.Getenv("TURSO_CONNECTION_URL")),
		TursoConnectionURL: strings.TrimSpace(os.Getenv("TURSO_CONNECTION_URL")),
		TursoAuthToken:     strings.TrimSpace(os.Getenv("TURSO_AUTH_TOKEN")),
		RedisURL:           strings.TrimSpace(os.Getenv("REDIS_URL")),
		RabbitMQURL:        strings.TrimSpace(os.Getenv("RABBITMQ_URL")),
		MQQueuePrefix:      strings.TrimSpace(os.Getenv("MQ_QUEUE_PREFIX")),

		PodName:          podIdentity(),
		PeerHostTemplate: strings.TrimSpace(os.Getenv("PEER_HOST_TEMPLATE")),

		E2EMode: strings.EqualFold(strings.TrimSpace(os.Getenv("E2E_MODE")), "true"),
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
	if e.PodcastSummaryModel == "" {
		e.PodcastSummaryModel = e.HostModel
	}
	if e.PodcastTranslationModel == "" {
		e.PodcastTranslationModel = e.PodcastSummaryModel
	}
	if e.JudgementModel == "" {
		e.JudgementModel = e.HostModel
	}
	if e.PodcastSummaryPPTModel == "" {
		e.PodcastSummaryPPTModel = e.PodcastSummaryModel
	}
	if e.TranscribeModel == "" {
		e.TranscribeModel = "gemini-2.5-flash"
	}
	if e.STTProvider == "" {
		e.STTProvider = "gemini"
	}
	if e.OutDir == "" {
		e.OutDir = "./out"
	}
	if e.NotionAppCallbackURL == "" {
		e.NotionAppCallbackURL = "debatepod://notion-callback"
	}
	if e.NotionAPIBaseURL == "" {
		e.NotionAPIBaseURL = "https://api.notion.com"
	}
	if e.RevenueCatAPIBaseURL == "" {
		e.RevenueCatAPIBaseURL = "https://api.revenuecat.com"
	}
	if e.PersistentRoot == "" {
		// Default the cross-session archive root to the user's OUT_DIR so
		// out-of-the-box runs put `tv-series/...` next to `session-<stamp>/...`.
		// This is captured BEFORE bootstrap appends the session stamp so
		// archived episodes survive across runs.
		e.PersistentRoot = e.OutDir
	}

	// In E2E mode the real provider credentials/models are irrelevant — the
	// fake LLM is wired in during bootstrap. Backfill placeholders so the rest
	// of the config (which expects non-empty model names / base URL) is valid,
	// and skip the required-env validation below.
	if e.E2EMode {
		// Safety: never let E2E mode touch the cloud database or cache, even when a
		// real DATABASE_URL / TURSO_CONNECTION_URL / REDIS_URL is present in .env
		// (godotenv.Overload makes .env win over the process environment, so the
		// orchestration script cannot reliably blank these from the outside). Forcing
		// them empty here guarantees a local SQLite file and disabled Redis.
		e.DatabaseURL = ""
		e.TursoConnectionURL = ""
		e.TursoAuthToken = ""
		e.RedisURL = ""
		// The full iOS E2E suite exercises the real broker-backed job queue. Admin
		// browser tests do not submit generation jobs, so they may opt into the
		// in-process queue and avoid bringing up unrelated infrastructure.
		if strings.EqualFold(strings.TrimSpace(os.Getenv("E2E_QUEUE_MODE")), "inline") {
			e.RabbitMQURL = ""
		} else {
			e.RabbitMQURL = strings.TrimSpace(os.Getenv("E2E_RABBITMQ_URL"))
			if e.RabbitMQURL == "" {
				e.RabbitMQURL = "amqp://guest:guest@127.0.0.1:5672/"
			}
		}
		if e.MQQueuePrefix == "" {
			e.MQQueuePrefix = "e2e-"
		}
		// Force an isolated, deterministic data root so the seeded SQLite DB and
		// per-run output never collide with a developer's real OUT_DIR (which .env
		// may pin). The orchestration script wipes E2E_DATA_ROOT before each run so
		// every run starts from a freshly seeded database.
		root := strings.TrimSpace(os.Getenv("E2E_DATA_ROOT"))
		if root == "" {
			root = filepath.Join(os.TempDir(), "debate-bot-e2e")
		}
		e.OutDir = root
		e.PersistentRoot = root
		if e.OpenAIBaseURL == "" {
			e.OpenAIBaseURL = "http://127.0.0.1:0/v1" // overridden in bootstrap
		}
		if e.OpenAIKey == "" {
			e.OpenAIKey = "e2e"
		}
		for _, m := range []*string{&e.HostModel, &e.CompressionModel, &e.ScenePlannerModel, &e.PodcastSummaryModel, &e.PodcastTranslationModel, &e.JudgementModel, &e.PodcastSummaryPPTModel} {
			if *m == "" {
				*m = "e2e-fake-model"
			}
		}
		if e.CompressionBaseURL == "" {
			e.CompressionBaseURL = e.OpenAIBaseURL
		}
		if e.CompressionKey == "" {
			e.CompressionKey = e.OpenAIKey
		}
		if e.GeminiAPIKey == "" {
			e.GeminiAPIKey = "e2e"
		}
		return e, nil
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
// podIdentity returns this process's pod name for cross-pod routing, preferring
// the explicit POD_NAME (set via the k8s downward API) and falling back to
// HOSTNAME (which on a StatefulSet pod is the stable "<name>-<ordinal>").
func podIdentity() string {
	if v := strings.TrimSpace(os.Getenv("POD_NAME")); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("HOSTNAME"))
}

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

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
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

// parseIntEnvDefault parses an integer env var, returning def when unset or
// invalid. Negative values are rejected (fall back to def) since every points
// quantity is non-negative.
func parseIntEnvDefault(key string, def int64) int64 {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return def
	}
	return n
}

// ErrEnvNotLoaded is returned when an Env was expected but not initialised.
var ErrEnvNotLoaded = errors.New("env not loaded")
