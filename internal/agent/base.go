package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/sirily11/debate-bot/internal/llm"
	"github.com/sirily11/debate-bot/internal/memory"
	"github.com/sirily11/debate-bot/internal/tools"
	"github.com/sirily11/debate-bot/internal/tts"
	"github.com/sirily11/debate-bot/internal/util"
)

// TranscriptProvider is the orchestrator-side transcript view a Base needs.
// Defined here to keep agent independent of the debate package.
type TranscriptProvider interface {
	Snapshot() []TranscriptLine
}

// Base is embedded by every concrete agent and provides shared behaviour.
type Base struct {
	name string
	safe string
	role Role

	voice tts.Voice
	llmC  *llm.Client
	mem   *memory.Memory
	comp  *memory.Compressor
	reg   *tools.Registry

	transcript TranscriptProvider
	mu         sync.Mutex
}

// NewBase creates a Base.
func NewBase(name string, role Role, llmC *llm.Client, mem *memory.Memory,
	comp *memory.Compressor, reg *tools.Registry, tp TranscriptProvider,
) *Base {
	return &Base{
		name:       name,
		safe:       util.Safe(name),
		role:       role,
		llmC:       llmC,
		mem:        mem,
		comp:       comp,
		reg:        reg,
		transcript: tp,
	}
}

func (b *Base) Name() string         { return b.name }
func (b *Base) SafeName() string     { return b.safe }
func (b *Base) Role() Role           { return b.role }
func (b *Base) Side() string         { return b.role.Side() }
func (b *Base) Voice() tts.Voice     { return b.voice }
func (b *Base) SetVoice(v tts.Voice) { b.voice = v }
func (b *Base) LLM() *llm.Client     { return b.llmC }
func (b *Base) Tools() *tools.Registry {
	return b.reg
}
func (b *Base) Memory() *memory.Memory { return b.mem }

// AgentName implements tools.AgentContext (called from Tool implementations).
func (b *Base) AgentName() string { return b.name }

// AppendMemory implements tools.AgentContext.
func (b *Base) AppendMemory(text string) error {
	if b.mem == nil {
		return nil
	}
	return b.mem.Append(text)
}

// Transcript implements tools.AgentContext.
func (b *Base) Transcript() []tools.TranscriptLine {
	if b.transcript == nil {
		return nil
	}
	src := b.transcript.Snapshot()
	out := make([]tools.TranscriptLine, len(src))
	for i, l := range src {
		out[i] = tools.TranscriptLine{
			Speaker: l.Speaker, Role: string(l.Role), Side: l.Side,
			Text: l.Text, At: l.At,
		}
	}
	return out
}

// Listen records a line in this agent's memory if it isn't their own.
func (b *Base) Listen(ctx context.Context, line TranscriptLine) error {
	if line.Speaker == b.name {
		return nil
	}
	if b.mem == nil {
		return nil
	}
	tag := line.Speaker
	if line.Side != "" {
		tag = line.Side + " - " + line.Speaker
	}
	if err := b.mem.Append(fmt.Sprintf("[%s] %s: %s",
		line.At.Format("15:04:05"), tag, oneLine(line.Text))); err != nil {
		return err
	}
	if b.comp != nil {
		go func() {
			_ = b.comp.MaybeCompress(context.Background(), b.mem)
		}()
	}
	return nil
}

// Compress forces an immediate compression pass.
func (b *Base) Compress(ctx context.Context) error {
	if b.comp == nil || b.mem == nil {
		return nil
	}
	return b.comp.MaybeCompress(ctx, b.mem)
}

// runStream is the shared streaming helper used by every concrete Speak method.
// It supplies system prompt + recent transcript + memory + directive, and
// returns the underlying llm.Stream for the orchestrator to consume.
func (b *Base) runStream(ctx context.Context, system string, p SpeakPrompt) (*llm.Stream, error) {
	mem := strings.TrimSpace(p.Memory)
	transcript := formatRecent(p.Recent)
	user := strings.Join([]string{
		"# Topic",
		p.TopicTitle,
		"",
		"# Your private memory (use to recall earlier moments)",
		fallback(mem, "(empty)"),
		"",
		"# Recent transcript",
		fallback(transcript, "(none yet)"),
		"",
		"# Directive from host",
		fallback(p.Instructions, "(speak naturally)"),
		"",
		fmt.Sprintf("Time budget for this turn: about %d seconds. Reply in %s. Speak naturally — full sentences only, no stage directions, no markdown.",
			p.SecondsBudget, p.TopicLanguage),
	}, "\n")
	hist := []llm.Message{{Role: llm.RoleUser, Content: user}}
	return b.llmC.Stream(ctx, system, hist, nil)
}

func formatRecent(lines []TranscriptLine) string {
	var b strings.Builder
	for _, l := range lines {
		tag := l.Speaker
		if l.Side != "" {
			tag = l.Side + " - " + l.Speaker
		}
		fmt.Fprintf(&b, "%s: %s\n", tag, oneLine(l.Text))
	}
	return strings.TrimRight(b.String(), "\n")
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func fallback(s, alt string) string {
	if strings.TrimSpace(s) == "" {
		return alt
	}
	return s
}
