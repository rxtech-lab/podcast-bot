package video

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/video/assets"
)

// Renderer composites the live debate state into RGBA frames. Thread-safe;
// frame goroutine reads state, Stage updates state.
type Renderer struct {
	width, height int

	// Decoded broadcast plates from internal/video/assets. Any of these may be
	// nil when the embedded file is the 1×1 placeholder — the renderer treats
	// nil as "draw the procedural fallback in this slot".
	bgPlate         *image.RGBA
	seriesBgPlate   *image.RGBA // full-bleed series narration fallback (no per-beat scene set)
	headerPlate     *image.RGBA
	lowerThirdPlate *image.RGBA
	panelAffPlate   *image.RGBA
	panelNegPlate   *image.RGBA
	subtitleBgPlate *image.RGBA

	titleFace     font.Face // topic title at the top
	phaseFace     font.Face // phase pill under the title
	clockFace     font.Face // elapsed/total clock at the bottom
	tagFace       font.Face // speaker pill in the subtitle
	bodyFace      font.Face // spoken text in the subtitle
	panelHdrFace  font.Face // side-panel section header ("正方")
	panelNameFace font.Face // side-panel speaker name (idle)
	panelActFace  font.Face // side-panel speaker name (active)
	panelPosFace  font.Face // side-panel position footer (very small)

	mu       sync.RWMutex
	topic    string
	phase    string
	speaker  string
	role     string
	side     string
	body     string
	affNames []string
	negNames []string
	affPos   string
	negPos   string

	// bodyStartedAt is the wall-clock moment the current `body` value first
	// became active. Reset on every body change (including the empty body that
	// fires between speakers). drawSubtitle reads it to compute the vertical
	// scroll offset when the wrapped sentence overflows the visible card —
	// resetting on each new sentence means scrolling always begins from the
	// top of that sentence so viewers don't miss the opening words.
	bodyStartedAt time.Time

	// bodyAudioDuration is the wall-clock length of the synthesized audio for
	// the current body. Captured on each body change. drawSubtitle uses it to
	// delay scroll start until t/2 - 3s into the audio, so the scroll lands
	// in the second half of playback rather than at a fixed offset.
	bodyAudioDuration time.Duration

	// speakerStartedAt is the wall-clock moment the current speaker became
	// non-empty. Distinct from bodyStartedAt: that one resets on every
	// sentence; this one resets only when the speaker actually changes (or
	// becomes empty). framePuzzle uses it to fade the surface-scene lower-
	// third name plate after 30s — the audience sees who's narrating early
	// in the surface narration, then the chrome fades so the imagery has
	// the screen.
	speakerStartedAt time.Time

	// Wall-clock display fed by the pipeline's once-per-second TickMsg.
	clockElapsed time.Duration
	clockTotal   time.Duration

	// Chat ticker state machine. Viewer messages are debounced into batches
	// (so a burst of rapid sends becomes one ticker scroll instead of N
	// clobbered ones), then played back through a queue that drains in order
	// — if the queue keeps getting fed, the ticker keeps rolling.
	//
	// Layout: incoming ShowUserMessage calls land in userPending. After
	// userMsgDebounceWindow of quiet, advanceUserTickerLocked() commits the
	// pending batch as one queuedTicker into userQueue. userQueue[0] is the
	// currently-scrolling head; userStart/userExpiry are its on-screen window.
	// When the head expires the queue advances; the next head's window is
	// recomputed from its own scrollDur.
	userPending     []pendingUserMsg
	userPendingLast time.Time
	userQueue       []queuedTicker
	userStart       time.Time
	userExpiry      time.Time

	// Stage animation state. The renderer auto-switches to stageActive the
	// first time SetState is called with a non-empty speaker, and back to
	// stageIdle when the speaker becomes empty. Each transition records its
	// start time so Frame() can interpolate. The default zero value of
	// stageMode is stageIdle and stageModeStart=zeroTime puts elapsed far in
	// the past — so the first frame snaps to a fully-settled idle layout.
	stageMode      stageMode
	stageModeStart time.Time

	// Puzzle / scene-bg state. When puzzleMode is true the renderer swaps
	// out the news-broadcast chrome for a minimal cinematic layout (scene
	// bg + subtitle anchored at the bottom). sceneBg is the active scene
	// image; prevSceneBg is the previous scene retained for the duration
	// of one crossfade so the renderer can blend old → new. Setters live
	// in SetPuzzleMode / SetSceneBackground.
	//
	// puzzleSceneName is the active scene name (one of scenes.Scene*).
	// framePuzzle reads it to choose a per-scene subtitle treatment —
	// the surface phase paints the caption directly on the scene with a
	// black outline (no quote-card chrome) for an opening-credits feel,
	// while QA / reveal / conclusion keep the slab-and-rule layout.
	puzzleMode           bool
	puzzleSceneName      string
	sceneBg              *image.RGBA
	prevSceneBg          *image.RGBA
	sceneTransitionStart time.Time

	// sceneMove is the active per-scene camera move (Ken-Burns-style
	// pan / zoom). prev retains the outgoing scene's move across a
	// crossfade so drawBackground composes both layers with their own
	// trajectories. sceneMoveStart anchors the elapsed-time computation
	// for the current scene; prevSceneMoveStart for the outgoing one.
	// Reset together with sceneBg / prevSceneBg in SetSceneBackground.
	//
	// On a back-to-back swap (a new SetSceneBackground while the prior
	// fade is still running), the outgoing layer's move is replaced
	// with MoveStall and prevSceneBg is replaced with a one-time
	// composited snapshot — that freezes the outgoing motion at its
	// current frame so the in-flight crossfade doesn't visibly leap.
	sceneMove          CameraMovement
	prevSceneMove      CameraMovement
	sceneMoveStart     time.Time
	prevSceneMoveStart time.Time

	// Series identification label. Painted top-left in narration mode and
	// faded out shortly after seriesLabelStart so the imagery owns the
	// rest of the frame. seriesShow == "" disables the label entirely.
	// seriesHost is the narrator's name, painted as the third line of
	// the label so the audience reads who's narrating without needing a
	// separate lower-third name plate.
	seriesShow       string
	seriesSeason     int
	seriesEpisode    int
	seriesHost       string
	seriesLabelStart time.Time
}

// pendingUserMsg is one viewer message buffered during the debounce window.
// The Renderer collects these as ShowUserMessage is called, then merges them
// into a single queuedTicker once userMsgDebounceWindow elapses with no new
// arrivals.
type pendingUserMsg struct {
	username string
	text     string
	ttl      time.Duration
}

// queuedTicker is one committed ticker entry waiting (or actively scrolling)
// in the queue. Always pre-formatted: when every pending message in the batch
// shared a username, that username sits in the accent slot and `text` is the
// joined bodies; mixed-username batches embed the names inline in `text` and
// leave `username` empty.
type queuedTicker struct {
	username string
	text     string
	duration time.Duration
}

// userMsgDebounceWindow is the quiet period after the last ShowUserMessage
// before the pending batch flushes into the queue. Tuned to match the
// orchestrator's planner debounce (internal/debate/agenda.go) so the visual
// batching mirrors the AI-side batching.
const userMsgDebounceWindow = 1500 * time.Millisecond

// stageMode is a coarse layout selector with two values: idle (only the bg
// and a centered title) and active (the full debate layout). The renderer
// crossfades between them when the mode changes.
type stageMode int

const (
	stageIdle stageMode = iota
	stageActive
)

// stageTransitionDuration is how long a mode change takes to complete. Tuned
// to feel like a confident broadcast move — fast enough not to drag, slow
// enough to read as intentional.
const stageTransitionDuration = 600 * time.Millisecond

// sceneTransitionDuration is the crossfade window when SetSceneBackground
// swaps in a new puzzle scene image. Tuned long enough that a back-to-back
// crossfade (two scene swaps within the previous fade's window) doesn't
// read as a flash — the eye sees a continuous dissolve instead of an
// interrupted one.
const sceneTransitionDuration = 1500 * time.Millisecond

// NewRendererForTest is an exported constructor for the render-smoke harness.
// Production code uses the unexported newRenderer via Encoder.
func NewRendererForTest(width, height int) (*Renderer, error) {
	return newRenderer(width, height)
}

// newRenderer builds the font faces and returns a ready-to-render compositor.
//
// Font selection: we want CJK glyphs because topics are often in zh-CN. Order
// is: $DEBATE_BOT_FONT (TTF/TTC) → known platform CJK fonts → embedded Go
// fonts (Latin only — last resort). When a CJK font loads we use it for every
// face; CJK fonts ship Latin glyphs too, and using one source keeps spacing
// consistent.
func newRenderer(width, height int) (*Renderer, error) {
	srcBody, srcBold, err := loadFontSources()
	if err != nil {
		return nil, err
	}

	mk := func(src *sfnt.Font, size float64) (font.Face, error) {
		return opentype.NewFace(src, &opentype.FaceOptions{
			Size:    size,
			DPI:     72,
			Hinting: font.HintingFull,
		})
	}

	titleFace, err := mk(srcBold, 42)
	if err != nil {
		return nil, err
	}
	phaseFace, err := mk(srcBold, 18)
	if err != nil {
		return nil, err
	}
	clockFace, err := mk(srcBold, 22)
	if err != nil {
		return nil, err
	}
	tagFace, err := mk(srcBold, 28)
	if err != nil {
		return nil, err
	}
	bodyFace, err := mk(srcBody, 32)
	if err != nil {
		return nil, err
	}
	panelHdrFace, err := mk(srcBold, 22)
	if err != nil {
		return nil, err
	}
	panelNameFace, err := mk(srcBody, 22)
	if err != nil {
		return nil, err
	}
	panelActFace, err := mk(srcBold, 24)
	if err != nil {
		return nil, err
	}
	panelPosFace, err := mk(srcBody, 13)
	if err != nil {
		return nil, err
	}

	return &Renderer{
		width: width, height: height,
		bgPlate:         loadPlate("bg.png", width, height),
		// series_bg ships at 1920×1080 so it survives a 1080p ffmpeg
		// upscale crisply — 0,0 here disables the size guard, and
		// drawBackground rescales it down to the 1280×720 composite
		// with Catmull-Rom on each frame.
		seriesBgPlate: loadPlate("series_bg.png", 0, 0),
		headerPlate:     loadPlate("header_bar.png", 0, 0),
		lowerThirdPlate: loadPlate("lower_third.png", 0, 0),
		panelAffPlate:   loadPlate("panel_aff.png", 0, 0),
		panelNegPlate:   loadPlate("panel_neg.png", 0, 0),
		subtitleBgPlate: loadPlate("subtitle_bg.png", 0, 0),
		titleFace:       titleFace,
		phaseFace:       phaseFace,
		clockFace:       clockFace,
		tagFace:         tagFace,
		bodyFace:        bodyFace,
		panelHdrFace:    panelHdrFace,
		panelNameFace:   panelNameFace,
		panelActFace:    panelActFace,
		panelPosFace:    panelPosFace,
	}, nil
}

// drawScaledOver resamples src into dstRect with Catmull-Rom and blits with
// alpha-aware "Over" so transparent pixels stay transparent. Used to fit the
// flat-design plates into the procedural layout slots without regenerating
// the asset for every layout tweak.
func drawScaledOver(dst *image.RGBA, src *image.RGBA, dstRect image.Rectangle) {
	xdraw.CatmullRom.Scale(dst, dstRect, src, src.Bounds(), xdraw.Over, nil)
}

// loadPlate reads an embedded PNG and returns it as RGBA. Returns nil when
// the file is missing, fails to decode, or is the 1×1 placeholder we ship to
// satisfy go:embed before ./cmd/gen-assets has been run. expectedW/H are the
// minimum acceptable size; pass 0 to skip the size check.
func loadPlate(name string, expectedW, expectedH int) *image.RGBA {
	data, err := assets.FS.ReadFile(name)
	if err != nil {
		return nil
	}
	src, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	b := src.Bounds()
	if b.Dx() < 2 || b.Dy() < 2 {
		// 1×1 placeholder — treat as absent.
		return nil
	}
	if expectedW > 0 && b.Dx() != expectedW {
		return nil
	}
	if expectedH > 0 && b.Dy() != expectedH {
		return nil
	}
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), src, b.Min, draw.Src)
	return out
}

// loadFontSources returns (regular, bold) sfnt.Font sources. If we can find a
// system CJK font, both pointers refer to it (size differences carry the
// emphasis). Otherwise we fall back to the Latin-only embedded Go fonts.
func loadFontSources() (regular, bold *sfnt.Font, err error) {
	if path := os.Getenv("DEBATE_BOT_FONT"); path != "" {
		f, perr := loadFontFile(path)
		if perr == nil {
			return f, f, nil
		}
	}
	for _, p := range cjkFontCandidates() {
		f, perr := loadFontFile(p)
		if perr == nil {
			return f, f, nil
		}
	}
	reg, err := sfnt.Parse(goregular.TTF)
	if err != nil {
		return nil, nil, err
	}
	bd, err := sfnt.Parse(gobold.TTF)
	if err != nil {
		return nil, nil, err
	}
	return reg, bd, nil
}

func loadFontFile(path string) (*sfnt.Font, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if coll, cerr := sfnt.ParseCollection(data); cerr == nil {
		return coll.Font(0)
	}
	return sfnt.Parse(data)
}

func cjkFontCandidates() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{
			"/System/Library/Fonts/PingFang.ttc",
			"/System/Library/Fonts/Hiragino Sans GB.ttc",
			"/System/Library/Fonts/STHeiti Medium.ttc",
			"/System/Library/Fonts/STHeiti Light.ttc",
			"/System/Library/Fonts/Supplemental/Songti.ttc",
			"/Library/Fonts/Arial Unicode.ttf",
		}
	case "linux":
		return []string{
			"/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc",
			"/usr/share/fonts/opentype/noto/NotoSansCJK.ttc",
			"/usr/share/fonts/google-noto-cjk/NotoSansCJK-Regular.ttc",
			"/usr/share/fonts/truetype/wqy/wqy-microhei.ttc",
			"/usr/share/fonts/truetype/wqy/wqy-zenhei.ttc",
			"/usr/share/fonts/truetype/arphic/uming.ttc",
			"/usr/share/fonts/truetype/arphic/ukai.ttc",
		}
	case "windows":
		return []string{
			`C:\Windows\Fonts\msyh.ttc`,
			`C:\Windows\Fonts\msyh.ttf`,
			`C:\Windows\Fonts\simhei.ttf`,
			`C:\Windows\Fonts\simsun.ttc`,
		}
	}
	return nil
}

// SetTopic updates the topic title shown at the top of the frame.
func (r *Renderer) SetTopic(s string) {
	r.mu.Lock()
	r.topic = s
	r.mu.Unlock()
}

// SetPhase updates the phase status line.
func (r *Renderer) SetPhase(s string) {
	r.mu.Lock()
	r.phase = s
	r.mu.Unlock()
}

// SetClock updates the elapsed / total wall-clock display in the top-right
// corner. Pass zero for total to hide the "/ MM:SS" half (still shows
// elapsed).
func (r *Renderer) SetClock(elapsed, total time.Duration) {
	r.mu.Lock()
	r.clockElapsed = elapsed
	r.clockTotal = total
	r.mu.Unlock()
}

// SetState updates the active-speaker subtitle. Empty speaker clears it and
// transitions the renderer back to its idle layout (centered title only,
// other elements faded out). Non-empty speaker triggers the active layout.
// audioDuration is the synthesized-audio length of body (0 when unknown);
// drawSubtitle uses it to align scroll start with the audio.
func (r *Renderer) SetState(speaker, role, side, body string, audioDuration time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	prevSpeaker := r.speaker
	r.speaker = speaker
	r.role = role
	r.side = side
	if body != r.body {
		// New sentence (or speaker changeover that cleared the body) — restart
		// the scroll clock so a long passage begins at line 0 rather than
		// wherever the previous body had scrolled to.
		r.bodyStartedAt = time.Now()
		r.bodyAudioDuration = audioDuration
	}
	if speaker != prevSpeaker {
		// Speaker actually changed (not just a sentence-internal body
		// update). Restart the speaker-on-screen clock so the surface
		// lower-third's 30s name-plate fade starts from this turn's
		// beginning, not the previous speaker's.
		if speaker == "" {
			r.speakerStartedAt = time.Time{}
		} else {
			r.speakerStartedAt = time.Now()
		}
	}
	// Series identification label starts its 15s lifetime on the first
	// frame where the show actually begins speaking — not on
	// SetSeriesLabel. SetSeriesLabel is called from handleTopic which
	// runs before TTS / image gen finishes, so anchoring the fade to
	// "first speaker on screen" guarantees the audience sees the label
	// once the episode is actually playing.
	if speaker != "" && r.seriesShow != "" && r.seriesLabelStart.IsZero() {
		r.seriesLabelStart = time.Now()
	}
	r.body = body

	want := stageIdle
	if speaker != "" {
		want = stageActive
	}
	if want != r.stageMode {
		r.stageMode = want
		r.stageModeStart = time.Now()
	}
}

// SetPuzzleMode toggles the cinematic puzzle layout. When true, Frame()
// composes minimal chrome (title at top, subtitle anchored at the bottom
// over the scene bg) instead of the CNN-style debate layout. When false,
// the renderer behaves exactly as before. Resets scene bg state on the
// off transition so a subsequent debate topic doesn't inherit a stale
// scene image.
func (r *Renderer) SetPuzzleMode(b bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.puzzleMode == b {
		return
	}
	r.puzzleMode = b
	if !b {
		r.sceneBg = nil
		r.prevSceneBg = nil
		r.puzzleSceneName = ""
		r.sceneTransitionStart = time.Time{}
	}
}

// SetPuzzleSceneName records the active puzzle scene (one of the
// scenes.Scene* names) so framePuzzle can apply a scene-specific
// subtitle treatment. Idempotent.
func (r *Renderer) SetPuzzleSceneName(name string) {
	r.mu.Lock()
	r.puzzleSceneName = name
	r.mu.Unlock()
}

// SetSeriesLabel records the series identification label painted top-left
// during narration mode. Three rows: show name, season/episode, host
// name. Repeated calls with identical values are no-ops so a redundant
// TopicMsg doesn't restart the fade. Setting an empty show clears the
// label.
//
// The fade clock is NOT started here — the label holds at full opacity
// until the show actually starts (first non-empty speaker arrives via
// SetState). Otherwise an episode whose TTS / image gen takes longer
// than seriesLabelTotalDuration to warm up would have the label
// disappear before the audience hears the first line.
func (r *Renderer) SetSeriesLabel(show string, season, episode int, host string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seriesShow == show && r.seriesSeason == season &&
		r.seriesEpisode == episode && r.seriesHost == host {
		return
	}
	r.seriesShow = show
	r.seriesSeason = season
	r.seriesEpisode = episode
	r.seriesHost = host
	r.seriesLabelStart = time.Time{}
}

// SetSceneBackground swaps in a new scene background, retaining the
// previous one so drawBackground can crossfade between them. Pass nil to
// clear (renderer falls back to bgPlate / procedural bg). Idempotent: a
// repeat call with the same pointer is a no-op so PhaseMsg storms don't
// re-trigger the fade.
//
// Two cases for the prev layer:
//
//  1. No fade in flight (sceneFadeFrac == 1 at swap time): the live
//     outgoing source is moved into prevSceneBg verbatim along with its
//     CameraMovement and start time. During the new crossfade both
//     layers keep animating on their own clocks — that's the cinematic
//     "two cams in motion through the dissolve" look.
//
//  2. Fade still in flight (back-to-back swap): rendering the outgoing
//     source with its still-running move would visibly leap when the
//     fresh fade starts (the in-progress composite snaps back to
//     "outgoing fully painted at α=1"). To avoid that, snapshot the
//     current composite into a fresh RGBA and swap that in as
//     prevSceneBg with MoveStall — so it holds its current frame for
//     the duration of the new fade.
//
// Resets the per-scene move to MoveStall — caller chains a
// SetSceneAnimation right after when a non-stall move is wanted.
func (r *Renderer) SetSceneBackground(img *image.RGBA) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sceneBg == img {
		return
	}
	now := time.Now()
	fadeFrac := sceneFadeFrac(r.sceneTransitionStart)
	if fadeFrac < 1 && (r.sceneBg != nil || r.prevSceneBg != nil) {
		// Back-to-back: bake the in-progress composite into a still and
		// stall it for the new fade.
		bounds := image.Rect(0, 0, r.width, r.height)
		snap := image.NewRGBA(bounds)
		Transition{Kind: "crossfade"}.Render(snap,
			r.prevSceneBg, r.prevSceneMove, r.moveProgressLocked(r.prevSceneMoveStart),
			r.sceneBg, r.sceneMove, r.moveProgressLocked(r.sceneMoveStart),
			fadeFrac)
		r.prevSceneBg = snap
		r.prevSceneMove = CameraMovement{Kind: MoveStall}
		r.prevSceneMoveStart = now
	} else {
		r.prevSceneBg = r.sceneBg
		r.prevSceneMove = r.sceneMove
		r.prevSceneMoveStart = r.sceneMoveStart
	}
	r.sceneBg = img
	r.sceneTransitionStart = now
	r.sceneMove = CameraMovement{Kind: MoveStall}
	r.sceneMoveStart = now
}

// SetSceneAnimation sets the camera move applied to the current scene
// background as it plays. Pass "" or "stall" for no motion. Safe to
// call before or after SetSceneBackground; the animation clock is
// anchored to the moment of this call so the trajectory always begins
// at t=0 from the caller's perspective. Stale values from a previous
// scene are blown away by SetSceneBackground (which resets the move
// to stall before this is called again).
func (r *Renderer) SetSceneAnimation(kind string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sceneMove = CameraMovement{Kind: parseMovementKind(kind)}
	r.sceneMoveStart = time.Now()
}

// parseMovementKind normalises a free-form animation token into one of
// the supported MovementKind values. Unknown / empty values map to
// MoveStall so the renderer holds the still image instead of crashing
// or freezing.
func parseMovementKind(s string) MovementKind {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case string(MovePanLeft):
		return MovePanLeft
	case string(MovePanRight):
		return MovePanRight
	case string(MovePanTop):
		return MovePanTop
	case string(MovePanBottom):
		return MovePanBottom
	case string(MoveZoomIn):
		return MoveZoomIn
	case string(MoveZoomOut):
		return MoveZoomOut
	default:
		return MoveStall
	}
}

// moveProgressLocked maps a per-layer move start time into eased 0..1
// progress. Identical curve to sceneFadeFrac so transitions and camera
// moves share the same feel. Returns 1 (held end-frame) when start is
// the zero value — for layers that never received a move kind.
//
// Caller must hold r.mu.
func (r *Renderer) moveProgressLocked(start time.Time) float64 {
	if start.IsZero() {
		return 1
	}
	t := time.Since(start).Seconds() / sceneAnimDuration.Seconds()
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	return easeInOutCubic(t)
}

// SetSides loads the affirmative / negative speaker rosters into the side
// panels. Names render in the order given; the panel highlights whichever one
// matches the active speaker. Safe to call once at startup.
func (r *Renderer) SetSides(aff, neg []string) {
	r.mu.Lock()
	r.affNames = append(r.affNames[:0], aff...)
	r.negNames = append(r.negNames[:0], neg...)
	r.mu.Unlock()
}

// SetPositions sets each side's position statement, drawn as small footer
// text inside the matching side panel so viewers can see what each side is
// arguing for. Empty strings hide the footer.
func (r *Renderer) SetPositions(aff, neg string) {
	r.mu.Lock()
	r.affPos = aff
	r.negPos = neg
	r.mu.Unlock()
}

// AdvanceUserMessageForTest forces the pending batch to flush immediately
// (so smoke tests don't have to wait for the debounce window) and then
// backdates the active head's start by d so the next Frame() captures it
// partway through its scroll. Test-only — used by cmd/render-smoke to
// produce a representative still.
func (r *Renderer) AdvanceUserMessageForTest(d time.Duration) {
	r.mu.Lock()
	r.flushPendingUserLocked()
	r.userStart = r.userStart.Add(-d)
	r.mu.Unlock()
}

// AdvanceStageForTest backdates the current stage transition's start by d
// (typically more than stageTransitionDuration) so Frame() captures the
// settled end-state rather than the moment the transition began. Test-only.
func (r *Renderer) AdvanceStageForTest(d time.Duration) {
	r.mu.Lock()
	r.stageModeStart = r.stageModeStart.Add(-d)
	r.mu.Unlock()
}

// AdvanceBodyForTest backdates the active body's start time by d so the next
// Frame() captures the subtitle scrolled forward by that much. Test-only —
// used by cmd/render-smoke to capture an overflowing subtitle mid-scroll
// without having to call Frame() in real time.
func (r *Renderer) AdvanceBodyForTest(d time.Duration) {
	r.mu.Lock()
	if !r.bodyStartedAt.IsZero() {
		r.bodyStartedAt = r.bodyStartedAt.Add(-d)
	}
	r.mu.Unlock()
}

// AdvanceSceneForTest backdates the current scene crossfade by d so Frame()
// captures the settled end-state instead of mid-fade. Test-only.
func (r *Renderer) AdvanceSceneForTest(d time.Duration) {
	r.mu.Lock()
	if !r.sceneTransitionStart.IsZero() {
		r.sceneTransitionStart = r.sceneTransitionStart.Add(-d)
	}
	r.mu.Unlock()
}

// AdvanceSpeakerForTest backdates speakerStartedAt by d so the next Frame()
// sees the speaker as having been on screen for that much longer. Lets
// the smoke harness step the cinematic lower-third fade-out frame by
// frame without waiting real-time. Test-only.
func (r *Renderer) AdvanceSpeakerForTest(d time.Duration) {
	r.mu.Lock()
	if !r.speakerStartedAt.IsZero() {
		r.speakerStartedAt = r.speakerStartedAt.Add(-d)
	}
	r.mu.Unlock()
}

// ShowUserMessage queues a viewer/chat message for the scrolling ticker.
// Messages are debounced into batches: rapid-fire sends collect in
// userPending and only commit to the ticker queue once userMsgDebounceWindow
// of quiet elapses (driven lazily by Frame() — no goroutine needed). username
// is rendered ahead of text in the ticker's accent colour when every message
// in the resulting batch shares it; pass "" to omit. ttl is a floor — if the
// caller's ttl is shorter than the time the merged batch needs to scroll
// fully off the left edge, the renderer stretches it so the audience always
// sees the entire message pass through.
func (r *Renderer) ShowUserMessage(text, username string, ttl time.Duration) {
	if text == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.userPending = append(r.userPending, pendingUserMsg{username: username, text: text, ttl: ttl})
	r.userPendingLast = time.Now()
}

// flushPendingUserLocked merges userPending into one queuedTicker and pushes
// it onto userQueue. Caller must hold r.mu.
func (r *Renderer) flushPendingUserLocked() {
	if len(r.userPending) == 0 {
		return
	}
	// Same-username batches keep that name in the accent slot and join just
	// the bodies; mixed-username batches drop the accent slot and embed each
	// "name: text" inline so every viewer is still attributed.
	allSame := true
	firstUser := r.userPending[0].username
	for i := 1; i < len(r.userPending); i++ {
		if r.userPending[i].username != firstUser {
			allSame = false
			break
		}
	}
	var item queuedTicker
	if allSame {
		bodies := make([]string, len(r.userPending))
		for i, p := range r.userPending {
			bodies[i] = p.text
		}
		item.username = firstUser
		item.text = strings.Join(bodies, " — ")
	} else {
		parts := make([]string, len(r.userPending))
		for i, p := range r.userPending {
			parts[i] = userTickerText(p.username, p.text)
		}
		item.text = strings.Join(parts, "  ·  ")
	}
	var maxTTL time.Duration
	for _, p := range r.userPending {
		if p.ttl > maxTTL {
			maxTTL = p.ttl
		}
	}
	combined := userTickerText(item.username, item.text)
	textW := (&font.Drawer{Face: r.bodyFace}).MeasureString(combined).Ceil()
	scrollDur := time.Duration(float64(r.width+textW)/tickerSpeedPxPerSec*float64(time.Second)) + 500*time.Millisecond
	if maxTTL > scrollDur {
		scrollDur = maxTTL
	}
	item.duration = scrollDur

	wasIdle := len(r.userQueue) == 0
	r.userQueue = append(r.userQueue, item)
	r.userPending = r.userPending[:0]
	if wasIdle {
		now := time.Now()
		r.userStart = now
		r.userExpiry = now.Add(item.duration)
	}
}

// advanceSceneFadeLocked retires the prev-scene layer once the active
// crossfade has completed. Without this, drawBackground keeps treating
// the renderer as "two layers, one fully opaque on top" and pays for an
// unnecessary CameraMovement.Render of the prev source plus a per-frame
// temp allocation in Transition.Render — visible as a 30 fps frame loop
// that misses its tick budget under CPU pressure (audio plays first
// because ffmpeg can't seal video segments at the manifest cadence).
//
// Caller must hold r.mu.
func (r *Renderer) advanceSceneFadeLocked() {
	if r.prevSceneBg == nil {
		return
	}
	if sceneFadeFrac(r.sceneTransitionStart) < 1 {
		return
	}
	r.prevSceneBg = nil
	r.prevSceneMove = CameraMovement{}
	r.prevSceneMoveStart = time.Time{}
}

// advanceUserTickerLocked drives the ticker state machine forward by one
// frame: flush the pending batch if the debounce window has elapsed, then
// pop expired heads off the queue (advancing to the next entry, if any, so
// the ticker keeps rolling as long as the queue keeps filling). Caller must
// hold r.mu.
func (r *Renderer) advanceUserTickerLocked() {
	now := time.Now()
	if len(r.userPending) > 0 && now.Sub(r.userPendingLast) >= userMsgDebounceWindow {
		r.flushPendingUserLocked()
	}
	for len(r.userQueue) > 0 && now.After(r.userExpiry) {
		r.userQueue = r.userQueue[1:]
		if len(r.userQueue) > 0 {
			r.userStart = now
			r.userExpiry = now.Add(r.userQueue[0].duration)
		}
	}
}

// userTickerText is the exact glyph sequence drawChatTicker will lay out for
// a (username, message) pair. Centralised so width measurements always match
// the rendered string.
func userTickerText(username, msg string) string {
	if username == "" {
		return msg
	}
	if msg == "" {
		return username
	}
	return username + ": " + msg
}

// Frame renders one RGBA frame. The lock-and-snapshot here is the only
// per-frame mutation point; once the snapshot is taken, dispatch to either
// frameDebate (CNN-style debate layout, in debate_renderer.go) or
// framePuzzle (HBO-style puzzle layout, in situation_puzzle_renderer.go).
//
// Debate layout (1280×720):
//
//	┌────────────────────────────────────────────────────────┐
//	│                  [Topic title]                         │  title (y≈70)
//	│                   [phase pill]                         │  phase (y≈110)
//	│ ┌───────────┐  ┌────────────────────┐  ┌───────────┐   │
//	│ │  正方     │  │  [AFFIRMATIVE — X] │  │   反方    │   │  panels + subtitle
//	│ │ AFFIRM.   │  │                    │  │  NEGATIVE │   │
//	│ │ • Alice ● │  │  spoken text …     │  │ • Linda   │   │
//	│ │ • Carol   │  │                    │  │ • Bob     │   │
//	│ └───────────┘  └────────────────────┘  └───────────┘   │
//	│                                                        │
//	│                  [02:14 / 30:00]                       │  clock (y≈660)
//	└────────────────────────────────────────────────────────┘
func (r *Renderer) Frame() []byte {
	// Lock (not RLock) because advanceUserTickerLocked may flush the pending
	// batch and pop expired ticker heads. Ticker bookkeeping is the only
	// per-frame mutation, so the brief exclusive section is acceptable.
	r.mu.Lock()
	r.advanceUserTickerLocked()
	r.advanceSceneFadeLocked()
	topic, phase := r.topic, r.phase
	speaker, role, body := r.speaker, r.role, r.body
	clockE, clockT := r.clockElapsed, r.clockTotal
	affNames := append([]string(nil), r.affNames...)
	negNames := append([]string(nil), r.negNames...)
	affPos, negPos := r.affPos, r.negPos
	var userName, userMsg string
	var userStart, userExpiry time.Time
	if len(r.userQueue) > 0 {
		head := r.userQueue[0]
		userName, userMsg = head.username, head.text
		userStart, userExpiry = r.userStart, r.userExpiry
	}
	mode := r.stageMode
	modeStart := r.stageModeStart
	bodyStart := r.bodyStartedAt
	bodyDur := r.bodyAudioDuration
	speakerStart := r.speakerStartedAt
	puzzleMode := r.puzzleMode
	puzzleScene := r.puzzleSceneName
	seriesShow := r.seriesShow
	seriesSeason := r.seriesSeason
	seriesEpisode := r.seriesEpisode
	seriesHost := r.seriesHost
	seriesLabelStart := r.seriesLabelStart
	r.mu.Unlock()

	if puzzleMode {
		// Series narration runs its own renderer (full-bleed scene, no
		// letterbox, no caption slab) so the puzzle chrome stays
		// untouched. Detected via the dedicated narration scene name —
		// SeriesStage is the only producer of that name.
		if puzzleScene == "narration" {
			return r.frameSeries(speaker, role, body,
				bodyStart, bodyDur,
				clockE, clockT,
				userName, userMsg, userStart, userExpiry,
				seriesShow, seriesSeason, seriesEpisode, seriesHost,
				seriesLabelStart)
		}
		return r.framePuzzle(topic, phase, puzzleScene, speaker, role, body,
			affPos, /* surface text shown in idle subtitle */
			bodyStart, bodyDur, speakerStart,
			clockE, clockT,
			userName, userMsg, userStart, userExpiry)
	}
	return r.frameDebate(topic, phase, speaker, role, body,
		bodyStart, bodyDur,
		affNames, negNames, affPos, negPos,
		clockE, clockT,
		userName, userMsg, userStart, userExpiry,
		mode, modeStart)
}

// drawBackground paints the bg layer that's visible in every frame regardless
// of stage mode. When a scene bg is set (puzzle mode) it delegates to the
// shared Transition primitive: prev scene at its own move/progress crossfaded
// into the current scene at its own move/progress over sceneTransitionDuration.
// When no scene bg is set, falls back to the static plate or procedural
// gradient.
func (r *Renderer) drawBackground(img *image.RGBA) {
	// Series narration mode prefers its own painterly bg plate when no
	// per-beat scene image is in play (gap between episodes, warmup
	// before the first scene PNG arrives). The debate bg looks like a
	// CNN newsroom and reads wrong on a narrated drama.
	fallback := r.bgPlate
	if r.puzzleSceneName == "narration" && r.seriesBgPlate != nil {
		fallback = r.seriesBgPlate
	}
	if r.sceneBg == nil && r.prevSceneBg == nil {
		if fallback != nil {
			drawScaledOver(img, fallback, img.Bounds())
			return
		}
		drawGradientBackground(img,
			color.RGBA{0x12, 0x14, 0x1f, 0xff},
			color.RGBA{0x07, 0x08, 0x0e, 0xff},
		)
		return
	}
	// Paint the static fallback first so a half-faded incoming scene with
	// no outgoing layer still renders against something other than zeroed
	// pixels at the edges (when the camera move shrinks the viewport
	// inside the dst, the surrounding area would otherwise be transparent).
	if r.prevSceneBg == nil {
		if fallback != nil {
			drawScaledOver(img, fallback, img.Bounds())
		} else {
			drawGradientBackground(img,
				color.RGBA{0x12, 0x14, 0x1f, 0xff},
				color.RGBA{0x07, 0x08, 0x0e, 0xff},
			)
		}
	}
	prevP := r.moveProgressLocked(r.prevSceneMoveStart)
	curP := r.moveProgressLocked(r.sceneMoveStart)
	alpha := sceneFadeFrac(r.sceneTransitionStart)
	Transition{Kind: "crossfade"}.Render(img,
		r.prevSceneBg, r.prevSceneMove, prevP,
		r.sceneBg, r.sceneMove, curP,
		alpha)
}

// sceneAnimDuration is how long each per-scene camera move takes to
// complete its trajectory. Beyond this window the source rect stays
// at its end position (still image) until the next SceneAdvance swaps
// in a fresh scene with a fresh trajectory. 12 s matches the typical
// per-beat narration length in a 海龜湯 surface story; shorter beats
// catch only the opening of the move (pleasant) while longer beats
// see a slower drift toward the still endpoint.
const sceneAnimDuration = 12 * time.Second

// sceneFadeFrac maps the time since SetSceneBackground was called into a
// 0..1 fraction with cubic ease in/out. Identical curve to stageActiveFrac
// so the two transitions have a unified feel.
func sceneFadeFrac(start time.Time) float64 {
	if start.IsZero() {
		return 1
	}
	t := time.Since(start).Seconds() / sceneTransitionDuration.Seconds()
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	return easeInOutCubic(t)
}

// easeInOutCubic is the standard smoothstep curve scaled to [0,1] → [0,1].
// Acceleration into the middle, deceleration out — reads as a confident move.
func easeInOutCubic(t float64) float64 {
	if t < 0.5 {
		return 4 * t * t * t
	}
	v := 2*t - 2
	return 1 + v*v*v/2
}

// lerpInt is a linear interpolation between two ints by frac in [0,1].
func lerpInt(a, b int, frac float64) int {
	return a + int(float64(b-a)*frac+0.5)
}

// blitWithGlobalAlphaAt is blitWithGlobalAlpha shifted by (offX, offY) — for
// compositing a small per-element buffer onto the frame at a known anchor
// without resizing it.
func blitWithGlobalAlphaAt(dst, src *image.RGBA, offX, offY int, alpha float64) {
	if alpha <= 0 {
		return
	}
	if alpha > 1 {
		alpha = 1
	}
	sb := src.Bounds()
	for sy := sb.Min.Y; sy < sb.Max.Y; sy++ {
		dy := sy + offY
		if dy < 0 || dy >= dst.Bounds().Max.Y {
			continue
		}
		for sx := sb.Min.X; sx < sb.Max.X; sx++ {
			dx := sx + offX
			if dx < 0 || dx >= dst.Bounds().Max.X {
				continue
			}
			si := src.PixOffset(sx, sy)
			di := dst.PixOffset(dx, dy)
			sa := float64(src.Pix[si+3]) * alpha / 255.0
			if sa <= 0 {
				continue
			}
			oneMinusA := 1 - sa
			dst.Pix[di] = uint8(float64(src.Pix[si])*alpha + float64(dst.Pix[di])*oneMinusA)
			dst.Pix[di+1] = uint8(float64(src.Pix[si+1])*alpha + float64(dst.Pix[di+1])*oneMinusA)
			dst.Pix[di+2] = uint8(float64(src.Pix[si+2])*alpha + float64(dst.Pix[di+2])*oneMinusA)
			dst.Pix[di+3] = 0xff
		}
	}
}

// blitWithGlobalAlpha composites src onto dst using src's per-pixel alpha
// multiplied by the supplied global alpha. dst stays opaque (RGB channels
// updated, alpha forced to 0xff). Both images must be the same size.
func blitWithGlobalAlpha(dst, src *image.RGBA, alpha float64) {
	if alpha <= 0 {
		return
	}
	if alpha > 1 {
		alpha = 1
	}
	for i := 0; i < len(src.Pix); i += 4 {
		sa := float64(src.Pix[i+3]) * alpha / 255.0
		if sa <= 0 {
			continue
		}
		oneMinusA := 1 - sa
		dst.Pix[i] = uint8(float64(src.Pix[i])*alpha + float64(dst.Pix[i])*oneMinusA)
		dst.Pix[i+1] = uint8(float64(src.Pix[i+1])*alpha + float64(dst.Pix[i+1])*oneMinusA)
		dst.Pix[i+2] = uint8(float64(src.Pix[i+2])*alpha + float64(dst.Pix[i+2])*oneMinusA)
		dst.Pix[i+3] = 0xff
	}
}

// tickerStripH is the height in pixels reserved at the bottom of the frame
// for the scrolling chat ticker. Sized to cover the lower-third plate.
const tickerStripH = 56

// tickerSpeedPxPerSec controls how fast the ticker text travels right-to-left.
// 110 px/s is roughly the cadence used by news-channel tickers — readable
// while still passing through in a reasonable amount of time on a 1280-wide
// frame even for short messages.
const tickerSpeedPxPerSec = 110

// drawChatTicker paints a horizontal scrolling banner at (x0, y0)-(x1, y1).
// Geometry: a translucent dark strip with a thin accent rail along the top,
// a short "FROM CHAT" pill anchored on the right (so it appears to lead the
// scrolling text from the right), and the message body translated leftward
// based on time elapsed since start. When username is non-empty it is drawn
// in the accent colour ahead of the message body so viewers can tell who
// sent each message; both glide together as one unit. The function returns
// immediately once the message has scrolled past the left edge, so the caller
// doesn't need to special-case completion.
func drawChatTicker(dst *image.RGBA,
	pillFace, bodyFace font.Face, username, msg string,
	x0, y0, x1, y1 int,
	start time.Time,
) {
	if msg == "" {
		return
	}
	stripW := x1 - x0

	bodyD := &font.Drawer{Face: bodyFace}
	namePrefix := ""
	if username != "" {
		namePrefix = username + ": "
	}
	nameW := bodyD.MeasureString(namePrefix).Ceil()
	bodyW := bodyD.MeasureString(msg).Ceil()
	textW := nameW + bodyW

	elapsed := time.Since(start).Seconds()
	if elapsed < 0 {
		elapsed = 0
	}
	// Start position: the right edge of the strip. textX is the left edge of
	// the message; it slides left as time advances. Stop drawing once the
	// right edge of the message has crossed the left edge of the strip.
	textX := x0 + stripW - int(elapsed*tickerSpeedPxPerSec)
	if textX+textW < x0 {
		return
	}

	// Background strip: translucent dark glass + thin amber accent line on top.
	accent := color.RGBA{0xfb, 0xbf, 0x24, 0xff}
	stripBG := color.RGBA{0x10, 0x12, 0x1a, 0xcc}
	textFG := color.RGBA{0xff, 0xfb, 0xeb, 0xff}

	stripRect := image.Rect(x0, y0, x1, y1)
	draw.Draw(dst, stripRect, &image.Uniform{stripBG}, image.Point{}, draw.Over)
	railRect := image.Rect(x0, y0, x1, y0+2)
	draw.Draw(dst, railRect, &image.Uniform{accent}, image.Point{}, draw.Src)

	// Center text vertically inside the strip.
	bodyM := bodyFace.Metrics()
	baseline := y0 + (y1-y0+bodyM.Ascent.Ceil()-bodyM.Descent.Ceil())/2

	// Username (accent) then body (foreground), drawn first so the pill on
	// the right edge composites on top of any glyphs scrolling underneath it.
	if namePrefix != "" {
		nd := &font.Drawer{Dst: dst, Src: image.NewUniform(accent), Face: bodyFace}
		nd.Dot = fixed.P(textX, baseline)
		nd.DrawString(namePrefix)
	}
	bd := &font.Drawer{Dst: dst, Src: image.NewUniform(textFG), Face: bodyFace}
	bd.Dot = fixed.P(textX+nameW, baseline)
	bd.DrawString(msg)

	pillText := "FROM CHAT"
	pillD := &font.Drawer{Face: pillFace}
	pillW := pillD.MeasureString(pillText).Ceil() + 32
	pillX0 := x1 - pillW - 16
	// Cover any text passing under the pill so the label stays legible.
	pillBoxBG := color.RGBA{0x10, 0x12, 0x1a, 0xff}
	pillCover := image.Rect(pillX0-10, y0+2, x1, y1)
	draw.Draw(dst, pillCover, &image.Uniform{pillBoxBG}, image.Point{}, draw.Src)
	pillM := pillFace.Metrics()
	pillCx := pillX0 + (pillW-32)/2 + 16
	pillCy := y0 + (y1-y0+pillM.Ascent.Ceil()-pillM.Descent.Ceil())/2
	drawCenteredPill(dst, pillFace, pillText, pillCx, pillCy,
		accent, color.RGBA{0x1a, 0x14, 0x06, 0xff})
}

// trimToWidth measures s and progressively drops trailing runes until it fits
// within maxWidth pixels under face. Used to keep ellipsised footer lines
// inside the panel.
func trimToWidth(face font.Face, s string, maxWidth int) string {
	d := &font.Drawer{Face: face}
	if d.MeasureString(s).Ceil() <= maxWidth {
		return s
	}
	runes := []rune(s)
	for len(runes) > 1 {
		runes = runes[:len(runes)-1]
		candidate := string(runes) + "…"
		if d.MeasureString(candidate).Ceil() <= maxWidth {
			return candidate
		}
	}
	return string(runes)
}

// drawGradientBackground paints a vertical gradient from top to bottom.
func drawGradientBackground(dst *image.RGBA, top, bot color.RGBA) {
	b := dst.Bounds()
	h := b.Dy()
	if h <= 1 {
		draw.Draw(dst, b, &image.Uniform{top}, image.Point{}, draw.Src)
		return
	}
	for y := 0; y < h; y++ {
		t := float64(y) / float64(h-1)
		c := color.RGBA{
			R: lerpByte(top.R, bot.R, t),
			G: lerpByte(top.G, bot.G, t),
			B: lerpByte(top.B, bot.B, t),
			A: 0xff,
		}
		line := image.Rect(b.Min.X, b.Min.Y+y, b.Max.X, b.Min.Y+y+1)
		draw.Draw(dst, line, &image.Uniform{c}, image.Point{}, draw.Src)
	}
}

func lerpByte(a, b uint8, t float64) uint8 {
	v := float64(a) + (float64(b)-float64(a))*t
	if v < 0 {
		v = 0
	}
	if v > 255 {
		v = 255
	}
	return uint8(v)
}

// withAlpha returns c with its alpha channel replaced. The RGB channels are
// premultiplied to match Go's image/color expectation.
func withAlpha(c color.RGBA, a uint8) color.RGBA {
	af := float64(a) / 255
	return color.RGBA{
		R: uint8(float64(c.R) * af),
		G: uint8(float64(c.G) * af),
		B: uint8(float64(c.B) * af),
		A: a,
	}
}

// fillCircle paints a filled circle of radius r centered at (cx, cy).
func fillCircle(dst *image.RGBA, cx, cy, r int, c color.RGBA) {
	if r <= 0 {
		return
	}
	for dy := -r; dy <= r; dy++ {
		dxMax := r*r - dy*dy
		if dxMax < 0 {
			continue
		}
		dx := isqrt(dxMax)
		line := image.Rect(cx-dx, cy+dy, cx+dx+1, cy+dy+1)
		draw.Draw(dst, line, &image.Uniform{c}, image.Point{}, draw.Src)
	}
}

func isqrt(n int) int {
	if n < 2 {
		return n
	}
	x := n
	y := (x + 1) / 2
	for y < x {
		x = y
		y = (x + n/x) / 2
	}
	return x
}

// drawClockPill paints "MM:SS / MM:SS" inside a soft floating chip centered
// on (cx, cy) — sits as a layer above the background. Elapsed renders in fg;
// the " / total" tail uses dimFG so the active counter pops.
func drawClockPill(dst *image.RGBA, face font.Face,
	elapsed, total time.Duration, cx, cy int, fg, dimFG color.RGBA,
) {
	main := formatMMSS(elapsed)
	tail := ""
	if total > 0 {
		tail = " / " + formatMMSS(total)
	}
	mainW := (&font.Drawer{Face: face}).MeasureString(main).Ceil()
	tailW := 0
	if tail != "" {
		tailW = (&font.Drawer{Face: face}).MeasureString(tail).Ceil()
	}
	totalW := mainW + tailW

	// Pill chrome: soft halo + filled chip + 1px stroke.
	const padX, padY = 18, 8
	metrics := face.Metrics()
	asc := metrics.Ascent.Ceil()
	desc := metrics.Descent.Ceil()
	chip := image.Rect(cx-totalW/2-padX, cy-asc-padY,
		cx+totalW/2+padX, cy+desc+padY)
	draw.Draw(dst, chip.Inset(-3),
		&image.Uniform{color.RGBA{0xff, 0xff, 0xff, 0x14}}, image.Point{}, draw.Over)
	draw.Draw(dst, chip,
		&image.Uniform{color.RGBA{0x1a, 0x1d, 0x28, 0xff}}, image.Point{}, draw.Src)
	drawRectOutline(dst, chip, 1, color.RGBA{0x2c, 0x30, 0x42, 0xff})

	startX := cx - totalW/2
	md := &font.Drawer{Dst: dst, Src: image.NewUniform(fg), Face: face}
	md.Dot = fixed.P(startX, cy)
	md.DrawString(main)
	if tail != "" {
		td := &font.Drawer{Dst: dst, Src: image.NewUniform(dimFG), Face: face}
		td.Dot = fixed.P(startX+mainW, cy)
		td.DrawString(tail)
	}
}

// formatTagPlain builds the speaker label drawn inside the colored pill.
func formatTagPlain(speaker, role string) string {
	switch agent.Role(role) {
	case agent.RoleHost:
		return "HOST"
	case agent.RoleAffirmative:
		return "AFFIRMATIVE — " + speaker
	case agent.RoleNegative:
		return "NEGATIVE — " + speaker
	case agent.RoleJudge:
		return "JUDGE"
	case agent.RoleViewer:
		return "VIEWER — " + speaker
	}
	if speaker == "" {
		return ""
	}
	return strings.ToUpper(speaker)
}

// phaseLabel converts the wire identifier into the human-readable Chinese label
// shown on the phase pill. Falls back to upper-cased Latin for unknown values
// so new phases still render something legible without a code change.
func phaseLabel(phase string) string {
	switch phase {
	case "opening":
		return "立論"
	case "free-debate":
		return "自由辯論"
	case "closing":
		return "結辯"
	case "verdict":
		return "判決"
	case "conclusion":
		return "總結"
	}
	return strings.ToUpper(phase)
}

// roleColor maps a debate role to its accent color. CNN-style palette: blue
// for the affirmative side, red for the negative side, with neutral picks for
// the host / judge / viewer / generic roles.
func roleColor(role string) color.RGBA {
	switch agent.Role(role) {
	case agent.RoleHost:
		return color.RGBA{0x1e, 0x29, 0x3b, 0xff} // dark slate so the white pill text stays readable
	case agent.RoleAffirmative:
		return color.RGBA{0x1a, 0x73, 0xe8, 0xff} // CNN blue
	case agent.RoleNegative:
		return color.RGBA{0xc8, 0x10, 0x16, 0xff} // CNN red
	case agent.RoleJudge:
		return color.RGBA{0xfb, 0xbf, 0x24, 0xff}
	case agent.RoleViewer:
		return color.RGBA{0xc0, 0x84, 0xfc, 0xff}
	}
	return color.RGBA{0x88, 0x8e, 0x9e, 0xff}
}

// formatMMSS turns a duration into "MM:SS" (or "HH:MM:SS" when ≥ 1 hour).
func formatMMSS(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Round(time.Second).Seconds())
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

// drawCenteredText draws text horizontally centered on cx, with baseline at y.
func drawCenteredText(dst *image.RGBA, face font.Face, text string, cx, y int, fg color.RGBA) {
	if text == "" {
		return
	}
	d := &font.Drawer{Dst: dst, Src: image.NewUniform(fg), Face: face}
	w := d.MeasureString(text).Ceil()
	d.Dot = fixed.P(cx-w/2, y)
	d.DrawString(text)
}

// drawCenteredPill paints a filled rectangle horizontally centered on cx,
// with text baseline at y, and the text drawn over it in fg.
func drawCenteredPill(dst *image.RGBA, face font.Face, text string, cx, y int, bg, fg color.RGBA) {
	if text == "" {
		return
	}
	d := &font.Drawer{Dst: dst, Src: image.NewUniform(fg), Face: face}
	w := d.MeasureString(text).Ceil()
	padX, padY := 24, 12
	metrics := face.Metrics()
	asc := metrics.Ascent.Ceil()
	desc := metrics.Descent.Ceil()
	rect := image.Rect(cx-w/2-padX, y-asc-padY, cx+w/2+padX, y+desc+padY)
	draw.Draw(dst, rect, &image.Uniform{bg}, image.Point{}, draw.Src)
	d.Dot = fixed.P(cx-w/2, y)
	d.DrawString(text)
}

// drawRectOutline strokes a rectangle outline of given width. The four bands
// are filled as solid rectangles — simple and avoids per-pixel work.
func drawRectOutline(dst *image.RGBA, r image.Rectangle, w int, c color.RGBA) {
	src := image.NewUniform(c)
	top := image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+w)
	bot := image.Rect(r.Min.X, r.Max.Y-w, r.Max.X, r.Max.Y)
	left := image.Rect(r.Min.X, r.Min.Y, r.Min.X+w, r.Max.Y)
	right := image.Rect(r.Max.X-w, r.Min.Y, r.Max.X, r.Max.Y)
	draw.Draw(dst, top, src, image.Point{}, draw.Src)
	draw.Draw(dst, bot, src, image.Point{}, draw.Src)
	draw.Draw(dst, left, src, image.Point{}, draw.Src)
	draw.Draw(dst, right, src, image.Point{}, draw.Src)
}

// wrapLines breaks text into lines that fit within maxWidth pixels.
//
// The algorithm walks the string rune-by-rune, tracking the most recent
// space (the preferred break point for both CJK-with-stripped-punctuation
// and Latin word-flow). When the running line would overflow, we break
// at the last space seen since the line started; if no space is
// available (long CJK run with no internal whitespace) we break at the
// current rune.
//
// After greedy wrap completes, the LAST line is rebalanced if it's
// noticeably shorter than the line above it — without that, a short
// trailing word ends up alone on its own row, which the puzzle subtitle
// scrolls to and reads as an awkward 1-word "line 2". The balancer
// reflows content from the previous line into the trailing line at a
// space-aligned cut point closer to the midpoint of the combined runes.
func wrapLines(face font.Face, text string, maxWidth int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	d := &font.Drawer{Face: face}
	maxFixed := fixed.I(maxWidth)
	runes := []rune(text)

	var lines []string
	start := 0
	for start < len(runes) {
		// Skip leading spaces left over from the previous break.
		for start < len(runes) && runes[start] == ' ' {
			start++
		}
		if start >= len(runes) {
			break
		}
		// Scan forward, tracking the most recent space, until adding the
		// next rune would push us over maxWidth.
		end := start
		lastSpace := -1
		for end < len(runes) {
			if d.MeasureString(string(runes[start:end+1])) > maxFixed {
				break
			}
			if runes[end] == ' ' {
				lastSpace = end
			}
			end++
		}
		if end == len(runes) {
			line := strings.TrimSpace(string(runes[start:]))
			if line != "" {
				lines = append(lines, line)
			}
			break
		}
		// Prefer breaking at the last space we saw — that yields word-
		// aligned breaks for Latin text and clean phrase breaks for CJK
		// content where punctuation has been stripped to spaces. Fall back
		// to a rune-level break only when no space is available within
		// the line (a long unbroken run, e.g. Chinese without
		// punctuation).
		breakAt := end
		if lastSpace > start {
			breakAt = lastSpace
		}
		line := strings.TrimSpace(string(runes[start:breakAt]))
		if line != "" {
			lines = append(lines, line)
		}
		start = breakAt
	}

	return balanceLastLine(lines, face, maxWidth)
}

// balanceLastLine rebalances the final two wrapped lines if the trailing
// one is much shorter than the line above it. Without this a 30-char
// "line 1 ─ line 2 of 1 word" wrap reads as an awkward dangling tail in
// the puzzle subtitle scroll. We re-split the combined trailing two lines
// at a cut point near the midpoint, preferring space-aligned cuts and
// requiring both halves to fit within maxWidth.
func balanceLastLine(lines []string, face font.Face, maxWidth int) []string {
	if len(lines) < 2 {
		return lines
	}
	n := len(lines)
	last := lines[n-1]
	prev := lines[n-2]
	lastN := utf8.RuneCountInString(last)
	prevN := utf8.RuneCountInString(prev)
	// Threshold: rebalance when the trailing line is less than 60% the
	// length of the line above it. Tuned to leave already-balanced wraps
	// alone (e.g. a 30-char line followed by a 25-char line) while
	// catching the pathological "30 chars + 1-2 chars" case.
	if lastN*100 >= prevN*60 {
		return lines
	}
	combined := strings.TrimSpace(prev) + " " + strings.TrimSpace(last)
	runes := []rune(combined)
	if len(runes) < 2 {
		return lines
	}

	d := &font.Drawer{Face: face}
	maxFixed := fixed.I(maxWidth)
	target := len(runes) / 2

	// Score each candidate split: distance from the midpoint plus a
	// strong penalty when the cut isn't on a space (so word-aligned cuts
	// win even when slightly off-centre). Both halves must fit within
	// maxWidth or we skip the candidate.
	bestSplit := -1
	bestCost := -1
	for i := 1; i < len(runes); i++ {
		left := strings.TrimSpace(string(runes[:i]))
		right := strings.TrimSpace(string(runes[i:]))
		if left == "" || right == "" {
			continue
		}
		if d.MeasureString(left) > maxFixed || d.MeasureString(right) > maxFixed {
			continue
		}
		dist := i - target
		if dist < 0 {
			dist = -dist
		}
		atSpace := runes[i-1] == ' ' || runes[i] == ' '
		cost := dist
		if !atSpace {
			cost += 100
		}
		if bestCost < 0 || cost < bestCost {
			bestCost = cost
			bestSplit = i
		}
	}
	if bestSplit < 0 {
		return lines
	}
	left := strings.TrimSpace(string(runes[:bestSplit]))
	right := strings.TrimSpace(string(runes[bestSplit:]))
	if left == "" || right == "" {
		return lines
	}
	out := append([]string(nil), lines[:n-2]...)
	out = append(out, left, right)
	return out
}

