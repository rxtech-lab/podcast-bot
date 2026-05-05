// Command movement-smoke exercises the puzzle scene-bg pipeline end-to-end:
// camera moves (pan / zoom) and image-to-image transitions are driven against
// the encoder so the output stream demonstrates each path in isolation.
//
// Inputs:  assets/image-0.png and assets/image-1.png (1280x720 RGB).
// Outputs: <out>/hls/stream.m3u8 (segmented) + <out>/preview.mp4 (single
// file, ready to play in any video player).
package main

import (
	"context"
	"flag"
	"fmt"
	"image"
	"image/png"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/sirily11/debate-bot/internal/video"
)

func main() {
	out := flag.String("out", "out/movement-smoke", "output directory (HLS goes under <out>/hls)")
	assetDir := flag.String("asset-dir", "assets", "directory holding image-0.png and image-1.png")
	beat := flag.Duration("dur", 6*time.Second, "duration each move/scene plays before the next swap")
	flag.Parse()

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	abs, _ := filepath.Abs(*out)
	fmt.Println("output dir:", abs)

	img0, err := loadRGBA(filepath.Join(*assetDir, "image-0.png"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "load image-0: %v\n", err)
		os.Exit(1)
	}
	img1, err := loadRGBA(filepath.Join(*assetDir, "image-1.png"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "load image-1: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	enc, err := video.New(ctx, *out, video.Resolution720p, log)
	if err != nil {
		fmt.Fprintln(os.Stderr, "encoder:", err)
		os.Exit(1)
	}
	enc.SetTopic("movement smoke — camera moves + transitions")
	enc.SetPhase("smoke")
	enc.SetPuzzleMode(true)
	enc.SetPuzzleSceneName("surface")

	// Each beat: (image, animation kind). We alternate which image is on
	// screen so every transition exercises both the prev-still-animating
	// path (between distinct images) and the freshly-set anim. The final
	// pair is fired back-to-back to exercise the in-flight-fade snapshot
	// path inside SetSceneBackground.
	type step struct {
		img  *image.RGBA
		kind string
		hold time.Duration
	}
	steps := []step{
		{img0, "zoomin", *beat},
		{img1, "panright", *beat},
		{img0, "zoomout", *beat},
		{img1, "pantop", *beat},
		{img0, "panleft", *beat},
		{img1, "panbottom", *beat},
		// Back-to-back: fire two SetSceneBackground calls within the
		// 1.5 s crossfade window so the renderer's snapshot path runs.
		{img0, "zoomin", 200 * time.Millisecond},
		{img1, "zoomout", *beat},
	}
	for i, s := range steps {
		fmt.Printf("[%d/%d] %s, hold %s\n", i+1, len(steps), s.kind, s.hold)
		enc.SetSceneBackground(s.img)
		enc.SetSceneAnimation(s.kind)
		time.Sleep(s.hold)
	}

	if err := enc.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "close:", err)
	}
	fmt.Println("hls dir:", enc.HLSDir())

	// Transmux the HLS playlist into a standalone MP4 the user can open
	// directly in QuickTime / VLC / mpv. -c copy is stream-copy (no
	// re-encode) so the MP4's bytes are identical to the HLS segments.
	mp4Path := filepath.Join(*out, "preview.mp4")
	manifest := filepath.Join(enc.HLSDir(), "stream.m3u8")
	cmd := exec.Command("ffmpeg",
		"-y",
		"-loglevel", "warning",
		"-i", manifest,
		"-c", "copy",
		"-movflags", "+faststart",
		mp4Path,
	)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "ffmpeg transmux failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("preview mp4:", mp4Path)
}

// loadRGBA decodes a PNG and copies the pixels into a fresh *image.RGBA so
// the renderer can hand it to the encoder pipeline. Mirrors the helper at
// internal/video/scenes/scenes.go::readCachedRGBA but keeps the smoke test
// self-contained.
func loadRGBA(path string) (*image.RGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	src, err := png.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	b := src.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			out.Set(x, y, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return out, nil
}
