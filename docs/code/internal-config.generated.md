---
slug: code/internal/config
title: Package internal/config
description: Auto-generated go doc reference for the internal/config package.
---

# Package `internal/config`

_Generated with `go doc -all ./internal/config`. Regenerate with `scripts/gen_go_docs.sh`._

```text
package config // import "github.com/sirily11/debate-bot/internal/config"


CONSTANTS

const (
	TTSProviderAzure  = "azure"
	TTSProviderEleven = "eleven"
)
    TTS provider identifiers used in topic.md `tts_provider:` field.

const (
	Resolution720p  = "720p"
	Resolution1080p = "1080p"
	Resolution4K    = "4k"
)
    Output resolutions selectable from topic.md `resolution:` field. The
    renderer composites at 1920×1080 by default; ffmpeg only scales when callers
    request a different delivery size.

const (
	ContentTypeDebate          = "debate"
	ContentTypeSituationPuzzle = "situation-puzzle"
	// ContentTypeSeries is a host-only narrated TV-style episode. Episodes
	// declare show + season + episode in frontmatter; the pipeline writes
	// every episode's assets (scene plan, generated PNGs, music, recap-
	// ready script) into a stable on-disk archive at
	// `<persistent-root>/tv-series/<show>/s<season>/e<episode>/`. Episode
	// N+1 reads that archive to (a) build a "previously on …" preamble
	// via the compression LLM and (b) re-use specific past beats by
	// emitting `<season-S-episode-E-image-N/>` markers in the host's
	// stream.
	ContentTypeSeries = "series"
)
    Content types selectable via the `type:` field in topic.md frontmatter.
    The orchestrator picks an agent set + planner based on this value.


VARIABLES

var ErrEnvNotLoaded = errors.New("env not loaded")
    ErrEnvNotLoaded is returned when an Env was expected but not initialised.


TYPES

type AgentSpec struct {
	Name    string `yaml:"name"`
	Model   string `yaml:"model"`
	BaseURL string `yaml:"base_url,omitempty"`
	APIKey  string `yaml:"api_key,omitempty"`
}
    AgentSpec describes one agent declared in topic.md frontmatter.
    BaseURL/APIKey are optional per-agent overrides; otherwise the env defaults
    are used.

type Channel struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
	Title  string `json:"title"`
}
    Channel is one TV-style channel definition: a stable id (referenced from
    debate.md frontmatter), a human-facing channel number, and a display title.

type ChannelsConfig struct {
	Channels []Channel `json:"channels"`
}
    ChannelsConfig is the on-disk shape of channels.json. The file is just
    a `{"channels": [...]}` wrapper so future top-level fields (defaults,
    etc.) can be added without breaking the schema.

func LoadChannels(path string) (*ChannelsConfig, error)
    LoadChannels parses channels.json and validates it: ids must be non-empty
    and unique, channel numbers must be unique, titles must be non-empty.

func (c *ChannelsConfig) Find(id string) *Channel
    Find returns the channel with the given id, or nil if not defined.

type DebateTopic struct {
	Title             string `yaml:"title"`
	Type              string `yaml:"type"`
	Language          string `yaml:"language"`
	TotalMinutes      int    `yaml:"total_minutes"`
	SegmentMaxSeconds int    `yaml:"segment_max_seconds"`
	TTSProvider       string `yaml:"tts_provider,omitempty"`
	Resolution        string `yaml:"resolution,omitempty"`
	// Channel is the id of the TV-style channel this debate belongs to.
	// Channels are defined in channels.json. Multiple debates with the same
	// channel id are queued and play sequentially within that channel; debates
	// on different channels run in parallel as independent video streams.
	// Required — startup fails if the id isn't defined in channels.json.
	Channel string `yaml:"channel"`

	// Debate-only roster.
	Affirmative []AgentSpec `yaml:"affirmative,omitempty"`
	Negative    []AgentSpec `yaml:"negative,omitempty"`
	Judge       AgentSpec   `yaml:"judge,omitempty"`

	// Situation-puzzle-only roster. PuzzleHost is the 出題者 who knows the
	// hidden truth and answers player questions with 是/不是/與此無關.
	// Players are 解題者 trying to deduce the truth.
	PuzzleHost AgentSpec   `yaml:"puzzle_host,omitempty"`
	Players    []AgentSpec `yaml:"players,omitempty"`

	// Series-only roster + metadata. Show is the human-readable show name
	// (slugified for the on-disk archive directory). Season + Episode are
	// 1-based; the recap engine treats lexicographic (season, episode)
	// order as canonical "before this episode" (so s2e1 follows s1e9).
	// SeriesHost is the single narrator agent; series episodes are
	// non-interactive (no players, no Q&A, no live audience).
	Show       string    `yaml:"show,omitempty"`
	Season     int       `yaml:"season,omitempty"`
	Episode    int       `yaml:"episode,omitempty"`
	SeriesHost AgentSpec `yaml:"series_host,omitempty"`

	// Shared across both content types.
	Viewers []AgentSpec `yaml:"viewers,omitempty"`

	// Body sections, populated from markdown after frontmatter.
	// Debate sections:
	Background     string `yaml:"-"`
	AffirmativePos string `yaml:"-"`
	NegativePos    string `yaml:"-"`
	Rules          string `yaml:"-"`
	// Situation-puzzle sections:
	Surface string `yaml:"-"` // 湯面 — visible to everyone
	Truth   string `yaml:"-"` // 湯底 — only the puzzle host's prompt sees it
}
    DebateTopic is the full topic.md content: YAML frontmatter + named markdown
    sections. Despite the name, it now covers every supported content type
    (debate + situation-puzzle); the active subset of fields depends on Type.

func LoadTopic(path string) (*DebateTopic, error)
    LoadTopic parses a debate.md file with YAML frontmatter and markdown body.

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
    Env holds all process-level configuration loaded from .env / environment.
    It is treated as immutable after LoadEnv returns.

func LoadEnv() (*Env, error)
    LoadEnv reads .env (if present) then env vars, validates, and freezes
    config. Compression endpoint/key fall back to OpenAI ones when blank.

    Uses godotenv.Overload so values in .env take precedence over the inherited
    shell environment — otherwise a stray OPENAI_API_KEY exported in ~/.zshrc
    silently shadows the project's .env, which is a frequent footgun.

type MCPConfig struct {
	MCPServers map[string]MCPServerConfig `json:"mcpServers"`
}
    MCPConfig is the top-level mcp.json structure.

func LoadMCPConfig(path string) (*MCPConfig, error)
    LoadMCPConfig reads an mcp.json. Returns an empty config if path is empty or
    the file does not exist.

type MCPServerConfig struct {
	// Stdio fields.
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`

	// HTTP fields.
	URL       string            `json:"url,omitempty"`
	Transport MCPTransport      `json:"transport,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}
    MCPServerConfig matches the Claude-Desktop mcp.json shape per server,
    extended to support remote HTTP transports (streamable-http, sse).

    Stdio entry:

        "name": { "command": "npx", "args": [...], "env": {"K":"V"} }

    Remote (streamable-http or sse) entry:

        "name": {
          "url":       "https://example.com/mcp",
          "transport": "streamable-http",   // optional; defaults to streamable-http when url is set
          "headers":   { "Authorization": "Bearer ..." }
        }

func (c MCPServerConfig) ResolvedTransport() MCPTransport
    ResolvedTransport returns the transport this entry should use, applying
    defaults: command-only → stdio; url-only → streamable-http; explicit
    transport overrides both.

func (c MCPServerConfig) Validate(name string) error
    Validate returns an error if neither a command nor a url is provided,
    or if mutually exclusive fields are mixed in confusing ways.

type MCPTransport string
    MCPTransport names the transport flavour for an MCP server.

const (
	// MCPTransportStdio launches a local subprocess and talks JSON-RPC over stdio.
	MCPTransportStdio MCPTransport = "stdio"
	// MCPTransportStreamableHTTP uses the modern streamable-http MCP transport.
	MCPTransportStreamableHTTP MCPTransport = "streamable-http"
	// MCPTransportSSE uses the older HTTP+SSE transport.
	MCPTransportSSE MCPTransport = "sse"
)
```
