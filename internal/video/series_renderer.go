package video

import (
	"fmt"
	"image"
	"image/color"
	"strings"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

// frameSeries composites a frame for the TV-series narration layout.
// Full-bleed scene image (no letterbox bars), no uptitle, no caption
// slab — captions paint outlined directly on the scene, and the only
// chrome is a small top-left identification label (show / season-ep /
// host) that fades out a few seconds in, plus the clock and chat
// ticker. Distinct from framePuzzle so the puzzle chrome stays
// untouched.
func (r *Renderer) frameSeries(
	speaker, role, body string,
	bodyStart time.Time, bodyDur time.Duration,
	userName, userMsg string, userStart, userExpiry time.Time,
	seriesShow string, seriesSeason, seriesEpisode int, seriesHost string,
	seriesLabelStart time.Time,
	sectionText string, sectionStart time.Time, sectionHold bool,
) []byte {
	img := image.NewRGBA(image.Rect(0, 0, r.width, r.height))
	r.drawBackground(img)
	r.drawSeriesOverlay(img, speaker, role, body,
		bodyStart, bodyDur,
		userName, userMsg, userStart, userExpiry,
		seriesShow, seriesSeason, seriesEpisode, seriesHost, seriesLabelStart,
		sectionText, sectionStart, sectionHold)
	return img.Pix
}

// drawSeriesOverlay paints the narration-mode chrome: outlined captions
// at the bottom, the small top-left "Show / S·E / Host" identification
// label (fades out within seriesLabelTotalDuration), an outlined
// character name+role tag when a non-narrator speaker has the floor,
// and the standard clock + chat ticker.
func (r *Renderer) drawSeriesOverlay(img *image.RGBA,
	speaker, role, body string,
	bodyStart time.Time, bodyDur time.Duration,
	userName, userMsg string, userStart, userExpiry time.Time,
	seriesShow string, seriesSeason, seriesEpisode int, seriesHost string,
	seriesLabelStart time.Time,
	sectionText string, sectionStart time.Time, sectionHold bool,
) {
	bodyFG := color.RGBA{0xf2, 0xf4, 0xf8, 0xff}

	const (
		marginX = 80
		// Bottom edge for the outlined caption block. With no letterbox
		// and no quote-card slab the caption sits very close to the
		// bottom edge — anchored just above the clock + ticker band so
		// the painted scene retains as much vertical space as possible.
		captionBottomMargin = 28
	)

	// Outlined caption: paint straight onto the scene, bottom-anchored.
	// Reuses the same wrapper / scroll math as the surface scene's
	// outlined caption so phrasing and timing match. Drawn at full
	// opacity with no fade — narrated drama reads cleaner with hard
	// sentence cuts than a 250 ms cross-dissolve on every swap.
	if strings.TrimSpace(body) != "" {
		qcLeft := marginX
		qcRight := r.width - marginX
		cardH := subtitleCardHeight(r.bodyFace, body, qcLeft, qcRight)
		qcBot := r.height - captionBottomMargin
		qcTop := qcBot - cardH
		drawHBOSubtitleBodyOutlined(img, r.bodyFace, body,
			qcLeft, qcTop, qcRight, qcBot,
			bodyFG, bodyStart, bodyDur)
	}

	// Character name + role tag. The narrator (role == "series-host") is
	// implicit and identified through the top-left label instead. Any
	// other role means a character is speaking — paint name + role as
	// outlined text directly on the scene (no slab, no flag).
	if speaker != "" && role != "" && role != "series-host" {
		drawSeriesCharacterTag(img, r.tagFace, r.panelPosFace,
			speaker, role,
			marginX, r.height-captionBottomMargin-160)
	}

	// Top-left identification label. Eased in (~400 ms) when the episode
	// activates, held at full opacity, then dissolved out — full window
	// is seriesLabelTotalDuration so the whole intro completes within
	// ~15 s.
	//
	// Painted onto an offscreen buffer at α=1 first; the global alpha
	// is applied with one blit so shadow + fill stay internally
	// consistent during the dissolve. Drawing them straight onto the
	// scene with per-color alpha caused the shadow to show through
	// the partially-transparent fill mid-fade and read as a smeared /
	// distorted glyph (the same artifact the puzzle lower-third
	// avoids by buffering then blitting).
	if seriesShow != "" {
		const labelW, labelH = 360, 240
		labelBuf := image.NewRGBA(image.Rect(0, 0, labelW, labelH))
		drawSeriesIDLabel(labelBuf, r.tagFace, r.panelPosFace, r.panelHdrFace,
			seriesShow, seriesSeason, seriesEpisode, seriesHost,
			0, 0)
		drawLabel := func(alpha float64) {
			blitWithGlobalAlphaAt(img, labelBuf, marginX, 60, alpha)
		}
		// seriesLabelStart stays zero until the first speaker arrives
		// (see Renderer.SetState). While the show is still warming up
		// we hold the label at full opacity; once the clock starts the
		// usual fade-in / hold / fade-out runs against that anchor.
		switch {
		case seriesLabelStart.IsZero():
			drawLabel(1)
		case time.Since(seriesLabelStart) < seriesLabelFadeIn:
			fadeIn(drawLabel, seriesLabelStart, seriesLabelFadeIn)
		case time.Since(seriesLabelStart) < seriesLabelTotalDuration-seriesLabelFadeOut:
			drawLabel(1)
		default:
			fadeOut(drawLabel,
				seriesLabelStart.Add(seriesLabelTotalDuration-seriesLabelFadeOut),
				seriesLabelFadeOut)
		}
	}

	// Section banner (recap / main-content) painted directly under the
	// ID label so the audience reads which section is on screen. Same
	// offscreen-buffer + global-alpha-blit trick as the ID label so the
	// drop shadow stays coherent during the dissolve. sectionStart
	// stays zero whenever sectionText == "" (cleared via SetSeriesSec-
	// tionLabel(""), false), so we treat "no start" as "don't paint".
	if sectionText != "" && !sectionStart.IsZero() {
		const (
			bannerW = 900
			bannerH = 80
			// Stack directly under the 240-px ID-label buffer (y=60 in
			// the parent coords).
			bannerY = 60 + 240
		)
		bannerBuf := image.NewRGBA(image.Rect(0, 0, bannerW, bannerH))
		drawSeriesSectionBanner(bannerBuf, r.bodyFace, sectionText, 0, 0)
		drawBanner := func(alpha float64) {
			blitWithGlobalAlphaAt(img, bannerBuf, marginX, bannerY, alpha)
		}
		switch {
		case time.Since(sectionStart) < seriesSectionFadeIn:
			fadeIn(drawBanner, sectionStart, seriesSectionFadeIn)
		case sectionHold:
			drawBanner(1)
		case time.Since(sectionStart) < seriesSectionTotalDuration-seriesSectionFadeOut:
			drawBanner(1)
		default:
			fadeOut(drawBanner,
				sectionStart.Add(seriesSectionTotalDuration-seriesSectionFadeOut),
				seriesSectionFadeOut)
		}
	}

	// Series narration intentionally has no on-screen clock — episodes
	// run for as long as the narration audio plays, with no time-up cut.

	// Chat ticker rides just above the caption band so it doesn't fight
	// with the painted captions.
	if userMsg != "" && time.Now().Before(userExpiry) {
		tickerBot := r.height - captionBottomMargin - 8
		drawChatTicker(img, r.tagFace, r.bodyFace, userName, userMsg,
			0, tickerBot-tickerStripH, r.width, tickerBot,
			userStart)
	}
}

// seriesLabelTotalDuration is the full lifetime of the top-left
// identification label — fade-in + hold + fade-out all complete within
// this window so the imagery takes over within roughly 15 s.
const seriesLabelTotalDuration = 15 * time.Second

// seriesLabelFadeIn is the smoothstep window the label uses when it
// first appears so it doesn't pop.
const seriesLabelFadeIn = 400 * time.Millisecond

// seriesLabelFadeOut is the dissolve at the end of the hold. Kept
// short so the dissolve reads as motion rather than a slow alpha
// drift.
const seriesLabelFadeOut = 1200 * time.Millisecond

// seriesSectionTotalDuration is the lifetime of the main-content
// banner: fade-in + hold + fade-out all complete within this window.
// The recap banner sets sectionHold = true and ignores this value.
const seriesSectionTotalDuration = 30 * time.Second

// seriesSectionFadeIn matches the ID-label fade-in so both top-left
// elements settle in lockstep when the section starts.
const seriesSectionFadeIn = 400 * time.Millisecond

// seriesSectionFadeOut is the dissolve when the main-content banner
// retires. Kept identical to the ID-label fade-out for consistency.
const seriesSectionFadeOut = 1200 * time.Millisecond

// drawSeriesSectionBanner paints a single-line banner (white fill +
// 2 px black drop shadow) at (x, y), used for the recap / main-content
// section subtitles. Always at full opacity — the caller renders into
// an offscreen buffer and applies fade alpha with blitWithGlobalAlphaAt
// so shadow + fill stay coherent during the dissolve.
func drawSeriesSectionBanner(dst *image.RGBA, face font.Face, text string, x, y int) {
	if text == "" {
		return
	}
	white := color.RGBA{0xff, 0xff, 0xff, 0xff}
	shadow := color.RGBA{0x00, 0x00, 0x00, 0xcc}

	m := face.Metrics()
	baseline := y + m.Ascent.Ceil()

	shadowDrawer := &font.Drawer{Dst: dst, Src: image.NewUniform(shadow), Face: face}
	shadowDrawer.Dot = fixed.P(x+2, baseline+2)
	shadowDrawer.DrawString(text)
	fill := &font.Drawer{Dst: dst, Src: image.NewUniform(white), Face: face}
	fill.Dot = fixed.P(x, baseline)
	fill.DrawString(text)
}

// drawSeriesIDLabel paints the small three-row identification at
// (x, y): show name on top, then "S{N} · E{N}", then host name. White
// text with a 2 px black drop shadow so glyphs read against any
// painted scene. Always paints at full opacity — the caller is
// expected to render onto an offscreen buffer and apply fade alpha
// with blitWithGlobalAlphaAt so the shadow + fill stay internally
// consistent during a dissolve. Empty values skip their row.
func drawSeriesIDLabel(dst *image.RGBA,
	showFace, epFace, hostFace font.Face,
	show string, season, episode int, host string,
	x, y int,
) {
	if show == "" {
		return
	}
	white := color.RGBA{0xff, 0xff, 0xff, 0xff}
	shadow := color.RGBA{0x00, 0x00, 0x00, 0xcc}

	stamp := func(face font.Face, text string, sx, sy int) int {
		if text == "" {
			return 0
		}
		shadowDrawer := &font.Drawer{Dst: dst,
			Src: image.NewUniform(shadow), Face: face}
		shadowDrawer.Dot = fixed.P(sx+2, sy+2)
		shadowDrawer.DrawString(text)
		fill := &font.Drawer{Dst: dst,
			Src: image.NewUniform(white), Face: face}
		fill.Dot = fixed.P(sx, sy)
		fill.DrawString(text)
		m := face.Metrics()
		return m.Ascent.Ceil() + m.Descent.Ceil()
	}

	cursor := y
	// Row 1: show name.
	showM := showFace.Metrics()
	cursor += showM.Ascent.Ceil()
	stamp(showFace, show, x, cursor)
	cursor += showM.Descent.Ceil() + 4

	// Row 2: "S{N} · E{N}". Suppressed when both season and episode
	// are zero so a "Show only" episode doesn't paint "S0 · E0".
	if season > 0 || episode > 0 {
		ep := fmt.Sprintf("S%d · E%d", season, episode)
		epM := epFace.Metrics()
		cursor += epM.Ascent.Ceil()
		stamp(epFace, ep, x, cursor)
		cursor += epM.Descent.Ceil() + 6
	}

	// Row 3: host name. Slightly larger than the season-ep so the
	// audience reads who's narrating.
	if host != "" {
		hostM := hostFace.Metrics()
		cursor += hostM.Ascent.Ceil()
		stamp(hostFace, host, x, cursor)
	}
}

// drawSeriesCharacterTag paints "{NAME}\n{ROLE}" as outlined text on
// the scene with no slab / flag background — the user explicitly asked
// for the speaker chrome to drop. Used only for non-narrator speakers
// during series narration.
func drawSeriesCharacterTag(dst *image.RGBA,
	nameFace, roleFace font.Face,
	name, role string, x, y int,
) {
	scaleA := func(a uint8) uint8 { return a }
	white := color.RGBA{0xff, 0xff, 0xff, scaleA(0xff)}
	gold := color.RGBA{0xc8, 0xa4, 0x5a, scaleA(0xff)}
	shadow := color.RGBA{0x00, 0x00, 0x00, scaleA(0xcc)}

	stamp := func(face font.Face, text string, fg color.RGBA, sx, sy int) {
		if text == "" {
			return
		}
		shadowDrawer := &font.Drawer{Dst: dst,
			Src: image.NewUniform(shadow), Face: face}
		shadowDrawer.Dot = fixed.P(sx+2, sy+2)
		shadowDrawer.DrawString(text)
		fill := &font.Drawer{Dst: dst,
			Src: image.NewUniform(fg), Face: face}
		fill.Dot = fixed.P(sx, sy)
		fill.DrawString(text)
	}

	nameM := nameFace.Metrics()
	stamp(nameFace, strings.ToUpper(name), white, x, y+nameM.Ascent.Ceil())
	if role != "" {
		roleY := y + nameM.Ascent.Ceil() + nameM.Descent.Ceil() + 4
		roleM := roleFace.Metrics()
		stamp(roleFace, strings.ToUpper(role), gold, x, roleY+roleM.Ascent.Ceil())
	}
}
