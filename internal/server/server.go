// Package server hosts the HTTP API for a debate run.
//
// The server always operates in TV-channel mode: one channels.json defines
// the available channels, debate.md files declare which channel they belong
// to, and each channel runs its own queue of debates sequentially while all
// channels run in parallel. Each channel has its own LiveStream + Encoder +
// HLS dir; channels with no assigned debates are listed as off-air.
//
// Endpoints:
//
//	GET  /api/topics                        — channel list (number, title, off-air, debates queue).
//	GET  /api/transcript?channel=<id>       — JSON snapshot of that channel's live transcript.
//	GET  /api/events[?channel=<id>]         — Server-Sent Events; channel filter is optional.
//	GET  /api/audio/<id>/stream             — chunked MP3 audio for that channel.
//	GET  /api/video/<id>/<file>             — HLS playlist + segments for that channel.
//	POST /api/messages?channel=<id>         — push a user message into that channel's orchestrator
//	                                          (uses the viewer's `debate-bot-username` cookie).
//	GET  /api/me                            — return the viewer's username; issues + sets a cookie
//	                                          on first request.
//	POST /api/me                            — change the viewer's username (body: {username}).
//	GET  /                                  — embedded web UI.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/audio"
	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/content_creator"
	"github.com/sirily11/debate-bot/internal/eventbus"
	"github.com/sirily11/debate-bot/internal/mq"
	"github.com/sirily11/debate-bot/internal/storage"
)

// HTTP server modes. Mirror the cmd-side mode strings so route mounting and
// the cmd flag stay in lockstep.
const (
	ModeStream    = "stream"
	ModeVideo     = "video"
	ModeDashboard = "dashboard"
)

// Deps wires the server to the event bus and the registry that tracks every
// channel + its current orchestrator. Per-channel streaming resources
// (LiveStream, HLS dir) are reached through Sessions.ChannelResources(id).
//
// Mode selects the HTTP surface: "stream" (default) mounts the channel /
// HLS / live-chat routes; "video" mounts /api/jobs/* on top of the same
// embedded SPA so a browser can upload a script.md and receive a
// downloadable mp4 + (for series) a zip archive.
//
// Jobs / SubmitJob are only consulted when Mode == "video". SubmitJob
// is a closure provided by main; the handler creates the job id, saves
// the upload files under UploadRoot/<jobID>/, registers the pending
// job, and hands off to SubmitJob which runs the orchestrator pipeline
// asynchronously and updates JobRegistry as the run progresses.
//
// UploadRoot is the directory where uploaded scripts + priors zips
// land. Each job gets its own subdirectory keyed by jobID.
type Deps struct {
	Mode        string
	Bus         *eventbus.Bus
	Sessions    *SessionRegistry
	Jobs        *JobRegistry
	Discussions *DiscussionStore
	// Points backs the points economy (per-user balance, ledger, charges). nil
	// disables points gating/charging/hiding entirely — the server behaves as
	// before. Wired from the same database as Discussions.
	Points *PointsStore
	// AppConfig holds admin-editable app-level configuration (e.g. the default
	// generation model) that overrides the env defaults. nil falls back to env
	// entirely. Wired from the same database as Discussions.
	AppConfig *AppConfigStore
	// IAPProducts holds the admin-owned in-app-purchase product catalog. Purchase
	// webhooks and admin top-ups use this table as the source of truth for
	// product ids and points grants. nil makes purchase products unavailable.
	IAPProducts *IAPProductStore
	// IAPProductSyncer pushes enabled admin-created products to RevenueCat. nil
	// means enabled product saves are rejected by the admin resource.
	IAPProductSyncer IAPProductSyncer
	// SubscriptionPermissions maps each subscription class (an IAP subscription
	// product, plus a free/no-subscription sentinel) to a nested permission
	// object. It backs the admin CRUD resource and the /api/entitlements
	// resolver. nil disables per-class gating (everyone resolves to the default
	// permissions). Wired from the same database as Discussions.
	SubscriptionPermissions *SubscriptionPermissionStore
	// Entitlements caches per-user resolved permissions in Redis for 60s so the
	// entitlements endpoint and UI-action gating stay cheap. nil disables
	// caching — the resolver recomputes on every call.
	Entitlements *EntitlementsStore
	// Maintenance backs scheduled-maintenance windows: the admin CRUD resource
	// and the precheck/config gating that pauses the app during a window. nil
	// disables maintenance gating. Wired from the JobRegistry's database.
	Maintenance *MaintenanceStore
	Progress    *DiscussionProgressStore
	// Planning backs the conversational planning phase (agent loop with tools,
	// persisted conversation, per-conversation billing). nil disables the
	// /api/discussions/{id}/planning* routes. Wired from the same database as
	// Discussions.
	Planning *PlanningStore
	// PlanningStreams stores active planning stream IDs and replayable SSE
	// frames in Redis so clients can resume after disconnecting.
	PlanningStreams *PlanningStreamStore
	// ModelCatalog caches the gateway's advertised model roster (GET /api/models)
	// in Redis for 24h. nil disables caching — the handler fetches live each time.
	ModelCatalog *ModelCatalogStore
	// VoiceCatalog caches the Azure TTS voice roster (GET /api/voices) in Redis
	// for 24h. nil disables caching — the handler fetches live each time.
	VoiceCatalog *VoiceCatalogStore
	Log          *slog.Logger
	UploadRoot   string
	SubmitJob    func(jobID string, sub JobSubmission) error
	// Password, when non-empty, gates every /api/* route behind a login
	// cookie. Empty disables auth entirely (the default).
	Password string

	// Env / MCPCfg back the dashboard-facing metadata + planning endpoints
	// (GET /api/models, GET /api/tools, POST /api/plan). Both may be nil in
	// deployments that don't expose those routes; the handlers degrade
	// gracefully when they are.
	Env    *config.Env
	MCPCfg *config.MCPConfig

	// AllowedOrigins, when non-empty, enables CORS for the listed browser
	// origins so a separately-hosted dashboard (different origin) can call
	// the API. Empty leaves CORS off (same-origin SPA only).
	AllowedOrigins []string

	// ServiceToken, when non-empty, lets a trusted backend (the dashboard's
	// Next.js server) authenticate via `Authorization: Bearer <token>` in
	// addition to / instead of the human password cookie. It is never
	// exposed to browsers — the dashboard forwards it server-side only.
	ServiceToken string

	// WebsiteBaseURL is the public base of the deep-link website (e.g.
	// https://podcast.rxlab.app) used to mint share-link URLs (…/s/{token}).
	// Empty falls back to https://podcast.rxlab.app.
	WebsiteBaseURL string

	// AuthIssuer, when non-empty (e.g. https://auth.rxlab.app), enables
	// per-user rxlab OAuth: a request carrying `Authorization: Bearer
	// <access token>` is validated against the issuer's OIDC userinfo
	// endpoint. This lets native clients (the iOS app) authenticate directly
	// with their RxAuthSwift access token, no service token required.
	AuthIssuer string

	// AdminAllowedClientIDs restricts which OAuth client_ids may call the
	// /admin API (rxlab-auth access tokens carry no `aud`). Passed to the admin
	// OIDC authenticator. Empty accepts any client presenting a valid
	// admin-role token. The admin API is only mounted when AuthIssuer is set.
	AdminAllowedClientIDs []string

	// Uploader, when enabled, serves finished videos from S3 (presigned
	// redirect) instead of local disk. nil / disabled keeps disk serving.
	Uploader *storage.Uploader

	// ForceAudio, when true, means the engine was started with --audio: every
	// job renders as an audio-only feed regardless of the request. Surfaced on
	// GET /api/config so a frontend can hide video-only controls.
	ForceAudio bool

	// PodName is this process's stable identity in a horizontally-scaled
	// deployment (the StatefulSet pod hostname). When set together with
	// PeerHostFor, the server reverse-proxies requests for an in-flight job to
	// the pod that owns its live orchestrator + audio stream. Empty disables
	// cross-pod routing (single-pod / local).
	PodName string

	// PeerHostFor maps an owner pod name to the host:port to dial for it
	// (typically "<pod>.<headless-svc>.<ns>.svc.cluster.local:<port>"). nil
	// disables cross-pod routing.
	PeerHostFor func(pod string) string

	// APNS sends native push notifications. nil disables push sending.
	APNS *APNSClient

	// MQPing reports RabbitMQ connectivity for /healthz. nil skips the
	// check (in-process queue fallback or non-video modes).
	MQPing func(context.Context) error

	// MQ publishes generation tasks (video render, summary, mindmap,
	// ppt/pdf export, planning turns) onto the durable job queue. nil in
	// modes that don't run generation.
	MQ mq.Client
}

// Server is the HTTP front-end.
type Server struct {
	d   Deps
	mux *http.ServeMux
	// handler is what ListenAndServe / Handler expose: the bare mux when no
	// password is set, or the auth-wrapped mux otherwise.
	handler http.Handler
	// authTok is the precomputed cookie token for the configured password.
	authTok string
	// oauth validates per-user rxlab bearer tokens via the issuer's userinfo
	// endpoint (nil when AuthIssuer is unset).
	oauth *oauthValidator
	apns  *APNSClient

	jobMessageRateMu   sync.Mutex
	jobMessageRateLast map[string]time.Time
	jobMessageRateNow  func() time.Time

	discussionPlanMu   sync.Mutex
	discussionPlanRuns map[string]*discussionPlanRun

	// planningRunMu guards planningRuns, a set of in-flight conversational
	// planning turns keyed by conversation id, so two concurrent stream/answer
	// requests for one conversation can't run the agent loop twice at once.
	planningRunMu sync.Mutex
	planningRuns  map[string]bool

	pushMu           sync.Mutex
	podcastStartSent map[string]bool
}

type discussionPlanRun struct {
	done chan struct{}
	err  error
}

// New builds a Server with all routes mounted.
func New(d Deps) *Server {
	s := &Server{
		d:                  d,
		mux:                http.NewServeMux(),
		jobMessageRateLast: make(map[string]time.Time),
		jobMessageRateNow:  time.Now,
		discussionPlanRuns: make(map[string]*discussionPlanRun),
		planningRuns:       make(map[string]bool),
		podcastStartSent:   make(map[string]bool),
	}
	if d.Password != "" {
		s.authTok = authToken(d.Password)
	}
	if d.AuthIssuer != "" {
		s.oauth = newOAuthValidator(d.AuthIssuer, d.Log)
	}
	if d.APNS != nil {
		s.apns = d.APNS
	} else if apns, err := NewAPNSClient(d.Env); err == nil {
		s.apns = apns
	} else {
		s.logger().Warn("APNs disabled", "err", err)
	}
	if s.apns != nil {
		s.logger().Info("APNs enabled", "environment", s.apns.Environment())
	} else {
		s.logger().Info("APNs disabled")
	}
	// Admin-backed stores. AppConfig (default-model override) shares the
	// discussion database; Maintenance (window gating) shares the job database.
	// Built here so the resolver, precheck/config gating, and the admin API can
	// all reach them. Gating is active wherever the backing store exists (video
	// + dashboard modes), not only where the admin API is mounted.
	if s.d.AppConfig == nil && d.Discussions != nil {
		if ac, err := NewAppConfigStore(d.Discussions); err != nil {
			s.logger().Warn("app config store disabled", "err", err)
		} else {
			s.d.AppConfig = ac
		}
	}
	if s.d.IAPProducts == nil && d.Discussions != nil {
		if ps, err := NewIAPProductStore(d.Discussions); err != nil {
			s.logger().Warn("iap product store disabled", "err", err)
		} else {
			s.d.IAPProducts = ps
		}
	}
	if s.d.IAPProductSyncer == nil {
		s.d.IAPProductSyncer = NewIAPProductSyncer(d.Env, nil)
	}
	if s.d.SubscriptionPermissions == nil && d.Discussions != nil {
		if sp, err := NewSubscriptionPermissionStore(d.Discussions); err != nil {
			s.logger().Warn("subscription permission store disabled", "err", err)
		} else {
			s.d.SubscriptionPermissions = sp
		}
	}
	if s.d.Maintenance == nil && d.Jobs != nil && d.Jobs.db != nil {
		if ms, err := NewMaintenanceStore(d.Jobs.db); err != nil {
			s.logger().Warn("maintenance store disabled", "err", err)
		} else {
			s.d.Maintenance = ms
		}
	}

	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /api/config", s.handleConfig)
	s.mux.HandleFunc("POST /api/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/logout", s.handleLogout)
	s.mux.HandleFunc("GET /api/events", s.handleEvents)
	s.mux.HandleFunc("GET /api/me", s.handleGetMe)
	s.mux.HandleFunc("POST /api/me", s.handlePostMe)
	s.mux.HandleFunc("GET /api/debug", s.handleDebug)
	s.mux.HandleFunc("GET /api/precheck", s.handlePrecheck)

	// Dashboard-facing metadata routes (always mounted): model + tool
	// discovery and the planning/script-generation endpoints. They degrade
	// gracefully when Env/MCPCfg are nil.
	s.mux.HandleFunc("GET /api/discussion-types", s.handleDiscussionTypes)
	s.mux.HandleFunc("GET /api/templates", s.handleTemplates)
	s.mux.HandleFunc("GET /api/models", s.handleModels)
	s.mux.HandleFunc("GET /api/entitlements", s.handleEntitlements)
	s.mux.HandleFunc("GET /api/voices", s.handleVoices)
	s.mux.HandleFunc("POST /api/voices/preview", s.handleVoicePreview)
	s.mux.HandleFunc("GET /api/tools", s.handleTools)
	s.mux.HandleFunc("POST /api/plan", s.handlePlan)
	s.mux.HandleFunc("POST /api/plan/improve", s.handlePlanImprove)
	s.mux.HandleFunc("POST /api/uploads/presign", s.handleUploadPresign)
	s.mux.HandleFunc("POST /api/uploads/complete", s.handleUploadComplete)
	s.mux.HandleFunc("POST /api/uploads", s.handleUpload)
	s.mux.HandleFunc("POST /api/transcribe", s.handleTranscribeAudio)

	// "video" and "dashboard" both run the upload-and-render job pipeline;
	// only the embedded SPA differs (dashboard has its own frontend). Stream
	// mode is everything else.
	jobsMode := d.Mode == ModeVideo || d.Mode == ModeDashboard

	// Stream-mode routes (channel queues, HLS, live chat).
	if !jobsMode {
		s.mux.HandleFunc("GET /api/topics", s.handleTopics)
		s.mux.HandleFunc("GET /api/transcript", s.handleTranscript)
		s.mux.HandleFunc("GET /api/audio/", s.handleAudio)
		s.mux.HandleFunc("GET /api/video/", s.handleVideo)
		s.mux.HandleFunc("POST /api/messages", s.handleMessages)
	}

	// Job-pipeline routes (video + dashboard modes).
	if jobsMode {
		s.mux.HandleFunc("POST /api/jobs", s.handleJobSubmit)
		s.mux.HandleFunc("POST /api/jobs/json", s.handleJobSubmitJSON)
		s.mux.HandleFunc("GET /api/jobs", s.handleJobList)
		s.mux.HandleFunc("GET /api/jobs/{id}", s.handleJobGet)
		s.mux.HandleFunc("GET /api/jobs/{id}/video", s.handleJobVideo)
		s.mux.HandleFunc("GET /api/jobs/{id}/audio", s.handleJobAudio)
		s.mux.HandleFunc("GET /api/jobs/{id}/transcript", s.handleJobTranscript)
		s.mux.HandleFunc("GET /api/jobs/{id}/subtitles", s.handleJobSubtitles)
		s.mux.HandleFunc("GET /api/jobs/{id}/subtitles/live", s.handleJobSubtitlesLive)
		s.mux.HandleFunc("GET /api/jobs/{id}/illustrations", s.handleJobIllustrations)
		s.mux.HandleFunc("GET /api/jobs/{id}/archive", s.handleJobArchive)
		s.mux.HandleFunc("GET /api/jobs/{id}/hls/{file}", s.handleJobHLS)
		s.mux.HandleFunc("GET /api/jobs/{id}/ws", s.handleJobWS)
		s.mux.HandleFunc("POST /api/jobs/{id}/messages", s.handleJobMessage)
		s.mux.HandleFunc("POST /api/jobs/{id}/stop", s.handleJobStop)
	}

	if d.Discussions != nil {
		s.mux.HandleFunc("GET /api/discussions", s.handleDiscussionList)
		s.mux.HandleFunc("GET /api/home/ui-actions", s.handleHomeUIActions)
		s.mux.HandleFunc("POST /api/discussions", s.handleDiscussionCreate)
		s.mux.HandleFunc("GET /api/discussions/parent-podcasts", s.handleDiscussionParentPodcastList)
		s.mux.HandleFunc("POST /api/discussions/plan", s.handleDiscussionPlan)
		s.mux.HandleFunc("POST /api/discussions/plan/stream", s.handleDiscussionPlanStream)
		s.mux.HandleFunc("POST /api/discussions/{id}/plan/stream", s.handleDiscussionPlanStreamForID)
		s.mux.HandleFunc("POST /api/discussions/{id}/create/plan", s.handleDiscussionCreateFromPlan)
		s.mux.HandleFunc("GET /api/discussions/{id}", s.handleDiscussionGet)
		s.mux.HandleFunc("GET /api/discussions/{id}/ui-actions", s.handleDiscussionUIActions)
		s.mux.HandleFunc("GET /api/discussions/{id}/parent-podcast", s.handleDiscussionParentPodcastGet)
		s.mux.HandleFunc("GET /api/discussions/{id}/summary", s.handleDiscussionSummary)
		s.mux.HandleFunc("GET /api/discussions/{id}/summary/pdf", s.handleDiscussionSummaryPDF)
		s.mux.HandleFunc("GET /api/discussions/{id}/summary/pptx", s.handleDiscussionSummaryPPTX)
		s.mux.HandleFunc("GET /api/discussions/{id}/summary/ppt/pdf", s.handleDiscussionSummaryPPTPDF)
		// Async exports: POST enqueues a queued render (with retry) that
		// stages the artifact in object storage; the GET routes above then
		// serve it straight from the cache. The GETs keep their synchronous
		// render as a fallback for older clients and S3-less dev setups.
		s.mux.HandleFunc("POST /api/discussions/{id}/summary/pdf/export", s.handleDiscussionSummaryExport(summaryExportKindPDF))
		s.mux.HandleFunc("POST /api/discussions/{id}/summary/pptx/export", s.handleDiscussionSummaryExport(summaryExportKindPPTX))
		s.mux.HandleFunc("POST /api/discussions/{id}/summary/ppt/pdf/export", s.handleDiscussionSummaryExport(summaryExportKindPPTPDF))
		s.mux.HandleFunc("POST /api/discussions/{id}/summary/generate", s.handleDiscussionSummaryGenerate)
		s.mux.HandleFunc("GET /api/discussions/{id}/mindmap", s.handleDiscussionMindmap)
		s.mux.HandleFunc("PUT /api/discussions/{id}/mindmap", s.handleDiscussionMindmapSave)
		s.mux.HandleFunc("POST /api/discussions/{id}/mindmap/generate", s.handleDiscussionMindmapGenerate)
		s.mux.HandleFunc("POST /api/discussions/{id}/video/generate", s.handleDiscussionVideoGenerate)
		s.mux.HandleFunc("POST /api/discussions/{id}/summary/notion", s.handleExportSummaryToNotion)
		s.mux.HandleFunc("PATCH /api/discussions/{id}", s.handleDiscussionRename)
		s.mux.HandleFunc("DELETE /api/discussions/{id}", s.handleDiscussionDelete)
		s.mux.HandleFunc("POST /api/discussions/{id}/improve", s.handleDiscussionImprove)
		s.mux.HandleFunc("POST /api/discussions/{id}/improve/stream", s.handleDiscussionImproveStream)
		if d.Planning != nil {
			s.mux.HandleFunc("GET /api/discussions/{id}/planning", s.handlePlanningConversationGet)
			s.mux.HandleFunc("GET /api/discussions/{id}/planning/stream", s.handlePlanningStreamResume)
			s.mux.HandleFunc("POST /api/discussions/{id}/planning/stream", s.handlePlanningStream)
			s.mux.HandleFunc("POST /api/discussions/{id}/planning/answer", s.handlePlanningAnswer)
		}
		s.mux.HandleFunc("POST /api/discussions/{id}/sources", s.handleDiscussionAddSources)
		s.mux.HandleFunc("POST /api/discussions/{id}/sources/stream", s.handleDiscussionAddSourcesStream)
		s.mux.HandleFunc("POST /api/discussions/{id}/sources/search", s.handleDiscussionSearchSources)
		s.mux.HandleFunc("POST /api/discussions/{id}/generate", s.handleDiscussionGenerate)
		s.mux.HandleFunc("GET /api/discussions/{id}/chapters", s.handleDiscussionChapters)
		s.mux.HandleFunc("POST /api/discussions/{id}/chapters/generate", s.handleDiscussionChaptersGenerate)
		s.mux.HandleFunc("GET /api/albums", s.handleAlbumList)
		s.mux.HandleFunc("POST /api/albums", s.handleAlbumCreate)
		s.mux.HandleFunc("GET /api/albums/{id}", s.handleAlbumGet)
		s.mux.HandleFunc("GET /api/albums/{id}/ui-actions", s.handleAlbumUIActions)
		s.mux.HandleFunc("POST /api/albums/{id}/publish", s.handleAlbumPublish)
		s.mux.HandleFunc("PATCH /api/albums/{id}", s.handleAlbumRename)
		s.mux.HandleFunc("PATCH /api/albums/{id}/cover", s.handleAlbumCoverSet)
		s.mux.HandleFunc("POST /api/albums/{id}/cover/generate", s.handleAlbumCoverGenerate)
		s.mux.HandleFunc("DELETE /api/albums/{id}", s.handleAlbumDelete)
		s.mux.HandleFunc("POST /api/albums/{id}/discussions", s.handleAlbumAddMembers)
		s.mux.HandleFunc("DELETE /api/albums/{id}/discussions/{discussionID}", s.handleAlbumRemoveMember)
		s.mux.HandleFunc("PATCH /api/discussions/{id}/visibility", s.handleDiscussionVisibility)
		s.mux.HandleFunc("PATCH /api/discussions/{id}/speaker-model", s.handleUpdateSpeakerModel)
		s.mux.HandleFunc("PATCH /api/discussions/{id}/speaker-voice", s.handleUpdateSpeakerVoice)
		s.mux.HandleFunc("POST /api/discussions/{id}/cover/generate", s.handleDiscussionCoverGenerate)
		s.mux.HandleFunc("PATCH /api/discussions/{id}/cover", s.handleDiscussionCoverSet)
		s.mux.HandleFunc("POST /api/discussions/{id}/lines", s.handleDiscussionAppendLine)
		s.mux.HandleFunc("POST /api/push-tokens", s.handlePushTokenRegister)
		s.mux.HandleFunc("DELETE /api/push-tokens", s.handlePushTokenDelete)
		s.mux.HandleFunc("POST /api/discussions/{id}/shares", s.handleDiscussionShareCreate)
		s.mux.HandleFunc("GET /api/discussions/{id}/shares", s.handleDiscussionShareList)
		s.mux.HandleFunc("DELETE /api/discussions/{id}/shares/{token}", s.handleDiscussionShareRevoke)
		s.mux.HandleFunc("POST /api/discussions/{id}/join", s.handleDiscussionJoin)
		s.mux.HandleFunc("POST /api/share/{token}/join", s.handleShareJoin)
		s.mux.HandleFunc("GET /api/share/{token}", s.handleShareResolve)
		s.mux.HandleFunc("GET /api/market/profile", s.handleMarketProfile)
		s.mux.HandleFunc("GET /api/market/creators/following", s.handleMarketCreatorFollowing)
		s.mux.HandleFunc("GET /api/market/creators/{id}", s.handleMarketCreatorGet)
		s.mux.HandleFunc("GET /api/market/creators/{id}/stations", s.handleMarketCreatorStations)
		s.mux.HandleFunc("POST /api/market/creators/{id}/follow", s.handleMarketCreatorFollow)
		s.mux.HandleFunc("DELETE /api/market/creators/{id}/follow", s.handleMarketCreatorUnfollow)
		s.mux.HandleFunc("GET /api/market/stations", s.handleMarketList)
		s.mux.HandleFunc("GET /api/market/stations/liked", s.handleMarketLikedList)
		s.mux.HandleFunc("GET /api/market/stations/{id}", s.handleMarketGet)
		s.mux.HandleFunc("GET /api/market/albums/{id}", s.handleMarketAlbumGet)
		s.mux.HandleFunc("POST /api/market/stations/{id}/like", s.handleMarketLike)
		s.mux.HandleFunc("DELETE /api/market/stations/{id}/like", s.handleMarketUnlike)
		s.mux.HandleFunc("GET /api/points/balance", s.handlePointsBalance)
		s.mux.HandleFunc("GET /api/points/history", s.handlePointsHistory)
		s.mux.HandleFunc("GET /api/notion/status", s.handleNotionStatus)
		s.mux.HandleFunc("GET /api/notion/oauth/url", s.handleNotionAuthURL)
		s.mux.HandleFunc("GET /api/notion/oauth/callback", s.handleNotionOAuthCallback)
		s.mux.HandleFunc("POST /api/notion/pages/search", s.handleNotionSearchPages)
		s.mux.HandleFunc("POST /api/notion/pages/attachment", s.handleNotionPageAttachment)
	}
	if s.apns != nil && d.Bus != nil && d.Discussions != nil {
		go s.watchPushEvents()
	}

	// The RevenueCat webhook authenticates with its own shared secret (it can't
	// carry an OAuth bearer), so it is mounted unconditionally and bypasses the
	// auth middleware allowlist; the handler verifies the secret itself.
	s.mux.HandleFunc("POST /api/revenuecat/webhook", s.handleRevenueCatWebhook)

	// The schema-driven admin API (dashboard mode only). It is mounted on the
	// mux under /admin/ — outside the /api/ auth allowlist — and enforces its
	// own OIDC bearer + admin-role authorization. Requires the rxlab OAuth
	// issuer; disabled (with a warning) when discovery fails. Hermetic E2E mode
	// supplies its own fixed admin identity, so it intentionally needs no issuer.
	if d.Mode == ModeDashboard && (d.AuthIssuer != "" || s.e2eMode()) {
		if adminHandler, err := s.newAdminHandler(context.Background()); err != nil {
			s.logger().Warn("admin API disabled", "err", err)
		} else {
			s.mux.Handle(adminBasePath+"/", adminHandler)
			s.logger().Info("admin API enabled", "base_path", adminBasePath)
		}
	}

	// The embedded TV SPA is served in stream + video modes. In dashboard
	// mode the Next.js app is the frontend, so "/" returns a tiny health
	// response instead of the bundled UI.
	if d.Mode == ModeDashboard {
		// Method-less "/" (not "GET /") so the more specific "/admin/" subtree
		// pattern is unambiguously preferred by ServeMux; a method-qualified
		// catch-all would conflict with the all-methods /admin/ registration.
		s.mux.HandleFunc("/", s.handleDashboardRoot)
	} else {
		s.mux.Handle("/", staticHandler())
	}

	// Auth engages when a human password, a service token, OR a per-user OAuth
	// issuer is configured. CORS, when configured, wraps the auth layer so
	// cross-origin preflight (OPTIONS, which carries no credentials) is answered
	// before auth runs.
	var handler http.Handler = s.mux
	if s.authTok != "" || d.ServiceToken != "" || d.AuthIssuer != "" {
		s.logAuthStartup(true)
		handler = s.withAuth(handler)
	} else {
		s.logAuthStartup(false)
	}
	// Maintenance gating wraps OUTSIDE auth so an active window returns 503 with
	// the message to every client (even unauthenticated ones) before auth runs,
	// while the admin API + config/precheck stay reachable (see the allowlist).
	handler = s.withMaintenance(handler)
	// Cross-pod routing wraps the local (auth+mux) handler: a request for an
	// in-flight job that this pod does not own is reverse-proxied to the owner
	// (which re-runs auth on the forwarded credentials); everything else falls
	// through to the local handler. No-op when PodName/PeerHostFor are unset.
	if jobsMode {
		handler = s.withJobProxy(handler)
	}
	if len(d.AllowedOrigins) > 0 {
		handler = withCORS(d.AllowedOrigins, handler)
	}
	s.handler = handler
	return s
}

// handleDashboardRoot answers "/" in dashboard mode with a tiny health/info
// payload (the embedded TV SPA isn't served — the Next.js dashboard is the
// frontend). Unknown paths still 404 since the catch-all GET / pattern also
// receives them.
func (s *Server) handleDashboardRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, map[string]any{
		"service": "debate-bot",
		"mode":    ModeDashboard,
		"status":  "ok",
	})
}

// handleConfig surfaces the server-side mode so the SPA can pick which
// view to render. The frontend hits this once on mount.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	mode := s.d.Mode
	if mode == "" {
		mode = "stream"
	}
	payload := map[string]any{
		"mode":          mode,
		"auth_required": s.authEnabled(),
		// authed lets the SPA skip the login screen when a valid cookie is
		// already present (e.g. a returning visitor or a page reload).
		"authed": s.requestAuthed(r),
		// force_audio mirrors the engine's --audio flag so the frontend can
		// hide video-only options and present an audio feed.
		"force_audio": s.d.ForceAudio,
	}
	// maintenance is present while a window is active or upcoming, so clients can
	// show a banner (or warn ahead) even though /api/config stays reachable.
	if m := s.relevantMaintenance(r); m != nil {
		payload["maintenance"] = m
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

type healthCheckResult struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type healthResponse struct {
	Status string                       `json:"status"`
	Checks map[string]healthCheckResult `json:"checks,omitempty"`
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	checks := map[string]healthCheckResult{}
	healthy := true
	check := func(name string, fn func(context.Context) error) {
		if err := fn(ctx); err != nil {
			healthy = false
			checks[name] = healthCheckResult{Status: "error", Error: err.Error()}
			return
		}
		checks[name] = healthCheckResult{Status: "ok"}
	}

	if s.d.Jobs != nil {
		check("jobs_db", s.d.Jobs.Ping)
	}
	if s.d.Discussions != nil {
		check("discussions_db", s.d.Discussions.Ping)
	}
	if s.d.Progress != nil {
		check("redis", s.d.Progress.Ping)
	}
	if s.d.MQPing != nil {
		check("rabbitmq", s.d.MQPing)
	}

	status := "ok"
	code := http.StatusOK
	if !healthy {
		status = "error"
		code = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(healthResponse{Status: status, Checks: checks})
}

// Handler exposes the served handler (auth-wrapped when a password is set).
// Useful for tests / custom mounting.
func (s *Server) Handler() http.Handler { return s.handler }

// ListenAndServe binds to addr and serves until ctx is cancelled. addr like
// ":8080" or "127.0.0.1:0" (random port). The actual bound address is returned
// via the started callback so callers can discover a random port.
func (s *Server) ListenAndServe(ctx context.Context, addr string, started func(*net.TCPAddr)) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	if started != nil {
		started(ln.Addr().(*net.TCPAddr))
	}
	srv := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// transcriptDTO is the JSON-serialisable form of an agent.TranscriptLine.
type transcriptDTO struct {
	Speaker          string                   `json:"speaker"`
	Role             string                   `json:"role"`
	Side             string                   `json:"side"`
	Text             string                   `json:"text"`
	ImageURL         string                   `json:"image_url,omitempty"`
	At               time.Time                `json:"at"`
	Sources          []agent.TranscriptSource `json:"sources,omitempty"`
	JudgementComment string                   `json:"judgement_comment,omitempty"`
	// AudioOffsetMS is the line's position on the audio timeline in
	// milliseconds (audiobook image lines only; 0/omitted = unknown).
	AudioOffsetMS int64 `json:"audio_offset_ms,omitempty"`
}

func toDTO(l agent.TranscriptLine) transcriptDTO {
	return transcriptDTO{
		Speaker: l.Speaker, Role: string(l.Role), Side: l.Side,
		Text: l.Text, ImageURL: l.ImageURL, At: l.At, Sources: l.Sources,
		JudgementComment: l.JudgementComment,
		AudioOffsetMS:    l.AudioOffsetMS,
	}
}

// topicsResponse is the body of GET /api/topics — every channel with its
// current debate queue. The frontend renders the channel switcher from this.
type topicsResponse struct {
	Channels []ChannelInfo `json:"channels"`
}

// handleDebug returns a snapshot of every channel's runtime state so a
// browser can see at a glance whether an orchestrator is actually airing.
// Useful when /api/messages keeps returning 503 ("no active debate") and
// you can't tell whether the channel is off-air, between debates, or stuck
// in setup.
func (s *Server) handleDebug(w http.ResponseWriter, r *http.Request) {
	type chDebug struct {
		ID            string `json:"id"`
		Number        int    `json:"number"`
		Title         string `json:"title"`
		OffAir        bool   `json:"off_air"`
		HasOrch       bool   `json:"has_orch"`
		HasLiveStream bool   `json:"has_live_stream"`
		HasHLSDir     bool   `json:"has_hls_dir"`
		HasDBPath     bool   `json:"has_db_path"`
		Debates       int    `json:"debates"`
		Current       string `json:"current_debate_id,omitempty"`
	}
	out := make([]chDebug, 0)
	for _, ch := range s.d.Sessions.List() {
		res := s.d.Sessions.ChannelResources(ch.ID)
		row := chDebug{
			ID: ch.ID, Number: ch.Number, Title: ch.Title,
			OffAir: ch.OffAir, Debates: len(ch.Debates),
			Current: ch.CurrentDebateID,
		}
		if res != nil {
			row.HasOrch = res.Orch != nil
			row.HasLiveStream = res.LiveStream != nil
			row.HasHLSDir = res.HLSDir != ""
			row.HasDBPath = res.CurrentDBPath != ""
		}
		out = append(out, row)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) handleTopics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(topicsResponse{
		Channels: s.d.Sessions.List(),
	})
}

// orchForRequest returns the orchestrator the request targets via
// ?channel=<id>. Returns nil when the channel is unknown or has no live
// orchestrator (off-air, between debates).
func (s *Server) orchForRequest(r *http.Request) *contentcreator.Orchestrator {
	id := r.URL.Query().Get("channel")
	if id == "" {
		return nil
	}
	if res := s.d.Sessions.ChannelResources(id); res != nil {
		return res.Orch
	}
	return nil
}

func (s *Server) handleTranscript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Live orchestrator: serve the in-memory snapshot — zero IO and the
	// freshest possible view of an in-progress debate.
	if cur := s.orchForRequest(r); cur != nil {
		writeTranscript(w, cur.Transcript.Snapshot())
		return
	}

	// No live orch: fall back to the channel's most-recently-aired debate's
	// sqlite file so a viewer who reloads after the debate ends still sees
	// the chat history.
	if path := s.dbPathForRequest(r); path != "" {
		lines, err := contentcreator.LoadSnapshot(path)
		if err != nil {
			s.d.Log.Warn("transcript disk load failed", "path", path, "err", err)
			writeTranscript(w, nil)
			return
		}
		writeTranscript(w, lines)
		return
	}

	writeTranscript(w, nil)
}

// dbPathForRequest returns the sqlite path the request targets, derived from
// the channel-id query parameter. Empty string when no channel is requested
// or no debate has aired on that channel yet.
func (s *Server) dbPathForRequest(r *http.Request) string {
	id := r.URL.Query().Get("channel")
	if id == "" {
		return ""
	}
	if res := s.d.Sessions.ChannelResources(id); res != nil {
		return res.CurrentDBPath
	}
	return ""
}

func writeTranscript(w http.ResponseWriter, lines []agent.TranscriptLine) {
	out := make([]transcriptDTO, len(lines))
	for i, l := range lines {
		out[i] = toDTO(l)
	}
	_ = json.NewEncoder(w).Encode(out)
}

func writeDiscussionTranscript(w http.ResponseWriter, lines []DiscussionLine) {
	out := make([]transcriptDTO, len(lines))
	for i, l := range lines {
		out[i] = transcriptDTO{
			Speaker:          l.Speaker,
			Role:             l.Role,
			Side:             l.Side,
			Text:             l.Text,
			ImageURL:         l.ImageURL,
			Sources:          l.Sources,
			JudgementComment: l.JudgementComment,
		}
	}
	_ = json.NewEncoder(w).Encode(out)
}

// eventEnvelope is the JSON shape emitted to SSE clients. The bus carries
// concrete debate.* event structs; we tag each with a string event name so
// browsers (and the TUI bridge) can dispatch on it.
type eventEnvelope struct {
	tag     string
	payload any
}

func envelope(v any, lang contentcreator.Lang) (eventEnvelope, bool) {
	switch m := v.(type) {
	case contentcreator.AgentActivityMsg:
		return eventEnvelope{"agent_activity", map[string]any{
			"channel_id": m.ChannelID,
			"agent":      m.Agent,
			"role":       m.Role,
			"activity":   string(m.Activity),
			"detail":     m.Detail,
		}}, true
	case contentcreator.TranscriptMsg:
		payload := map[string]any{
			"channel_id": m.ChannelID,
			"speaker":    m.Speaker, "role": string(m.Role), "side": m.Side,
			"text": m.Text, "done": m.Done,
			"isUserMessage": m.IsUserMessage,
		}
		if len(m.Sources) > 0 {
			payload["sources"] = m.Sources
		}
		if m.JudgementComment != "" {
			payload["judgement_comment"] = m.JudgementComment
		}
		if m.SenderUserID != "" {
			payload["sender_user_id"] = m.SenderUserID
		}
		if m.AudioURL != "" {
			payload["audio_url"] = m.AudioURL
		}
		if m.ImageURL != "" {
			payload["image_url"] = m.ImageURL
		}
		if m.AudioOffsetMS > 0 {
			payload["audio_offset_ms"] = m.AudioOffsetMS
		}
		return eventEnvelope{"transcript", payload}, true
	case contentcreator.TickMsg:
		return eventEnvelope{"tick", map[string]any{
			"channel_id":   m.ChannelID,
			"elapsed_ms":   m.Elapsed.Milliseconds(),
			"remaining_ms": m.Remaining.Milliseconds(),
		}}, true
	case contentcreator.PhaseMsg:
		// The label is re-derived per-connection in the caller's negotiated
		// language rather than using the Traditional-default m.Label stamped
		// at emit time — PhaseMsg is broadcast to every subscriber, so the
		// translation must happen here where the request's Accept-Language is
		// known. Frontend still displays `label` as-is.
		return eventEnvelope{"phase", map[string]any{
			"channel_id": m.ChannelID,
			"phase":      m.Phase.String(),
			"label":      contentcreator.PhaseLabelLang(m.Type, m.Phase, lang),
			"type":       m.Type,
		}}, true
	case contentcreator.StatusMsg:
		return eventEnvelope{"status", map[string]any{
			"channel_id": m.ChannelID,
			"text":       m.Text,
		}}, true
	case contentcreator.ErrorMsg:
		text := ""
		if m.Err != nil {
			text = m.Err.Error()
		}
		return eventEnvelope{"error", map[string]any{
			"channel_id": m.ChannelID,
			"text":       text,
		}}, true
	case contentcreator.EndedMsg:
		return eventEnvelope{"ended", map[string]any{
			"channel_id":      m.ChannelID,
			"transcript_path": m.TranscriptPath,
			"audio_path":      m.AudioPath,
		}}, true
	case contentcreator.TopicMsg:
		return eventEnvelope{"topic", map[string]any{
			"channel_id": m.ChannelID,
			"id":         m.ID,
			"title":      m.Title,
			"type":       m.Type,
			"index":      m.Index,
			"total":      m.Total,
			"show":       m.Show,
			"season":     m.Season,
			"episode":    m.Episode,
		}}, true
	case contentcreator.SummaryReadyMsg:
		return eventEnvelope{"summary_ready", map[string]any{
			"channel_id": m.ChannelID,
			"doc_type":   m.DocType,
			"status":     m.Status,
		}}, true
	case contentcreator.ResourceUpdatedMsg:
		action := m.Action
		if action == "" {
			action = "update"
		}
		payload := map[string]any{
			"channel_id":    m.ChannelID,
			"action":        action,
			"resource_type": m.ResourceType,
			"resource_id":   m.ResourceID,
			"deep_link":     m.DeepLink,
			"id":            m.DeepLink,
			"text":          m.Text,
		}
		if len(m.Changes) > 0 {
			payload["changes"] = m.Changes
		}
		return eventEnvelope{"resource_updated", payload}, true
	case contentcreator.TopicsChangedMsg:
		_ = m
		// Empty payload — clients re-fetch /api/topics on receipt.
		return eventEnvelope{"topics_changed", map[string]any{}}, true
	}
	return eventEnvelope{}, false
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	channelFilter := r.URL.Query().Get("channel")
	lang := contentcreator.LangFromAcceptLanguage(r.Header.Get("Accept-Language"))
	sse := newSSEWriter(w)
	ch, cancel := s.d.Bus.Subscribe(128)
	defer cancel()

	// Initial heartbeat — confirms the connection to the client.
	if err := sse.comment("ok"); err != nil {
		return
	}

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if err := sse.comment("hb"); err != nil {
				return
			}
		case v, ok := <-ch:
			if !ok {
				return
			}
			// In parallel mode each event is stamped with its channel id; an
			// empty filter means "send everything" (sequential mode default).
			if channelFilter != "" {
				eid := contentcreator.MsgChannelID(v)
				if eid != "" && eid != channelFilter {
					continue
				}
			}
			env, fine := envelope(v, lang)
			if !fine {
				continue
			}
			if err := sse.send(env.tag, env.payload); err != nil {
				return
			}
		}
	}
}

func (s *Server) handleAudio(w http.ResponseWriter, r *http.Request) {
	live := s.liveStreamForRequest(r)
	if live == nil {
		http.Error(w, "no audio stream", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	rc := http.NewResponseController(w)

	ch, cancel := live.Subscribe(128)
	defer cancel()

	for {
		select {
		case <-r.Context().Done():
			return
		case chunk, ok := <-ch:
			if !ok {
				return
			}
			if _, err := w.Write(chunk); err != nil {
				return
			}
			_ = rc.Flush()
		}
	}
}

// liveStreamForRequest picks the LiveStream the request targets from the
// /api/audio/<id>/stream URL.
func (s *Server) liveStreamForRequest(r *http.Request) *audio.LiveStream {
	const prefix = "/api/audio/"
	rel := strings.TrimPrefix(r.URL.Path, prefix)
	rel = strings.TrimSuffix(rel, "/stream")
	if rel == "" || rel == "stream" {
		return nil
	}
	if res := s.d.Sessions.ChannelResources(rel); res != nil {
		return res.LiveStream
	}
	return nil
}

// handleVideo serves the HLS playlist + segments produced by the encoder.
// It refuses any path that would escape the configured HLS directory and only
// serves files whose extensions are recognised HLS artefacts.
//
// URL shape:
//
//	/api/video/<channel>/<file>   uses ChannelResources(channel).HLSDir
func (s *Server) handleVideo(w http.ResponseWriter, r *http.Request) {
	const prefix = "/api/video/"
	rel := strings.TrimPrefix(r.URL.Path, prefix)
	if rel == "" || strings.Contains(rel, "..") {
		http.NotFound(w, r)
		return
	}

	hlsDir, file := s.resolveVideoTarget(rel)
	if hlsDir == "" {
		http.Error(w, "channel off-air", http.StatusNotFound)
		return
	}
	if file == "" || strings.ContainsAny(file, `/\`) {
		http.NotFound(w, r)
		return
	}

	switch {
	case strings.HasSuffix(file, ".m3u8"):
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-cache")
	case strings.HasSuffix(file, ".ts"):
		w.Header().Set("Content-Type", "video/mp2t")
		w.Header().Set("Cache-Control", "max-age=10")
	default:
		http.NotFound(w, r)
		return
	}
	full := filepath.Join(hlsDir, file)
	// Final containment check after Join.
	clean := filepath.Clean(full)
	if !strings.HasPrefix(clean, filepath.Clean(hlsDir)+string(filepath.Separator)) &&
		clean != filepath.Clean(hlsDir) {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, full)
}

// resolveVideoTarget splits "<channel>/<file>" into (HLSDir, file). Returns
// ("","") when the channel is unknown or off-air, or when no channel segment
// is present.
func (s *Server) resolveVideoTarget(rel string) (hlsDir, file string) {
	i := strings.Index(rel, "/")
	if i <= 0 {
		return "", ""
	}
	channelID := rel[:i]
	file = rel[i+1:]
	if res := s.d.Sessions.ChannelResources(channelID); res != nil {
		return res.HLSDir, file
	}
	return "", ""
}

type postMessageReq struct {
	Text string `json:"text"`
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 8*1024))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	var req postMessageReq
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if req.Text == "" {
		http.Error(w, "empty text", http.StatusBadRequest)
		return
	}
	cur := s.orchForRequest(r)
	if cur == nil {
		http.Error(w, "no active debate", http.StatusServiceUnavailable)
		return
	}
	username := s.ensureUsername(w, r)
	cur.PushUserMessage(req.Text, username)
	w.WriteHeader(http.StatusNoContent)
}
