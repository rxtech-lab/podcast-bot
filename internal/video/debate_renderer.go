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

// frameDebate composites a frame in CNN-style debate mode: red banner +
// magic-move title, side panels, subtitle card, and lower-third ticker.
// Called from Frame() once the snapshotted state has been read out from
// under r.mu — all timing inputs (mode, modeStart, bodyStart, …) are
// passed in so the function itself takes no locks.
func (r *Renderer) frameDebate(
	topic, phase, speaker, role, body string,
	bodyStart time.Time, bodyDur time.Duration,
	affNames, negNames []string,
	affPos, negPos string,
	clockE, clockT time.Duration,
	userName, userMsg string, userStart, userExpiry time.Time,
	mode stageMode, modeStart time.Time,
) []byte {
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
