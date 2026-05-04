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
	headerPlate     *image.RGBA
	lowerThirdPlate *image.RGBA
	panelAffPlate   *image.RGBA
	panelNegPlate   *image.RGBA
	subtitleBgPlate *image.RGBA

	titleFace      font.Face // topic title at the top
	phaseFace      font.Face // phase pill under the title
	clockFace      font.Face // elapsed/total clock at the bottom
	tagFace        font.Face // speaker pill in the subtitle
	bodyFace       font.Face // spoken text in the subtitle
	panelHdrFace   font.Face // side-panel section header ("正方")
	panelNameFace  font.Face // side-panel speaker name (idle)
	panelActFace   font.Face // side-panel speaker name (active)
	panelPosFace   font.Face // side-panel position footer (very small)

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

// Frame renders one RGBA frame. Layout (1280×720):
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
	r.mu.Unlock()

	// activeFrac is 0 when fully idle, 1 when fully active, with a smooth
	// ease-in-out cubic curve in between. The title's Y position lerps with
	// it; everything else is rendered to an overlay and composited at this
	// alpha so the supporting elements fade as a group.
	activeFrac := stageActiveFrac(mode, modeStart)

	titleFG := color.RGBA{0xff, 0xff, 0xff, 0xff}

	img := image.NewRGBA(image.Rect(0, 0, r.width, r.height))
	r.drawBackground(img)

	// Magic-move title geometry: idle position is centered vertically, active
	// position is the broadcast slot at y=70.
	idleTitleY := r.height / 2
	activeTitleY := 70
	titleY := lerpInt(idleTitleY, activeTitleY, activeFrac)

	// Idle decorations: a small "today's topic" label above the title, plus
	// solid navy and red backdrops behind the label and the title text.
	// Only worth painting when at least partially visible.
	if activeFrac < 1 {
		idle := image.NewRGBA(image.Rect(0, 0, r.width, r.height))
		r.drawIdleDecorations(idle, topic, idleTitleY)
		blitWithGlobalAlpha(img, idle, 1-activeFrac)
	}

	// Active overlay — only worth building when at least partially visible.
	if activeFrac > 0 {
		overlay := image.NewRGBA(image.Rect(0, 0, r.width, r.height))
		r.drawActiveOverlay(overlay,
			topic, phase, speaker, role, body, bodyStart, bodyDur,
			affNames, negNames, affPos, negPos,
			clockE, clockT,
			userName, userMsg, userStart, userExpiry)
		blitWithGlobalAlpha(img, overlay, activeFrac)
	}

	// Title is drawn LAST and ONCE so the same glyph slides smoothly between
	// the two endpoints (true magic move). Always at full alpha — readable
	// over the idle navy box, the active red banner, and any midpoint blend.
	if topic != "" {
		drawCenteredText(img, r.titleFace, topic, r.width/2, titleY, titleFG)
	}

	// Phase chip is its own top-most layer so it always renders above the
	// active overlay's banner image — never mixed into the overlay's alpha
	// blend. We still fade it with activeFrac so it doesn't show in idle.
	if phase != "" && activeFrac > 0 {
		pill := image.NewRGBA(image.Rect(0, 0, r.width, 60))
		drawCenteredPill(pill, r.phaseFace, phaseLabel(phase),
			r.width/2, 30,
			color.RGBA{0xff, 0xff, 0xff, 0xff},
			color.RGBA{0xc8, 0x10, 0x16, 0xff})
		// Blit the pill buffer onto the frame so its top sits just below the
		// banner edge (banner ends at y=122). Pill baseline 30 inside the
		// buffer means the buffer's y=0 is the pill's top — so anchor the
		// buffer at y=92 to land the pill near the banner edge.
		blitWithGlobalAlphaAt(img, pill, 0, 92, activeFrac)
	}

	return img.Pix
}

// drawIdleDecorations paints the centered idle layout: a small "今日辯題 /
// TODAY'S TOPIC" label above the title, with solid color backdrops behind
// both. The title text itself is NOT drawn here — Frame() draws it once at
// its lerped position so the same glyph slides during the magic move. We do
// however paint the navy backdrop for the title at idleTitleY so the
// title-on-navy combo only shows at the centered position.
func (r *Renderer) drawIdleDecorations(dst *image.RGBA, topic string, titleY int) {
	if topic == "" {
		return
	}
	cnnRed := color.RGBA{0xc8, 0x10, 0x16, 0xff}
	titleBoxBG := color.RGBA{0x14, 0x1c, 0x32, 0xff}

	// Title backdrop: a solid navy box wrapping the topic text width.
	tw := (&font.Drawer{Face: r.titleFace}).MeasureString(topic).Ceil()
	tm := r.titleFace.Metrics()
	titlePadX, titlePadY := 36, 18
	titleBox := image.Rect(
		r.width/2-tw/2-titlePadX, titleY-tm.Ascent.Ceil()-titlePadY,
		r.width/2+tw/2+titlePadX, titleY+tm.Descent.Ceil()+titlePadY,
	)
	draw.Draw(dst, titleBox, &image.Uniform{titleBoxBG}, image.Point{}, draw.Src)

	// Label above the title: a red pill with white bilingual text.
	const label = "今日辯題  ·  TODAY'S TOPIC"
	labelFace := r.phaseFace
	labelBaseline := titleBox.Min.Y - 24
	drawCenteredPill(dst, labelFace, label,
		r.width/2, labelBaseline,
		cnnRed, color.RGBA{0xff, 0xff, 0xff, 0xff})
}

// drawBackground paints the bg layer that's visible in every frame regardless
// of stage mode.
func (r *Renderer) drawBackground(img *image.RGBA) {
	if r.bgPlate != nil {
		draw.Draw(img, img.Bounds(), r.bgPlate, image.Point{}, draw.Src)
		return
	}
	drawGradientBackground(img,
		color.RGBA{0x12, 0x14, 0x1f, 0xff},
		color.RGBA{0x07, 0x08, 0x0e, 0xff},
	)
}

// drawActiveOverlay paints every element that belongs to the "active" layout
// (banner, phase chip, side panels, subtitle, clock, lower-third, chat
// ticker) onto a transparent buffer. The caller composites this buffer onto
// the frame with a global alpha so the whole supporting cast fades in/out
// together.
func (r *Renderer) drawActiveOverlay(img *image.RGBA,
	topic, phase, speaker, role, body string,
	bodyStart time.Time, bodyDur time.Duration,
	affNames, negNames []string,
	affPos, negPos string,
	clockE, clockT time.Duration,
	userName, userMsg string, userStart, userExpiry time.Time,
) {
	titleFG := color.RGBA{0xff, 0xff, 0xff, 0xff}
	phaseChipBG := color.RGBA{0xff, 0xff, 0xff, 0xff}
	phaseChipFG := color.RGBA{0xc8, 0x10, 0x16, 0xff}
	dimFG := color.RGBA{0xb6, 0xbc, 0xcc, 0xff}
	hintFG := color.RGBA{0x88, 0x8e, 0x9e, 0xff}
	bodyFG := color.RGBA{0xf2, 0xf4, 0xf8, 0xff}

	// Red CNN-style title banner — procedural for exact centering.
	bannerRect := image.Rect(40, 18, r.width-40, 122)
	cnnRed := color.RGBA{0xc8, 0x10, 0x16, 0xff}
	draw.Draw(img, bannerRect, &image.Uniform{cnnRed}, image.Point{}, draw.Src)
	drawRectOutline(img, bannerRect.Inset(6), 2,
		color.RGBA{0xff, 0xff, 0xff, 0x66})

	// Lower-third strip aligned with the ticker.
	if r.lowerThirdPlate != nil {
		drawScaledOver(img, r.lowerThirdPlate,
			image.Rect(0, r.height-tickerStripH, r.width, r.height))
	}

	// Title and phase pill are NOT drawn here — Frame() draws each in their
	// own top-most layer so they slide / fade independently of the overlay.
	_ = topic
	_ = phase
	_ = phaseChipBG
	_ = phaseChipFG

	// Thin accent rail separating header from stage.
	rail := image.Rect(120, 138, r.width-120, 140)
	draw.Draw(img, rail, &image.Uniform{color.RGBA{0x24, 0x28, 0x36, 0xff}},
		image.Point{}, draw.Src)

	// Side panels + subtitle column.
	const (
		panelW      = 240
		panelMargin = 36
		panelTop    = 168
		panelBot    = 588
	)
	leftX := panelMargin
	rightX := r.width - panelMargin - panelW

	drawSidePanel(img,
		r.panelHdrFace, r.panelNameFace, r.panelActFace, r.panelPosFace,
		leftX, panelTop, panelW, panelBot-panelTop,
		"正方", "AFFIRMATIVE", affNames, affPos,
		speaker, role, "affirmative",
		roleColor("affirmative"),
		r.panelAffPlate)

	drawSidePanel(img,
		r.panelHdrFace, r.panelNameFace, r.panelActFace, r.panelPosFace,
		rightX, panelTop, panelW, panelBot-panelTop,
		"反方", "NEGATIVE", negNames, negPos,
		speaker, role, "negative",
		roleColor("negative"),
		r.panelNegPlate)

	subLeft := leftX + panelW + 28
	subRight := rightX - 28
	if r.subtitleBgPlate != nil {
		sb := r.subtitleBgPlate.Bounds()
		areaCx := (subLeft + subRight) / 2
		areaCy := (panelTop + panelBot) / 2
		dst := image.Rect(areaCx-sb.Dx()/2, areaCy-sb.Dy()/2,
			areaCx+sb.Dx()-sb.Dx()/2, areaCy+sb.Dy()-sb.Dy()/2)
		draw.Draw(img, dst, r.subtitleBgPlate, sb.Min, draw.Over)
	}
	if speaker != "" {
		drawSubtitle(img, r.tagFace, r.bodyFace,
			speaker, role, body,
			subLeft, panelTop+8, subRight, panelBot-8,
			bodyFG, r.subtitleBgPlate != nil, bodyStart, bodyDur)
	} else {
		drawCenteredText(img, r.bodyFace, "等待辯手發言…",
			(subLeft+subRight)/2, (panelTop+panelBot)/2, hintFG)
	}

	if clockE > 0 || clockT > 0 {
		drawClockPill(img, r.clockFace, clockE, clockT,
			r.width/2, r.height-tickerStripH-30, titleFG, dimFG)
	}

	if userMsg != "" && time.Now().Before(userExpiry) {
		drawChatTicker(img, r.tagFace, r.bodyFace, userName, userMsg,
			0, r.height-tickerStripH, r.width, r.height,
			userStart)
	}
}

// stageActiveFrac maps (mode, modeStart) to a 0..1 fraction with cubic ease
// in/out. 0 means fully idle (only bg + centered title visible); 1 means
// fully active (full broadcast layout). modeStart=zero (renderer just
// constructed, no transitions yet) yields 0 because elapsed is huge → t=1
// → activeFrac=0 in the idle branch.
func stageActiveFrac(mode stageMode, modeStart time.Time) float64 {
	if modeStart.IsZero() {
		// Pre-transition default: fully idle.
		return 0
	}
	elapsed := time.Since(modeStart).Seconds()
	t := elapsed / stageTransitionDuration.Seconds()
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	t = easeInOutCubic(t)
	if mode == stageActive {
		return t
	}
	return 1 - t
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

// drawSubtitle paints the centered subtitle card inside the rectangle
// (areaLeft,areaTop)-(areaRight,areaBot): a role-colored pill with the speaker
// label, then the spoken text wrapped within a bounded box. When the wrapped
// body overflows what physically fits, the text auto-scrolls vertically
// (initial dwell → smooth scroll → final dwell at the last lines) so viewers
// see the entire sentence rather than only the most recent fragment.
// bodyStart is when the current body became active — the renderer resets it
// on every body change so each new sentence begins its scroll from the top.
// bodyAudioDuration is the synthesized-audio length of body; when known
// (>0) the scroll is delayed to start at audioDuration/2 - 3s so motion
// lands in the second half of playback. Zero falls back to a fixed dwell.
func drawSubtitle(dst *image.RGBA, tagFace, bodyFace font.Face,
	speaker, role, body string,
	areaLeft, areaTop, areaRight, areaBot int,
	fg color.RGBA,
	hasChrome bool,
	bodyStart time.Time,
	bodyAudioDuration time.Duration,
) {
	const (
		pad         = 26
		gapTagBody  = 18
		lineGap     = 10
		maxLinesCap = 6

		// scrollDuration is the wall-clock window for the scroll itself once
		// it begins. Independent of overflow size, so longer passages scroll
		// faster instead of taking proportionally longer to finish.
		scrollDuration = 2500 * time.Millisecond

		// fallbackDwell is used when bodyAudioDuration is unknown (==0) — keeps
		// the legacy behaviour for callers that don't supply audio timing.
		fallbackDwell = 500 * time.Millisecond
	)

	// scrollDwellStart aligns scroll start with the audio: t/2 - 3s. Negative
	// results clamp to 0 so very short clips scroll immediately.
	scrollDwellStart := fallbackDwell
	if bodyAudioDuration > 0 {
		scrollDwellStart = bodyAudioDuration/2 - 3*time.Second
		if scrollDwellStart < 0 {
			scrollDwellStart = 0
		}
	}

	areaW := areaRight - areaLeft
	areaH := areaBot - areaTop
	maxBoxW := areaW
	innerW := maxBoxW - 2*pad

	tagText := formatTagPlain(speaker, role)
	tagW := 0
	if tagText != "" {
		d := &font.Drawer{Face: tagFace}
		tagW = d.MeasureString(tagText).Ceil() + 48 // pill internal padding
	}

	bodyMetrics := bodyFace.Metrics()
	lineH := bodyMetrics.Height.Ceil() + lineGap
	tagH := tagFace.Metrics().Ascent.Ceil() + tagFace.Metrics().Descent.Ceil() + 24

	// Cap visible body lines to whatever physically fits between the tag and
	// the bottom of the area — otherwise the bounded box clamps to areaH while
	// the text keeps drawing past it (overflow under the green outline).
	maxLines := maxLinesCap
	if avail := areaH - 2*pad - tagH - gapTagBody; avail > 0 && lineH > 0 {
		if fit := avail / lineH; fit < maxLines {
			maxLines = fit
		}
	}
	if maxLines < 1 {
		maxLines = 1
	}

	lines := wrapLines(bodyFace, body, innerW)
	overflow := len(lines) > maxLines

	// Visible-line count drives the box height so the card stays a stable
	// size whether the speaker has produced one short line or a long passage
	// that's about to scroll.
	visLineCount := len(lines)
	if visLineCount > maxLines {
		visLineCount = maxLines
	}

	// Box width fits the widest line across ALL wrapped lines (not just the
	// visible window) so a longer line that's about to scroll into view
	// doesn't get horizontally clipped when it arrives.
	contentW := tagW
	{
		d := &font.Drawer{Face: bodyFace}
		for _, ln := range lines {
			if w := d.MeasureString(ln).Ceil(); w > contentW {
				contentW = w
			}
		}
	}
	boxW := min(contentW+2*pad, maxBoxW)
	if boxW < min(420, maxBoxW) {
		boxW = min(420, maxBoxW)
	}

	bodyH := 0
	if visLineCount > 0 {
		bodyH = visLineCount*lineH + gapTagBody
	}
	boxH := pad*2 + tagH + bodyH
	if boxH < 200 {
		boxH = 200
	}
	if boxH > areaH {
		boxH = areaH
	}

	// Center the box inside the available area.
	boxX := areaLeft + (areaW-boxW)/2
	boxY := areaTop + (areaH-boxH)/2

	box := image.Rect(boxX, boxY, boxX+boxW, boxY+boxH)
	outline := roleColor(role)
	if !hasChrome {
		// Procedural backdrop: subtle outer halo + dark glass fill + outline.
		halo := withAlpha(outline, 0x22)
		draw.Draw(dst, box.Inset(-6),
			&image.Uniform{halo}, image.Point{}, draw.Over)
		draw.Draw(dst, box,
			&image.Uniform{color.RGBA{0x18, 0x1b, 0x26, 0xff}}, image.Point{}, draw.Src)
		drawRectOutline(dst, box, 2, outline)
	}

	// Pill: centered horizontally inside the box, near the top.
	pillCx := boxX + boxW/2
	pillCy := boxY + pad + tagFace.Metrics().Ascent.Ceil()
	drawCenteredPill(dst, tagFace, tagText, pillCx, pillCy,
		outline, color.RGBA{0xff, 0xff, 0xff, 0xff})

	// Vertical scroll offset. When the body fits, scroll stays at 0 and the
	// renderer behaves exactly like the legacy fixed layout.
	scrollPx := 0
	if overflow && !bodyStart.IsZero() {
		overflowPx := (len(lines) - maxLines) * lineH
		elapsed := time.Since(bodyStart)
		if elapsed > scrollDwellStart {
			scrollElapsed := elapsed - scrollDwellStart
			if scrollElapsed >= scrollDuration {
				scrollPx = overflowPx
			} else {
				scrollPx = int(float64(overflowPx) *
					float64(scrollElapsed) / float64(scrollDuration))
			}
		}
	}

	// Body lines: centered horizontally, top-down below the pill, drawn into
	// a sub-image clipped to the body region so glyphs scrolling past the top
	// or bottom edge don't bleed onto the pill or out of the card.
	textTop := boxY + pad + tagH + gapTagBody + bodyMetrics.Ascent.Ceil()
	bodyClip := image.Rect(
		boxX,
		boxY+pad+tagH+gapTagBody,
		boxX+boxW,
		boxY+boxH-pad,
	).Intersect(dst.Bounds())
	clip, _ := dst.SubImage(bodyClip).(*image.RGBA)
	if clip == nil {
		clip = dst
	}
	d := &font.Drawer{Dst: clip, Src: image.NewUniform(fg), Face: bodyFace}
	asc, desc := bodyMetrics.Ascent.Ceil(), bodyMetrics.Descent.Ceil()
	for i, ln := range lines {
		w := d.MeasureString(ln).Ceil()
		x := boxX + (boxW-w)/2
		y := textTop + i*lineH - scrollPx
		// Skip lines fully outside the clip — saves a DrawString call per
		// glyph that would otherwise be no-ops thanks to clipping.
		if y+desc < bodyClip.Min.Y || y-asc > bodyClip.Max.Y {
			continue
		}
		d.Dot = fixed.P(x, y)
		d.DrawString(ln)
	}
}

// drawSidePanel paints one of the two roster panels (affirmative/negative).
// activeSpeaker + activeRole are the current speaker; if their role matches
// panelSide, the matching name is highlighted. accent is the role color used
// for the header text and the active row's marker. position, when non-empty,
// is rendered as wrapped small text in a footer band so viewers can read each
// side's stance.
func drawSidePanel(dst *image.RGBA,
	hdrFace, nameFace, activeFace, posFace font.Face,
	x, y, w, h int,
	zh, en string,
	names []string,
	position string,
	activeSpeaker, activeRole, panelSide string,
	accent color.RGBA,
	chrome *image.RGBA,
) {
	box := image.Rect(x, y, x+w, y+h)

	// CNN-style: both the chrome plate and the procedural fallback are dark,
	// so a single light-on-dark palette works for both paths.
	hasChrome := chrome != nil
	hdrFG := accent
	enFG := color.RGBA{0xb6, 0xbc, 0xcc, 0xff}
	dividerFG := color.RGBA{0x2a, 0x2d, 0x3c, 0xff}
	idleFG := color.RGBA{0xc8, 0xcd, 0xdb, 0xff}
	activeFG := color.RGBA{0xff, 0xff, 0xff, 0xff}
	activeRowBG := withAlpha(accent, 0x44)
	activeRowOutline := withAlpha(accent, 0x99)

	// Panel chrome is now drawn procedurally — the image-gen plates were
	// unreliable about both edge placement and how much of the canvas they
	// filled, and since the panel color is so close to the bg the asset gave
	// no visible benefit. We use a slightly lighter navy so the card reads as
	// distinct from the bg.
	_ = chrome
	_ = hasChrome
	draw.Draw(dst, box,
		&image.Uniform{color.RGBA{0x14, 0x1c, 0x32, 0xff}}, image.Point{}, draw.Src)
	// Thin matching-accent line along the TOP edge for the CNN news-channel
	// header bar feel.
	topAccent := image.Rect(x, y, x+w, y+3)
	draw.Draw(dst, topAccent, &image.Uniform{accent}, image.Point{}, draw.Src)
	// Vertical accent rail on the OUTER edge of the panel.
	railW := 4
	var rail image.Rectangle
	if panelSide == "affirmative" {
		rail = image.Rect(x, y, x+railW, y+h)
	} else {
		rail = image.Rect(x+w-railW, y, x+w, y+h)
	}
	draw.Draw(dst, rail, &image.Uniform{accent}, image.Point{}, draw.Src)

	// Header (Chinese label on top, English subtitle below).
	hdrTop := y + 28
	d := &font.Drawer{Face: hdrFace}
	zhW := d.MeasureString(zh).Ceil()
	dz := &font.Drawer{Dst: dst, Src: image.NewUniform(hdrFG), Face: hdrFace}
	dz.Dot = fixed.P(x+(w-zhW)/2, hdrTop+hdrFace.Metrics().Ascent.Ceil())
	dz.DrawString(zh)

	enFace := nameFace
	enW := (&font.Drawer{Face: enFace}).MeasureString(en).Ceil()
	enY := hdrTop + hdrFace.Metrics().Height.Ceil() + 4 + enFace.Metrics().Ascent.Ceil()
	de := &font.Drawer{Dst: dst, Src: image.NewUniform(enFG), Face: enFace}
	de.Dot = fixed.P(x+(w-enW)/2, enY)
	de.DrawString(en)

	// Divider under header.
	divY := enY + enFace.Metrics().Descent.Ceil() + 18
	div := image.Rect(x+24, divY, x+w-24, divY+1)
	draw.Draw(dst, div, &image.Uniform{dividerFG}, image.Point{}, draw.Src)

	// Name list.
	listTop := divY + 28
	rowH := 44
	for i, name := range names {
		rowCy := listTop + i*rowH
		isActive := name == activeSpeaker && string(activeRole) == panelSide
		face := nameFace
		fg := idleFG
		if isActive {
			face = activeFace
			fg = activeFG
			// Active row pill background spanning the inner width.
			pad := 12
			rowBox := image.Rect(x+pad, rowCy-22, x+w-pad, rowCy+12)
			draw.Draw(dst, rowBox, &image.Uniform{activeRowBG}, image.Point{}, draw.Over)
			drawRectOutline(dst, rowBox, 1, activeRowOutline)
		}
		// Marker dot on the inner side.
		markerCx := x + 24
		if panelSide == "negative" {
			markerCx = x + w - 24
		}
		markerR := 4
		if isActive {
			markerR = 6
		}
		fillCircle(dst, markerCx, rowCy-6, markerR, fg)

		// Name text aligned away from the marker.
		nd := &font.Drawer{Dst: dst, Src: image.NewUniform(fg), Face: face}
		nameW := nd.MeasureString(name).Ceil()
		var nx int
		if panelSide == "affirmative" {
			nx = markerCx + 16
		} else {
			nx = markerCx - 16 - nameW
		}
		nd.Dot = fixed.P(nx, rowCy)
		nd.DrawString(name)
	}

	// Position footer: very small wrapped text along the bottom of the panel
	// so viewers can read each side's stance at a glance. Anchored to the
	// bottom edge so it sits below the names list regardless of roster size,
	// with a thin divider above it.
	if strings.TrimSpace(position) != "" {
		const (
			footerInset = 12
			footerPad   = 10
			lineGap     = 3
			maxLines    = 4
		)
		innerW := w - 2*(footerInset+footerPad)
		if innerW < 40 {
			innerW = 40
		}
		posLines := wrapLines(posFace, position, innerW)
		if len(posLines) > maxLines {
			posLines = posLines[:maxLines]
			// Ellipsis on the last visible line so truncation reads as
			// intentional rather than a clip.
			last := posLines[len(posLines)-1]
			posLines[len(posLines)-1] = trimToWidth(posFace, last+"…", innerW)
		}
		pm := posFace.Metrics()
		lineH := pm.Height.Ceil() + lineGap
		footerH := footerPad*2 + len(posLines)*lineH
		footerBox := image.Rect(
			x+footerInset, y+h-footerInset-footerH,
			x+w-footerInset, y+h-footerInset,
		)
		// Subtle accent-tinted plate so the footer reads as part of the panel
		// rather than floating text. Outline echoes the side's color.
		draw.Draw(dst, footerBox,
			&image.Uniform{withAlpha(accent, 0x1f)}, image.Point{}, draw.Over)
		drawRectOutline(dst, footerBox, 1, withAlpha(accent, 0x55))

		posFG := color.RGBA{0xd4, 0xd9, 0xe6, 0xff}
		pd := &font.Drawer{Dst: dst, Src: image.NewUniform(posFG), Face: posFace}
		textTop := footerBox.Min.Y + footerPad + pm.Ascent.Ceil()
		for i, ln := range posLines {
			lw := pd.MeasureString(ln).Ceil()
			lx := footerBox.Min.X + (footerBox.Dx()-lw)/2
			pd.Dot = fixed.P(lx, textTop+i*lineH)
			pd.DrawString(ln)
		}
	}
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

// wrapLines breaks text into lines that fit within maxWidth pixels. CJK text
// is one continuous run with no spaces; we wrap by measuring rune-by-rune. For
// Latin text, words from strings.Fields give nicer breaks. We pick whichever
// produces sane output: any rune outside ASCII forces per-rune wrapping.
func wrapLines(face font.Face, text string, maxWidth int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	d := &font.Drawer{Face: face}
	maxFixed := fixed.I(maxWidth)

	if hasCJK(text) {
		var lines []string
		var cur strings.Builder
		for _, ch := range text {
			cand := cur.String() + string(ch)
			if d.MeasureString(cand) > maxFixed && cur.Len() > 0 {
				lines = append(lines, cur.String())
				cur.Reset()
			}
			cur.WriteRune(ch)
		}
		if cur.Len() > 0 {
			lines = append(lines, cur.String())
		}
		return lines
	}

	words := strings.Fields(text)
	var lines []string
	cur := ""
	for _, w := range words {
		candidate := w
		if cur != "" {
			candidate = cur + " " + w
		}
		if d.MeasureString(candidate) > maxFixed && cur != "" {
			lines = append(lines, cur)
			cur = w
		} else {
			cur = candidate
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// hasCJK reports whether s contains any character in the common CJK ranges.
// Used to switch wrapping strategy: per-rune for CJK (no inter-glyph spaces),
// per-word for Latin-ish text.
func hasCJK(s string) bool {
	for _, r := range s {
		switch {
		case r >= 0x3000 && r <= 0x303f, // CJK symbols and punctuation
			r >= 0x3400 && r <= 0x4dbf, // CJK ext A
			r >= 0x4e00 && r <= 0x9fff, // CJK unified
			r >= 0xff00 && r <= 0xffef, // halfwidth/fullwidth
			r >= 0x3040 && r <= 0x30ff, // hiragana/katakana
			r >= 0xac00 && r <= 0xd7af: // hangul
			return true
		}
	}
	return false
}
