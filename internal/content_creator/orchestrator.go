package contentcreator

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/audio"
	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/llm"
	"github.com/sirily11/debate-bot/internal/memory"
	debatemcp "github.com/sirily11/debate-bot/internal/mcp"
	"github.com/sirily11/debate-bot/internal/tools"
	"github.com/sirily11/debate-bot/internal/tts"
)

// Orchestrator wires every package together for one debate run.
type Orchestrator struct {
	Env        *config.Env
	Topic      *config.DebateTopic
	MCPConfig  *config.MCPConfig
	Tools      *tools.Registry
	MemStore   *memory.Store
	Compressor *memory.Compressor
	TTS        tts.Provider
	MCPSrvs    []*debatemcp.Server

	Registry   *agent.Registry
	Transcript *Transcript
	Store      *Store
	Tracker    *Tracker
	Queue      *userQueue
	Send       func(any)
	Log        *slog.Logger
	LiveStream *audio.LiveStream

	// puzzleMusic maps planner directive → mp3 path for situation-puzzle
	// turns that should play with a Lyria background bed underneath the
	// host's TTS. Populated by the caller via SetPuzzleMusic before Run.
	// Empty when music generation failed or for non-puzzle topics.
	puzzleMusic map[string]string

	// surfaceFrames is how many surface scene images the visual planner
	// generated for this puzzle. The puzzle host's system prompt uses
	// surfaceFrames-1 as the exact number of `<scene/>` markers to emit
	// during the surface narration so each marker advances the on-screen
	// image to the next planned beat (no repeats, no overshoot). The
	// pipeline also caps SceneAdvanceMsg events at surfaceFrames-1 as a
	// belt-and-braces safeguard if the LLM misses the count.
	// 0 means "host falls back to a guidance range, pipeline doesn't cap".
	surfaceFrames int
	// conclusionFrames is the same idea for the conclusion phase. The
	// conclusion now reads as a longer reflective epilogue (matching the
	// surface's cinematic feel) and uses scene markers to advance through
	// the planned aftermath beats instead of a wall-clock timer.
	conclusionFrames int
}

// New constructs an Orchestrator after loaders + .env are validated.
// liveStream is the shared mp3 broadcaster the pipeline writes audio into.
func New(env *config.Env, topic *config.DebateTopic, mcpCfg *config.MCPConfig,
	send func(any), log *slog.Logger, liveStream *audio.LiveStream,
) (*Orchestrator, error) {
	memStore, err := memory.NewStore(filepath.Join(env.OutDir, "memory"))
	if err != nil {
		return nil, fmt.Errorf("memory store: %w", err)
	}
	compLLM := llm.New(env.CompressionBaseURL, env.CompressionKey, env.CompressionModel)
	compressor := memory.New(compLLM, memory.DefaultThreshold)
	ttsClient, err := buildTTSProvider(env, topic)
	if err != nil {
		return nil, err
	}
	ttsRegistry := tools.New()
	tools.RegisterBuiltins(ttsRegistry)

	// Per-debate sqlite db: lives next to debate.mp3 / transcript.txt so
	// the whole debate is portable as one folder. Failure to open the db
	// is a hard error — without persistence, reload-after-end shows nothing,
	// and the user explicitly asked for that to work.
	store, err := OpenStore(filepath.Join(env.OutDir, "session.db"), log)
	if err != nil {
		return nil, fmt.Errorf("transcript store: %w", err)
	}

	o := &Orchestrator{
		Env:        env,
		Topic:      topic,
		MCPConfig:  mcpCfg,
		Tools:      ttsRegistry,
		MemStore:   memStore,
		Compressor: compressor,
		TTS:        ttsClient,
		Transcript: NewTranscriptWithStore(store),
		Store:      store,
		Tracker:    NewTracker(time.Duration(topic.TotalMinutes) * time.Minute),
		Queue:      &userQueue{},
		Send:       send,
		Log:        log,
		LiveStream: liveStream,
	}
	return o, nil
}

// Setup performs all blocking-but-deterministic initialisation: voice fetch,
// MCP boot, agent construction, voice assignment.
func (o *Orchestrator) Setup(ctx context.Context) error {
	if err := audio.VerifyTools(); err != nil {
		return err
	}
	// Dump the loaded env (with secrets masked) so users can confirm godotenv
	// picked up the values they expected.
	o.Log.Info("env loaded",
		"OPENAI_BASE_URL", o.Env.OpenAIBaseURL,
		"OPENAI_API_KEY_len", len(o.Env.OpenAIKey),
		"OPENAI_API_KEY_preview", maskKey(o.Env.OpenAIKey),
		"HOST_MODEL", o.Env.HostModel,
		"COMPRESSION_BASE_URL", o.Env.CompressionBaseURL,
		"COMPRESSION_API_KEY_len", len(o.Env.CompressionKey),
		"COMPRESSION_API_KEY_preview", maskKey(o.Env.CompressionKey),
		"COMPRESSION_MODEL", o.Env.CompressionModel,
		"TTS_PROVIDER", o.Topic.TTSProvider,
		"AZURE_SPEECH_REGION", o.Env.AzureSpeechRegion,
		"AZURE_SPEECH_KEY_len", len(o.Env.AzureSpeechKey),
		"AZURE_SPEECH_KEY_preview", maskKey(o.Env.AzureSpeechKey),
		"ELEVENLABS_API_KEY_len", len(o.Env.ElevenLabsAPIKey),
		"ELEVENLABS_API_KEY_preview", maskKey(o.Env.ElevenLabsAPIKey),
		"OUT_DIR", o.Env.OutDir)

	o.Send(StatusMsg{Text: fmt.Sprintf("fetching %s voice list...", o.Topic.TTSProvider)})
	voices, err := o.TTS.FetchVoices(ctx, o.Topic.Language)
	if err != nil {
		return fmt.Errorf("voice list: %w", err)
	}
	o.Log.Info("voices fetched", "count", len(voices))

	if len(o.MCPConfig.MCPServers) > 0 {
		o.Send(StatusMsg{Text: "starting MCP servers..."})
		o.MCPSrvs, _ = debatemcp.StartAll(ctx, o.MCPConfig, o.Log)
		mcpTools, err := debatemcp.ListAllTools(ctx, o.MCPSrvs)
		if err != nil {
			o.Log.Warn("mcp list tools failed", "err", err)
		} else {
			tools.RegisterMCPTools(o.Tools, mcpTools)
			o.Log.Info("mcp tools registered", "count", len(mcpTools))
		}
	}

	if err := o.buildAgents(); err != nil {
		return err
	}
	agent.AssignVoices(voices, o.Registry.All(), o.Topic.Language, time.Now().UnixNano(), o.Log)
	for _, a := range o.Registry.All() {
		o.Log.Info("agent ready",
			"name", a.Name(),
			"role", string(a.Role()),
			"model", a.Model(),
			"voice", a.Voice().ShortName)
	}
	// Clear the setup-phase status text so the TUI status bar stops showing
	// "starting MCP servers..." once the pipeline takes over.
	o.Send(StatusMsg{Text: ""})
	o.Send(PhaseMsg{Phase: agent.PhaseOpening})
	return nil
}

// makeAgent constructs one role-typed agent from a config.AgentSpec, falling
// back to env-level defaults for any blank fields. Shared between the per-
// format buildAgents methods (see debate_orchestrator.go and
// situation_puzzle_orchestrator.go) — every role recognised by the registry
// is wired up here so a new format only needs to call this with its specs.
func (o *Orchestrator) makeAgent(spec config.AgentSpec, role agent.Role, defaultModel string) agent.Agent {
	baseURL := spec.BaseURL
	if baseURL == "" {
		baseURL = o.Env.OpenAIBaseURL
	}
	key := spec.APIKey
	if key == "" {
		key = o.Env.OpenAIKey
	}
	model := spec.Model
	if model == "" {
		model = defaultModel
	}
	client := llm.New(baseURL, key, model)
	mem := o.MemStore.For(spec.Name)
	base := agent.NewBase(spec.Name, role, client, mem, o.Compressor, o.Tools, o.Transcript)
	switch role {
	case agent.RoleHost:
		return agent.NewHost(base)
	case agent.RoleAffirmative, agent.RoleNegative:
		return agent.NewCandidate(base)
	case agent.RoleJudge:
		return agent.NewJudge(base)
	case agent.RoleViewer:
		return agent.NewViewer(base)
	case agent.RolePlayer:
		return agent.NewPlayer(base)
	case agent.RolePuzzleHost:
		return agent.NewPuzzleHost(base, o.Topic.Surface, o.Topic.Truth,
			o.surfaceFrames, o.conclusionFrames)
	}
	return nil
}

// buildAgents wires up the registry. Viewers are shared by every content
// type so they're populated here; the format-specific roster (host, judge,
// candidates / puzzle host, players) is built by the per-format method this
// dispatches to.
func (o *Orchestrator) buildAgents() error {
	o.Registry = &agent.Registry{}
	for _, s := range o.Topic.Viewers {
		o.Registry.Viewers = append(o.Registry.Viewers, o.makeAgent(s, agent.RoleViewer, ""))
	}
	switch o.Topic.Type {
	case config.ContentTypeSituationPuzzle:
		return o.buildPuzzleAgents()
	default:
		// Debate is also the implicit fallback if a future content type is
		// added before its branch lands here; config validation rejects
		// unknown types in practice.
		return o.buildDebateAgents()
	}
}

// newPlanner picks the per-content-type planner. Today: debate vs situation-
// puzzle. Adding a third content type means adding a branch here AND a
// matching newXPlanner method in its own file.
func (o *Orchestrator) newPlanner() Planner {
	if o.Topic.Type == config.ContentTypeSituationPuzzle {
		return o.newPuzzlePlanner()
	}
	return o.newDebatePlanner()
}

// Run executes Setup then drives the pipeline. Blocks until the planner finishes.
func (o *Orchestrator) Run(ctx context.Context) error {
	if err := o.Setup(ctx); err != nil {
		return err
	}
	planner := o.newPlanner()
	pipe := NewPipeline(Deps{
		Planner: planner, Tracker: o.Tracker, Registry: o.Registry,
		TTS: o.TTS, OutDir: o.Env.OutDir,
		Send: o.Send, Log: o.Log,
		Topic: o.Topic.Title, Language: o.Topic.Language,
		ContentType:      o.Topic.Type,
		Transcript:       o.Transcript,
		LiveStream:       o.LiveStream,
		MusicPaths:       o.puzzleMusic,
		SurfaceFrames:    o.surfaceFrames,
		ConclusionFrames: o.conclusionFrames,
	})
	files, err := pipe.Run(ctx)
	if err != nil {
		return err
	}

	transcriptPath := filepath.Join(o.Env.OutDir, "transcript.txt")
	if err := o.Transcript.Save(transcriptPath); err != nil {
		o.Log.Warn("save transcript failed", "err", err)
	}
	debatePath := filepath.Join(o.Env.OutDir, "debate.mp3")
	if len(files) > 0 {
		if err := audio.ConcatToMP3(o.Env.OutDir, debatePath, files); err != nil {
			o.Log.Warn("ffmpeg concat failed", "err", err)
		}
	}
	o.Send(EndedMsg{TranscriptPath: transcriptPath, AudioPath: debatePath})
	return nil
}

// Shutdown stops MCP subprocesses and closes the per-debate sqlite handle.
// The DB file is left in place so a viewer who reloads after the debate
// ends still sees the chat history.
func (o *Orchestrator) Shutdown() {
	debatemcp.StopAll(context.Background(), o.MCPSrvs)
	if err := o.Store.Close(); err != nil {
		o.Log.Warn("transcript store close failed", "err", err)
	}
}

// PushUserMessage queues user input into the planner. username is the
// viewer's chosen handle (typically a random name persisted in localStorage
// on the frontend); empty string falls back to "user" for the speaker tag so
// past clients without a username still render reasonably.
func (o *Orchestrator) PushUserMessage(text, username string) {
	o.Queue.push(userMessage{Username: username, Text: text})
	if text == "/end" {
		return
	}
	speaker := username
	if speaker == "" {
		speaker = "user"
	}
	o.Transcript.AppendUser(speaker, text)
	o.Send(TranscriptMsg{Speaker: speaker, Role: "user", Text: text, Done: true})
}

// EnsureOutDir makes sure the output dir exists (called before logger setup).
func EnsureOutDir(p string) error {
	return os.MkdirAll(p, 0o755)
}

// buildTTSProvider constructs the TTS provider selected by topic.tts_provider
// and validates the env vars that provider requires. Defaults to Azure when
// the field is blank.
func buildTTSProvider(env *config.Env, topic *config.DebateTopic) (tts.Provider, error) {
	provider := topic.TTSProvider
	if provider == "" {
		provider = config.TTSProviderAzure
	}
	switch provider {
	case config.TTSProviderAzure:
		var missing []string
		if env.AzureSpeechRegion == "" {
			missing = append(missing, "AZURE_SPEECH_REGION")
		}
		if env.AzureSpeechKey == "" {
			missing = append(missing, "AZURE_SPEECH_KEY")
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("tts_provider=azure but missing env vars: %v", missing)
		}
		return tts.NewAzure(env.AzureSpeechRegion, env.AzureSpeechKey), nil
	case config.TTSProviderEleven:
		if env.ElevenLabsAPIKey == "" {
			return nil, fmt.Errorf("tts_provider=eleven but ELEVENLABS_API_KEY is not set")
		}
		return tts.NewElevenLabs(env.ElevenLabsAPIKey), nil
	default:
		return nil, fmt.Errorf("unknown tts_provider %q", provider)
	}
}

// maskKey shows the first 4 and last 4 characters with the middle elided so
// debug logs can confirm a key was loaded without leaking it.
func maskKey(k string) string {
	if k == "" {
		return "<empty>"
	}
	if len(k) <= 8 {
		return "***"
	}
	return k[:4] + "..." + k[len(k)-4:]
}
