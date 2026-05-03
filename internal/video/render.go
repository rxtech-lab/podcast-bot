package video

import (
	"image"
	"image/color"
	"image/draw"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"github.com/sirily11/debate-bot/internal/agent"
)

// Renderer composites the live debate state into RGBA frames. Thread-safe;
// frame goroutine reads state, Stage updates state.
type Renderer struct {
	width, height int

	tagFace  font.Face
	bodyFace font.Face

	mu      sync.RWMutex
	speaker string
	role    string
	side    string
	body    string
}

// newRenderer allocates fonts (using the embedded Go fonts so we don't depend
// on system fonts at all) and returns a ready-to-render compositor.
func newRenderer(width, height int) (*Renderer, error) {
	bold, err := opentype.Parse(gobold.TTF)
	if err != nil {
		return nil, err
	}
	reg, err := opentype.Parse(goregular.TTF)
	if err != nil {
		return nil, err
	}
	tagFace, err := opentype.NewFace(bold, &opentype.FaceOptions{Size: 38, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil, err
	}
	bodyFace, err := opentype.NewFace(reg, &opentype.FaceOptions{Size: 30, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil, err
	}
	return &Renderer{width: width, height: height, tagFace: tagFace, bodyFace: bodyFace}, nil
}

// SetState updates what the next rendered frames will display.
func (r *Renderer) SetState(speaker, role, side, body string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.speaker = speaker
	r.role = role
	r.side = side
	r.body = body
}

// Frame renders a single RGBA frame and returns its raw bytes (length =
// width*height*4). Called once per video tick.
func (r *Renderer) Frame() []byte {
	r.mu.RLock()
	speaker, role, body := r.speaker, r.role, r.body
	r.mu.RUnlock()

	img := image.NewRGBA(image.Rect(0, 0, r.width, r.height))
	bg := color.RGBA{0x0e, 0x0e, 0x10, 0xff}
	draw.Draw(img, img.Bounds(), &image.Uniform{bg}, image.Point{}, draw.Src)

	// Speaker pill near the top.
	if speaker != "" {
		tagText := formatTagPlain(speaker, role)
		pillBg := roleColor(role)
		drawPill(img, r.tagFace, tagText, 80, 90, pillBg, color.RGBA{0xff, 0xff, 0xff, 0xff})
	}

	// Body text: simple word wrap based on font measurement so different
	// glyph widths still fit. We keep only the trailing N lines.
	if body != "" {
		drawWrappedText(img, r.bodyFace, body, 80, 220, r.width-160, color.RGBA{0xee, 0xee, 0xf1, 0xff}, 14, 11)
	}

	return img.Pix
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

// drawPill paints a filled rectangle behind the text + the text itself.
// We don't bother with rounded corners — straight rectangle reads cleanly at
// these sizes.
func drawPill(dst *image.RGBA, face font.Face, text string, x, y int, bg, fg color.RGBA) {
	if text == "" {
		return
	}
	d := &font.Drawer{Dst: dst, Src: image.NewUniform(fg), Face: face}
	width := d.MeasureString(text).Ceil()
	padX, padY := 24, 14
	metrics := face.Metrics()
	ascent := metrics.Ascent.Ceil()
	descent := metrics.Descent.Ceil()
	rect := image.Rect(x-padX, y-ascent-padY, x+width+padX, y+descent+padY)
	draw.Draw(dst, rect, &image.Uniform{bg}, image.Point{}, draw.Src)
	d.Dot = fixed.P(x, y)
	d.DrawString(text)
}

// drawWrappedText word-wraps text into available width and draws up to
// maxLines lines starting at (x, y). Older lines are dropped from the top.
func drawWrappedText(dst *image.RGBA, face font.Face, text string, x, y, maxWidth int, fg color.RGBA, lineGap, maxLines int) {
	d := &font.Drawer{Dst: dst, Src: image.NewUniform(fg), Face: face}
	words := strings.Fields(text)
	if len(words) == 0 {
		return
	}
	maxFixed := fixed.I(maxWidth)
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
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	metrics := face.Metrics()
	lineH := metrics.Height.Ceil() + lineGap
	for i, line := range lines {
		d.Dot = fixed.P(x, y+i*lineH)
		d.DrawString(line)
	}
}
