package video

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

type audioBookConversationSegment struct {
	Speaker string
	Text    string
	Seconds float64
}

func renderConversationalAudioBookVideo(outPath, audioPath, vttPath string,
	imagePaths []string, res Resolution, opts AudioBookVideoOptions, audioSeconds float64,
) error {
	w, h := outputDims(res)
	rend, err := newRenderer(w, h)
	if err != nil {
		return fmt.Errorf("render audiobook conversational video: renderer: %w", err)
	}
	rend.SetTopic(opts.Title)
	backgrounds, err := loadAudioBookBackgrounds(imagePaths)
	if err != nil {
		return err
	}
	avatars := loadAudioBookAvatars(opts.Avatars)
	segments := buildAudioBookConversationSegments(opts, audioSeconds)
	if len(segments) == 0 {
		return fmt.Errorf("render audiobook conversational video: no transcript lines")
	}
	backgroundStarts := audioBookImageStarts(opts.ImageOffsets, len(backgrounds), audioSeconds)
	backgroundIndices := audioBookConversationBackgroundIndices(segments, backgroundStarts)

	frameDir := outPath + ".frames"
	if err := os.MkdirAll(frameDir, 0o755); err != nil {
		return fmt.Errorf("render audiobook conversational video: mkdir frames: %w", err)
	}
	defer os.RemoveAll(frameDir)

	var list strings.Builder
	for i, seg := range segments {
		img := rend.renderAudioBookConversationFrame(
			backgrounds[backgroundIndices[i]],
			audioBookConversationCast(opts, seg.Speaker),
			avatars,
			seg,
		)
		framePath := filepath.Join(frameDir, fmt.Sprintf("frame_%04d.png", i))
		if err := writePNGFile(framePath, img); err != nil {
			return fmt.Errorf("render audiobook conversational video: write frame %d: %w", i, err)
		}
		abs, aerr := filepath.Abs(framePath)
		if aerr != nil {
			abs = framePath
		}
		fmt.Fprintf(&list, "file '%s'\nduration %.3f\n", concatEscape(abs), seg.Seconds)
	}
	last := filepath.Join(frameDir, fmt.Sprintf("frame_%04d.png", len(segments)-1))
	lastAbs, _ := filepath.Abs(last)
	fmt.Fprintf(&list, "file '%s'\n", concatEscape(lastAbs))

	listPath := outPath + ".concat.txt"
	if err := os.WriteFile(listPath, []byte(list.String()), 0o644); err != nil {
		return fmt.Errorf("render audiobook conversational video: write concat list: %w", err)
	}
	defer os.Remove(listPath)

	hasSubs := false
	if vttPath != "" {
		if _, serr := os.Stat(vttPath); serr == nil {
			hasSubs = true
		}
	}
	args := []string{"-y", "-f", "concat", "-safe", "0", "-i", listPath, "-i", audioPath}
	if hasSubs {
		args = append(args, "-i", vttPath)
	}
	args = append(args, "-map", "0:v", "-map", "1:a")
	if hasSubs {
		args = append(args, "-map", "2:s")
	}
	args = append(args,
		"-vf", "fps=25,format=yuv420p",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "128k",
		// No -shortest: never truncate the narration to the frame track.
	)
	if hasSubs {
		args = append(args, "-c:s", "mov_text")
		args = appendSubtitleTrackMetadata(args, []SubtitleTrack{{
			Language: opts.Language,
			Default:  true,
		}})
	}
	args = append(args, "-movflags", "+faststart", outPath)

	cmd := exec.Command("ffmpeg", args...)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("render audiobook conversational video: ffmpeg: %w", err)
	}
	return nil
}

func loadAudioBookBackgrounds(paths []string) ([]*image.RGBA, error) {
	out := make([]*image.RGBA, 0, len(paths))
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			return nil, fmt.Errorf("render audiobook conversational video: open image %s: %w", p, err)
		}
		src, _, err := image.Decode(f)
		_ = f.Close()
		if err != nil {
			return nil, fmt.Errorf("render audiobook conversational video: decode image %s: %w", p, err)
		}
		b := src.Bounds()
		rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		draw.Draw(rgba, rgba.Bounds(), src, b.Min, draw.Src)
		out = append(out, rgba)
	}
	return out, nil
}

func loadAudioBookAvatars(in []AudioBookVideoAvatar) map[string]*image.RGBA {
	out := make(map[string]*image.RGBA)
	for _, avatar := range in {
		name := normalizeAudioBookSpeakerName(avatar.Name)
		if name == "" || avatar.Path == "" {
			continue
		}
		f, err := os.Open(avatar.Path)
		if err != nil {
			continue
		}
		src, _, err := image.Decode(f)
		_ = f.Close()
		if err != nil {
			continue
		}
		b := src.Bounds()
		rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		draw.Draw(rgba, rgba.Bounds(), src, b.Min, draw.Src)
		out[name] = rgba
	}
	return out
}

func buildAudioBookConversationSegments(opts AudioBookVideoOptions, audioSeconds float64) []audioBookConversationSegment {
	lines := make([]AudioBookVideoLine, 0, len(opts.Lines))
	for _, line := range opts.Lines {
		if strings.TrimSpace(line.Text) == "" {
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return nil
	}
	totalWeight := 0
	weights := make([]int, len(lines))
	for i, line := range lines {
		w := len([]rune(strings.TrimSpace(line.Text)))
		if w < 12 {
			w = 12
		}
		weights[i] = w
		totalWeight += w
	}
	out := make([]audioBookConversationSegment, 0, len(lines))
	for i, line := range lines {
		sec := audioSeconds / float64(len(lines))
		if totalWeight > 0 {
			sec = audioSeconds * float64(weights[i]) / float64(totalWeight)
		}
		if sec < 1.25 {
			sec = 1.25
		}
		out = append(out, audioBookConversationSegment{
			Speaker: strings.TrimSpace(line.Speaker),
			Text:    strings.TrimSpace(line.Text),
			Seconds: sec,
		})
	}
	return out
}

func audioBookConversationBackgroundIndices(segments []audioBookConversationSegment, starts []float64) []int {
	out := make([]int, len(segments))
	if len(starts) == 0 {
		return out
	}
	bg := 0
	elapsed := 0.0
	for i, seg := range segments {
		for bg+1 < len(starts) && elapsed >= starts[bg+1] {
			bg++
		}
		out[i] = bg
		elapsed += seg.Seconds
	}
	return out
}

type audioBookConversationCastView struct {
	Left        string
	Right       string
	ActiveRight bool
}

func audioBookConversationCast(opts AudioBookVideoOptions, active string) audioBookConversationCastView {
	host := strings.TrimSpace(opts.Host)
	if host == "" {
		host = "Narrator"
	}
	guest := ""
	for _, s := range opts.Speakers {
		s = strings.TrimSpace(s)
		if s == "" || strings.EqualFold(s, host) {
			continue
		}
		guest = s
		break
	}
	if guest == "" {
		guest = "Guest"
	}
	active = strings.TrimSpace(active)
	return audioBookConversationCastView{
		Left:        host,
		Right:       guest,
		ActiveRight: active != "" && !strings.EqualFold(active, host),
	}
}

func (r *Renderer) renderAudioBookConversationFrame(bg *image.RGBA,
	cast audioBookConversationCastView, avatars map[string]*image.RGBA, seg audioBookConversationSegment,
) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, r.width, r.height))
	drawImageCover(dst, bg)
	draw.Draw(dst, dst.Bounds(), &image.Uniform{color.RGBA{0x06, 0x08, 0x0f, 0x86}}, image.Point{}, draw.Over)
	drawAudioBookConversationCharacter(dst, r.tagFace, r.panelHdrFace, cast.Left, false, !cast.ActiveRight,
		audioBookAvatarFor(avatars, cast.Left))
	drawAudioBookConversationCharacter(dst, r.tagFace, r.panelHdrFace, cast.Right, true, cast.ActiveRight,
		audioBookAvatarFor(avatars, cast.Right))
	drawAudioBookConversationPanel(dst, r.titleFace, r.tagFace, r.bodyFace, r.panelHdrFace,
		r.topicOrFallback(seg), seg)
	return dst
}

func audioBookAvatarFor(avatars map[string]*image.RGBA, name string) *image.RGBA {
	if len(avatars) == 0 {
		return nil
	}
	return avatars[normalizeAudioBookSpeakerName(name)]
}

func normalizeAudioBookSpeakerName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func (r *Renderer) topicOrFallback(seg audioBookConversationSegment) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if strings.TrimSpace(r.topic) != "" {
		return strings.TrimSpace(r.topic)
	}
	return strings.TrimSpace(seg.Speaker)
}

func drawImageCover(dst *image.RGBA, src *image.RGBA) {
	if src == nil {
		drawGradientBackground(dst, color.RGBA{0x18, 0x21, 0x2d, 0xff}, color.RGBA{0x05, 0x07, 0x0d, 0xff})
		return
	}
	db := dst.Bounds()
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	dw, dh := db.Dx(), db.Dy()
	if sw <= 0 || sh <= 0 || dw <= 0 || dh <= 0 {
		return
	}
	scale := float64(dw) / float64(sw)
	if float64(sh)*scale < float64(dh) {
		scale = float64(dh) / float64(sh)
	}
	cw := int(float64(sw)*scale + 0.5)
	ch := int(float64(sh)*scale + 0.5)
	rect := image.Rect((dw-cw)/2, (dh-ch)/2, (dw+cw)/2, (dh+ch)/2)
	xdraw.CatmullRom.Scale(dst, rect, src, sb, xdraw.Src, nil)
}

func drawAudioBookConversationCharacter(dst *image.RGBA, nameFace, metaFace font.Face,
	name string, right bool, active bool, avatar *image.RGBA,
) {
	cx := 280
	if right {
		cx = dst.Bounds().Dx() - 280
	}
	baseY := dst.Bounds().Dy() - 98
	accent := color.RGBA{0x5a, 0xc8, 0xfa, 0xff}
	if right {
		accent = color.RGBA{0xff, 0xb8, 0x6b, 0xff}
	}
	body := color.RGBA{0x26, 0x2d, 0x3b, 0xff}
	head := color.RGBA{0xf1, 0xd2, 0xae, 0xff}
	if !active {
		accent = color.RGBA{0x72, 0x78, 0x86, 0xff}
		body = color.RGBA{0x18, 0x1d, 0x27, 0xff}
		head = color.RGBA{0x8c, 0x80, 0x78, 0xff}
	}

	if avatar != nil {
		slot := image.Rect(cx-260, baseY-660, cx+260, baseY-42)
		if active {
			halo := image.Rect(cx-220, baseY-595, cx+220, baseY-96)
			draw.Draw(dst, halo.Inset(-12), &image.Uniform{withAlpha(accent, 0x34)}, image.Point{}, draw.Over)
			drawRectOutline(dst, halo, 3, accent)
		}
		drawImageContainOver(dst, avatar, slot, active)
	} else {
		fillCircle(dst, cx, baseY-160, 210, body)
		draw.Draw(dst, image.Rect(cx-130, baseY-255, cx+130, baseY-60), &image.Uniform{body}, image.Point{}, draw.Src)
		fillCircle(dst, cx, baseY-340, 98, head)
		fillCircle(dst, cx-42, baseY-355, 12, color.RGBA{0x15, 0x18, 0x20, 0xff})
		fillCircle(dst, cx+42, baseY-355, 12, color.RGBA{0x15, 0x18, 0x20, 0xff})
		draw.Draw(dst, image.Rect(cx-45, baseY-310, cx+45, baseY-302), &image.Uniform{color.RGBA{0x84, 0x4d, 0x46, 0xff}}, image.Point{}, draw.Src)
	}

	plate := image.Rect(cx-180, baseY-42, cx+180, baseY+34)
	draw.Draw(dst, plate, &image.Uniform{color.RGBA{0x09, 0x0c, 0x13, 0xd8}}, image.Point{}, draw.Over)
	drawRectOutline(dst, plate, 2, accent)
	label := trimToWidth(nameFace, strings.ToUpper(name), plate.Dx()-40)
	drawCenteredText(dst, nameFace, label, cx, plate.Min.Y+49, color.RGBA{0xff, 0xff, 0xff, 0xff})
	if active {
		drawCenteredText(dst, metaFace, "SPEAKING", cx, plate.Min.Y-18, accent)
	}
}

func drawImageContainOver(dst *image.RGBA, src *image.RGBA, slot image.Rectangle, active bool) {
	if src == nil || slot.Empty() {
		return
	}
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	if sw <= 0 || sh <= 0 {
		return
	}
	scale := float64(slot.Dx()) / float64(sw)
	if float64(sh)*scale > float64(slot.Dy()) {
		scale = float64(slot.Dy()) / float64(sh)
	}
	cw := int(float64(sw)*scale + 0.5)
	ch := int(float64(sh)*scale + 0.5)
	rect := image.Rect(slot.Min.X+(slot.Dx()-cw)/2, slot.Max.Y-ch, slot.Min.X+(slot.Dx()+cw)/2, slot.Max.Y)
	if active {
		xdraw.CatmullRom.Scale(dst, rect, src, sb, xdraw.Over, nil)
		return
	}
	buf := image.NewRGBA(image.Rect(0, 0, cw, ch))
	xdraw.CatmullRom.Scale(buf, buf.Bounds(), src, sb, xdraw.Src, nil)
	blitWithGlobalAlphaAt(dst, buf, rect.Min.X, rect.Min.Y, 0.34)
}

func drawAudioBookConversationPanel(dst *image.RGBA, titleFace, tagFace, bodyFace, metaFace font.Face,
	title string, seg audioBookConversationSegment,
) {
	b := dst.Bounds()
	panelW := b.Dx() * 38 / 100
	if panelW > 760 {
		panelW = 760
	}
	if panelW < 560 {
		panelW = 560
	}
	panelH := b.Dy() * 62 / 100
	if panelH > 680 {
		panelH = 680
	}
	if panelH < 520 {
		panelH = 520
	}
	panel := image.Rect(
		b.Dx()/2-panelW/2,
		b.Dy()/2-panelH/2,
		b.Dx()/2+panelW/2,
		b.Dy()/2+panelH/2,
	)
	draw.Draw(dst, panel.Inset(-12), &image.Uniform{color.RGBA{0xff, 0xff, 0xff, 0x14}}, image.Point{}, draw.Over)
	draw.Draw(dst, panel, &image.Uniform{color.RGBA{0x0b, 0x0f, 0x19, 0xdc}}, image.Point{}, draw.Over)
	drawRectOutline(dst, panel, 2, color.RGBA{0xeb, 0xd7, 0xa0, 0xff})

	titleMaxW := panel.Dx() - 80
	titleDrawFace := titleFace
	if (&font.Drawer{Face: titleDrawFace}).MeasureString(title).Ceil() > titleMaxW {
		titleDrawFace = tagFace
	}
	title = trimToWidth(titleDrawFace, title, titleMaxW)
	drawCenteredText(dst, titleDrawFace, title, panel.Min.X+panel.Dx()/2, panel.Min.Y+68, color.RGBA{0xff, 0xf8, 0xdf, 0xff})
	drawCenteredText(dst, metaFace, "AUDIOBOOK CONVERSATION", panel.Min.X+panel.Dx()/2, panel.Min.Y+108, color.RGBA{0xb8, 0xc1, 0xd4, 0xff})

	speaker := strings.TrimSpace(seg.Speaker)
	if speaker == "" {
		speaker = "Speaker"
	}
	pillText := trimToWidth(tagFace, strings.ToUpper(speaker), panel.Dx()-120)
	pillY := panel.Min.Y + 160
	pillW := (&font.Drawer{Face: tagFace}).MeasureString(pillText).Ceil() + 56
	pill := image.Rect(panel.Min.X+panel.Dx()/2-pillW/2, pillY-42, panel.Min.X+panel.Dx()/2+pillW/2, pillY+18)
	draw.Draw(dst, pill, &image.Uniform{color.RGBA{0x2e, 0x37, 0x4b, 0xff}}, image.Point{}, draw.Src)
	drawRectOutline(dst, pill, 1, color.RGBA{0xf1, 0xc4, 0x6b, 0xff})
	drawCenteredText(dst, tagFace, pillText, panel.Min.X+panel.Dx()/2, pillY, color.RGBA{0xff, 0xff, 0xff, 0xff})

	textBox := image.Rect(panel.Min.X+58, panel.Min.Y+218, panel.Max.X-58, panel.Max.Y-56)
	lines := wrapLines(bodyFace, seg.Text, textBox.Dx())
	lines = clampLines(bodyFace, lines, textBox.Dy(), 12)
	metrics := bodyFace.Metrics()
	lineH := metrics.Height.Ceil() + 12
	totalH := len(lines) * lineH
	y := textBox.Min.Y + (textBox.Dy()-totalH)/2 + metrics.Ascent.Ceil()
	d := &font.Drawer{Dst: dst, Src: image.NewUniform(color.RGBA{0xf6, 0xf1, 0xe8, 0xff}), Face: bodyFace}
	for _, ln := range lines {
		x := textBox.Min.X + (textBox.Dx()-d.MeasureString(ln).Ceil())/2
		d.Dot = fixed.P(x, y)
		d.DrawString(ln)
		y += lineH
	}
}

func clampLines(face font.Face, lines []string, maxH, maxLines int) []string {
	if len(lines) == 0 {
		return lines
	}
	lineH := face.Metrics().Height.Ceil() + 12
	allowed := maxH / lineH
	if allowed < 1 {
		allowed = 1
	}
	if maxLines > 0 && allowed > maxLines {
		allowed = maxLines
	}
	if len(lines) <= allowed {
		return lines
	}
	out := append([]string(nil), lines[:allowed]...)
	out[len(out)-1] = strings.TrimSpace(out[len(out)-1]) + "..."
	return out
}

func writePNGFile(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}
