package video

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"

	"github.com/sirily11/debate-bot/internal/agent"
)

// Renderer composites the live debate state into RGBA frames. Thread-safe;
// frame goroutine reads state, Stage updates state.
type Renderer struct {
	width, height int

	titleFace      font.Face // topic title at the top
	phaseFace      font.Face // phase pill under the title
	clockFace      font.Face // elapsed/total clock at the bottom
	tagFace        font.Face // speaker pill in the subtitle
	bodyFace       font.Face // spoken text in the subtitle
	panelHdrFace   font.Face // side-panel section header ("正方")
	panelNameFace  font.Face // side-panel speaker name (idle)
	panelActFace   font.Face // side-panel speaker name (active)

	mu       sync.RWMutex
	topic    string
	phase    string
	speaker  string
	role     string
	side     string
	body     string
	affNames []string
	negNames []string

	// Wall-clock display fed by the pipeline's once-per-second TickMsg.
	clockElapsed time.Duration
	clockTotal   time.Duration

	// Transient overlay for user/chat messages. Drawn on top of the subtitle
	// without disturbing it; expires automatically.
	userMsg    string
	userExpiry time.Time
}

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

	return &Renderer{
		width: width, height: height,
		titleFace:     titleFace,
		phaseFace:     phaseFace,
		clockFace:     clockFace,
		tagFace:       tagFace,
		bodyFace:      bodyFace,
		panelHdrFace:  panelHdrFace,
		panelNameFace: panelNameFace,
		panelActFace:  panelActFace,
	}, nil
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

// SetState updates the active-speaker subtitle. Empty speaker clears it (idle
// state — shows a "waiting" hint instead of a subtitle).
func (r *Renderer) SetState(speaker, role, side, body string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.speaker = speaker
	r.role = role
	r.side = side
	r.body = body
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

// ShowUserMessage flashes a viewer/chat message on the frame for ttl. It does
// NOT disturb the active speaker subtitle or any other state — it's a
// stand-alone overlay that disappears on its own.
func (r *Renderer) ShowUserMessage(text string, ttl time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.userMsg = text
	r.userExpiry = time.Now().Add(ttl)
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
	r.mu.RLock()
	topic, phase := r.topic, r.phase
	speaker, role, body := r.speaker, r.role, r.body
	clockE, clockT := r.clockElapsed, r.clockTotal
	affNames := append([]string(nil), r.affNames...)
	negNames := append([]string(nil), r.negNames...)
	userMsg := r.userMsg
	userActive := userMsg != "" && time.Now().Before(r.userExpiry)
	r.mu.RUnlock()

	img := image.NewRGBA(image.Rect(0, 0, r.width, r.height))
	drawGradientBackground(img,
		color.RGBA{0x12, 0x14, 0x1f, 0xff}, // top
		color.RGBA{0x07, 0x08, 0x0e, 0xff}, // bottom
	)

	titleFG := color.RGBA{0xff, 0xff, 0xff, 0xff}
	phaseChipBG := color.RGBA{0x24, 0x27, 0x35, 0xff}
	phaseChipFG := color.RGBA{0xc8, 0xcd, 0xdb, 0xff}
	dimFG := color.RGBA{0x88, 0x90, 0xa3, 0xff}
	hintFG := color.RGBA{0x60, 0x66, 0x76, 0xff}
	bodyFG := color.RGBA{0xee, 0xee, 0xf1, 0xff}

	// Title bar.
	if topic != "" {
		drawCenteredText(img, r.titleFace, topic, r.width/2, 70, titleFG)
	}
	if phase != "" {
		drawCenteredPill(img, r.phaseFace, strings.ToUpper(phase),
			r.width/2, 110, phaseChipBG, phaseChipFG)
	}

	// Thin accent rail under the title bar — visually separates the header
	// from the stage area.
	rail := image.Rect(120, 138, r.width-120, 140)
	draw.Draw(img, rail, &image.Uniform{color.RGBA{0x24, 0x28, 0x36, 0xff}},
		image.Point{}, draw.Src)

	// Stage area: side panels + centered subtitle.
	const (
		panelW      = 240
		panelMargin = 36
		panelTop    = 168
		panelBot    = 588
	)
	leftX := panelMargin
	rightX := r.width - panelMargin - panelW

	drawSidePanel(img,
		r.panelHdrFace, r.panelNameFace, r.panelActFace,
		leftX, panelTop, panelW, panelBot-panelTop,
		"正方", "AFFIRMATIVE", affNames,
		speaker, role, "affirmative",
		roleColor("affirmative"))

	drawSidePanel(img,
		r.panelHdrFace, r.panelNameFace, r.panelActFace,
		rightX, panelTop, panelW, panelBot-panelTop,
		"反方", "NEGATIVE", negNames,
		speaker, role, "negative",
		roleColor("negative"))

	// Subtitle area sits between the two panels.
	subLeft := leftX + panelW + 28
	subRight := rightX - 28
	if speaker != "" {
		drawSubtitle(img, r.tagFace, r.bodyFace,
			speaker, role, body,
			subLeft, panelTop+8, subRight, panelBot-8,
			bodyFG)
	} else {
		drawCenteredText(img, r.bodyFace, "等待辯手發言…",
			(subLeft+subRight)/2, (panelTop+panelBot)/2, hintFG)
	}

	// Clock floats as a pill above the background at bottom-center.
	if clockE > 0 || clockT > 0 {
		drawClockPill(img, r.clockFace, clockE, clockT,
			r.width/2, 668, titleFG, dimFG)
	}

	// User overlay — drawn last so it sits on top, centered in the subtitle
	// column so it never overlaps the side panels.
	if userActive {
		drawUserOverlay(img, r.tagFace, r.bodyFace, userMsg,
			subLeft, panelTop+8, subRight)
	}

	return img.Pix
}

// drawUserOverlay paints a transient amber notification card anchored to the
// top of the subtitle column (between the side panels) so it never overlaps
// the affirmative / negative roster panels. The card width adapts to the
// message length up to the column width.
func drawUserOverlay(dst *image.RGBA,
	tagFace, bodyFace font.Face, msg string,
	areaLeft, areaTop, areaRight int,
) {
	const (
		pad        = 16
		gapTagText = 8
		lineGap    = 6
		maxLines   = 3
	)
	if msg == "" {
		return
	}

	accent := color.RGBA{0xfb, 0xbf, 0x24, 0xff}
	boxBG := color.RGBA{0x1f, 0x1a, 0x10, 0xff}
	textFG := color.RGBA{0xff, 0xfb, 0xeb, 0xff}
	pillFG := color.RGBA{0x1a, 0x14, 0x06, 0xff}

	maxBoxW := areaRight - areaLeft
	innerW := maxBoxW - 2*pad

	lines := wrapLines(bodyFace, msg, innerW)
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		lines[maxLines-1] = strings.TrimRight(lines[maxLines-1], " ") + "…"
	}

	contentW := 0
	{
		d := &font.Drawer{Face: bodyFace}
		for _, ln := range lines {
			if w := d.MeasureString(ln).Ceil(); w > contentW {
				contentW = w
			}
		}
	}
	boxW := contentW + 2*pad
	if boxW > maxBoxW {
		boxW = maxBoxW
	}
	if min := 360; boxW < min && min < maxBoxW {
		boxW = min
	}

	bodyM := bodyFace.Metrics()
	tagM := tagFace.Metrics()
	lineH := bodyM.Height.Ceil() + lineGap
	tagH := tagM.Ascent.Ceil() + tagM.Descent.Ceil() + 16
	boxH := pad*2 + tagH + gapTagText + len(lines)*lineH

	boxX := areaLeft + (maxBoxW-boxW)/2
	boxY := areaTop
	box := image.Rect(boxX, boxY, boxX+boxW, boxY+boxH)

	draw.Draw(dst, box.Inset(-4),
		&image.Uniform{withAlpha(accent, 0x22)}, image.Point{}, draw.Over)
	draw.Draw(dst, box, &image.Uniform{boxBG}, image.Point{}, draw.Src)
	drawRectOutline(dst, box, 2, accent)

	// Tag pill: left-aligned inside the box.
	tagText := "FROM CHAT"
	tagD := &font.Drawer{Face: tagFace}
	tagW := tagD.MeasureString(tagText).Ceil()
	pillCx := boxX + pad + tagW/2 + 14
	pillCy := boxY + pad + tagM.Ascent.Ceil()
	drawCenteredPill(dst, tagFace, tagText, pillCx, pillCy, accent, pillFG)

	// Message lines: left-aligned within the box.
	textTop := boxY + pad + tagH + gapTagText + bodyM.Ascent.Ceil()
	d := &font.Drawer{Dst: dst, Src: image.NewUniform(textFG), Face: bodyFace}
	for i, ln := range lines {
		d.Dot = fixed.P(boxX+pad, textTop+i*lineH)
		d.DrawString(ln)
	}
}

// drawSubtitle paints the centered subtitle card inside the rectangle
// (areaLeft,areaTop)-(areaRight,areaBot): a role-colored pill with the speaker
// label, then the spoken text wrapped within a bounded box. Text is
// right-trimmed to the most recent N lines so it always fits.
func drawSubtitle(dst *image.RGBA, tagFace, bodyFace font.Face,
	speaker, role, body string,
	areaLeft, areaTop, areaRight, areaBot int,
	fg color.RGBA,
) {
	const (
		pad         = 26
		gapTagBody  = 18
		lineGap     = 10
		maxLinesCap = 6
	)

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
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}

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
	if len(lines) > 0 {
		bodyH = len(lines)*lineH + gapTagBody
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
	// Layered backdrop: a subtle outer glow halo plus the card fill.
	outline := roleColor(role)
	halo := withAlpha(outline, 0x22)
	draw.Draw(dst, box.Inset(-6),
		&image.Uniform{halo}, image.Point{}, draw.Over)
	draw.Draw(dst, box,
		&image.Uniform{color.RGBA{0x18, 0x1b, 0x26, 0xff}}, image.Point{}, draw.Src)
	drawRectOutline(dst, box, 2, outline)

	// Pill: centered horizontally inside the box, near the top.
	pillCx := boxX + boxW/2
	pillCy := boxY + pad + tagFace.Metrics().Ascent.Ceil()
	drawCenteredPill(dst, tagFace, tagText, pillCx, pillCy,
		outline, color.RGBA{0xff, 0xff, 0xff, 0xff})

	// Body lines: centered horizontally, top-down below the pill.
	textTop := boxY + pad + tagH + gapTagBody + bodyMetrics.Ascent.Ceil()
	d := &font.Drawer{Dst: dst, Src: image.NewUniform(fg), Face: bodyFace}
	for i, ln := range lines {
		w := d.MeasureString(ln).Ceil()
		x := boxX + (boxW-w)/2
		y := textTop + i*lineH
		d.Dot = fixed.P(x, y)
		d.DrawString(ln)
	}
}

// drawSidePanel paints one of the two roster panels (affirmative/negative).
// activeSpeaker + activeRole are the current speaker; if their role matches
// panelSide, the matching name is highlighted. accent is the role color used
// for the header text and the active row's marker.
func drawSidePanel(dst *image.RGBA,
	hdrFace, nameFace, activeFace font.Face,
	x, y, w, h int,
	zh, en string,
	names []string,
	activeSpeaker, activeRole, panelSide string,
	accent color.RGBA,
) {
	box := image.Rect(x, y, x+w, y+h)
	// Soft halo behind the card, tinted with the side accent.
	draw.Draw(dst, box.Inset(-4),
		&image.Uniform{withAlpha(accent, 0x18)}, image.Point{}, draw.Over)
	// Card fill.
	draw.Draw(dst, box,
		&image.Uniform{color.RGBA{0x14, 0x16, 0x21, 0xff}}, image.Point{}, draw.Src)

	// Vertical accent rail on the outside edge of the card.
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
	dz := &font.Drawer{Dst: dst, Src: image.NewUniform(accent), Face: hdrFace}
	dz.Dot = fixed.P(x+(w-zhW)/2, hdrTop+hdrFace.Metrics().Ascent.Ceil())
	dz.DrawString(zh)

	enFace := nameFace
	enFG := color.RGBA{0x90, 0x96, 0xa6, 0xff}
	enW := (&font.Drawer{Face: enFace}).MeasureString(en).Ceil()
	enY := hdrTop + hdrFace.Metrics().Height.Ceil() + 4 + enFace.Metrics().Ascent.Ceil()
	de := &font.Drawer{Dst: dst, Src: image.NewUniform(enFG), Face: enFace}
	de.Dot = fixed.P(x+(w-enW)/2, enY)
	de.DrawString(en)

	// Divider under header.
	divY := enY + enFace.Metrics().Descent.Ceil() + 18
	div := image.Rect(x+24, divY, x+w-24, divY+1)
	draw.Draw(dst, div,
		&image.Uniform{color.RGBA{0x2a, 0x2d, 0x3c, 0xff}}, image.Point{}, draw.Src)

	// Name list.
	listTop := divY + 28
	rowH := 44
	for i, name := range names {
		rowCy := listTop + i*rowH
		isActive := name == activeSpeaker && string(activeRole) == panelSide
		face := nameFace
		fg := color.RGBA{0xb6, 0xbc, 0xcc, 0xff}
		if isActive {
			face = activeFace
			fg = color.RGBA{0xff, 0xff, 0xff, 0xff}
			// Active row pill background spanning the inner width.
			pad := 12
			rowBox := image.Rect(x+pad, rowCy-22, x+w-pad, rowCy+12)
			draw.Draw(dst, rowBox,
				&image.Uniform{withAlpha(accent, 0x33)}, image.Point{}, draw.Over)
			drawRectOutline(dst, rowBox, 1, withAlpha(accent, 0x66))
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

func roleColor(role string) color.RGBA {
	switch agent.Role(role) {
	case agent.RoleHost:
		return color.RGBA{0x4e, 0xc9, 0xff, 0xff}
	case agent.RoleAffirmative:
		return color.RGBA{0x4a, 0xde, 0x80, 0xff}
	case agent.RoleNegative:
		return color.RGBA{0xf8, 0x71, 0x71, 0xff}
	case agent.RoleJudge:
		return color.RGBA{0xfb, 0xbf, 0x24, 0xff}
	case agent.RoleViewer:
		return color.RGBA{0xc0, 0x84, 0xfc, 0xff}
	}
	return color.RGBA{0x91, 0x47, 0xff, 0xff}
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
