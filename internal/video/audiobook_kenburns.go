package video

import (
	"fmt"
	"image"
	"image/draw"
	"math"
	"os"
	"os/exec"
)

// Ken Burns post-pass settings for the narration-style audiobook video.
// 25 fps matches the previous concat slideshow; the camera move eases over
// at most kenBurnsMaxMoveSeconds then holds, so a long segment doesn't turn
// the pan glacial and a short one doesn't turn it frantic. Segment
// boundaries crossfade briefly, mirroring the series renderer's feel.
const (
	kenBurnsFPS              = 25
	kenBurnsMaxMoveSeconds   = 20.0
	kenBurnsMinMoveSeconds   = 4.0
	kenBurnsCrossfadeSeconds = 0.5
)

// kenBurnsFallbackCycle mirrors the scenes planner's fallback animation
// palette — used when the caller has no per-image animation plan (legacy
// audiobooks re-rendered through the manual endpoint).
var kenBurnsFallbackCycle = []MovementKind{
	MoveZoomIn, MovePanRight, MoveStall, MoveZoomOut,
	MovePanLeft, MovePanBottom, MoveStall, MovePanTop,
}

// audioBookImageStarts resolves the start time (seconds) of every image
// segment. offsets, when valid (same length as n, first entry near zero,
// non-decreasing, all within the audio), are the live-run emission times of
// each illustration; otherwise the images split the duration evenly (the
// legacy behaviour, and the right call for old runs with no recorded
// offsets). The first segment is always pinned to 0 so the video opens on
// an image.
func audioBookImageStarts(offsets []float64, n int, dur float64) []float64 {
	starts := make([]float64, n)
	if n == 0 {
		return starts
	}
	valid := len(offsets) == n && dur > 0
	if valid {
		prev := 0.0
		for i, off := range offsets {
			if off < 0 || off >= dur || off < prev {
				valid = false
				break
			}
			// A dense plan whose first image only appears deep into the
			// audio means the offsets don't describe the timeline we're
			// rendering — fall back rather than open on a long black hold.
			if i == 0 && off > dur*0.5 {
				valid = false
				break
			}
			prev = off
		}
	}
	if valid {
		copy(starts, offsets)
		starts[0] = 0
		return starts
	}
	per := dur / float64(n)
	for i := range starts {
		starts[i] = float64(i) * per
	}
	return starts
}

// audioBookImageMovements maps the per-image animation tokens onto camera
// moves, padding/cycling when the plan is shorter than the image list.
func audioBookImageMovements(anims []string, n int) []CameraMovement {
	out := make([]CameraMovement, n)
	for i := 0; i < n; i++ {
		if i < len(anims) && anims[i] != "" {
			out[i] = CameraMovement{Kind: parseMovementKind(anims[i])}
			continue
		}
		out[i] = CameraMovement{Kind: kenBurnsFallbackCycle[i%len(kenBurnsFallbackCycle)]}
	}
	return out
}

// kenBurnsSourceCache lazily loads and pre-scales illustration sources to
// the output size, keeping only a small sliding window alive — a 40-image
// audiobook at 1080p would otherwise pin ~330 MB of RGBA.
type kenBurnsSourceCache struct {
	paths  []string
	w, h   int
	loaded map[int]*image.RGBA
}

func (c *kenBurnsSourceCache) get(i int) (*image.RGBA, error) {
	if img, ok := c.loaded[i]; ok {
		return img, nil
	}
	f, err := os.Open(c.paths[i])
	if err != nil {
		return nil, fmt.Errorf("open image %d: %w", i, err)
	}
	src, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		return nil, fmt.Errorf("decode image %d: %w", i, err)
	}
	b := src.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), src, b.Min, draw.Src)
	// Pre-scale once to cover the output frame so the per-frame affine
	// resample works on a right-sized source instead of the raw 1920×1080
	// (or larger) PNG.
	scaled := image.NewRGBA(image.Rect(0, 0, c.w, c.h))
	drawImageCover(scaled, rgba)
	c.loaded[i] = scaled
	// Slide the window: anything before i-1 can no longer be referenced
	// (the crossfade only ever looks one segment back).
	for k := range c.loaded {
		if k < i-1 {
			delete(c.loaded, k)
		}
	}
	return scaled, nil
}

// renderKenBurnsAudioBookVideo renders the narration-style audiobook video
// with per-image Ken Burns camera moves: frames are rasterised in Go
// (reusing the live renderer's CameraMovement engine) and piped to ffmpeg
// as raw RGBA, muxed with the narration audio and optional soft subtitles.
func renderKenBurnsAudioBookVideo(outPath, audioPath, vttPath string,
	imagePaths []string, res Resolution, opts AudioBookVideoOptions, dur float64,
) error {
	w, h := outputDims(res)
	n := len(imagePaths)
	starts := audioBookImageStarts(opts.ImageOffsets, n, dur)
	moves := audioBookImageMovements(opts.Animations, n)
	cache := &kenBurnsSourceCache{paths: imagePaths, w: w, h: h, loaded: map[int]*image.RGBA{}}

	hasSubs := false
	if vttPath != "" {
		if _, serr := os.Stat(vttPath); serr == nil {
			hasSubs = true
		}
	}
	args := []string{"-y",
		"-f", "rawvideo", "-pix_fmt", "rgba",
		"-s", fmt.Sprintf("%dx%d", w, h),
		"-r", fmt.Sprintf("%d", kenBurnsFPS),
		"-i", "pipe:0",
		"-i", audioPath,
	}
	if hasSubs {
		args = append(args, "-i", vttPath)
	}
	args = append(args, "-map", "0:v", "-map", "1:a")
	if hasSubs {
		args = append(args, "-map", "2:s")
	}
	args = append(args,
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "23", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "128k",
		"-shortest",
	)
	if hasSubs {
		args = append(args, "-c:s", "mov_text")
	}
	args = append(args, "-movflags", "+faststart", outPath)

	cmd := exec.Command("ffmpeg", args...)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("render audiobook kenburns: stdin pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("render audiobook kenburns: start ffmpeg: %w", err)
	}

	frame := image.NewRGBA(image.Rect(0, 0, w, h))
	fadeTmp := image.NewRGBA(image.Rect(0, 0, w, h))
	totalFrames := int(math.Ceil(dur * kenBurnsFPS))
	writeErr := func() error {
		defer stdin.Close()
		seg := 0
		for fi := 0; fi < totalFrames; fi++ {
			t := float64(fi) / kenBurnsFPS
			for seg+1 < n && t >= starts[seg+1] {
				seg++
			}
			cur, cerr := cache.get(seg)
			if cerr != nil {
				return cerr
			}
			moves[seg].Render(frame, cur, kenBurnsProgress(t, seg, starts, dur))

			// Brief crossfade from the previous segment at each boundary.
			if seg > 0 {
				if fade := (t - starts[seg]) / kenBurnsCrossfadeSeconds; fade < 1 {
					prev, perr := cache.get(seg - 1)
					if perr == nil {
						moves[seg-1].Render(fadeTmp, prev, kenBurnsProgress(t, seg-1, starts, dur))
						blitWithGlobalAlpha(frame, fadeTmp, 1-fade)
					}
				}
			}
			if _, werr := stdin.Write(frame.Pix); werr != nil {
				return fmt.Errorf("write frame %d: %w", fi, werr)
			}
		}
		return nil
	}()
	waitErr := cmd.Wait()
	if writeErr != nil && waitErr == nil {
		return fmt.Errorf("render audiobook kenburns: %w", writeErr)
	}
	if waitErr != nil {
		if writeErr != nil {
			return fmt.Errorf("render audiobook kenburns: ffmpeg: %w (frame writer: %v)", waitErr, writeErr)
		}
		return fmt.Errorf("render audiobook kenburns: ffmpeg: %w", waitErr)
	}
	return nil
}

// kenBurnsProgress computes the eased camera-move progress for segment seg
// at absolute time t. The move plays out over the segment length clamped to
// [kenBurnsMinMoveSeconds, kenBurnsMaxMoveSeconds], then holds at its end
// pose for the remainder of the segment.
func kenBurnsProgress(t float64, seg int, starts []float64, dur float64) float64 {
	segEnd := dur
	if seg+1 < len(starts) {
		segEnd = starts[seg+1]
	}
	moveLen := segEnd - starts[seg]
	if moveLen > kenBurnsMaxMoveSeconds {
		moveLen = kenBurnsMaxMoveSeconds
	}
	if moveLen < kenBurnsMinMoveSeconds {
		moveLen = kenBurnsMinMoveSeconds
	}
	p := (t - starts[seg]) / moveLen
	if p < 0 {
		p = 0
	}
	if p > 1 {
		p = 1
	}
	return easeInOutCubic(p)
}
