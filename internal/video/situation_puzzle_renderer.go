package video

import (
	"image"
	"image/color"
	"image/draw"
	"strings"
	"time"
	"unicode"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

// framePuzzle composites a frame in cinematic puzzle mode: scene bg
// (already painted by drawBackground) with minimal title chrome at the
// top and a subtitle anchored at the bottom third. No side panels, no
// CNN-style red banner, no lower-third strip.
//
// scene is the active scenes.Scene* name (surface / qa / reveal /
// conclusion / "" when none yet). drawPuzzleOverlay uses it to pick a
// per-scene subtitle treatment — surface paints the caption directly on
// the scene with a black outline, others keep the slab-and-rule look.
//
// speakerStart is when the current speaker first appeared on screen
// (zero when no speaker). The surface-scene path uses it to fade the
// lower-third name plate after the first 30s of the narration so the
// imagery owns the rest of the screen time.
func (r *Renderer) framePuzzle(
	topic, phase, scene, speaker, role, body, surface string,
	bodyStart time.Time, bodyDur time.Duration, speakerStart time.Time,
	clockE, clockT time.Duration,
	userName, userMsg string, userStart, userExpiry time.Time,
) []byte {
	img := image.NewRGBA(image.Rect(0, 0, r.width, r.height))
	r.drawBackground(img)
	r.drawPuzzleOverlay(img, topic, phase, scene, speaker, role, body, surface,
		bodyStart, bodyDur, speakerStart, clockE, clockT,
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
	topic, phase, scene, speaker, role, body, surface string,
	bodyStart time.Time, bodyDur time.Duration, speakerStart time.Time,
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
		// Cinematic-narration scenes (surface, conclusion) drop the slab
		// chrome entirely: the caption paints straight on the painted
		// scene with a black outline, and the speaker name plate fades
		// out 30s in so the imagery owns the rest of the screen. The
		// pivot scenes (qa, reveal) keep the HBO slab look — they're
		// short and the slab carries the documentary-promo feel.
		isCinematic := scene == "surface" || scene == "conclusion"

		var ltTop int
		if hasBody {
			// Auto-fit the card to the wrapped body height so a one-line
			// "是。但更精確地說……" doesn't sit inside an oversized slab.
			// Card anchors at the bottom (just above the letterbox); the
			// lower-third floats above it with a small gap.
			cardH := subtitleCardHeight(r.bodyFace, body, qcLeft, qcRight)
			qcBot := bottomBarTop - 18
			qcTop := qcBot - cardH

			if isCinematic {
				drawHBOSubtitleBodyOutlined(img, r.bodyFace, body,
					qcLeft, qcTop, qcRight, qcBot,
					bodyFG, bodyStart, bodyDur)
			} else {
				drawHBOQuoteCard(img, qcLeft, qcTop, qcRight, qcBot)
				drawHBOSubtitleBody(img, r.bodyFace, body,
					qcLeft, qcTop, qcRight, qcBot,
					bodyFG, bodyStart, bodyDur)
			}

			ltTop = qcTop - ltGap - ltH
		} else {
			// No body → no chrome. Park the name plate where the card's
			// bottom edge would have been so the speaker label stays at
			// roughly the same eye-line and doesn't pop on every gap.
			ltTop = bottomBarTop - 18 - ltH
		}

		ltAlpha := 1.0
		ltDy := 0
		if isCinematic {
			// Cinematic narration: lower-third name plate fades out after
			// the first ~22 s so the imagery has the screen for the rest
			// of the storytelling. Eased curve + a small lift so the
			// plate departs upward rather than dimming in place.
			ltAlpha, ltDy = surfaceLowerThirdFade(speakerStart)
		}
		if ltAlpha > 0 {
			drawHBOLowerThirdAlpha(img,
				ltLeft, ltTop+ltDy, ltLeft+ltW, ltTop+ltH+ltDy,
				ltGoldW, speaker, hboPuzzleRoleLabel(role),
				r.tagFace, r.panelPosFace, ltAlpha)
		}

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

// stripPuzzleSubtitlePunct removes punctuation, pause indicators and
// stray symbols from a puzzle subtitle body so the cinematic single-row
// caption shows only the readable words. Targets:
//   - CJK fullstop / comma / pauses: 。 ， 、 ； ： ！ ？ 「 」 『 』 （ ）《 》 【 】
//   - CJK pause/ellipsis sequences: …… —— ···
//   - Latin punctuation: . , ; : ! ? — - … " ' ( ) [ ] { }
//
// Stripping happens before line wrapping so a residue line that would
// otherwise contain only "。" or ", " disappears from the display
// entirely. Letters / digits / CJK glyphs are kept verbatim. Whitespace
// is collapsed to a single space so the wrapper still has word
// boundaries for Latin text.
func stripPuzzleSubtitlePunct(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if isPuzzleSubtitleWordRune(r) {
			b.WriteRune(r)
			prevSpace = false
			continue
		}
		// Map any non-word rune to a single collapsed space so Latin
		// "Hello, world" doesn't become "Helloworld" while CJK still
		// reads cleanly (CJK has no inter-glyph spaces, so the trim at
		// the end strips trailing/leading spaces and consecutive spaces
		// inside CJK runs are collapsed to one each).
		if !prevSpace {
			b.WriteByte(' ')
			prevSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

// isPuzzleSubtitleWordRune reports whether r is a content rune that
// should appear in the cinematic puzzle caption — letter, digit, or any
// glyph in the CJK Unified Ideographs (and adjacent) blocks. Colons
// (`:` / `：`) are also kept because they're typically structural
// (e.g. "今晚的题目：归途" or "Alice: …") and dropping them collapses
// otherwise-meaningful labels. Everything else (sentence-final
// punctuation, pause indicators, symbols, control, whitespace) is
// dropped or mapped to a separator by stripPuzzleSubtitlePunct.
func isPuzzleSubtitleWordRune(r rune) bool {
	if unicode.IsLetter(r) || unicode.IsDigit(r) {
		return true
	}
	// Keep colons — both ASCII and the fullwidth CJK variant.
	if r == ':' || r == '：' {
		return true
	}
	// IsLetter already covers Han ideographs in modern Go, but be
	// explicit so a future stdlib quirk doesn't silently drop them.
	switch {
	case r >= 0x4E00 && r <= 0x9FFF, // CJK Unified Ideographs
		r >= 0x3400 && r <= 0x4DBF,   // CJK Unified Ext A
		r >= 0x20000 && r <= 0x2A6DF, // CJK Unified Ext B
		r >= 0x3040 && r <= 0x309F,   // Hiragana
		r >= 0x30A0 && r <= 0x30FF,   // Katakana
		r >= 0xAC00 && r <= 0xD7AF:   // Hangul syllables
		return true
	}
	return false
}

// lineWeights returns the dwell-time weight for each wrapped subtitle
// line. Weight is the count of content runes — letters, digits, CJK
// glyphs — so a line of "是" weighs 1 while a line of 30 characters
// weighs 30, and the audio time gets distributed proportionally instead
// of equally. A line whose content count is 0 (theoretical edge case
// after stripPuzzleSubtitlePunct) gets a min weight of 1 so it still
// dwells briefly on screen.
func lineWeights(lines []string) []int {
	out := make([]int, len(lines))
	for i, ln := range lines {
		w := 0
		for _, r := range ln {
			if isPuzzleSubtitleWordRune(r) {
				w++
			}
		}
		if w < 1 {
			w = 1
		}
		out[i] = w
	}
	return out
}

// subtitleCardHeight returns the snug height of the speaker-mode quote
// card given the wrapped body, mirroring the padding/line-metric math
// inside drawHBOSubtitleBody so the chrome wraps the text instead of
// floating inside an oversized slab. Caps the visible rows at
// puzzleSubtitleMaxLines so longer passages clip to that height and
// rely on drawHBOSubtitleBody's vertical scroll for overflow. Body is
// punctuation-stripped before wrapping to match what drawHBOSubtitleBody
// will actually render.
func subtitleCardHeight(face font.Face, body string, x0, x1 int) int {
	body = stripPuzzleSubtitlePunct(body)
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
// with body text, top-anchored.
//
// The body is first run through stripPuzzleSubtitlePunct so periods,
// pause indicators (……, ——), commas and other punctuation don't show on
// the cinematic single-row caption — only the readable words remain.
//
// Long passages auto-scroll vertically with stepped jumps. Each wrapped
// line dwells for a duration proportional to its content length (rune
// count of letters/digits/CJK glyphs after the punctuation strip), so a
// short tail line that would otherwise sit on screen as long as a
// content-heavy line gets a proportionally short slot.
func drawHBOSubtitleBody(dst *image.RGBA, face font.Face, body string,
	x0, y0, x1, y1 int, fg color.RGBA,
	bodyStart time.Time, bodyAudioDuration time.Duration,
) {
	body = stripPuzzleSubtitlePunct(body)
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

	// Stepped vertical scroll, with PER-LINE dwell weighted by line
	// content length. Equal-time slotting (the previous behaviour) gave a
	// 30-character line and a 2-character tail line the same screen time
	// — bad for reading flow and especially noticeable when wrap leaves a
	// short residue on the last row. Now each step k dwells for
	// weights[k] / sum(weights) * audioDur. Weights are the rune count of
	// each line's content runes (letters/digits/CJK glyphs only — the
	// punctuation-stripped body still excludes them, but lineWeight
	// double-checks). Single-rune lines get a min weight of 1 so an
	// edge-case all-punctuation line still appears briefly.
	scrollPx := 0
	if overflow && !bodyStart.IsZero() && bodyAudioDuration > 0 {
		overflowSlots := len(lines) - maxLines
		weights := lineWeights(lines)
		var totalW int64
		for _, w := range weights {
			totalW += int64(w)
		}
		if totalW > 0 {
			elapsed := time.Since(bodyStart)
			if elapsed < 0 {
				elapsed = 0
			}
			step := overflowSlots
			var acc int64
			for k := 0; k <= overflowSlots; k++ {
				acc += int64(weights[k])
				boundary := time.Duration(int64(bodyAudioDuration) * acc / totalW)
				if elapsed < boundary {
					step = k
					break
				}
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

// drawHBOLowerThirdAlpha paints the lower-third with a global alpha
// scaler (0..1) so callers can fade the entire plate in or out. Used by
// the surface-scene path to dissolve the name plate after the first 30s
// of the host's narration. alpha values <= 0 are treated as "skip"; the
// caller should usually short-circuit before calling.
func drawHBOLowerThirdAlpha(dst *image.RGBA, x0, y0, x1, y1, goldW int,
	name, role string, nameFace, roleFace font.Face, alpha float64) {
	if alpha <= 0 {
		return
	}
	if alpha > 1 {
		alpha = 1
	}
	scaleA := func(a uint8) uint8 { return uint8(float64(a) * alpha) }

	bg := color.RGBA{0x07, 0x07, 0x0a, scaleA(0xee)}
	box := image.Rect(x0, y0, x1, y1)
	draw.Draw(dst, box, &image.Uniform{bg}, image.Point{}, draw.Over)
	flag := image.Rect(x0, y0, x0+goldW, y1)
	gold := color.RGBA{hboGold.R, hboGold.G, hboGold.B, scaleA(hboGold.A)}
	draw.Draw(dst, flag, &image.Uniform{gold}, image.Point{}, draw.Over)

	// Name — bold white, slightly larger.
	nameY := y0 + 38
	d := &font.Drawer{Dst: dst,
		Src:  image.NewUniform(color.RGBA{0xff, 0xff, 0xff, scaleA(0xff)}),
		Face: nameFace}
	d.Dot.X = fixed.I(x0 + goldW + 18)
	d.Dot.Y = fixed.I(nameY)
	d.DrawString(strings.ToUpper(name))

	// Role — gold, smaller.
	if role != "" {
		roleY := y0 + 64
		dr := &font.Drawer{Dst: dst,
			Src: image.NewUniform(gold), Face: roleFace}
		dr.Dot.X = fixed.I(x0 + goldW + 18)
		dr.Dot.Y = fixed.I(roleY)
		dr.DrawString(strings.ToUpper(role))
	}
}

// surfaceFadeHoldDuration is how long the surface-scene lower-third name
// plate stays at full opacity before it begins to fade. The audience gets
// a clean read of "who's narrating · HOST" early in the surface story,
// then the chrome dissolves so the imagery has the screen.
const surfaceFadeHoldDuration = 22 * time.Second

// surfaceFadeOutDuration is the eased fade-out window applied immediately
// after surfaceFadeHoldDuration. Combined with the hold, the plate is
// fully gone at speakerStart + ~30 s. Lengthened from 5 s → 8 s so the
// dissolve has room to breathe; the curve below distributes the work
// non-linearly so the plate doesn't plateau in the middle.
const surfaceFadeOutDuration = 8 * time.Second

// surfaceFadeSlideUp is the maximum upward displacement the plate
// gathers as it fades. The final pixel offset is `surfaceFadeSlideUp *
// (1 - α)` so the slide is locked to the same easing curve as the
// alpha — feels like the plate is being lifted away rather than just
// dimmed in place. 14 px is enough to read as motion without leaving
// the safe area.
const surfaceFadeSlideUp = 14

// surfaceLowerThirdFade returns (alpha, dy) for the surface-scene name
// plate at the current frame given when the speaker first appeared.
// alpha is in [0,1] with cubic ease-in-out applied so the dissolve
// starts gently, accelerates through the middle, and settles smoothly
// rather than the previous linear ramp (which clipped at the start /
// end and read as a flat slide). dy is the vertical offset (positive =
// move upward / negative numbers in the canvas Y axis); rendered
// alongside the fade so the plate departs as it dims.
//
// A zero speakerStart (no speaker / not yet recorded) returns (1, 0)
// so the chrome shows from the first frame and the fade clock starts
// once SetState records a real time.
func surfaceLowerThirdFade(speakerStart time.Time) (alpha float64, dy int) {
	if speakerStart.IsZero() {
		return 1, 0
	}
	elapsed := time.Since(speakerStart)
	if elapsed < surfaceFadeHoldDuration {
		return 1, 0
	}
	if elapsed >= surfaceFadeHoldDuration+surfaceFadeOutDuration {
		return 0, -surfaceFadeSlideUp
	}
	t := float64(elapsed-surfaceFadeHoldDuration) / float64(surfaceFadeOutDuration)
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	eased := easeInOutCubic(t)
	alpha = 1 - eased
	dy = -int(float64(surfaceFadeSlideUp)*eased + 0.5)
	return alpha, dy
}

// drawHBOSubtitleBodyOutlined paints the puzzle subtitle text directly
// on the scene background — no quote-card slab — with a black outline
// (1-2px stroke) so glyphs stay legible against any painted scene.
// Mirrors drawHBOSubtitleBody's wrapping, weighted-scroll math, and
// punctuation strip so the on-screen text size is identical between the
// two paths and a phase change between scenes doesn't cause subtle
// reflow.
func drawHBOSubtitleBodyOutlined(dst *image.RGBA, face font.Face, body string,
	x0, y0, x1, y1 int, fg color.RGBA,
	bodyStart time.Time, bodyAudioDuration time.Duration,
) {
	body = stripPuzzleSubtitlePunct(body)
	if body == "" {
		return
	}
	const (
		padX     = 32
		padY     = 22
		lineGap  = 10
		strokePx = 2
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

	scrollPx := 0
	if overflow && !bodyStart.IsZero() && bodyAudioDuration > 0 {
		overflowSlots := len(lines) - maxLines
		weights := lineWeights(lines)
		var totalW int64
		for _, w := range weights {
			totalW += int64(w)
		}
		if totalW > 0 {
			elapsed := time.Since(bodyStart)
			if elapsed < 0 {
				elapsed = 0
			}
			step := overflowSlots
			var acc int64
			for k := 0; k <= overflowSlots; k++ {
				acc += int64(weights[k])
				boundary := time.Duration(int64(bodyAudioDuration) * acc / totalW)
				if elapsed < boundary {
					step = k
					break
				}
			}
			if step > overflowSlots {
				step = overflowSlots
			}
			scrollPx = step * lineH
		}
	}

	clipRect := image.Rect(innerL, innerT, innerR, innerB).Intersect(dst.Bounds())
	clip, _ := dst.SubImage(clipRect).(*image.RGBA)
	if clip == nil {
		clip = dst
	}

	outline := color.RGBA{0x00, 0x00, 0x00, 0xff}
	stroke := &font.Drawer{Dst: clip, Src: image.NewUniform(outline), Face: face}
	fill := &font.Drawer{Dst: clip, Src: image.NewUniform(fg), Face: face}

	for i, ln := range lines {
		y := innerT + asc + i*lineH - scrollPx
		if y+desc < innerT || y-asc > innerB {
			continue
		}
		w := stroke.MeasureString(ln).Ceil()
		x := innerL + (innerW-w)/2

		// 8-direction stroke: stamp the glyph at every offset within
		// strokePx of (x, y). Cheap (8 extra DrawString calls per line)
		// and visually equivalent to a Gaussian-feathered outline at this
		// glyph size.
		for dy := -strokePx; dy <= strokePx; dy++ {
			for dx := -strokePx; dx <= strokePx; dx++ {
				if dx == 0 && dy == 0 {
					continue
				}
				stroke.Dot = fixed.P(x+dx, y+dy)
				stroke.DrawString(ln)
			}
		}
		fill.Dot = fixed.P(x, y)
		fill.DrawString(ln)
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
