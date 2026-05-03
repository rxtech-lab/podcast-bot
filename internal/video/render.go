package video

import (
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

	titleFace font.Face // topic title at the top
	phaseFace font.Face // phase status line
	tagFace   font.Face // speaker pill in the subtitle
	bodyFace  font.Face // spoken text in the subtitle

	mu      sync.RWMutex
	topic   string
	phase   string
	speaker string
	role    string
	side    string
	body    string

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

	titleFace, err := mk(srcBold, 44)
	if err != nil {
		return nil, err
	}
	phaseFace, err := mk(srcBody, 22)
	if err != nil {
		return nil, err
	}
	tagFace, err := mk(srcBold, 32)
	if err != nil {
		return nil, err
	}
	bodyFace, err := mk(srcBody, 36)
	if err != nil {
		return nil, err
	}

	return &Renderer{
		width: width, height: height,
		titleFace: titleFace,
		phaseFace: phaseFace,
		tagFace:   tagFace,
		bodyFace:  bodyFace,
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

// ShowUserMessage flashes a viewer/chat message on the frame for ttl. It does
// NOT disturb the active speaker subtitle or any other state — it's a
// stand-alone overlay that disappears on its own.
func (r *Renderer) ShowUserMessage(text string, ttl time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.userMsg = text
	r.userExpiry = time.Now().Add(ttl)
}

// Frame renders one RGBA frame. Layout:
//
//	┌──────────────────────────────────────────┐
//	│              [Topic title]               │ ← always (when known)
//	│              [Phase: opening]            │
//	│                                          │
//	│       ┌──────────────────────────┐       │
//	│       │  [Pill: AFFIRMATIVE — X] │       │ ← only when speaking
//	│       │  spoken text wrapped …   │       │
//	│       └──────────────────────────┘       │
//	└──────────────────────────────────────────┘
func (r *Renderer) Frame() []byte {
	r.mu.RLock()
	topic, phase := r.topic, r.phase
	speaker, role, body := r.speaker, r.role, r.body
	userMsg := r.userMsg
	userActive := userMsg != "" && time.Now().Before(r.userExpiry)
	r.mu.RUnlock()

	img := image.NewRGBA(image.Rect(0, 0, r.width, r.height))
	bg := color.RGBA{0x0e, 0x0e, 0x10, 0xff}
	draw.Draw(img, img.Bounds(), &image.Uniform{bg}, image.Point{}, draw.Src)

	titleFG := color.RGBA{0xff, 0xff, 0xff, 0xff}
	phaseFG := color.RGBA{0xa0, 0xa8, 0xb8, 0xff}
	hintFG := color.RGBA{0x70, 0x78, 0x88, 0xff}
	bodyFG := color.RGBA{0xee, 0xee, 0xf1, 0xff}

	// Top banner.
	if topic != "" {
		drawCenteredText(img, r.titleFace, topic, r.width/2, 70, titleFG)
	}
	if phase != "" {
		drawCenteredText(img, r.phaseFace, "Phase: "+phase, r.width/2, 120, phaseFG)
	}

	// Subtitle area sits in the lower-middle region.
	if speaker != "" {
		drawSubtitle(img, r.tagFace, r.bodyFace,
			speaker, role, body,
			r.width, r.height,
			bodyFG)
	} else {
		// Idle: show a centered "waiting" hint instead of a subtitle box.
		drawCenteredText(img, r.bodyFace, "等待辯手發言…", r.width/2, r.height/2+40, hintFG)
	}

	// User overlay — drawn last so it sits ON TOP of whatever is below.
	if userActive {
		drawUserOverlay(img, r.tagFace, r.bodyFace, userMsg, r.width)
	}

	return img.Pix
}

// drawUserOverlay paints a transient amber notification card anchored to the
// top-right corner. It's narrow enough to leave the centered topic title and
// the lower-third subtitle untouched.
func drawUserOverlay(dst *image.RGBA, tagFace, bodyFace font.Face, msg string, width int) {
	const (
		pad         = 16
		gapTagText  = 8
		lineGap     = 6
		maxLines    = 4
		boxW        = 460
		rightMargin = 32
		topY        = 160
	)
	if msg == "" {
		return
	}

	accent := color.RGBA{0xfb, 0xbf, 0x24, 0xff}
	boxBG := color.RGBA{0x1f, 0x1a, 0x10, 0xff}
	textFG := color.RGBA{0xff, 0xfb, 0xeb, 0xff}
	pillFG := color.RGBA{0x1a, 0x14, 0x06, 0xff}

	innerW := boxW - 2*pad

	lines := wrapLines(bodyFace, msg, innerW)
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		lines[maxLines-1] = strings.TrimRight(lines[maxLines-1], " ") + "…"
	}

	bodyM := bodyFace.Metrics()
	tagM := tagFace.Metrics()
	lineH := bodyM.Height.Ceil() + lineGap
	tagH := tagM.Ascent.Ceil() + tagM.Descent.Ceil() + 16
	boxH := pad*2 + tagH + gapTagText + len(lines)*lineH

	boxX := width - rightMargin - boxW
	box := image.Rect(boxX, topY, boxX+boxW, topY+boxH)

	draw.Draw(dst, box, &image.Uniform{boxBG}, image.Point{}, draw.Src)
	drawRectOutline(dst, box, 2, accent)

	// Tag pill: left-aligned inside the box.
	tagText := "FROM CHAT"
	tagD := &font.Drawer{Face: tagFace}
	tagW := tagD.MeasureString(tagText).Ceil()
	pillCx := boxX + pad + tagW/2 + 14
	pillCy := topY + pad + tagM.Ascent.Ceil()
	drawCenteredPill(dst, tagFace, tagText, pillCx, pillCy, accent, pillFG)

	// Message lines: left-aligned within the box.
	textTop := topY + pad + tagH + gapTagText + bodyM.Ascent.Ceil()
	d := &font.Drawer{Dst: dst, Src: image.NewUniform(textFG), Face: bodyFace}
	for i, ln := range lines {
		d.Dot = fixed.P(boxX+pad, textTop+i*lineH)
		d.DrawString(ln)
	}
}

// drawSubtitle paints a centered lower-third subtitle: a role-colored pill
// with the speaker label, then the spoken text wrapped within a bounded box.
// Text is right-trimmed to the most recent N lines so it always fits.
func drawSubtitle(dst *image.RGBA, tagFace, bodyFace font.Face,
	speaker, role, body string,
	width, height int,
	fg color.RGBA,
) {
	const (
		pad         = 28
		gapTagBody  = 18
		lineGap     = 10
		maxLines    = 6
		boxMinW     = 600
		sideMargin  = 80
		topAnchorY  = 360 // top edge of the subtitle box (above-center)
	)

	maxBoxW := width - 2*sideMargin
	innerW := maxBoxW - 2*pad

	tagText := formatTagPlain(speaker, role)
	tagW := 0
	if tagText != "" {
		d := &font.Drawer{Face: tagFace}
		tagW = d.MeasureString(tagText).Ceil() + 48 // pill internal padding
	}

	lines := wrapLines(bodyFace, body, innerW)
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}

	// Box width = max(tagW, longest line width, boxMinW), capped at maxBoxW.
	contentW := tagW
	{
		d := &font.Drawer{Face: bodyFace}
		for _, ln := range lines {
			if w := d.MeasureString(ln).Ceil(); w > contentW {
				contentW = w
			}
		}
	}
	boxW := min(max(contentW+2*pad, boxMinW), maxBoxW)

	bodyMetrics := bodyFace.Metrics()
	lineH := bodyMetrics.Height.Ceil() + lineGap
	tagH := tagFace.Metrics().Ascent.Ceil() + tagFace.Metrics().Descent.Ceil() + 28
	bodyH := 0
	if len(lines) > 0 {
		bodyH = len(lines)*lineH + gapTagBody
	}
	boxH := max(pad*2+tagH+bodyH, 140)

	// Center the box horizontally; anchor near the vertical middle.
	boxX := (width - boxW) / 2
	boxY := topAnchorY
	if boxY+boxH > height-40 {
		boxY = height - 40 - boxH
	}

	// Box background — semi-transparent dark over the dark frame just adds
	// definition; we use a slightly lighter shade than the page background.
	box := image.Rect(boxX, boxY, boxX+boxW, boxY+boxH)
	draw.Draw(dst, box, &image.Uniform{color.RGBA{0x1a, 0x1c, 0x22, 0xff}}, image.Point{}, draw.Src)
	// 2px outline using role color.
	outline := roleColor(role)
	drawRectOutline(dst, box, 2, outline)

	// Pill: centered horizontally inside the box, near the top.
	pillCx := boxX + boxW/2
	pillCy := boxY + pad + tagFace.Metrics().Ascent.Ceil()
	drawCenteredPill(dst, tagFace, tagText, pillCx, pillCy, outline, color.RGBA{0xff, 0xff, 0xff, 0xff})

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
