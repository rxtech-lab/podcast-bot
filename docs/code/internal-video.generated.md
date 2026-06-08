---
slug: code/internal/video
title: Package internal/video
description: Auto-generated go doc reference for the internal/video package.
---

# Package `internal/video`

_Generated with `go doc -all ./internal/video`. Regenerate with `scripts/gen_go_docs.sh`._

```text
package video // import "github.com/sirily11/debate-bot/internal/video"

Package video runs a long-lived ffmpeg encoder that bakes the live debate
transcript onto a video stream. Output is HLS (m3u8 + .ts segments) served by
the HTTP server.

The HLS stream carries video AND audio in one mux. The audio side is fed by an
in-Go pump (encoderAudioPump) that consumes the realtime-paced LiveStream and
pads with pre-generated silent MP3 frames whenever no TTS bytes are flowing
— without continuous audio packets the HLS muxer would stall at the segment
boundary. /api/audio/stream is kept around for fallback clients.

Frames are rendered in Go using golang.org/x/image so we don't depend on
ffmpeg's drawtext filter (which requires --enable-libfreetype, missing from many
distro/brew default builds). ffmpeg only encodes + muxes.

FUNCTIONS

func StitchMP4(hlsDir, outPath string, opts StitchOpts) error
    StitchMP4 muxes the HLS playlist at hlsDir/stream.m3u8 into a single .mp4 at
    outPath. Both video and audio are stream-copied straight from the HLS source
    — the encoder already mixed the music bed underneath every TTS turn into the
    HLS audio track, so the stitched mp4 carries the same sonic mix the live
    stream listener heard. No separate audio concat step is needed (or wanted:
    a sidecar `debate.mp3` would only have the dry TTS without the music bed,
    which is why the previous version's mp4 sounded unmusical compared to the
    live channel).

    Returns an error if the playlist is missing or ffmpeg exits non-zero.


TYPES

type CameraMovement struct {
	Kind  MovementKind
	Value [2]float64
}
    CameraMovement is a Ken-Burns-style pan/zoom applied to a still image
    source. It is a value type with no internal clock — the caller passes
    progress p ∈ [0,1] each frame, so the same instance can drive multiple
    sources in parallel and the renderer keeps the time bookkeeping.

    Value carries movement-specific parameters:
      - pan* → Value[0] = pan magnitude as fraction of source (default 0.30 when
        zero). Value[1] is reserved.
      - zoom* → Value[0] = start scale, Value[1] = end scale. When both are zero
        the defaults kick in: zoomin 1.0→0.7, zoomout 0.7→1.0.
      - stall → Value is ignored; render is identity.

func (m CameraMovement) Render(dst *image.RGBA, src *image.RGBA, p float64)
    Render draws src into dst applying the camera move at progress p ∈ [0,1].
    p must already be eased — Render does not apply any easing of its own so
    callers can choose linear, cubic, ease-out etc.

    Implementation uses xdraw.CatmullRom.Transform with a sub-pixel-precise
    affine matrix. Both viewport position and viewport size are floats end-
    to-end, so a slow zoom no longer snaps the source rect by whole pixels
    between adjacent frames — the earthquake shimmer that integer rect rounding
    produced is gone.

    No-op when src or dst is nil. Empty bounds are treated as no-op so a 1×1
    placeholder source doesn't crash the resampler.

type DebateStage struct {
	// Has unexported fields.
}
    DebateStage drives the encoder's renderer for content of type "debate".
    It tracks the topic title, the phase, and the active-speaker subtitle.
    Bound to one Encoder; cheap to construct; Run blocks until ctx is done.

    In parallel (channel) mode, each DebateStage is bound to a specific channel
    id and ignores events whose ChannelID doesn't match. An empty channel id
    means "accept all events" — the sequential default.

    Type gating: the stage only acts while the most recent TopicMsg's Type is
    debate (or unset, for back-compat with topics produced before the field
    existed). When a non-debate topic becomes active, the stage goes idle so the
    matching stage (e.g. PuzzleStage) can drive the encoder unopposed.

func NewDebateChannelStage(enc *Encoder, channelID string) *DebateStage
    NewDebateChannelStage creates a DebateStage that only reacts to events whose
    ChannelID matches the given id. Use this in parallel mode so every channel's
    Encoder only sees its own debate's transcript / phase / topic events.
    The stage starts idle and activates on the first matching debate TopicMsg.

func NewDebateStage(enc *Encoder) *DebateStage
    NewDebateStage creates a DebateStage that consumes every event on the bus
    (sequential mode). The topic title and side panels are installed dynamically
    when the orchestrator publishes a debate-type TopicMsg, so the same Stage/
    Encoder pair is reused across sequential debate topics.

func (s *DebateStage) Run(ctx context.Context, bus *eventbus.Bus)
    Run subscribes to bus and dispatches transcript + phase events. Returns when
    ctx is cancelled or the bus closes.

type Encoder struct {
	// Has unexported fields.
}
    Encoder owns the ffmpeg process, the in-process frame compositor, and the
    HLS output directory.

func New(ctx context.Context, sessionDir string, res Resolution, log *slog.Logger) (*Encoder, error)
    New starts the encoder in live (sliding-window HLS) mode. Equivalent to
    NewWithOptions with default Options. Kept as a wrapper so existing callers
    don't need to be retrofitted.

func NewWithOptions(ctx context.Context, sessionDir string, res Resolution, opts Options, log *slog.Logger) (*Encoder, error)
    NewWithOptions starts the encoder. sessionDir is where HLS segments +
    the ffmpeg stderr log are written. res selects the output resolution;
    the renderer composites at 1920×1080 and ffmpeg's scale filter changes
    delivery size when needed. opts.Archival flips the HLS muxer into VOD mode
    so segments are retained for a downstream stitch.

func (e *Encoder) AttachAudio(ctx context.Context, ls *audio.LiveStream)
    AttachAudio subscribes the encoder's audio pump to the shared LiveStream so
    real TTS bytes get muxed into the HLS output. The pump pads with silent MP3
    frames whenever the LiveStream is idle, so ffmpeg's HLS muxer always has
    audio packets to interleave with video.

func (e *Encoder) AudioStartOffset() time.Duration
    AudioStartOffset returns how long the encoder ran before any real
    (non-silent) audio bytes arrived. Returns 0 if the audio pump never reported
    real bytes (degenerate run with no TTS output) or the encoder hasn't started
    yet. Stitch trims the front of the mp4 by this duration so the output starts
    at the show's actual first sound, not the silent prep prefix.

func (e *Encoder) Close() error
    Close flushes video, waits up to 2s for ffmpeg to exit, then SIGKILLs.

func (e *Encoder) HLSDir() string
    HLSDir returns the directory holding stream.m3u8 + segments.

func (e *Encoder) SetBody(s string, audioDuration time.Duration)
    SetBody updates the spoken text shown inside the subtitle box. audioDuration
    is the wall-clock length of the synthesized audio for s and drives
    time-based subtitle motion (scroll start). Pass 0 when unknown.

func (e *Encoder) SetClock(elapsed, total time.Duration)
    SetClock updates the elapsed/total clock display at the bottom of the frame.

func (e *Encoder) SetPhase(s string)
    SetPhase updates the phase status line under the topic title.

func (e *Encoder) SetPositions(aff, neg string)
    SetPositions sets each side's position statement (the stance they argue
    for), drawn as small footer text inside the side panels.

func (e *Encoder) SetPuzzleMode(b bool)
    SetPuzzleMode toggles the cinematic puzzle layout — minimal chrome over
    AI-generated scene backgrounds. PuzzleStage flips this on when a puzzle
    topic activates and off when it idles.

func (e *Encoder) SetPuzzleSceneName(name string)
    SetPuzzleSceneName records the active puzzle scene name (one of
    scenes.Scene*) so the renderer can apply scene-specific subtitle styling
    — today, the surface phase paints the caption directly on the scene with
    a black outline (no quote-card chrome), while QA/reveal/ conclusion
    keep the slab-and-rule look. PuzzleStage calls this in lockstep with
    SetSceneBackground.

func (e *Encoder) SetSceneAnimation(kind string)
    SetSceneAnimation forwards the per-beat camera move name (one of
    scenes.Animation*) to the renderer so the still scene image plays with a
    Ken-Burns-style pan / zoom. Pass "" or "stall" to hold the still image.
    PuzzleStage calls this immediately after every SetSceneBackground so the
    trajectory is locked to the new image.

func (e *Encoder) SetSceneBackground(img *image.RGBA)
    SetSceneBackground swaps the active scene image, crossfading from the
    previous one. Pass nil to clear (renderer falls back to its default bg
    plate). Used by PuzzleStage on TopicMsg + PhaseMsg as the puzzle moves
    surface → Q&A → reveal → conclusion.

func (e *Encoder) SetSeriesLabel(show string, season, episode int, host string)
    SetSeriesLabel records the show / season / episode / host quadruple painted
    as a small top-left identification label in series narration mode. Three
    rows: show name, season-episode, host name. The renderer fades the label
    out shortly after activation, mirroring how regular TV episodes show their
    identification at the start of an episode. Empty show clears the label.

func (e *Encoder) SetSeriesSectionLabel(text string, hold bool)
    SetSeriesSectionLabel installs the section subtitle painted under the ID
    label. text == "" clears it. hold == true keeps the banner at full opacity
    until cleared (used for the recap section, ended by the next phase); hold ==
    false runs a 30 s fade-out (main-content section).

func (e *Encoder) SetSides(aff, neg []string)
    SetSides loads the affirmative / negative speaker rosters into the side
    panels rendered on the left and right of the stage.

func (e *Encoder) SetSpeaker(speaker, role, side string)
    SetSpeaker activates the centered subtitle box for the given speaker.
    role values match agent.Role string values ("host", "affirmative", etc).
    Calling with empty speaker hides the subtitle (idle state).

func (e *Encoder) SetTopic(s string)
    SetTopic shows the topic title at the top of the video.

func (e *Encoder) ShowUserMessage(text, username string)
    ShowUserMessage flashes a chat/viewer message on the video for a few seconds
    without disturbing the active speaker subtitle. username is the viewer's
    handle and is rendered ahead of the message in the ticker's accent colour;
    pass "" to suppress the prefix.

type MovementKind string
    MovementKind enumerates supported camera moves. String values are stable
    across the codebase: planner JSON, render state, and smoke tests all use the
    same lower-case tokens.

const (
	MoveStall     MovementKind = "stall"
	MovePanLeft   MovementKind = "panleft"
	MovePanRight  MovementKind = "panright"
	MovePanTop    MovementKind = "pantop"
	MovePanBottom MovementKind = "panbottom"
	MoveZoomIn    MovementKind = "zoomin"
	MoveZoomOut   MovementKind = "zoomout"
)
type Options struct {
	Archival bool

	// BurnInSeriesCaptions controls whether the series-narration
	// renderer paints the spoken sentence onto the scene as
	// always-visible burned-in text. False (default) leaves the
	// imagery clean — soft-sub clients toggle the .vtt sidecar
	// instead. Has no effect on debate / puzzle modes, where the
	// caption slab is part of the chrome.
	BurnInSeriesCaptions bool
}
    Options configures encoder construction. Archival switches the HLS muxer
    from sliding-window (live) mode to retain-everything mode, so the playlist
    + segments survive long enough for an offline stitch pass to read them.
    Used by modeVideo (cmd/debate-bot --mode=video) where the entire show is
    post-processed into a downloadable mp4 — the live HLS sliding window would
    otherwise delete the earliest segments before stitch can reach them.

type PuzzleStage struct {
	// Has unexported fields.
}
    PuzzleStage drives the encoder for content of type "situation-puzzle" (海龜湯).
    Layout-wise it shares the Encoder/Renderer with DebateStage but remaps the
    panels: the puzzle host (出題者) sits alone on the left side, the players (解題者)
    on the right, and the soup-surface text (湯面) is placed in the left panel's
    footer slot so it stays visible the whole round.

    Type gating mirrors DebateStage: the stage only acts while the most recent
    TopicMsg.Type is situation-puzzle. Other content idles it. Two stages run
    per channel; whichever matches the active topic drives the encoder.

    Subtitle handling differs from debate in one respect: there is no
    affirmative/negative side, so the speaker pill doesn't try to colour-code
    by side — the puzzle host's role string ("puzzle-host") and the players'
    role string ("player") flow straight through to the renderer, and any future
    role-specific styling lives in render.go's roleColor.

func NewPuzzleChannelStage(enc *Encoder, channelID string) *PuzzleStage
    NewPuzzleChannelStage creates a PuzzleStage that only reacts to events
    whose ChannelID matches. The stage starts idle and activates on the first
    situation-puzzle TopicMsg.

func NewPuzzleStage(enc *Encoder) *PuzzleStage
    NewPuzzleStage creates a sequential-mode PuzzleStage (no channel filter).

func (s *PuzzleStage) AttachConclusion(imgs []*image.RGBA)
    AttachConclusion fills in the conclusion variant slice on a previously-
    attached PuzzleScenes. Called by cmd/debate-bot when the deferred
    conclusion-image generation finishes — the podcast can already be in
    flight at that point because surface assets unblock the run. If the stage
    is already in the conclusion phase, paints frame 0; subsequent frames are
    advanced by `<scene/>` markers in the host's conclusion narration (see
    advanceScene), so no timer rotation is started here.

func (s *PuzzleStage) AttachScenes(sc *scenes.PuzzleScenes)
    AttachScenes hands pre-generated scene images to the stage. Caller is
    cmd/debate-bot, which kicks off scene generation asynchronously when a
    puzzle topic is admitted and calls AttachScenes on completion. Safe to
    call before or after the topic activates — the active scene is applied
    immediately if the stage is currently active.

    Additive merge: each call writes only the non-nil entries of sc into the
    stage's accumulated PuzzleScenes. Per-index merge for the slice fields
    (Surface / Conclusion) so a streaming caller that already installed
    individual frames via AttachSurfaceFrame doesn't have those frames clobbered
    by a later wholesale attach with mostly-nil slots.

func (s *PuzzleStage) AttachSurfaceAnimations(anims []string)
    AttachSurfaceAnimations records the planner's per-surface-frame camera-move
    list. Caller (cmd/debate-bot) hands this to the stage alongside the scene
    plan so each surface beat's image plays with its planned pan / zoom move
    when the host emits the matching `<scene N/>` marker. Empty / nil disables
    the feature; the renderer holds the still image instead. Safe to call before
    or after AttachScenes — the next applyScene / applySceneAdvance picks up the
    new list.

func (s *PuzzleStage) AttachSurfaceFrame(variant int, img *image.RGBA)
    AttachSurfaceFrame installs a single surface variant produced by the
    streaming gen path. Used by cmd/debate-bot to hand frames to the stage as
    they finish so the show can start once the first N priority variants land
    without waiting for the slowest frames in the tail. Out-of-range indices
    grow the underlying slice (subsequent indices remain nil — ByNameIdx skips
    them, ByNameIdxExact returns nil so applySceneAdvance leaves the current
    background in place). No-op for a nil image.

func (s *PuzzleStage) Run(ctx context.Context, bus *eventbus.Bus)
    Run subscribes to bus and dispatches puzzle events to the encoder. Returns
    when ctx is cancelled or the bus closes.

type Renderer struct {
	// Has unexported fields.
}
    Renderer composites the live debate state into RGBA frames. Thread-safe;
    frame goroutine reads state, Stage updates state.

func NewRendererForTest(width, height int) (*Renderer, error)
    NewRendererForTest is an exported constructor for the render-smoke harness.
    Production code uses the unexported newRenderer via Encoder.

func (r *Renderer) AdvanceBodyForTest(d time.Duration)
    AdvanceBodyForTest backdates the active body's start time by d so the next
    Frame() captures the subtitle scrolled forward by that much. Test-only —
    used by cmd/render-smoke to capture an overflowing subtitle mid-scroll
    without having to call Frame() in real time.

func (r *Renderer) AdvanceSceneForTest(d time.Duration)
    AdvanceSceneForTest backdates the current scene crossfade by d so Frame()
    captures the settled end-state instead of mid-fade. Test-only.

func (r *Renderer) AdvanceSpeakerForTest(d time.Duration)
    AdvanceSpeakerForTest backdates speakerStartedAt by d so the next Frame()
    sees the speaker as having been on screen for that much longer. Lets the
    smoke harness step the cinematic lower-third fade-out frame by frame without
    waiting real-time. Test-only.

func (r *Renderer) AdvanceStageForTest(d time.Duration)
    AdvanceStageForTest backdates the current stage transition's start by
    d (typically more than stageTransitionDuration) so Frame() captures the
    settled end-state rather than the moment the transition began. Test-only.

func (r *Renderer) AdvanceUserMessageForTest(d time.Duration)
    AdvanceUserMessageForTest forces the pending batch to flush immediately (so
    smoke tests don't have to wait for the debounce window) and then backdates
    the active head's start by d so the next Frame() captures it partway through
    its scroll. Test-only — used by cmd/render-smoke to produce a representative
    still.

func (r *Renderer) Frame() []byte
    Frame renders one RGBA frame. The lock-and-snapshot here is the only
    per-frame mutation point; once the snapshot is taken, dispatch to either
    frameDebate (CNN-style debate layout, in debate_renderer.go) or framePuzzle
    (HBO-style puzzle layout, in situation_puzzle_renderer.go).

    Debate layout (1920×1080):

        ┌────────────────────────────────────────────────────────┐
        │                  [Topic title]                         │  title (y≈70)
        │                   [phase pill]                         │  phase (y≈110)
        │ ┌───────────┐  ┌────────────────────┐  ┌───────────┐   │
        │ │  正方     │  │  [AFFIRMATIVE — X] │  │   反方    │   │  panels + subtitle
        │ │ AFFIRM.   │  │                    │  │  NEGATIVE │   │
        │ │ • Alice ● │  │  spoken text …     │  │ • Linda   │   │
        │ │ • Carol   │  │                    │  │ • Bob     │   │
        │ └───────────┘  └────────────────────┘  └───────────┘   │
        │                                                        │
        │                  [02:14 / 30:00]                       │  clock (y≈660)
        └────────────────────────────────────────────────────────┘

func (r *Renderer) SetBurnInSeriesCaptions(b bool)
    SetBurnInSeriesCaptions toggles whether series-narration frames paint the
    active sentence on the scene. Called once by the encoder at construction;
    not safe to flip mid-stream (the renderer doesn't fade across the boundary).

func (r *Renderer) SetClock(elapsed, total time.Duration)
    SetClock updates the elapsed / total wall-clock display in the top-right
    corner. Pass zero for total to hide the "/ MM:SS" half (still shows
    elapsed).

func (r *Renderer) SetPhase(s string)
    SetPhase updates the phase status line.

func (r *Renderer) SetPositions(aff, neg string)
    SetPositions sets each side's position statement, drawn as small footer text
    inside the matching side panel so viewers can see what each side is arguing
    for. Empty strings hide the footer.

func (r *Renderer) SetPuzzleMode(b bool)
    SetPuzzleMode toggles the cinematic puzzle layout. When true, Frame()
    composes minimal chrome (title at top, subtitle anchored at the bottom
    over the scene bg) instead of the CNN-style debate layout. When false,
    the renderer behaves exactly as before. Resets scene bg state on the off
    transition so a subsequent debate topic doesn't inherit a stale scene image.

func (r *Renderer) SetPuzzleSceneName(name string)
    SetPuzzleSceneName records the active puzzle scene (one of the scenes.Scene*
    names) so framePuzzle can apply a scene-specific subtitle treatment.
    Idempotent.

func (r *Renderer) SetSceneAnimation(kind string)
    SetSceneAnimation sets the camera move applied to the current scene
    background as it plays. Pass "" or "stall" for no motion. Safe to call
    before or after SetSceneBackground; the animation clock is anchored to
    the moment of this call so the trajectory always begins at t=0 from the
    caller's perspective. Stale values from a previous scene are blown away by
    SetSceneBackground (which resets the move to stall before this is called
    again).

func (r *Renderer) SetSceneBackground(img *image.RGBA)
    SetSceneBackground swaps in a new scene background, retaining the previous
    one so drawBackground can crossfade between them. Pass nil to clear
    (renderer falls back to bgPlate / procedural bg). Idempotent: a repeat call
    with the same pointer is a no-op so PhaseMsg storms don't re-trigger the
    fade.

    Two cases for the prev layer:

     1. No fade in flight (sceneFadeFrac == 1 at swap time): the live outgoing
        source is moved into prevSceneBg verbatim along with its CameraMovement
        and start time. During the new crossfade both layers keep animating on
        their own clocks — that's the cinematic "two cams in motion through the
        dissolve" look.

     2. Fade still in flight (back-to-back swap): rendering the outgoing source
        with its still-running move would visibly leap when the fresh fade
        starts (the in-progress composite snaps back to "outgoing fully painted
        at α=1"). To avoid that, snapshot the current composite into a fresh
        RGBA and swap that in as prevSceneBg with MoveStall — so it holds its
        current frame for the duration of the new fade.

    Resets the per-scene move to MoveStall — caller chains a SetSceneAnimation
    right after when a non-stall move is wanted.

func (r *Renderer) SetSeriesLabel(show string, season, episode int, host string)
    SetSeriesLabel records the series identification label painted top-left
    during narration mode. Three rows: show name, season/episode, host name.
    Repeated calls with identical values are no-ops so a redundant TopicMsg
    doesn't restart the fade. Setting an empty show clears the label.

    The fade clock is NOT started here — the label holds at full opacity
    until the show actually starts (first non-empty speaker arrives via
    SetState). Otherwise an episode whose TTS / image gen takes longer than
    seriesLabelTotalDuration to warm up would have the label disappear before
    the audience hears the first line.

func (r *Renderer) SetSeriesSectionLabel(text string, hold bool)
    SetSeriesSectionLabel installs the section banner painted under the series
    ID label. text == "" clears it. hold == true keeps the banner at full
    opacity for the lifetime of the section (used for the recap section, which
    ends when the next phase arrives). hold == false runs the standard fade-in
    / hold / fade-out against seriesSectionTotalDuration so the main-content
    banner clears itself 30 s in. Repeated calls with identical text/hold are
    no-ops so a PhaseMsg storm doesn't restart the fade.

func (r *Renderer) SetSides(aff, neg []string)
    SetSides loads the affirmative / negative speaker rosters into the side
    panels. Names render in the order given; the panel highlights whichever one
    matches the active speaker. Safe to call once at startup.

func (r *Renderer) SetState(speaker, role, side, body string, audioDuration time.Duration)
    SetState updates the active-speaker subtitle. Empty speaker clears it and
    transitions the renderer back to its idle layout (centered title only,
    other elements faded out). Non-empty speaker triggers the active layout.
    audioDuration is the synthesized-audio length of body (0 when unknown);
    drawSubtitle uses it to align scroll start with the audio.

func (r *Renderer) SetTopic(s string)
    SetTopic updates the topic title shown at the top of the frame.

func (r *Renderer) ShowUserMessage(text, username string, ttl time.Duration)
    ShowUserMessage queues a viewer/chat message for the scrolling ticker.
    Messages are debounced into batches: rapid-fire sends collect in userPending
    and only commit to the ticker queue once userMsgDebounceWindow of quiet
    elapses (driven lazily by Frame() — no goroutine needed). username is
    rendered ahead of text in the ticker's accent colour when every message
    in the resulting batch shares it; pass "" to omit. ttl is a floor — if the
    caller's ttl is shorter than the time the merged batch needs to scroll fully
    off the left edge, the renderer stretches it so the audience always sees the
    entire message pass through.

type Resolution string
    Resolution is the encoder's output resolution. The renderer composites at
    1920×1080 by default; ffmpeg scales to outputDims() when needed.

const (
	Resolution720p  Resolution = "720p"
	Resolution1080p Resolution = "1080p"
	Resolution4K    Resolution = "4k"
)
type SeriesStage struct {
	// Has unexported fields.
}
    SeriesStage drives the encoder for content of type "series". Layout-wise
    it shares the Encoder + Renderer with the existing PuzzleStage (the chrome
    look — host-on-left, no debate sides — works the same way for a narrated
    TV-series episode), but the scene model is simpler: there is only one phase,
    "narration", with a single rotating beat list. The host also emits
    cross-episode `<season-S-episode-E-image-N/>` markers, which arrive as
    ImageRefMsg events and resolve through an in-memory map of pre-loaded
    prior-episode PNGs.

    Type gating mirrors PuzzleStage: only acts while the most recent
    TopicMsg.Type is `series`. Other content idles it. Two stages run per
    channel today (debate + puzzle); SeriesStage adds a third. Whichever matches
    the active topic drives the encoder.

func NewSeriesChannelStage(enc *Encoder, channelID string) *SeriesStage
    NewSeriesChannelStage creates a SeriesStage that filters bus events by
    channelID. Idle until a series TopicMsg arrives; switches off again on the
    next non-series topic.

func (s *SeriesStage) AttachAnimations(anims []string)
    AttachAnimations records the planner's per-beat camera-move list. Empty /
    nil disables motion (renderer holds the still image).

func (s *SeriesStage) AttachImageRefs(refs map[string]*image.RGBA)
    AttachImageRefs installs the cross-episode resolver map. Each entry maps
    a canonical image-ref key (s<S>e<E>i<N>, see contentcreator.ImageRefKey)
    to an in-memory *image.RGBA pre-loaded from the prior episode's archive.
    Empty / nil disables image-reuse painting for this episode.

func (s *SeriesStage) AttachNarrationFrame(variant int, img *image.RGBA)
    AttachNarrationFrame installs a single narration variant produced by the
    streaming gen path. Mirrors PuzzleStage.AttachSurfaceFrame.

func (s *SeriesStage) AttachScenes(sc *scenes.PuzzleScenes)
    AttachScenes additively installs every non-nil entry in sc.Narration on
    the stage's narration bank. Caller invokes this after a streaming gen pass
    completes; per-frame attaches via AttachNarrationFrame already may have
    populated some slots.

func (s *SeriesStage) PostEpisodeIdle()
    PostEpisodeIdle parks the stage between two series episodes: caption / name
    plate cleared, scene image dropped, but puzzleMode + the "narration" scene
    name stay on so drawBackground keeps painting the series fallback plate (not
    the debate one). The channel runner calls this after orch.Run drains so the
    audience sees a clean intermission frame for the inter-episode pause window.
    Idempotent.

func (s *SeriesStage) Preactivate()
    Preactivate flips the renderer into series narration mode synchronously —
    separate from the bus-driven activate() that fires when TopicMsg arrives.
    The channel runner calls it BEFORE sending the topic so frames rendered
    during the gap between TopicMsg dispatch and bus delivery don't briefly show
    the debate-style "TODAY'S TOPIC" idle card. Idempotent.

func (s *SeriesStage) Run(ctx context.Context, bus *eventbus.Bus)
    Run subscribes to bus and dispatches series events to the encoder. Returns
    when ctx is cancelled or the bus closes.

type Stage interface {
	Run(ctx context.Context, bus *eventbus.Bus)
}
    Stage is the per-content-type video composer. One Stage is bound to one
    Encoder; it subscribes to the event bus and translates orchestrator events
    into Renderer state updates so the live show is baked into the video stream.

    Two implementations live side-by-side: DebateStage (debate format) and
    PuzzleStage (situation-puzzle / 海龜湯). Both can run concurrently against
    the same Encoder — each gates internally on TopicMsg.Type so only the stage
    matching the active content drives the encoder. When a TopicMsg flips the
    channel from debate → puzzle (or vice versa), the previously-active stage
    goes idle and the matching stage takes over.

type StitchOpts struct {
	SoftSubs       bool
	BurnSubs       bool // ignored; see doc comment
	SubtitlesPath  string
	Language       string
	SubtitleTracks []SubtitleTrack
	StartOffset    time.Duration
	AudioFadeOut   time.Duration
}
    StitchOpts configures how StitchMP4 invokes ffmpeg.

    SoftSubs muxes SubtitlesPath into the output as a `mov_text` subtitle track
    (toggleable in players that support soft subs). Compatible with stream copy.

    BurnSubs is no longer applied at stitch time — the renderer paints captions
    directly into the HLS frames when its own BurnInSeriesCaptions flag is set,
    so re-applying ffmpeg's subtitles filter would double up. The field is kept
    for ABI continuity but ignored. Set Encoder Options.BurnInSeriesCaptions
    instead.

    SubtitlesPath is the .vtt sidecar (only used when SoftSubs is set; required
    in that case).

    Language is the BCP-47 tag stamped on the soft-sub track metadata (default
    "und" when blank). Ignored unless SoftSubs.

    StartOffset trims the front of the stitched mp4 by this many wall-clock
    seconds, dropping the silent prep prefix that the encoder accumulates
    before the show actually starts speaking. Zero (default) keeps the full HLS
    timeline. Stitch rounds the offset down to the nearest HLS segment boundary
    so -c:v copy can seek without an out-of-keyframe freeze.

    AudioFadeOut, when positive, applies a fade to the final span of the
    stitched audio track. Video remains stream-copied, but audio is re-encoded
    because ffmpeg filters cannot run with -c:a copy.

type SubtitleTrack struct {
	Path     string
	Language string
	Default  bool
}
    SubtitleTrack is one WebVTT input to mux into the stitched MP4.

type Transition struct {
	// Kind selects the transition style. Today only "crossfade" is
	// implemented; "" defaults to crossfade. Future: "cut", "dissolve",
	// "dip-to-black".
	Kind string
}
    Transition composes two animated sources with a transition between them.
    Like CameraMovement it carries no clock — the caller drives progress per
    frame and the type itself is just a configured renderer.

func (t Transition) Render(
	dst *image.RGBA,
	srcA *image.RGBA, moveA CameraMovement, progressA float64,
	srcB *image.RGBA, moveB CameraMovement, progressB float64,
	p float64,
)
    Render draws a transition from srcA to srcB into dst at progress p ∈ [0,1].
    Both sources may carry their own camera movement and per-source progress so
    a crossfade between two animated images keeps both layers in motion (each on
    its own clock) instead of freezing one.

    Pass srcA = nil when the transition has no outgoing layer (first scene
    fade-in). Pass srcB = nil when there's no incoming layer (rare, but
    supported). When both are nil the call is a no-op.

    p is the (eased) transition progress; p=0 reads as srcA fully visible,
    p=1 reads as srcB fully visible.
```
