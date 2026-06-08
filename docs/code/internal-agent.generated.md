---
slug: code/internal/agent
title: Package internal/agent
description: Auto-generated go doc reference for the internal/agent package.
---

# Package `internal/agent`

_Generated with `go doc -all ./internal/agent`. Regenerate with `scripts/gen_go_docs.sh`._

```text
package agent // import "github.com/sirily11/debate-bot/internal/agent"


FUNCTIONS

func AssignCharacterVoices(voices []tts.Voice, names []string, genders map[string]string,
	language string, seed int64, excludeUsed map[string]bool, log *slog.Logger,
) map[string]string
    AssignCharacterVoices assigns one Azure neural voice to each name in `names`
    from the locale-filtered pool, biased by the supplied gender hint when
    present. excludeUsed is the set of voice ShortNames already claimed by
    agents (so the host narrator and a character don't share a voice). Returned
    map is keyed by character name; missing entries (rare — only when the entire
    pool fits inside excludeUsed) are left out so the caller can detect & fall
    back. Same scoring + shuffle pipeline as AssignVoices so the picks feel
    consistent with the rest of the cast.

func AssignVoices(voices []tts.Voice, agents []Agent, language string, seed int64, log *slog.Logger)
    AssignVoices assigns one Azure neural voice to every agent. Voices are
    filtered by the topic language (locale prefix), then ranked so HD voices
    (e.g. "...DragonHDFlashLatestNeural") and standard un-accented locales (e.g.
    "zh-CN" rather than "zh-CN-shaanxi") are picked first. For each agent the
    picker also prefers voices whose Gender matches the agent's name (Bob →
    Male, Linda → Female via the nameGender table). Duplicates are avoided when
    supply allows; otherwise voices recycle and a warning is logged.

    seed makes intra-tier ordering deterministic when desired.


TYPES

type Agent interface {
	Name() string
	SafeName() string
	Role() Role
	Side() string
	Model() string
	Voice() tts.Voice
	SetVoice(v tts.Voice)
	Speak(ctx context.Context, p SpeakPrompt) (*llm.Stream, error)
	Listen(ctx context.Context, line TranscriptLine) error
	Compress(ctx context.Context) error
}
    Agent is the OOP contract every participant satisfies.

type AskDecision struct {
	Ask      bool   `json:"ask"`
	Question string `json:"question"`
	Target   string `json:"target,omitempty"`
}
    AskDecision is the JSON-mode response shape for WantsToAsk.

type Base struct {
	// Has unexported fields.
}
    Base is embedded by every concrete agent and provides shared behaviour.

func NewBase(name string, role Role, llmC *llm.Client, mem *memory.Memory,
	comp *memory.Compressor, reg *tools.Registry, tp TranscriptProvider,
) *Base
    NewBase creates a Base.

func (b *Base) AgentName() string
    AgentName implements tools.AgentContext (called from Tool implementations).

func (b *Base) AppendMemory(text string) error
    AppendMemory implements tools.AgentContext.

func (b *Base) Compress(ctx context.Context) error
    Compress forces an immediate compression pass.

func (b *Base) LLM() *llm.Client

func (b *Base) Listen(ctx context.Context, line TranscriptLine) error
    Listen records a line in this agent's memory if it isn't their own.

func (b *Base) ListenSelf(ctx context.Context, line TranscriptLine) error
    ListenSelf records the agent's OWN turn into its memory, bypassing the
    self-skip in Listen. Opt-in path used by the host so it can see its own past
    intros / handoffs / address-user lines and avoid recycling phrasing.

func (b *Base) Memory() *memory.Memory

func (b *Base) MemoryRead() string
    MemoryRead returns the agent's memory.md contents for inclusion in the next
    SpeakPrompt. Read errors are swallowed (returning "") because a missing or
    unreadable memory file should not abort a turn — the prompt simply falls
    back to "(empty)". The pipeline calls this through an interface assertion
    (interface{ MemoryRead() string }), so the signature must stay exactly this.

func (b *Base) Model() string

func (b *Base) Name() string

func (b *Base) Role() Role

func (b *Base) SafeName() string

func (b *Base) SetVoice(v tts.Voice)

func (b *Base) Side() string

func (b *Base) Tools() *tools.Registry

func (b *Base) Transcript() []tools.TranscriptLine
    Transcript implements tools.AgentContext.

func (b *Base) Voice() tts.Voice

type Candidate struct{ *Base }
    Candidate is one side's debater.

func NewCandidate(b *Base) *Candidate

func (c *Candidate) Speak(ctx context.Context, p SpeakPrompt) (*llm.Stream, error)
    Speak emits a candidate turn. The orchestrator passes p.Side and topic info.

type Host struct{ *Base }
    Host moderates the debate.

func NewHost(b *Base) *Host

func (h *Host) Speak(ctx context.Context, p SpeakPrompt) (*llm.Stream, error)
    Speak emits a host turn.

type ImageRefCatalogEntry struct {
	Season      int
	Episode     int
	Beat        int
	Description string
}
    ImageRefCatalogEntry is one row in the cross-episode image-reuse catalog
    surfaced to the series host. Season/Episode/Beat identify the prior archived
    frame; Description is the planner's per-beat direction for that frame (so
    the host can pick reuse candidates that match the current beat).

type Judge struct{ *Base }
    Judge is silent through phases 1-3, then declares a verdict and closes.

func NewJudge(b *Base) *Judge

func (j *Judge) Speak(ctx context.Context, p SpeakPrompt) (*llm.Stream, error)

type Phase int
    Phase is the current debate phase.

const (
	PhaseSetup Phase = iota
	PhaseOpening
	PhaseFreeSpeech
	PhaseClosing
	PhaseVerdict
	PhaseConclusion
	PhaseEnded
)
func (p Phase) String() string

type Player struct{ *Base }
    Player (解題者) is a contestant in a 海龜湯 / situation-puzzle round. Players
    never see the hidden truth — they must deduce it through yes/no questions to
    the host.

func NewPlayer(b *Base) *Player

func (pl *Player) Speak(ctx context.Context, p SpeakPrompt) (*llm.Stream, error)
    Speak emits a player turn.

type PuzzleHost struct {
	*Base

	// Has unexported fields.
}
    PuzzleHost (出題者) runs a 海龜湯 / situation-puzzle round. It alone knows the
    hidden truth and answers player yes/no questions in the canonical format.

func NewPuzzleHost(b *Base, surface, truth string, surfacePlan, surfaceAnchors, conclusionPlan []string, soundPlan []SoundDirection) *PuzzleHost
    NewPuzzleHost constructs a puzzle host. Both surface (湯面) and truth (湯底) are
    interpolated into the system prompt: the surface so the host can narrate
    the full original setup verbatim on the "surface" directive (without it the
    LLM was inventing a brief summary instead of reading the prepared story),
    and the truth so it can reason about each yes/no question against the actual
    answer. Players never see either via this path.

    surfacePlan / conclusionPlan are the visual director's per-frame direction
    lists. surfaceAnchors[i] is a short verbatim snippet from the surface text
    that begins beat i's narration; the host's system prompt asks it to emit
    "<scene N/>" immediately before saying anchor N so markers land on the
    planner-aligned frame (surface-vN.png) regardless of how the host paragraphs
    the prose. Pass nil for any of these when unavailable — the host falls back
    to soft guidance with unnumbered markers.

func (h *PuzzleHost) Speak(ctx context.Context, p SpeakPrompt) (*llm.Stream, error)
    Speak emits a puzzle-host turn.

type Registry struct {
	Host       Agent
	Judge      Agent
	Affirmatve []Agent
	Negative   []Agent

	PuzzleHost Agent
	Players    []Agent

	// SeriesHost is the single narrator agent for a TV-series episode.
	// Series content uses this role exclusively — no players, no judge,
	// no other speakers.
	SeriesHost Agent

	Viewers []Agent
}
    Registry holds every agent constructed for one content run. Debate-mode
    content uses Host/Judge/Affirmatve/Negative; situation-puzzle content uses
    PuzzleHost/Players. Viewers are shared across both formats.

func (r *Registry) All() []Agent
    All returns every agent in a deterministic order.

func (r *Registry) FindByName(name string) Agent
    FindByName looks up an agent by display name.

type Role string
    Role names a debate participant's category.

const (
	RoleHost        Role = "host"
	RoleAffirmative Role = "affirmative"
	RoleNegative    Role = "negative"
	RoleJudge       Role = "judge"
	RoleViewer      Role = "viewer"
	RolePuzzleHost  Role = "puzzle-host"
	RolePlayer      Role = "player"
	// RoleSeriesHost is the single narrator on a host-only TV-series episode.
	// Series episodes have no debate, no Q&A, no audience interjection — the
	// host reads the prepared synopsis and (optionally) a "previously on …"
	// preamble. See agent/series_host.go.
	RoleSeriesHost Role = "series-host"
)
func (r Role) Side() string
    Side returns "affirmative" or "negative" for candidate roles, otherwise "".

type SeriesCharacter struct {
	Name        string
	Gender      string
	Description string
	AzureVoice  string
}
    SeriesCharacter is one extra speaking role surfaced to the host. Mirrors
    the scenes.SeriesCharacter struct (the wiring layer translates one to
    the other so the agent package doesn't import scenes). AzureVoice is the
    assigned voice ShortName the orchestrator picks from the locale's available
    pool — empty when no Azure provider is configured (the host is still told
    the character exists so it can name them in narration, but the synth path
    collapses to the narrator voice for that span).

type SeriesHost struct {
	*Base

	// Has unexported fields.
}
    SeriesHost is the single narrator on a TV-series episode. Episodes are
    non-interactive: the host reads a prepared synopsis (the `## Surface`
    section in topic.md) and, when this isn't the season's first episode,
    a short "previously on …" recap synthesised by the compression LLM.

    SeriesHost reuses the same scene/sound marker protocol as PuzzleHost so
    the renderer's marker-stripping pipeline (situation_puzzle_pipeline.go)
    works without per-content branching. Series adds one extra marker family:
    `<season-S-episode-E-image-N/>` — the host references a specific past beat,
    the renderer paints that prior episode's archived PNG.

func NewSeriesHost(b *Base, show string, season, episode int, synopsis, previouslyOn string,
	narrationPlan, narrationAnchors []string, soundPlan []SoundDirection,
	imageRefs []ImageRefCatalogEntry, characters []SeriesCharacter,
) *SeriesHost
    NewSeriesHost wires a series host. show / season / episode go directly
    into the system prompt so the LLM can reference them in its narration (e.g.
    cold-open style intro lines). previouslyOn is the recap text (empty for
    episode 1). narrationPlan + narrationAnchors mirror the puzzle host's
    surfacePlan + surfaceAnchors. imageRefs is the cross- episode reuse catalog
    — pass nil for episode 1 / when the planner found no prior plans to mine.

func (h *SeriesHost) Characters() []SeriesCharacter
    Characters returns the per-episode cast roster (without the narrator).
    The pipeline reads this in synthSentence to map `<char-N>...</char-N>`
    markers to Azure voice ShortNames when building multi-voice SSML.

func (h *SeriesHost) SetCharacterVoices(byName map[string]string)
    SetCharacterVoices fills in the AzureVoice ShortName on each character
    entry by name. Called by the orchestrator after the per-locale voice pool
    is fetched + the per-character voices are picked. Names not in the supplied
    map are left untouched (their AzureVoice stays empty, which the synth path
    treats as "fall back to the narrator voice").

func (h *SeriesHost) Speak(ctx context.Context, p SpeakPrompt) (*llm.Stream, error)
    Speak emits one series-host turn for the supplied directive.

type SoundDirection struct {
	Mode   string
	Prompt string
	Anchor string
}
    SoundDirection mirrors scenes.SoundDirection. Lives in the agent package
    so the host's system prompt can render the per-cue list without importing
    scenes (which would cycle back into agent via llm-driven planning). Caller
    (orchestrator) translates between the two when constructing the host.

type SpeakPrompt struct {
	Phase         Phase
	SegmentNo     int
	SecondsBudget int
	Recent        []TranscriptLine
	Memory        string
	Instructions  string // host directive: "opening", "rebut:Linda", "closing", "address-user:<text>", ...
	TopicTitle    string
	TopicLanguage string
	Side          string // for candidates only; convenience copy of agent's side
}
    SpeakPrompt is the orchestrator's request to an Agent for one segment.

type TranscriptLine struct {
	Speaker   string
	Role      Role
	Side      string
	Text      string
	At        time.Time
	Pending   bool
	Addressed bool
}
    TranscriptLine is one entry in the running debate transcript. Pending marks
    audience lines that the host has not yet acknowledged on-air; such lines
    are kept out of agent prompts so candidates don't pre-empt the host's
    introduction of the question. Addressed marks audience lines that have
    already been answered on-air (host's address-user / candidate's answer-user
    turn finished). Once addressed, the audience-steering block stops re-firing
    on every subsequent agent turn — without this flag every player kept opening
    with "since the audience asked..." for the rest of the round.

type TranscriptProvider interface {
	Snapshot() []TranscriptLine
}
    TranscriptProvider is the orchestrator-side transcript view a Base needs.
    Defined here to keep agent independent of the debate package.

type Viewer struct{ *Base }
    Viewer is an audience member who can self-trigger questions.

func NewViewer(b *Base) *Viewer

func (v *Viewer) Speak(ctx context.Context, p SpeakPrompt) (*llm.Stream, error)
    Speak emits an audience turn (a question, a reaction, etc).

func (v *Viewer) WantsToAsk(ctx context.Context, recent []TranscriptLine) (AskDecision, error)
    WantsToAsk asks the viewer's LLM whether it wants to interject right now.
    Used by the agenda planner during free debate.
```
