package video

import (
	"image"
	"image/color"
	"image/draw"
	"strings"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

// framePuzzle composites a frame in cinematic puzzle mode: scene bg
// (already painted by drawBackground) with minimal title chrome at the
// top and a subtitle anchored at the bottom third. No side panels, no
// CNN-style red banner, no lower-third strip.
func (r *Renderer) framePuzzle(
	topic, phase, speaker, role, body, surface string,
	bodyStart time.Time, bodyDur time.Duration,
	clockE, clockT time.Duration,
	userName, userMsg string, userStart, userExpiry time.Time,
) []byte {
	img := image.NewRGBA(image.Rect(0, 0, r.width, r.height))
	r.drawBackground(img)
	r.drawPuzzleOverlay(img, topic, phase, speaker, role, body, surface,
		bodyStart, bodyDur, clockE, clockT,
		userName, userMsg, userStart, userExpiry)
	return img.Pix
}

// hboGold is the premium accent color used across the puzzle layout —
// thin rules, the lower-third flag, and the quote-card top border. Picked
// to read as "premium documentary" against any scene image.
var hboGold = color.RGBA{0xc8, 0xa4, 0x5a, 0xff}

// puzzleSubtitleMaxLines caps the visible row count of the puzzle
// subtitle quote card to a single row — single-line cinematic captions
// with the speaker plate stacked above. Bodies that wrap to more rows
// clip to one row visually and auto-scroll vertically via
// drawHBOSubtitleBody so the rest of the passage glides through that
// single line without ever growing the chrome.
const puzzleSubtitleMaxLines = 1

// drawPuzzleOverlay paints the HBO reality-show layout on top of the
// scene bg already in img:
//
//	┌──────────────────────────────────────┐
//	│ ▒ letterbox bar ▒ (cinematic feel)   │
//	│ ▓ title scrim ─ topic title ─ phase  │
//	│                                      │
//	│            scene bg                  │
//	│                                      │
//	│  ┃ NAME            ┌──────────────┐  │  ← lower-third (gold flag)
//	│  ┃ role            │ subtitle…    │  │  ← quote card  (gold rule)
//	│ ▒ letterbox bar ▒                    │
//	└──────────────────────────────────────┘
//
// All chrome is procedural — solid black slabs + thin gold rules — so it
// stays crisp at any output resolution. Scene image content remains the
// only painterly element, which is exactly the HBO documentary look.
func (r *Renderer) drawPuzzleOverlay(img *image.RGBA,
	topic, phase, speaker, role, body, surface string,
	bodyStart time.Time, bodyDur time.Duration,
	clockE, clockT time.Duration,
	userName, userMsg string, userStart, userExpiry time.Time,
) {
	titleFG := color.RGBA{0xff, 0xff, 0xff, 0xff}
	dimFG := color.RGBA{0xc8, 0xa4, 0x5a, 0xff} // gold for dim/clock-elapsed
	bodyFG := color.RGBA{0xf2, 0xf4, 0xf8, 0xff}

	// Cinematic letterbox bars top + bottom. Thin enough not to crush the
	// scene image, thick enough to anchor the eye.
	const letterboxH = 60
	black := color.RGBA{0, 0, 0, 0xff}
	draw.Draw(img, image.Rect(0, 0, r.width, letterboxH),
		&image.Uniform{black}, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(0, r.height-letterboxH, r.width, r.height),
		&image.Uniform{black}, image.Point{}, draw.Src)

	// Single hairline gold rule under the top bar — the most elemental
	// HBO-promo cue.
	draw.Draw(img, image.Rect(0, letterboxH, r.width, letterboxH+2),
		&image.Uniform{hboGold}, image.Point{}, draw.Src)

	// Topic title centered inside the top letterbox bar. Suppressed in
	// idle mode (speaker == "") because the idle card below carries the
	// title as its main content — drawing it twice reads as redundant
	// chrome and competes with the centered idle card for the eye.
	if topic != "" && speaker != "" {
		drawCenteredText(img, r.titleFace, topic, r.width/2, 44, titleFG)
	}

	// Phase chip floats just below the gold rule. Gold-on-black instead
	// of red CNN-style — matches the HBO palette.
	if phase != "" {
		pill := image.NewRGBA(image.Rect(0, 0, r.width, 60))
		drawCenteredPill(pill, r.phaseFace, phaseLabel(phase),
			r.width/2, 30, hboGold, color.RGBA{0x07, 0x07, 0x0a, 0xff})
		blitWithGlobalAlphaAt(img, pill, 0, letterboxH+12, 1)
	}

	// HBO lower-third name plate + quote card. Stacked above the bottom
	// letterbox bar with the name plate ON TOP of the subtitle card so a
	// viewer sees "who's talking" before the words land. The card only
	// renders when there's actual body text — otherwise the lower-third
	// floats alone above the letterbox so the bottom of the frame doesn't
	// carry an empty slab during inter-sentence gaps.
	const (
		ltMarginX = 80
		ltW       = 420
		ltH       = 86
		ltGoldW   = 6 // vertical gold flag width
		ltGap     = 14
	)
	bottomBarTop := r.height - letterboxH
	ltLeft := ltMarginX

	qcLeft := ltMarginX
	qcRight := r.width - ltMarginX

	switch {
	case speaker != "":
		hasBody := strings.TrimSpace(body) != ""

		var ltTop int
		if hasBody {
			// Auto-fit the card to the wrapped body height so a one-line
			// "是。但更精確地說……" doesn't sit inside an oversized slab.
			// Card anchors at the bottom (just above the letterbox); the
			// lower-third floats above it with a small gap.
			cardH := subtitleCardHeight(r.bodyFace, body, qcLeft, qcRight)
			qcBot := bottomBarTop - 18
			qcTop := qcBot - cardH

			drawHBOQuoteCard(img, qcLeft, qcTop, qcRight, qcBot)
			drawHBOSubtitleBody(img, r.bodyFace, body,
				qcLeft, qcTop, qcRight, qcBot,
				bodyFG, bodyStart, bodyDur)

			ltTop = qcTop - ltGap - ltH
		} else {
			// No body → no chrome. Park the name plate where the card's
			// bottom edge would have been so the speaker label stays at
			// roughly the same eye-line and doesn't pop on every gap.
			ltTop = bottomBarTop - 18 - ltH
		}

		drawHBOLowerThird(img, ltLeft, ltTop, ltLeft+ltW, ltTop+ltH,
			ltGoldW, speaker, hboPuzzleRoleLabel(role),
			r.tagFace, r.panelPosFace)

	case topic != "":
		// Idle puzzle frame (no one speaking yet — typically while Gemini
		// is still generating scene bgs). The card is sized snugly around
		// the single-line title and floated to the upper-mid of the scene
		// area so the framing reads as a movie-poster title rather than
		// a half-empty subtitle slab anchored to the bottom.
		const (
			idlePadTop = 12 // breathing room above title glyph
			idlePadBot = 18 // larger so descenders sit comfortably
		)
		titleM := r.titleFace.Metrics()
		titleAsc := titleM.Ascent.Ceil()
		titleDesc := titleM.Descent.Ceil()
		idleCardH := idlePadTop + titleAsc + titleDesc + idlePadBot
		// Vertical anchor: 40% down the visible scene area (between the
		// two letterbox bars). Reads higher than dead-center, which is
		// where a documentary's "title card" usually lands.
		sceneTop := letterboxH
		sceneBot := r.height - letterboxH
		idleCardCenter := sceneTop + (sceneBot-sceneTop)*4/10
		idleQcTop := idleCardCenter - idleCardH/2
		idleQcBot := idleQcTop + idleCardH

		drawHBOQuoteCard(img, qcLeft, idleQcTop, qcRight, idleQcBot)

		const labelText = "今日海龜湯  ·  TODAY'S PUZZLE"
		drawCenteredPill(img, r.phaseFace, labelText,
			(qcLeft+qcRight)/2, idleQcTop-22,
			hboGold, color.RGBA{0x07, 0x07, 0x0a, 0xff})

		// Single-line title, top-anchored within the card (less wasted
		// vertical space than calling drawHBOSubtitleBody, which uses a
		// 22px symmetric pad and centers via line metrics).
		titleY := idleQcTop + idlePadTop + titleAsc
		drawCenteredText(img, r.titleFace, topic,
			(qcLeft+qcRight)/2, titleY, bodyFG)

		_ = surface // surface text intentionally unused here
	}

	// Clock floats in the bottom-right corner of the bottom letterbox.
	if clockE > 0 || clockT > 0 {
		drawClockPill(img, r.clockFace, clockE, clockT,
			r.width-120, r.height-letterboxH/2, titleFG, dimFG)
	}

	// Chat ticker rides just above the bottom letterbox bar so it
	// doesn't fight with the lower-third or the quote card.
	if userMsg != "" && time.Now().Before(userExpiry) {
		drawChatTicker(img, r.tagFace, r.bodyFace, userName, userMsg,
			0, r.height-letterboxH-tickerStripH, r.width,
			r.height-letterboxH,
			userStart)
	}
}

// subtitleCardHeight returns the snug height of the speaker-mode quote
// card given the wrapped body, mirroring the padding/line-metric math
// inside drawHBOSubtitleBody so the chrome wraps the text instead of
// floating inside an oversized slab. Caps the visible rows at
// puzzleSubtitleMaxLines so longer passages clip to that height and
// rely on drawHBOSubtitleBody's vertical scroll for overflow.
func subtitleCardHeight(face font.Face, body string, x0, x1 int) int {
	const (
		padX    = 32
		padY    = 22
		lineGap = 10
		// minH keeps the chrome readable when the body is empty or a
		// single-character ack — the gold top rule + a short text line
		// still need vertical space to read as a card and not a hairline.
		minH = 84
	)
	innerW := (x1 - x0) - 2*padX
	if innerW < 1 {
		innerW = 1
	}
	lines := wrapLines(face, body, innerW)
	if len(lines) == 0 {
		return minH
	}
	m := face.Metrics()
	asc, desc := m.Ascent.Ceil(), m.Descent.Ceil()
	lineH := m.Height.Ceil() + lineGap
	if lineH <= 0 {
		lineH = asc + desc + lineGap
	}
	n := len(lines)
	if n > puzzleSubtitleMaxLines {
		n = puzzleSubtitleMaxLines
	}
	// Match the innerH budget drawHBOSubtitleBody computes maxLines from
	// (innerH / lineH), so a 2-line wrap always satisfies maxLines >= 2
	// and doesn't trigger scroll prematurely.
	h := 2*padY + n*lineH
	if h < minH {
		h = minH
	}
	return h
}

// drawHBOQuoteCard paints the subtitle slab: solid black with one thin
// gold rule along the top edge.
func drawHBOQuoteCard(dst *image.RGBA, x0, y0, x1, y1 int) {
	box := image.Rect(x0, y0, x1, y1)
	draw.Draw(dst, box, &image.Uniform{color.RGBA{0x07, 0x07, 0x0a, 0xee}},
		image.Point{}, draw.Over)
	rule := image.Rect(x0, y0, x1, y0+3)
	draw.Draw(dst, rule, &image.Uniform{hboGold}, image.Point{}, draw.Src)
}

// drawHBOSubtitleBody paints wrapped body text inside the puzzle's quote
// card. Unlike drawSubtitle (which auto-sizes a small inner content box
// and reserves space for a role pill), this fills the full chrome rect
// with body text, top-anchored. Long passages auto-scroll vertically with
// stepped jumps: with n wrapped lines and maxLines visible at once, there
// are (n - maxLines + 1) distinct visible window positions (line 0, 1, …,
// n - maxLines). The audio duration is split evenly across those
// positions so each position dwells for audioDuration/(n - maxLines + 1)
// — including the final position, which would otherwise only appear at
// t = audioDuration the moment the next sentence replaces the body.
func drawHBOSubtitleBody(dst *image.RGBA, face font.Face, body string,
	x0, y0, x1, y1 int, fg color.RGBA,
	bodyStart time.Time, bodyAudioDuration time.Duration,
) {
	if body == "" {
		return
	}
	const (
		padX    = 32
		padY    = 22
		lineGap = 10
	)

	innerL := x0 + padX
	innerT := y0 + padY
	innerR := x1 - padX
	innerB := y1 - padY
	innerW := innerR - innerL
	innerH := innerB - innerT
	if innerW <= 0 || innerH <= 0 {
		return
	}

	metrics := face.Metrics()
	asc, desc := metrics.Ascent.Ceil(), metrics.Descent.Ceil()
	lineH := metrics.Height.Ceil() + lineGap
	if lineH <= 0 {
		lineH = asc + desc + lineGap
	}

	maxLines := innerH / lineH
	if maxLines < 1 {
		maxLines = 1
	}
	if maxLines > puzzleSubtitleMaxLines {
		maxLines = puzzleSubtitleMaxLines
	}

	lines := wrapLines(face, body, innerW)
	overflow := len(lines) > maxLines

	// Stepped vertical scroll. With n wrapped lines and maxLines visible,
	// there are (n - maxLines + 1) distinct visible window positions
	// (line 0 at the top through line n - maxLines at the top). Splitting
	// bodyAudioDuration evenly across those positions gives each one a
	// dwell of audioDuration/(n - maxLines + 1), and the scroll position
	// snaps one lineH forward at every slot boundary. The final position
	// is reached at t = audioDuration * (n - maxLines)/(n - maxLines + 1)
	// so the last line is read for one full slot, not just the moment
	// before the next sentence lands.
	scrollPx := 0
	if overflow && !bodyStart.IsZero() && bodyAudioDuration > 0 {
		overflowSlots := len(lines) - maxLines
		slotDuration := bodyAudioDuration / time.Duration(overflowSlots+1)
		if slotDuration > 0 {
			step := int(time.Since(bodyStart) / slotDuration)
			if step < 0 {
				step = 0
			}
			if step > overflowSlots {
				step = overflowSlots
			}
			scrollPx = step * lineH
		}
	}

	// Clip to the inner area so glyphs scrolling past the top/bottom
	// edges don't bleed outside the chrome.
	clipRect := image.Rect(innerL, innerT, innerR, innerB).Intersect(dst.Bounds())
	clip, _ := dst.SubImage(clipRect).(*image.RGBA)
	if clip == nil {
		clip = dst
	}
	d := &font.Drawer{Dst: clip, Src: image.NewUniform(fg), Face: face}

	for i, ln := range lines {
		y := innerT + asc + i*lineH - scrollPx
		if y+desc < innerT || y-asc > innerB {
			continue
		}
		w := d.MeasureString(ln).Ceil()
		// Centered horizontally inside the chrome — reads as a confessional
		// pull-quote rather than a left-aligned caption.
		x := innerL + (innerW-w)/2
		d.Dot = fixed.P(x, y)
		d.DrawString(ln)
	}
}

// drawHBOLowerThird paints the HBO-style speaker name plate: solid black
// rectangle with a gold vertical flag on the left edge, the speaker
// name in bold caps, and a smaller gold subtitle row for role.
func drawHBOLowerThird(dst *image.RGBA, x0, y0, x1, y1, goldW int,
	name, role string, nameFace, roleFace font.Face) {
	box := image.Rect(x0, y0, x1, y1)
	draw.Draw(dst, box, &image.Uniform{color.RGBA{0x07, 0x07, 0x0a, 0xee}},
		image.Point{}, draw.Over)
	flag := image.Rect(x0, y0, x0+goldW, y1)
	draw.Draw(dst, flag, &image.Uniform{hboGold}, image.Point{}, draw.Src)

	// Name — bold white, slightly larger.
	nameY := y0 + 38
	d := &font.Drawer{Dst: dst,
		Src:  image.NewUniform(color.RGBA{0xff, 0xff, 0xff, 0xff}),
		Face: nameFace}
	d.Dot.X = fixed.I(x0 + goldW + 18)
	d.Dot.Y = fixed.I(nameY)
	d.DrawString(strings.ToUpper(name))

	// Role — gold, smaller.
	if role != "" {
		roleY := y0 + 64
		dr := &font.Drawer{Dst: dst,
			Src: image.NewUniform(hboGold), Face: roleFace}
		dr.Dot.X = fixed.I(x0 + goldW + 18)
		dr.Dot.Y = fixed.I(roleY)
		dr.DrawString(strings.ToUpper(role))
	}
}

// hboPuzzleRoleLabel maps the puzzle's internal roles to short
// HBO-promo-style labels for the lower-third (e.g. "出題者 · HOST").
func hboPuzzleRoleLabel(role string) string {
	switch role {
	case "puzzle-host":
		return "出題者 · HOST"
	case "player":
		return "解題者 · CONTESTANT"
	case "viewer":
		return "觀眾 · GUEST"
	}
	return role
}
