---
slug: code/internal/content_creator
title: Package internal/content_creator
description: Auto-generated go doc reference for the internal/content_creator package.
---

# Package `internal/content_creator`

_Generated with `go doc -all ./internal/content_creator`. Regenerate with `scripts/gen_go_docs.sh`._

```text
package contentcreator // import "github.com/sirily11/debate-bot/internal/content_creator"


CONSTANTS

const SeriesArchiveSubdir = "tv-series"
    SeriesArchiveSubdir is the directory underneath the persistent root that
    holds every series episode's archive. Kept as a constant so callers and
    tests don't drift on the spelling.


VARIABLES

var ErrNoStore = errors.New("no transcript store")
    ErrNoStore signals that no on-disk transcript exists yet for the requested
    debate (e.g. the run was killed before any line was persisted).


FUNCTIONS

func BuildImageRefCatalog(priors []PriorEpisodeContent,
	frameLookup func(dir string, beat int) string,
) (catalog []agent.ImageRefCatalogEntry, paths map[string]string)
    BuildImageRefCatalog walks priors and builds the cross-episode reuse
    catalog the series host receives in its prompt + the resolver map the
    stage uses. catalog entries describe what is in each archived frame;
    paths map canonical keys to absolute on-disk PNGs. A frame is only
    catalogued when both its description (from scene-plan.json) AND the PNG file
    (`<dir>/scenes/narration-vN-*.png`) exist.

    Today the caller hands a frameLookup callback that resolves a given
    narration-vN frame's on-disk path under <dir>/scenes/. We keep the
    lookup external to avoid hardcoding the cache-filename format here —
    scenes.go already mints content-addressed names with sha1(prompt) and
    only the scenes layer knows the prompt that produced each frame. The
    caller (cmd/debate-bot/series.go) probes the directory and matches on the
    "narration-vN" prefix.

func BuildRecap(ctx context.Context, comp *llm.Client,
	priors []PriorEpisodeContent, showName string,
) (string, []string, error)
    BuildRecap synthesises a "previously on …" preamble for episode > 1.
    Returns ("", nil) for episode 1 / when no prior content can be loaded.
    Errors are LLM/transport-level only — a thin recap or an empty highlight
    list is treated as success (the caller uses whatever came back).

    `comp` is the compression LLM client (Env.CompressionBaseURL/Key/Model). We
    pick this rather than the host LLM because it's already tuned for short-form
    summarisation and avoids burning the host model on non-creative text.

func EnsureEpisodeDir(persistentRoot, show string, season, episode int) (string, error)
    EnsureEpisodeDir creates EpisodeDir(...) and its standard subdirectories.
    Returns the episode directory path. Errors are wrapped with the path that
    failed so callers can surface a useful message.

func EnsureOutDir(p string) error
    EnsureOutDir makes sure the output dir exists (called before logger setup).

func EpisodeDir(persistentRoot, show string, season, episode int) string
    EpisodeDir returns the canonical archive directory for one episode.
    The directory layout is:

        <persistentRoot>/tv-series/<show>/s<NN>/e<NN>/
          ├── scenes/             generated narration PNGs (one per beat)
          ├── music/              looping music bed mp3
          ├── sounds/              per-cue sound clips
          ├── scene-plan.json     full ScenePlan / SeriesScenePlan
          ├── script.txt          verbatim host narration (markers + all)
          ├── subtitles.vtt       sidecar WebVTT for player CC
          └── episode.mp3         concatenated audio archive

    Season/episode are zero-padded to two digits so a directory listing sorts
    lexicographically through s10/e10 without surprise reordering.

func FormatImageRefMarker(season, episode, beat int) string
    FormatImageRefMarker renders the human-readable marker form the host emits.
    Mirrors the marker regex used downstream.

func ImageRefKey(season, episode, beat int) string
    ImageRefKey formats a stable cross-episode image-reference key. Used both
    as the `<season-S-episode-E-image-N/>` marker payload AND as the in-memory
    map key the resolver hands back to the renderer. Keeping this in one place
    avoids drift between the host's prompt format and the resolver's lookup.

func LoadSnapshot(path string) ([]agent.TranscriptLine, error)
    LoadSnapshot is a convenience for callers that just need to read a
    transcript out of an existing .db file (e.g. the HTTP server serving
    /api/transcript for a finished debate). Returns ErrNoStore if the file
    doesn't exist yet.

func MsgChannelID(v any) string
    MsgChannelID extracts the channel id from any debate event message.
    Returns "" for unknown types (which are treated as broadcast by per-channel
    filters).

func PhaseLabel(contentType string, p agent.Phase) string
    PhaseLabel returns the human-readable phase name for the given content type.
    Single source of truth for both the video renderer's on-frame chip and the
    SSE PhaseMsg.Label field, so frontends never need to map phase IDs to text
    themselves.

func ShowDir(persistentRoot, show string) string
    ShowDir returns `<persistentRoot>/tv-series/<slug(show)>` — the per-show
    folder that holds every season/episode subdirectory for one series.
    persistentRoot is the non-session OUT_DIR (i.e. the user-supplied value
    before bootstrap appends `session-<stamp>`); see Env.PersistentRoot.

func SlugifyShow(s string) string
    SlugifyShow normalises a show name to a filesystem-safe slug. Mirrors the
    debate slug logic in cmd/debate-bot/main.go but kept package-local so the
    content_creator code does not depend on cmd/.

func StampChannelID(v any, id string) any
    StampChannelID returns a copy of v with its ChannelID set. Used by the
    per-channel send wrapper so orchestrators don't need to know their own id.
    Unknown types are returned unchanged.

func WriteSubtitleCues(path string, cues []SubtitleCue) error
    WriteSubtitleCues emits cues as a WebVTT file. It is shared by the live
    writer and translated sidecar generation so all subtitle tracks keep the
    same escaping and timestamp format.


TYPES

type DebatePlanner struct {
	// Has unexported fields.
}
    DebatePlanner drives the affirmative-vs-negative debate format.

func NewDebatePlanner(topic *config.DebateTopic, tracker *Tracker, reg *agent.Registry, q *userQueue, tr *Transcript) *DebatePlanner
    NewDebatePlanner constructs the debate-format planner; the queue is kept by
    the orchestrator.

func (p *DebatePlanner) Next(ctx context.Context) (*Turn, bool)
    Next produces the next Turn, or returns false to end the debate. It is
    single-threaded — the pipeline calls it from one goroutine.

type Deps struct {
	Planner  Planner
	Tracker  *Tracker
	Registry *agent.Registry
	TTS      tts.Provider
	OutDir   string
	Send     func(any) // event-bus publish wrapper
	Log      *slog.Logger
	Topic    string
	Language string
	// ContentType is the topic.Type discriminator (config.ContentType*).
	// Stamped onto PhaseMsg so the frontend can label phases without
	// hardcoding the per-format mapping.
	ContentType string
	Transcript  *Transcript
	LiveStream  *audio.LiveStream // shared mp3 broadcaster (paced by ffmpeg -re)

	// MusicPaths maps planner directive prefix → mp3 file path for turns
	// that should play with a Lyria-generated background bed mixed under
	// the host's TTS. Today the situation-puzzle planner uses keys
	// "surface" and "reveal"; other content types leave this nil.
	// pipeline.produce looks the key up by t.Directive (matching either
	// the bare directive or its prefix before any ":") and routes that
	// turn's TTS through musicmixer.New. Empty/missing key → dry TTS.
	MusicPaths map[string]string

	// SurfaceFrames is the visual director's surface-frame count for the
	// current puzzle. The pipeline caps SceneAdvanceMsg events emitted
	// from the surface narration at SurfaceFrames-1 so excess markers
	// from the host LLM don't wrap the rotation back to frame 0 mid-show.
	// 0 disables the cap (no plan available, accept whatever the host
	// emits).
	SurfaceFrames int
	// ConclusionFrames is the same idea for the conclusion phase. The
	// conclusion now reads as a longer reflective epilogue with scene
	// markers driving the image rotation; the pipeline caps marker count
	// at ConclusionFrames-1.
	ConclusionFrames int
	// NarrationFrames is the visual director's per-episode narration-frame
	// count for the series content type. Mirrors SurfaceFrames. The pipeline
	// caps SceneAdvanceMsg events emitted from a `narrate` directive at
	// NarrationFrames-1 so excess markers from the host LLM don't wrap the
	// rotation back to frame 0 mid-episode. 0 disables the cap.
	NarrationFrames int

	// HasSeriesPreviouslyOn means this series episode includes the optional
	// opening recap turn. The stitched mp4 lands soft subtitles slightly early
	// on those episodes, so the VTT sidecar gets a small extra delay.
	HasSeriesPreviouslyOn bool

	// SoundPaths is the planner's per-cue clip list — index N is the
	// on-disk mp3 path the mixer plays when the host emits
	// "<sound-overlapped-N/>" or "<sound-replace-N/>". Nil / empty
	// disables the feature (host's prompt omits the sound section so
	// no markers appear in the stream). Paths that don't exist are
	// dropped at dispatch time with a warning rather than failing the
	// turn.
	SoundPaths []string
}
    Deps are everything the pipeline needs to run.

type EndedMsg struct {
	ChannelID      string
	TranscriptPath string
	AudioPath      string
}
    EndedMsg tells the TUI the orchestrator has finished and it should quit.

type ErrorMsg struct {
	ChannelID string
	Err       error
}
    ErrorMsg surfaces a non-fatal error.

type ImageRefMsg struct {
	ChannelID string
	Key       string
}
    ImageRefMsg asks the active stage to swap to a specific cross-episode
    archived image. Emitted by the producer when the series host stream
    contains a `<season-S-episode-E-image-N/>` marker. Key is the canonical
    image-reference id (see contentcreator.ImageRefKey) — the stage holds
    a resolver map from key → in-memory *image.RGBA loaded from the prior
    episode's archive at startup. Stages without that resolver populated
    (debate, situation-puzzle, or a series stage that didn't preload any prior
    imagery) treat this as a no-op.

type MessageRow struct {
	ID      uint   `gorm:"primaryKey;autoIncrement"`
	Speaker string `gorm:"index;size:64;not null"`
	Role    string `gorm:"index;size:32;not null"`
	Side    string `gorm:"size:32"`
	Text    string `gorm:"type:text;not null"`
	At      time.Time
}
    MessageRow is one persisted transcript line — both user-typed messages
    and AI-spoken turns share the same table because the chat UI renders them
    uniformly. Auto-incrementing ID gives us stable ordering on reload (the `at`
    timestamp resolution alone wasn't enough — sub-millisecond turns from the
    same agent could land out of order).

func (MessageRow) TableName() string
    TableName pins the table to "messages" so future rename of the Go type
    doesn't accidentally invalidate existing on-disk schemas.

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

	// Has unexported fields.
}
    Orchestrator wires every package together for one debate run.

func New(env *config.Env, topic *config.DebateTopic, mcpCfg *config.MCPConfig,
	send func(any), log *slog.Logger, liveStream *audio.LiveStream,
) (*Orchestrator, error)
    New constructs an Orchestrator after loaders + .env are validated.
    liveStream is the shared mp3 broadcaster the pipeline writes audio into.

func NewForTest(send func(any), store *Store) *Orchestrator
    NewForTest builds a minimal Orchestrator suitable for tests that only
    exercise the user-message queue and transcript emission. The send callback
    receives every event the orchestrator emits — wrap it with the bus +
    StampChannelID stamp the same way main.go does to mimic real channel
    routing.

    store may be nil for tests that don't care about persistence; pass a real
    *Store to verify reload-from-disk behavior. Production code must use
    New(); this constructor skips agent / TTS / MCP / memory wiring so any
    orchestration call (Setup, Run) will panic.

func (o *Orchestrator) PushUserMessage(text, username string)
    PushUserMessage queues user input into the planner. username is the viewer's
    chosen handle (typically a random name persisted in localStorage on the
    frontend); empty string falls back to "user" for the speaker tag so past
    clients without a username still render reasonably.

func (o *Orchestrator) Run(ctx context.Context) error
    Run executes Setup then drives the pipeline. Blocks until the planner
    finishes.

func (o *Orchestrator) SeriesCharacters() []agent.SeriesCharacter
    SeriesCharacters returns the cast roster with Azure voice IDs already
    assigned by the orchestrator. The pipeline reads this in synthSentence to
    map `<char-N>` markers to voice ShortNames at synth time.

func (o *Orchestrator) SeriesNarrationFrames() int
    SeriesNarrationFrames reports the planner's narration-frame count for this
    episode. Used by the pipeline to size the marker-clamp budget so a stray
    `<scene 99/>` against a 14-frame plan doesn't pin the rotation.

func (o *Orchestrator) SetConclusionPlan(plan []string)
    SetConclusionPlan is the same as SetSurfacePlan for the conclusion phase.
    Conclusion uses scene-marker advancement (not a wall-clock timer) so the
    host needs to know what each numbered beat depicts to emit the right markers
    in the right order.

func (o *Orchestrator) SetPuzzleMusic(music map[string]string)
    SetPuzzleMusic installs the per-directive music file map for the upcoming
    pipeline run. Caller (cmd/debate-bot) populates this after musicgen.Generate
    finishes so the surface and reveal turns mix the generated bed under the
    host's TTS. No-op if music is empty or nil. Must be called before Run.

func (o *Orchestrator) SetSeriesCharacters(cast []SeriesCharacter)
    SetSeriesCharacters installs the planner's per-episode cast roster.
    The orchestrator stores these as-is and assigns Azure neural voices to
    each during Setup (after FetchVoices succeeds) so the host's prompt and
    the pipeline's multi-voice SSML synth path see fully-populated voice IDs.
    Empty / nil disables the feature for this episode (the host's prompt omits
    the character section entirely).

func (o *Orchestrator) SetSeriesImageRefs(catalog []SeriesImageRefCatalogEntry, paths map[string]string)
    SetSeriesImageRefs installs the cross-episode reuse catalog (visible to
    the host's prompt) AND the resolver map (canonical key → on-disk PNG path)
    consumed by the stage. catalog and paths are independent inputs: the catalog
    drives what the LLM may emit, the paths determine what the renderer can
    actually paint. Empty catalog → host omits the image-reuse section from its
    prompt; empty paths → ImageRefMsg events become no-ops at the stage.

func (o *Orchestrator) SetSeriesMusic(path string)
    SetSeriesMusic installs the optional looping music bed path for the upcoming
    episode run. Caller (cmd/debate-bot/series.go) populates this after musicgen
    finishes. Empty path is a no-op.

func (o *Orchestrator) SetSeriesPlan(plan, anchors, animations []string)
    SetSeriesPlan records the visual director's per-frame direction list +
    anchors + animations for the series narration. Mirrors SetSurfacePlan /
    SetSurfaceAnchors on the puzzle side. nil / empty inputs are no-ops.

func (o *Orchestrator) SetSeriesPreviouslyOn(recap string)
    SetSeriesPreviouslyOn installs the compression-LLM-generated recap text for
    this episode. Empty string disables the recap turn entirely (the planner
    won't emit one and the host's prompt block stays empty so the LLM never
    invents one). Must be called before Run, since the host agent captures its
    prompt at construction time inside makeAgent.

func (o *Orchestrator) SetSeriesSoundPlan(plan []SoundCueDirection, paths []string)
    SetSeriesSoundPlan mirrors SetSoundPlan but applies to series episodes.
    Same trim-to-shorter-length semantics as the puzzle setter.

func (o *Orchestrator) SetSoundPlan(plan []SoundCueDirection, paths []string)
    SetSoundPlan installs the planner's sound-cue list and the parallel list of
    generated clip paths. Index N of either slice must describe the same cue;
    mismatched lengths are tolerated by trimming both to the shorter length
    so a partial generation failure (one clip out of five) doesn't pin a stray
    index on the wrong path. No-op when either list is empty — the host's prompt
    then omits the sound section so the LLM never emits a sound marker. Caller
    invokes this after musicgen finishes generating each clip and before Run.

func (o *Orchestrator) SetSurfaceAnchors(anchors []string)
    SetSurfaceAnchors records the planner's per-beat verbatim anchor list
    (parallel to SurfacePlan). The puzzle host's system prompt embeds each
    anchor under its beat so the host can string-match its narration position
    against the surface and drop "<scene N/>" markers exactly at the planner's
    intended boundaries — replaces the old "count paragraph breaks" heuristic
    that drifted in long narrations. No-op when the slice is empty (host falls
    back to its own paragraph judgement).

func (o *Orchestrator) SetSurfacePlan(plan []string)
    SetSurfacePlan records the visual director's surface beat directions for
    the upcoming run. Each entry is a one-sentence direction describing what the
    matching cached image (surface-vN) depicts; the puzzle host's system prompt
    enumerates them as "Beat N: <direction>" so the host can emit "<scene N/>"
    markers locked to the planner's beats. Caller (cmd/debate-bot) sets this
    from scenes.Plan / scenes.FallbackPlan output after planning completes and
    before Run is called. No-op for empty plans.

func (o *Orchestrator) Setup(ctx context.Context) error
    Setup performs all blocking-but-deterministic initialisation: voice fetch,
    MCP boot, agent construction, voice assignment.

func (o *Orchestrator) Shutdown()
    Shutdown stops MCP subprocesses and closes the per-debate sqlite handle.
    The DB file is left in place so a viewer who reloads after the debate ends
    still sees the chat history.

func (o *Orchestrator) SubtitleCues() []SubtitleCue
    SubtitleCues returns the WebVTT cue timings generated by the most recent
    Run.

type PhaseMsg struct {
	ChannelID string
	Phase     agent.Phase
	Label     string
	Type      string
}
    PhaseMsg announces a phase change.

    Label is the human-readable phase name, content-type aware so the frontend
    can show "問答" during a puzzle's Q&A round and "自由辯論" during a debate's
    free-speech round without baking that mapping into the client. The pipeline
    stamps it at emit time using the topic's content type. Type carries the
    content discriminator (mirrors TopicMsg.Type) so the frontend can also
    adjust styling by format.

type Pipeline struct {
	// Has unexported fields.
}
    Pipeline owns the goroutines for produce/memory stages.

func NewPipeline(d Deps) *Pipeline
    NewPipeline creates a Pipeline.

func (p *Pipeline) Run(ctx context.Context) ([]string, error)
    Run boots all stages and blocks until the planner stops emitting turns AND
    every stage drains. Returns the produced audio file paths in order.

func (p *Pipeline) SubtitleCues() []SubtitleCue
    SubtitleCues returns the timed WebVTT cues accumulated so far.

type Planner interface {
	Next(ctx context.Context) (*Turn, bool)
}
    Planner is the per-content-type next-turn scheduler. The pipeline only calls
    Next; concrete implementations (DebatePlanner, PuzzlePlanner, ...) own their
    own state machines.

type PriorEpisode struct {
	Season  int
	Episode int
	Dir     string
}
    PriorEpisode is one entry from SiblingEpisodeDirs — the (season, episode)
    pair plus the absolute archive directory. Sorted in lexicographic order on
    (season, episode) so callers can read them as the show's chronology.

func SiblingEpisodeDirs(persistentRoot, show string, curSeason, curEpisode int) ([]PriorEpisode, error)
    SiblingEpisodeDirs lists every prior episode of `show` whose (season,
    episode) is strictly before (curSeason, curEpisode) under lexicographic
    order. Used for both:

      - "previously on …" recap input — read each prior `script.txt` /
        `scene-plan.json` and feed them to the compression LLM.
      - cross-episode image resolver — the host emits markers like
        `<season-1-episode-3-image-7/>` and the renderer needs to know which
        on-disk PNG to load.

    Missing or malformed directory names (anything that isn't `sNN` / `eNN`) are
    skipped silently rather than surfacing as an error: the show's archive is
    allowed to grow and shrink over time, and a half-deleted episode shouldn't
    block the next one from rendering.

type PriorEpisodeContent struct {
	Season  int
	Episode int
	Dir     string
	// Script is the concatenated script.txt content from the prior
	// episode's archive directory. Empty when the file wasn't written
	// (legacy archives, or a prior run that crashed before
	// finishCloseoutEpisode).
	Script string
	// Plan is the parsed scene-plan.json from the prior archive. nil
	// when the file is missing or malformed — caller should treat that
	// as "no reusable imagery from this episode".
	Plan *PriorScenePlan
}
    PriorEpisodeContent is one prior episode's recap-relevant artefacts —
    the verbatim host narration and the parsed scene plan. Used internally by
    BuildRecap and exposed to callers (cmd/debate-bot/series.go) so they can
    build the cross-episode image-reuse catalog from the same parsed plan data.

func LoadPriorEpisodes(persistentRoot, show string, season, episode int) ([]PriorEpisodeContent, error)
    LoadPriorEpisodes reads every entry returned by SiblingEpisodeDirs and
    fills in the script + scene-plan content. Errors per-episode are silently
    swallowed (the recap and catalog should both gracefully degrade when one
    prior archive is corrupt or partial); only a fatal SiblingEpisodeDirs error
    bubbles out.

type PriorScenePlan struct {
	Surface    []string `json:"surface"`
	Conclusion []string `json:"conclusion"`
	Narration  []string `json:"narration"`
}
    PriorScenePlan mirrors the shape of internal/video/scenes.ScenePlan but is
    duplicated here as a small struct so the content_creator package doesn't
    depend on internal/video/scenes (which would create an import cycle
    through anything that imports both). Only the fields BuildRecap and the
    cross-episode catalog need are decoded.

type PuzzlePlanner struct {
	// Has unexported fields.
}
    PuzzlePlanner drives a 海龜湯 / situation-puzzle round. The flow is:

     1. Surface (PhaseOpening): the puzzle host presents the soup-surface
        situation and invites players to ask questions.
     2. Q&A loop (PhaseFreeSpeech): players ask yes/no questions in round-robin
        order; the puzzle host answers each one. Every few rounds the planner
        probes audience viewers for an interjection (mirrors the debate-side
        askAnyViewer behaviour). Live audience messages are surfaced as the host
        paraphrasing then answering.
     3. Reveal (PhaseVerdict): when time is short OR /end has been pushed OR a
        player's proposed solution has been judged correct, the host reveals the
        full truth.
     4. Conclusion (PhaseEnded): each player gives a short reaction, the host
        signs off, and the planner returns ok=false.

    Phase constants are reused (rather than introducing new ones) so the rest of
    the system — video stage, web UI, transcript persistence — needs no changes
    for the new format.

func NewPuzzlePlanner(topic *config.DebateTopic, tracker *Tracker, reg *agent.Registry, q *userQueue, tr *Transcript) *PuzzlePlanner
    NewPuzzlePlanner constructs the puzzle-format planner.

func (p *PuzzlePlanner) Next(ctx context.Context) (*Turn, bool)
    Next produces the next Turn or returns false to end the round.

type SceneAdvanceMsg struct {
	ChannelID string
	Index     int
}
    SceneAdvanceMsg asks any active visual stage to swap to a specific scene
    variant for the current phase. Emitted by the producer when the speaker's
    streamed text contains a scene-switch marker (today: the puzzle host's
    surface and conclusion narration use `<scene N/>` to flag a beat boundary so
    images follow the audio beats instead of a fixed timer).

    Index is the 0-based absolute frame the renderer should jump to. A negative
    Index (markerIdxNoNumber) preserves the legacy "advance by one" semantics
    for unnumbered `<scene/>` markers — the renderer increments curSceneIdx by
    one mod count.

    Stages without a multi-variant scene active treat it as a no-op.

type SeriesCharacter struct {
	Name        string
	Gender      string
	VoiceHint   string
	Description string
}
    SeriesCharacter is the contentcreator-facing mirror of
    agent.SeriesCharacter. Lets the prepare layer (internal/series) wire the
    cast list without importing the agent package directly. AzureVoice is left
    empty by the caller — the orchestrator fills it in after FetchVoices runs in
    Setup, picking from the locale's voice pool.

type SeriesImageRefCatalogEntry struct {
	Season      int
	Episode     int
	Beat        int
	Description string
}
    SeriesImageRefCatalogEntry is the contentcreator-facing mirror of
    agent.ImageRefCatalogEntry. Lets cmd/ wire the catalog without importing
    the agent package directly. The orchestrator's SetSeriesImageRefs translates
    these into the agent struct on the way to the host.

type SeriesPlanner struct {
	// Has unexported fields.
}
    SeriesPlanner emits the ordered turn list for a TV-series episode. It is
    intentionally tiny — series episodes are non-interactive, so the entire flow
    is at most:

     1. PhaseOpening — directive "previously" (only when previouslyOn is set)
     2. PhaseFreeSpeech — directive "narrate" (the main episode)
     3. PhaseEnded — return false

    We reuse the existing Phase enum rather than introducing a
    series-only one (the puzzle planner did the same — see comment on
    situation_puzzle_planner.go:26-29).

func NewSeriesPlanner(topic *config.DebateTopic, tracker *Tracker, reg *agent.Registry, hasRecap bool) *SeriesPlanner
    NewSeriesPlanner constructs the series-format planner. hasRecap controls
    whether the recap turn is emitted; the recap text itself lives on the
    orchestrator and is read from the host agent's prompt.

func (p *SeriesPlanner) Next(ctx context.Context) (*Turn, bool)
    Next produces the recap turn (if applicable), then the main narration turn.
    Returns (nil, false) once both have been emitted.

type SoundCueDirection struct {
	Mode            string
	Prompt          string
	Anchor          string
	DurationSeconds int
}
    SoundCueDirection mirrors scenes.SoundDirection but lives in content_creator
    so the orchestrator doesn't need to import the scenes package.
    Caller (cmd/debate-bot) translates one to the other after planning + clip
    generation.

type SoundCueMode string
    SoundCueMode is the dispatch mode embedded in a `<sound-…/>` marker.
    Overlap mixes the planner-generated clip on top of the running music bed
    (atmospheric stinger). Replace cross-fades the bed itself over to the new
    clip so the underlying texture changes (e.g. tonal shift at a key beat).

const (
	SoundCueOverlap SoundCueMode = "overlap"
	SoundCueReplace SoundCueMode = "replace"
)
type SoundCueMsg struct {
	ChannelID string
	Index     int
	Mode      SoundCueMode
}
    SoundCueMsg asks the audio mixer to dispatch one of the planner's
    pre-generated sound clips. Emitted by the producer when the host stream
    contains a `<sound-overlapped-N/>` or `<sound-replace-N/>` marker.
    Index is the 0-based slot of the clip in the puzzle's sound plan; Mode picks
    between additive overlay (overlap) and a cross-fade of the running music bed
    (replace). Stages / runtimes that don't have a sound mixer attached treat
    this as a no-op.

type SoundMarker struct {
	Mode  SoundCueMode
	Index int
}
    SoundMarker is one parsed sound-cue token. Mode comes from the marker's
    verb ("overlapped" → overlap, "replace" → replace) and Index is the 0-based
    slot the planner assigned to the underlying clip; the pipeline emits one
    SoundCueMsg per marker so the mixer can dispatch.

type StatusMsg struct {
	ChannelID string
	Text      string
}
    StatusMsg pushes a status-line note (e.g. "MCP server X connected").

type Store struct {
	// Has unexported fields.
}
    Store is the per-debate sqlite-backed persistence layer for the
    chat transcript. Each debate gets its own .db file (typically
    `{outdir}/session.db`), which keeps a debate's data co-located with its
    audio + text artefacts and makes archival a single-file copy.

    Append is non-blocking on writes that fail (logged and dropped) so a
    disk-full or locked-DB condition can't stall the live debate. Reads are
    strict — a load failure returns the error so the caller can fall back to the
    in-memory snapshot or surface the error to the UI.

func OpenStore(path string, log *slog.Logger) (*Store, error)
    OpenStore creates / migrates the messages table at path. The parent dir must
    already exist (the orchestrator's debate.EnsureOutDir call covers this in
    production).

func (s *Store) Append(line agent.TranscriptLine)
    Append persists one transcript line. Failures are logged and dropped — the
    in-memory transcript remains the source of truth for the live UI; the DB is
    for reload-after-end and post-mortem inspection.

func (s *Store) Close() error
    Close releases the underlying sqlite handle. Safe to call on a nil store.

func (s *Store) Snapshot() ([]agent.TranscriptLine, error)
    Snapshot returns every row in insertion order. Callers should treat the
    returned slice as read-only.

type SubtitleCue struct {
	Start time.Duration
	End   time.Duration
	Text  string
}
    SubtitleCue is the exported, immutable view of a generated subtitle cue. It
    lets job-level post-processing, such as translation, reuse the exact timings
    the live pipeline computed without parsing WebVTT text back from disk.

type TickMsg struct {
	ChannelID string
	Elapsed   time.Duration
	Remaining time.Duration
}
    TickMsg updates the elapsed/remaining clock display.

type TopicMsg struct {
	ChannelID string
	ID        string
	Title     string
	// Type carries the content-type discriminator (config.ContentTypeDebate /
	// config.ContentTypeSituationPuzzle). Stage subscribers gate on it so each
	// content kind has its own video-generation flow.
	Type     string
	Index    int // 0-based position in the queue
	Total    int // total topics in the queue
	AffNames []string
	NegNames []string

	// Position statements rendered as small footer text inside each side
	// panel so viewers can see what each side argues. For debate, these are
	// the affirmative / negative position summaries. For situation-puzzle,
	// AffPosition holds the soup-surface (湯面); NegPosition stays empty.
	// May be empty when the topic .md omits the section.
	AffPosition string
	NegPosition string

	// Series-only metadata. The renderer paints "Show\nS{Season} · E{Episode}"
	// as a small top-left label that fades out a few seconds in, mirroring
	// the way regular TV episodes show their identification. Empty / zero
	// for non-series content.
	Show    string
	Season  int
	Episode int
}
    TopicMsg announces that the active debate topic has changed (sequential
    multi-topic runs). The Stage uses it to reset the encoder title + side
    panels; the web UI uses it to clear the live transcript and refresh the
    topic list. In parallel mode, ID and ChannelID are equal — each channel
    emits its own TopicMsg at start.

type TopicsChangedMsg struct{}
    TopicsChangedMsg signals that the channel/debate list has changed (e.g.
    a new debate.md was discovered by the folder watcher and added to a
    channel's queue). The frontend reacts by re-fetching /api/topics. Broadcast
    only — ChannelID is intentionally empty so every connected SSE client gets
    it regardless of which channel they're tuned to.

type Tracker struct {
	// Has unexported fields.
}
    Tracker tracks elapsed time and per-speaker speaking budget.

func NewTracker(total time.Duration) *Tracker
    NewTracker starts the clock.

func (t *Tracker) AddSpeaking(speaker string, d time.Duration)
    AddSpeaking adds d to a speaker's running total.

func (t *Tracker) Elapsed() time.Duration
    Elapsed returns wall-clock time since the tracker started.

func (t *Tracker) FairShare(speakers int) time.Duration
    FairShare returns the per-speaker budget given a count of equal-share
    speakers.

func (t *Tracker) Remaining() time.Duration
    Remaining returns total minus elapsed (clamped at zero).

func (t *Tracker) Total() time.Duration
    Total returns the configured total budget.

func (t *Tracker) Used(speaker string) time.Duration
    Used returns a speaker's accumulated speaking time.

type Transcript struct {
	// Has unexported fields.
}
    Transcript is the orchestrator-wide append-only log of completed turns.
    store is optional: when set, every appended line is also persisted to sqlite
    so reloads after the orchestrator exits can recover the full chat history.

func NewTranscript() *Transcript
    NewTranscript constructs an empty Transcript with no persistence.

func NewTranscriptWithStore(s *Store) *Transcript
    NewTranscriptWithStore constructs a Transcript backed by the given Store.
    On construction, any lines already in the store are loaded so a reload
    after a server restart (or tuning into a finished debate) preserves the chat
    history. If the load fails the transcript starts empty — the live debate is
    still usable, just without recovered history.

func (t *Transcript) AcknowledgeUserLines() int
    AcknowledgeUserLines clears the Pending flag on every audience line,
    making them visible to subsequent agent prompts. Called when the host's
    address-user turn begins producing.

func (t *Transcript) AppendFromTurn(tn *Turn) agent.TranscriptLine
    AppendFromTurn drains all sentences accumulated by a played turn and appends
    one consolidated line to the transcript.

func (t *Transcript) AppendLine(line agent.TranscriptLine)
    AppendLine inserts a fully-formed line — used by tests and by callers that
    already have an agent.TranscriptLine in hand (e.g. replaying a recovered
    transcript). Production code should prefer AppendFromTurn / AppendUser so
    the same input → output mapping is exercised in tests.

func (t *Transcript) AppendUser(speaker, text string) agent.TranscriptLine
    AppendUser inserts a synthetic user line so the user's input shows up
    alongside agent turns in the transcript. Marked Pending until the host
    addresses it on-air, which keeps it out of agent prompts in the meantime
    (so already-buffered candidate turns don't prematurely respond to it).
    speaker carries the viewer's display name (cookie-issued); empty falls back
    to "user" so old clients still render reasonably.

func (t *Transcript) MarkUserLinesAddressed() int
    MarkUserLinesAddressed flips Addressed=true on every non-pending audience
    line. Called after a turn whose directive answered the audience finishes
    (host's address-user, candidate's answer-user, puzzle host's address-user).
    Without this, latestUserLine keeps returning the same audience line on every
    subsequent prompt and the audience-steering block makes each player open
    with "since the audience asked..." for the rest of the round.

func (t *Transcript) RecentN(n int) []agent.TranscriptLine
    RecentN returns the last N lines.

func (t *Transcript) Save(path string) error
    Save writes the transcript to disk in `name: text` format.

func (t *Transcript) Snapshot() []agent.TranscriptLine
    Snapshot returns a copy of all current lines.

type TranscriptMsg struct {
	ChannelID     string
	Speaker       string
	Role          agent.Role
	Side          string
	Text          string
	Done          bool
	AudioDuration time.Duration
}
    TranscriptMsg is one sentence (or fragment) of one turn.

    AudioDuration is the wall-clock length of the synthesized audio for Text,
    measured from the bytes the TTS provider produced. Subscribers that drive
    time-based UI (subtitle scrolling) use it to align motion with the audio.
    Zero means "unknown" — emitters that don't have measured audio (e.g.
    the final Done=true marker) leave it unset.

type Turn struct {
	ID        int
	Phase     agent.Phase
	Speaker   agent.Agent
	Directive string
	Budget    time.Duration

	// PrevTurn, when non-nil, points to a previously-emitted turn whose
	// produced text must be inlined into this turn's directive at production
	// time. Used by the situation-puzzle planner: the host's "answer:"
	// directive needs the player's actual rendered question, but the planner
	// runs ahead of the producer (turnCh is buffered) so transcript-based
	// lookup at planner time always misses. Storing a pointer lets produce()
	// resolve the directive once the predecessor's full text is known.
	PrevTurn *Turn

	// Filled by the producer. TextOut emits sentence-level text for the TUI.
	TextOut   chan string
	AudioPath string

	// Has unexported fields.
}
    Turn is the unit of work that flows through the pipeline. The producer
    streams sentences via TextOut and writes audio bytes into the shared
    LiveStream + per-turn file in Deps.

func (t *Turn) AppendText(s string)
    AppendText accumulates one sentence into the turn's full-text buffer.
    Called by the producer for every sentence the LLM emits.

func (t *Turn) Err() error
    Err returns the terminal error if any.

func (t *Turn) FullText() string
    FullText returns everything AppendText has captured so far. Safe to call
    after the producer finishes producing this turn.

func (t *Turn) MarkPlayed()
    MarkPlayed signals the turn is fully done.

func (t *Turn) SetErr(err error)
    SetErr records a terminal error for this turn.
```
