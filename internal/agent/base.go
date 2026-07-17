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

	// emit reports this agent's activity changes (searching / taking memory /
	// idle) so the orchestrator can fan them onto the event bus. Supplied by
	// the orchestrator via SetActivityEmitter; nil (the default) is a no-op so
	// CLI runs and tests are unaffected. Kept as a plain string callback to
	// keep the agent package free of the content_creator event types (which
	// already depend on agent).
	emit ActivityEmitter
}

// ActivityEmitter receives an agent's activity transitions. activity is one of
// "searching" / "memory" / "speaking" / "idle"; detail is an optional hint
// (e.g. the tool name).
type ActivityEmitter func(activity, detail string)

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
func (b *Base) Model() string {
	if b.llmC == nil {
		return ""
	}
	return b.llmC.Model()
}
func (b *Base) Tools() *tools.Registry {
	return b.reg
}
func (b *Base) Memory() *memory.Memory { return b.mem }

// SetActivityEmitter wires the orchestrator's activity sink. Safe to call with
// nil to disable.
func (b *Base) SetActivityEmitter(fn ActivityEmitter) { b.emit = fn }

// EmitActivity reports an activity transition, no-op when no emitter is set.
// Exported so the orchestrator can stamp "speaking"/"idle" at turn boundaries.
func (b *Base) EmitActivity(activity, detail string) {
	if b.emit != nil {
		b.emit(activity, detail)
	}
}

// classifyToolActivity maps a tool name to the activity it represents so the
// diagram can distinguish "searching the web" from "saving a note".
func classifyToolActivity(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "note"), strings.Contains(n, "memory"),
		strings.Contains(n, "data_store"), strings.Contains(n, "store"):
		return "memory"
	default:
		// look_up_quote, firecrawl/web search, and any MCP tool read as
		// "searching" — the agent is gathering information.
		return "searching"
	}
}

// MemoryRead returns the agent's memory.md contents for inclusion in the next
// SpeakPrompt. Read errors are swallowed (returning "") because a missing or
// unreadable memory file should not abort a turn — the prompt simply falls
// back to "(empty)". The pipeline calls this through an interface assertion
// (interface{ MemoryRead() string }), so the signature must stay exactly this.
func (b *Base) MemoryRead() string {
	if b.mem == nil {
		return ""
	}
	s, err := b.mem.Read()
	if err != nil {
		return ""
	}
	return s
}

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
	out := make([]tools.TranscriptLine, 0, len(src))
	for _, l := range src {
		if strings.TrimSpace(l.Text) == "" {
			continue
		}
		out = append(out, tools.TranscriptLine{
			Speaker: l.Speaker, Role: string(l.Role), Side: l.Side,
			Text: l.Text, At: l.At,
		})
	}
	return out
}

// Listen records a line in this agent's memory if it isn't their own.
func (b *Base) Listen(ctx context.Context, line TranscriptLine) error {
	if line.Speaker == b.name {
		return nil
	}
	return b.recordLine(line)
}

// ListenSelf records the agent's OWN turn into its memory, bypassing the
// self-skip in Listen. Opt-in path used by the host so it can see its own
// past intros / handoffs / address-user lines and avoid recycling phrasing.
func (b *Base) ListenSelf(ctx context.Context, line TranscriptLine) error {
	return b.recordLine(line)
}

func (b *Base) recordLine(line TranscriptLine) error {
	if b.mem == nil {
		return nil
	}
	if strings.TrimSpace(line.Text) == "" {
		return nil
	}
	tag := line.Speaker
	if line.Side != "" {
		tag = line.Side + " - " + line.Speaker
	}
	// Mark teammate lines explicitly so anti-repetition can avoid restating
	// points an ally already made on the same side.
	if mySide := b.Side(); mySide != "" && line.Side == mySide && line.Speaker != b.name {
		tag = "TEAMMATE " + tag
	}
	if err := b.mem.Append(fmt.Sprintf("[%s] %s: %s",
		line.At.Format("15:04:05"), tag, oneLine(line.Text))); err != nil {
		return err
	}
	if jc := strings.TrimSpace(line.JudgementComment); jc != "" {
		if err := b.mem.Append(fmt.Sprintf("[%s] JUDGEMENT on %s: %s",
			line.At.Format("15:04:05"), line.Speaker, oneLine(jc))); err != nil {
			return err
		}
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
// returns the underlying llm.Stream for the orchestrator to consume. When the
// speaker has a side (affirmative/negative), the most recent line from the
// opposing side is highlighted as a dedicated "rebut this" block so the LLM
// has the exact claim to counter rather than picking through transcript noise.
func (b *Base) runStream(ctx context.Context, system string, p SpeakPrompt) (*llm.Stream, error) {
	mem := strings.TrimSpace(p.Memory)
	transcript := formatRecent(p.Recent)

	parts := []string{
		"# Topic",
		p.TopicTitle,
	}

	if bg := strings.TrimSpace(p.Background); bg != "" {
		parts = append(parts, "", "# Background", bg)
	}
	if docs := strings.TrimSpace(p.SourceDocuments); docs != "" {
		parts = append(parts, "",
			"# Source documents (the user's original uploaded material)",
			docs,
			"When a passage directly supports a point, quote a short excerpt verbatim and name the document it comes from.",
		)
	}

	parts = append(parts, "",
		"# Your private memory (use to recall earlier moments)",
		fallback(mem, "(empty)"),
		"",
		"# Recent transcript",
		fallback(transcript, "(none yet)"),
	)

	if opp := latestOpposingLine(p.Recent, p.Side); opp != nil {
		parts = append(parts, "",
			"# Opponent's most recent claim — REBUT THIS DIRECTLY",
			fmt.Sprintf("Speaker: %s (%s side)", opp.Speaker, opp.Side),
			"Claim: "+oneLine(opp.Text),
			"Open your turn by naming "+opp.Speaker+" and quoting or tightly paraphrasing this claim, then dismantle it with concrete counter-evidence before advancing your own point.",
		)
	}

	if u := latestUserLine(p.Recent); u != nil {
		parts = append(parts, "",
			"# Audience steering — weave this angle into your speech",
			"The live audience just asked: "+oneLine(u.Text),
			"Acknowledge this angle naturally as you build your argument — do not ignore it, but do not abandon your side's position to chase it either. Use it as fresh framing for your existing points.",
		)
	}

	parts = append(parts, "",
		"# Directive from host",
		fallback(p.Instructions, "(speak naturally)"),
		"",
		fmt.Sprintf("Time budget for this turn: about %d seconds. Reply in %s. Speak naturally — full sentences only, no stage directions, no markdown.",
			p.SecondsBudget, p.TopicLanguage),
	)
	user := strings.Join(parts, "\n")
	hist := []llm.Message{{Role: llm.RoleUser, Content: user}}
	return b.llmC.StreamWithTools(ctx, system, hist, b.reg.AsOpenAIParams(),
		func(ctx context.Context, name, jsonArgs string) (string, error) {
			// Light up the diagram while a tool runs, then return to the
			// agent's prevailing "speaking" state once it completes.
			b.EmitActivity(classifyToolActivity(name), name)
			res, err := b.reg.Dispatch(ctx, name, jsonArgs, b)
			if err == nil && p.ToolResult != nil {
				p.ToolResult(name, jsonArgs, res)
			}
			b.EmitActivity("speaking", "")
			return res, err
		})
}

// latestOpposingLine scans recent transcript backwards for the most recent
// line spoken by the OPPOSING side. Returns nil if speaker has no side
// (host/judge/viewer) or no opposing line has been said yet.
func latestOpposingLine(recent []TranscriptLine, mySide string) *TranscriptLine {
	if mySide == "" {
		return nil
	}
	for i := len(recent) - 1; i >= 0; i-- {
		l := recent[i]
		if l.Side != "" && l.Side != mySide {
			return &l
		}
	}
	return nil
}

// latestUserLine scans recent transcript backwards for the most recent line
// from the live audience (Role == "user"). Returns nil if none. Used to
// surface audience steering ("talk about X") into every candidate/viewer
// prompt so the whole panel — not just the host — incorporates the request.
// Pending lines (not yet acknowledged by the host on-air) are skipped, as
// are Addressed lines (already answered on-air): without the Addressed
// guard the steering block re-fires on every subsequent turn and players
// keep parroting "since the audience asked..." indefinitely.
func latestUserLine(recent []TranscriptLine) *TranscriptLine {
	for i := len(recent) - 1; i >= 0; i-- {
		l := recent[i]
		if l.Role == "user" && !l.Pending && !l.Addressed {
			return &l
		}
	}
	return nil
}

func formatRecent(lines []TranscriptLine) string {
	var b strings.Builder
	for _, l := range lines {
		if l.Pending || strings.TrimSpace(l.Text) == "" {
			continue
		}
		tag := l.Speaker
		if l.Side != "" {
			tag = l.Side + " - " + l.Speaker
		}
		fmt.Fprintf(&b, "%s: %s\n", tag, oneLine(l.Text))
		if jc := strings.TrimSpace(l.JudgementComment); jc != "" {
			fmt.Fprintf(&b, "[Judgement fact-check on the line above]: %s\n", oneLine(jc))
		}
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
