// Command series-smoke renders a quick visual preview of the TV-series
// channel style without going through the orchestrator, AI image gen,
// TTS, or any other production-content path. It loads a topic.md just
// to pick up the title / show name / surface text, then walks a fixed
// set of "narration beats" through the renderer using committed PNGs
// from --assets (defaults to assets/) as scene backgrounds. Output is
// a single mp4 you can scrub through to eyeball the layout, lower-third,
// camera moves, and scene crossfades.
//
// Frames are piped raw RGBA into ffmpeg so we get a real mp4 instead of
// a stack of PNGs. No network calls, no API keys, no cached AI artefacts —
// just the renderer + on-disk asset PNGs.
//
// Flags:
//
//	--topic   path to a series topic.md (default channels/series/01_pilot.md)
//	--out     output dir (default out/series-smoke)
//	--assets  dir of background PNGs to rotate through (default assets/,
//	          matches assets/image-*.png)
package main

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	xdraw "golang.org/x/image/draw"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/video"
	"github.com/sirily11/debate-bot/internal/video/scenes"
)

const (
	width  = 1280
	height = 720
	fps    = 30
)

func main() {
	topic := flag.String("topic", "channels/series/01_pilot.md", "path to a series topic.md")
	out := flag.String("out", "out/series-smoke", "output directory for the preview mp4")
	assetDir := flag.String("assets", "assets", "directory of background PNGs (image-*.png) to rotate through")
	flag.Parse()

	if err := run(*topic, *out, *assetDir); err != nil {
		fmt.Fprintln(os.Stderr, "series-smoke:", err)
		os.Exit(1)
	}
}

func run(topicPath, outDir, assetDir string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("mkdir out: %w", err)
	}
	tp, err := config.LoadTopic(topicPath)
	if err != nil {
		return fmt.Errorf("load topic: %w", err)
	}
	if tp.Type != config.ContentTypeSeries {
		return fmt.Errorf("topic type=%q, want series", tp.Type)
	}

	bgs, err := loadBackgrounds(assetDir, width, height)
	if err != nil {
		return err
	}
	if len(bgs) == 0 {
		return fmt.Errorf("no image-*.png files found in %s", assetDir)
	}
	fmt.Printf("series-smoke: loaded %d background(s) from %s\n", len(bgs), assetDir)

	speaker := tp.SeriesHost.Name
	if speaker == "" {
		speaker = "Narrator"
	}

	beats := buildBeats(tp)

	rend, err := video.NewRendererForTest(width, height)
	if err != nil {
		return err
	}
	// Series narration mode: full-bleed scene, no letterbox, no caption
	// slab. The renderer branches on SceneNarration to its own series
	// renderer instead of the puzzle chrome.
	rend.SetPuzzleMode(true)
	rend.SetTopic(tp.Title)
	rend.SetPhase("narration")
	rend.SetSides([]string{speaker}, nil)
	rend.SetPositions(tp.Surface, "")
	rend.SetPuzzleSceneName(scenes.SceneNarration)
	rend.SetSeriesLabel(tp.Show, tp.Season, tp.Episode, speaker)

	// Park the stage transition so the very first frame is settled.
	rend.AdvanceStageForTest(2 * time.Second)

	mp4Path := filepath.Join(outDir, "preview.mp4")
	cmd, stdin, stderr, err := startFFmpeg(mp4Path)
	if err != nil {
		return err
	}

	frameDur := time.Second / time.Duration(fps)
	var totalDur time.Duration
	for _, b := range beats {
		totalDur += b.duration
	}

	var elapsed time.Duration
	for i, b := range beats {
		rend.SetSceneBackground(bgs[i%len(bgs)])
		rend.SetSceneAnimation(b.anim)
		rend.SetState(speaker, "series-host", "", b.text, b.duration)
		// First beat: skip the fade-in by parking the scene crossfade so the
		// preview opens on a clean fully-painted frame. Subsequent beats let
		// the crossfade play naturally between backgrounds.
		if i == 0 {
			rend.AdvanceSceneForTest(2 * time.Second)
		}

		nFrames := int(b.duration / frameDur)
		for f := 0; f < nFrames; f++ {
			rend.SetClock(elapsed, totalDur)
			pix := rend.Frame()
			if _, werr := stdin.Write(pix); werr != nil {
				_ = stdin.Close()
				_ = cmd.Wait()
				return fmt.Errorf("write frame (beat %d/%d): %w", i, f, werr)
			}
			elapsed += frameDur
		}
	}

	if err := stdin.Close(); err != nil {
		return fmt.Errorf("close ffmpeg stdin: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("ffmpeg wait: %w\n%s", err, stderr.String())
	}
	abs, _ := filepath.Abs(mp4Path)
	fmt.Printf("series-smoke: preview → %s\n", abs)
	return nil
}

type beat struct {
	text     string
	anim     string
	duration time.Duration
}

// buildBeats hand-crafts a few narration beats so the smoke shows the
// layout settling, the lower-third painting, and the scene crossfade +
// camera move between two backgrounds. Text is sampled from the topic's
// Surface section when present so non-Latin glyphs and long-line wrap
// also get exercised; falls back to a generic English narration script.
func buildBeats(tp *config.DebateTopic) []beat {
	surface := tp.Surface
	if surface == "" {
		surface = "A late-night narrator walks an empty street as fog climbs the river. Tonight, a letter arrives that should not exist."
	}
	// Crude split: first ~80 runes, next ~80, then the rest. Keeps each
	// beat short enough to fit the slab subtitle without requiring a
	// scroll mid-preview.
	parts := splitForBeats([]rune(surface), 80)

	beats := make([]beat, 0, len(parts)+1)
	anims := []string{"zoomin", "panright", "zoomout", "panleft"}
	for i, p := range parts {
		beats = append(beats, beat{
			text:     string(p),
			anim:     anims[i%len(anims)],
			duration: 5 * time.Second,
		})
	}
	// Closing beat — gives the lower-third time to fade out cleanly.
	beats = append(beats, beat{
		text:     tp.Title,
		anim:     "zoomin",
		duration: 4 * time.Second,
	})
	return beats
}

func splitForBeats(rs []rune, chunk int) [][]rune {
	if len(rs) == 0 {
		return nil
	}
	var out [][]rune
	for i := 0; i < len(rs); i += chunk {
		end := i + chunk
		if end > len(rs) {
			end = len(rs)
		}
		out = append(out, rs[i:end])
	}
	return out
}

func loadBackgrounds(dir string, w, h int) ([]*image.RGBA, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "image-*.png"))
	if err != nil {
		return nil, fmt.Errorf("glob %s: %w", dir, err)
	}
	sort.Strings(matches)
	out := make([]*image.RGBA, 0, len(matches))
	for _, p := range matches {
		img, err := loadAndResize(p, w, h)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		out = append(out, img)
		fmt.Printf("series-smoke: bg %s\n", p)
	}
	return out, nil
}

func loadAndResize(path string, w, h int) (*image.RGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	src, err := png.Decode(f)
	if err != nil {
		return nil, err
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Over, nil)
	return dst, nil
}

func startFFmpeg(outPath string) (*exec.Cmd, interface {
	Write([]byte) (int, error)
	Close() error
}, *bytes.Buffer, error) {
	args := []string{
		"-y",
		"-loglevel", "error",
		"-f", "rawvideo",
		"-pix_fmt", "rgba",
		"-s", fmt.Sprintf("%dx%d", width, height),
		"-r", fmt.Sprintf("%d", fps),
		"-i", "pipe:0",
		"-c:v", "libx264",
		"-pix_fmt", "yuv420p",
		"-movflags", "+faststart",
		outPath,
	}
	cmd := exec.Command("ffmpeg", args...)
	stderr := &bytes.Buffer{}
	cmd.Stdout = os.Stdout
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("ffmpeg stdin: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, fmt.Errorf("ffmpeg start: %w", err)
	}
	return cmd, stdin, stderr, nil
}
