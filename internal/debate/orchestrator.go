package debate

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
	Topic      *config.Topic
	MCPConfig  *config.MCPConfig
	Tools      *tools.Registry
	MemStore   *memory.Store
	Compressor *memory.Compressor
	TTS        *tts.Client
	MCPSrvs    []*debatemcp.Server

	Registry   *agent.Registry
	Transcript *Transcript
	Tracker    *Tracker
	Queue      *userQueue
	Send       func(any)
	Log        *slog.Logger
}

// New constructs an Orchestrator after loaders + .env are validated.
func New(env *config.Env, topic *config.Topic, mcpCfg *config.MCPConfig,
	send func(any), log *slog.Logger,
) (*Orchestrator, error) {
	memStore, err := memory.NewStore(filepath.Join(env.OutDir, "memory"))
	if err != nil {
		return nil, fmt.Errorf("memory store: %w", err)
	}
	compLLM := llm.New(env.CompressionBaseURL, env.CompressionKey, env.CompressionModel)
	compressor := memory.New(compLLM, memory.DefaultThreshold)
	ttsClient := tts.New(env.AzureSpeechRegion, env.AzureSpeechKey)
	ttsRegistry := tools.New()
	tools.RegisterBuiltins(ttsRegistry)

	o := &Orchestrator{
		Env:        env,
		Topic:      topic,
		MCPConfig:  mcpCfg,
		Tools:      ttsRegistry,
		MemStore:   memStore,
		Compressor: compressor,
		TTS:        ttsClient,
		Transcript: NewTranscript(),
		Tracker:    NewTracker(time.Duration(topic.TotalMinutes) * time.Minute),
		Queue:      &userQueue{},
		Send:       send,
		Log:        log,
	}
	return o, nil
}

// Setup performs all blocking-but-deterministic initialisation: voice fetch,
// MCP boot, agent construction, voice assignment.
func (o *Orchestrator) Setup(ctx context.Context) error {
	if err := audio.VerifyTools(); err != nil {
		return err
	}
	o.Send(StatusMsg{Text: "fetching Azure voice list..."})
	voices, err := tts.FetchVoices(ctx, o.Env.AzureSpeechRegion, o.Env.AzureSpeechKey)
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
		o.Log.Info("agent ready", "name", a.Name(), "role", string(a.Role()), "voice", a.Voice().ShortName)
	}
	return nil
}

func (o *Orchestrator) buildAgents() error {
	o.Registry = &agent.Registry{}

	mk := func(spec config.AgentSpec, role agent.Role, defaultModel string) agent.Agent {
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
		}
		return nil
	}

	o.Registry.Host = mk(config.AgentSpec{Name: "Host", Model: o.Env.HostModel}, agent.RoleHost, o.Env.HostModel)
	o.Registry.Judge = mk(config.AgentSpec{Name: "Judge", Model: o.Topic.Judge.Model,
		BaseURL: o.Topic.Judge.BaseURL, APIKey: o.Topic.Judge.APIKey}, agent.RoleJudge, o.Env.HostModel)

	for _, s := range o.Topic.Affirmative {
		o.Registry.Affirmatve = append(o.Registry.Affirmatve, mk(s, agent.RoleAffirmative, ""))
	}
	for _, s := range o.Topic.Negative {
		o.Registry.Negative = append(o.Registry.Negative, mk(s, agent.RoleNegative, ""))
	}
	for _, s := range o.Topic.Viewers {
		o.Registry.Viewers = append(o.Registry.Viewers, mk(s, agent.RoleViewer, ""))
	}
	return nil
}

// Run executes Setup then drives the pipeline. Blocks until the planner finishes.
func (o *Orchestrator) Run(ctx context.Context) error {
	if err := o.Setup(ctx); err != nil {
		return err
	}
	planner := NewPlanner(o.Topic, o.Tracker, o.Registry, o.Queue)
	pipe := NewPipeline(Deps{
		Planner: planner, Tracker: o.Tracker, Registry: o.Registry,
		TTS: o.TTS, OutDir: o.Env.OutDir,
		Send: o.Send, Log: o.Log,
		Topic: o.Topic.Title, Language: o.Topic.Language, Transcript: o.Transcript,
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

// Shutdown stops MCP subprocesses.
func (o *Orchestrator) Shutdown() {
	debatemcp.StopAll(context.Background(), o.MCPSrvs)
}

// PushUserMessage queues user input into the planner.
func (o *Orchestrator) PushUserMessage(text string) {
	o.Queue.push(text)
	if text != "/end" {
		o.Transcript.AppendUser(text)
		o.Send(TranscriptMsg{Speaker: "user", Role: "user", Text: text, Done: true})
	}
}

// EnsureOutDir makes sure the output dir exists (called before logger setup).
func EnsureOutDir(p string) error {
	return os.MkdirAll(p, 0o755)
}
