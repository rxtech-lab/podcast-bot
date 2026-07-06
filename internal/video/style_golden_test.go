package video

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sirily11/debate-bot/internal/video/scenes"
)

// updateGolden regenerates the committed golden PNGs under smoke-test/ instead
// of comparing against them. Run with:
//
//	go test ./internal/video -run TestStyleGolden -update-golden
//
// then review the diff and commit the new smoke-test/ images. The plain test
// run (no flag) re-renders the same frames and asserts they still match the
// goldens — a "force style" regression guard that fails the moment a renderer
// change alters the pixels of any content-type layout.
var updateGolden = flag.Bool("update-golden", false, "regenerate smoke-test golden images instead of comparing")

// goldenDir is the committed reference-image tree, relative to this package
// (internal/video → repo root → smoke-test). One subdir per content type.
const goldenDir = "../../smoke-test"

// styleFontPath is the pinned CJK font the style test renders with, relative to
// this package. It is fetched on demand by scripts/fetch-style-font.sh (not
// committed). Pinning it (via DEBATE_BOT_FONT in TestMain) is what makes the
// goldens reproducible across machines: without it the renderer would pick a
// platform CJK font (PingFang on macOS, Noto on Linux, …) and the goldens
// generated on one OS would never match the frames rendered on another.
const styleFontPath = "testdata/fonts/NotoSansSC-Regular.otf"

// TestMain pins DEBATE_BOT_FONT to the fetched font before any renderer is
// built, so TestStyleGolden generates and compares against deterministic,
// platform-independent frames. It fails loudly if the font is missing rather
// than letting loadFontSources silently fall back to a different face (which
// would turn the "force style" guard into a false red — or worse, regenerate
// goldens against the wrong font).
func TestMain(m *testing.M) {
	abs, err := filepath.Abs(styleFontPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "style golden: resolve font path: %v\n", err)
		os.Exit(1)
	}
	if _, err := os.Stat(abs); err != nil {
		fmt.Fprintf(os.Stderr, "style golden: font %s not found: %v\n"+
			"Run `make style-font` (or ./scripts/fetch-style-font.sh) first; "+
			"`make style-test` does this automatically.\n", abs, err)
		os.Exit(1)
	}
	if err := os.Setenv("DEBATE_BOT_FONT", abs); err != nil {
		fmt.Fprintf(os.Stderr, "style golden: set DEBATE_BOT_FONT: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// Style-test tolerances. Rendering is deterministic on a given machine, but the
// pure-Go font rasteriser can differ by a hair across platforms (the goldens
// are generated on one OS and the test may run on another in CI). We allow a
// tiny fraction of pixels to differ by a small per-channel amount; anything
// larger — a moved card, a recoloured slab, a swapped font — trips the guard.
const (
	// pixelChannelTolerance is the max per-channel delta for a pixel to still
	// count as "matching".
	pixelChannelTolerance = 12
	// maxDiffFraction is the largest share of pixels allowed to exceed
	// pixelChannelTolerance before the frame is considered changed.
	maxDiffFraction = 0.005 // 0.5%
)

// styleCase is one golden frame: a content type, a name, and the renderer
// setup that produces a deterministic, animation-settled frame.
type styleCase struct {
	kind   string // subdir under smoke-test/ (debate|puzzle|series|discussion)
	name   string // file stem
	setup  func(r *Renderer)
	render func(t *testing.T, r *Renderer) *image.RGBA
}

// settle pushes every wall-clock-anchored animation far past its end so the
// captured frame is time-invariant: stage magic-move, scene crossfade, and any
// speaker plate fade all clamp to their terminal value. Bodies are kept short
// enough not to overflow (so the subtitle scroll offset is pinned at 0), and
// the cinematic plate is parked inside its steady full-opacity hold window.
func settle(r *Renderer) {
	r.AdvanceStageForTest(5 * time.Second)
	r.AdvanceSceneForTest(5 * time.Second)
	// Land inside the surface/conclusion plate's 22 s hold (after the ~300 ms
	// fade-in, before fade-out) so cinematic frames show the plate at a stable
	// alpha == 1. qa/reveal/debate paint the plate at full opacity regardless.
	r.AdvanceSpeakerForTest(5 * time.Second)
}

func styleCases() []styleCase {
	const (
		debateTopic = "AI 是否會取代程序員"
		affPos      = "AI 是程序員的放大器：它讓人類更有產能，但判斷與責任仍由工程師承擔。"
		negPos      = "AI 將取代大量初階程序員：寫代碼可被自動化，行業面臨結構性收縮。"
	)

	return []styleCase{
		// ---- debate (CNN two-panel chrome) ----
		{kind: "debate", name: "01-idle", setup: func(r *Renderer) {
			r.SetTopic(debateTopic)
			r.SetPhase("opening")
			r.SetSides([]string{"Alice", "Carol"}, []string{"Linda", "Bob"})
			r.SetPositions(affPos, negPos)
			settle(r)
		}},
		{kind: "debate", name: "02-affirmative", setup: func(r *Renderer) {
			r.SetTopic(debateTopic)
			r.SetPhase("free-debate")
			r.SetSides([]string{"Alice", "Carol"}, []string{"Linda", "Bob"})
			r.SetPositions(affPos, negPos)
			r.SetState("Alice", "affirmative", "affirmative",
				"AI 會放大初階工程師的產能。", 5*time.Second)
			r.SetClock(1*time.Minute+12*time.Second, 30*time.Minute)
			settle(r)
		}},
		{kind: "debate", name: "03-negative", setup: func(r *Renderer) {
			r.SetTopic(debateTopic)
			r.SetPhase("free-debate")
			r.SetSides([]string{"Alice", "Carol"}, []string{"Linda", "Bob"})
			r.SetPositions(affPos, negPos)
			r.SetState("Linda", "negative", "negative",
				"責任邊界仍由人界定，工具再強也一樣。", 5*time.Second)
			r.SetClock(14*time.Minute+27*time.Second, 30*time.Minute)
			settle(r)
		}},

		// ---- situation puzzle (cinematic slab caption) ----
		{kind: "puzzle", name: "01-idle-surface", setup: func(r *Renderer) {
			r.SetPuzzleMode(true)
			r.SetPuzzleSceneName(scenes.SceneSurface)
			r.SetTopic("海龜湯")
			r.SetPositions("一名男子點了一碗海龜湯，喝完便結束了自己的生命。為什麼？", "")
			r.SetSceneBackground(gradientPlate(color.RGBA{0x12, 0x1c, 0x36, 0xff}, color.RGBA{0x05, 0x08, 0x14, 0xff}, 101))
			settle(r)
		}},
		{kind: "puzzle", name: "02-qa", setup: func(r *Renderer) {
			r.SetPuzzleMode(true)
			r.SetPuzzleSceneName(scenes.SceneQA)
			r.SetTopic("海龜湯")
			r.SetSceneBackground(gradientPlate(color.RGBA{0x1a, 0x22, 0x2c, 0xff}, color.RGBA{0x06, 0x0a, 0x10, 0xff}, 2))
			r.SetState("Alice", "player", "",
				"他是不是認出了什麼讓他震驚的東西？", 5*time.Second)
			r.SetClock(4*time.Minute+30*time.Second, 25*time.Minute)
			settle(r)
		}},
		{kind: "puzzle", name: "03-reveal", setup: func(r *Renderer) {
			r.SetPuzzleMode(true)
			r.SetPuzzleSceneName(scenes.SceneReveal)
			r.SetTopic("海龜湯")
			r.SetSceneBackground(gradientPlate(color.RGBA{0x3a, 0x10, 0x12, 0xff}, color.RGBA{0x08, 0x02, 0x05, 0xff}, 3))
			r.SetState("出題者", "puzzle-host", "",
				"他嚐到的味道，與當年截然不同。", 5*time.Second)
			r.SetClock(22*time.Minute, 25*time.Minute)
			settle(r)
		}},

		// ---- series (narration mode: top-left ID label + slab caption) ----
		{kind: "series", name: "01-idle", setup: func(r *Renderer) {
			r.SetPuzzleMode(true)
			r.SetPuzzleSceneName(scenes.SceneNarration)
			r.SetSeriesLabel("Dreamers", 1, 2, "Host")
			r.SetTopic("迷霧裡的旅人")
			r.SetSceneBackground(gradientPlate(color.RGBA{0x14, 0x2c, 0x24, 0xff}, color.RGBA{0x04, 0x0a, 0x08, 0xff}, 33))
			settle(r)
		}},
		{kind: "series", name: "02-narration", setup: func(r *Renderer) {
			r.SetPuzzleMode(true)
			r.SetPuzzleSceneName(scenes.SceneNarration)
			r.SetSeriesLabel("Dreamers", 1, 2, "Host")
			r.SetTopic("迷霧裡的旅人")
			r.SetSceneBackground(gradientPlate(color.RGBA{0x1a, 0x22, 0x3a, 0xff}, color.RGBA{0x05, 0x07, 0x10, 0xff}, 11))
			r.SetState("旁白", "host", "", "旅人在霧裡看見了一盞燈。", 5*time.Second)
			settle(r)
		}},

		// ---- discussion (qa caption + discussion idle pill) ----
		{kind: "discussion", name: "01-idle", setup: func(r *Renderer) {
			r.SetPuzzleMode(true)
			r.SetPuzzleSceneName(scenes.SceneQA)
			r.SetPuzzleIdleLabel("討論  ·  DISCUSSION")
			r.SetTopic("Vibe Coding 是炒作還是新常態？")
			r.SetPositions("一場關於 AI 輔助開發前景的圓桌討論。", "")
			r.SetSceneBackground(gradientPlate(color.RGBA{0x33, 0x18, 0x2c, 0xff}, color.RGBA{0x0a, 0x04, 0x08, 0xff}, 22))
			settle(r)
		}},
		{kind: "discussion", name: "02-speaking", setup: func(r *Renderer) {
			r.SetPuzzleMode(true)
			r.SetPuzzleSceneName(scenes.SceneQA)
			r.SetPuzzleIdleLabel("討論  ·  DISCUSSION")
			r.SetTopic("Vibe Coding 是炒作還是新常態？")
			r.SetSceneBackground(gradientPlate(color.RGBA{0x14, 0x2c, 0x24, 0xff}, color.RGBA{0x04, 0x0a, 0x08, 0xff}, 33))
			r.SetState("Diego", "discussant", "", "真正能收費的是雲端執行與協作。", 5*time.Second)
			settle(r)
		}},

		// ---- audiobook conversation (left/right speaker avatars + center panel) ----
		// Title and body mix Chinese and English on purpose: audiobook content
		// is frequently CJK, and this is the guard that catches a renderer or
		// font regression that drops CJK glyphs (Latin-only fallback).
		{kind: "audiobook", name: "01-conversation", setup: func(r *Renderer) {
			r.SetTopic("人類創造力的未來 · The Future of Human Creativity")
			settle(r)
		}, render: func(t *testing.T, r *Renderer) *image.RGBA {
			avatars := map[string]*image.RGBA{
				"mina":   readSmokeAvatarFixture(t, "audiobook_avatar_mina.png"),
				"jordan": readSmokeAvatarFixture(t, "audiobook_avatar_jordan.png"),
			}
			return r.renderAudioBookConversationFrame(
				gradientPlate(color.RGBA{0x1b, 0x32, 0x46, 0xff}, color.RGBA{0x06, 0x08, 0x10, 0xff}, 501),
				audioBookConversationCastView{Left: "Mina", Right: "Jordan"},
				avatars,
				audioBookConversationSegment{
					Speaker: "Mina",
					Text:    "當一本書變成一場對話，敘事者仍要穩住場景，but the guest needs a visible place in the frame.",
					Seconds: 3,
				},
			)
		}},
	}
}

// TestStyleGolden renders one deterministic frame per content-type style case
// and asserts it matches the committed golden under smoke-test/. With
// -update-golden it writes the goldens instead of comparing.
func TestStyleGolden(t *testing.T) {
	for _, c := range styleCases() {
		c := c
		t.Run(c.kind+"/"+c.name, func(t *testing.T) {
			got := renderStyleFrame(t, c)

			dir := filepath.Join(goldenDir, c.kind)
			goldenPath := filepath.Join(dir, c.name+".png")

			if *updateGolden {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("mkdir %s: %v", dir, err)
				}
				if err := writePNG(goldenPath, got); err != nil {
					t.Fatalf("write golden %s: %v", goldenPath, err)
				}
				t.Logf("wrote golden %s", goldenPath)
				return
			}

			want, err := readPNG(goldenPath)
			if err != nil {
				t.Fatalf("read golden %s: %v (run `go test ./internal/video -run TestStyleGolden -update-golden` to create it)", goldenPath, err)
			}

			frac, maxd, err := compareRGBA(want, got)
			if err != nil {
				t.Fatalf("compare %s: %v", c.name, err)
			}
			if frac > maxDiffFraction {
				actual := dumpActual(t, c, got)
				t.Errorf("style drift in %s/%s: %.3f%% of pixels differ (max channel delta %d) — exceeds %.3f%% budget.\n  golden: %s\n  actual: %s\nIf this change is intentional, regenerate goldens with: go test ./internal/video -run TestStyleGolden -update-golden",
					c.kind, c.name, frac*100, maxd, maxDiffFraction*100, goldenPath, actual)
			}
		})
	}
}

// renderStyleFrame builds a fresh 1920×1080 renderer, applies the case setup,
// and returns the rendered frame as an *image.RGBA.
func renderStyleFrame(t *testing.T, c styleCase) *image.RGBA {
	t.Helper()
	r := renderForTest(t)
	c.setup(r)
	if c.render != nil {
		return c.render(t, r)
	}
	pix := r.Frame()
	return &image.RGBA{
		Pix:    pix,
		Stride: 1920 * 4,
		Rect:   image.Rect(0, 0, 1920, 1080),
	}
}

// compareRGBA returns the fraction of pixels whose per-channel delta exceeds
// pixelChannelTolerance, plus the largest single-channel delta seen. Errors if
// the dimensions differ (always a real style change worth failing on).
func compareRGBA(want, got *image.RGBA) (frac float64, maxDelta int, err error) {
	if want.Rect != got.Rect {
		return 1, 255, fmt.Errorf("size mismatch: golden %v vs actual %v", want.Rect, got.Rect)
	}
	var diff int
	total := len(want.Pix) / 4
	for i := 0; i+3 < len(want.Pix); i += 4 {
		dr := absDiff(want.Pix[i], got.Pix[i])
		dg := absDiff(want.Pix[i+1], got.Pix[i+1])
		db := absDiff(want.Pix[i+2], got.Pix[i+2])
		d := dr
		if dg > d {
			d = dg
		}
		if db > d {
			d = db
		}
		if d > maxDelta {
			maxDelta = d
		}
		if d > pixelChannelTolerance {
			diff++
		}
	}
	if total == 0 {
		return 0, maxDelta, nil
	}
	return float64(diff) / float64(total), maxDelta, nil
}

func absDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

// dumpActual writes the freshly rendered frame next to the goldens under
// smoke-test/_actual/ so a failing run can be eyeballed / diffed. Returns the
// path (or a note if the write failed).
func dumpActual(t *testing.T, c styleCase, img *image.RGBA) string {
	t.Helper()
	dir := filepath.Join(goldenDir, "_actual", c.kind)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "(failed to write actual: " + err.Error() + ")"
	}
	path := filepath.Join(dir, c.name+".png")
	if err := writePNG(path, img); err != nil {
		return "(failed to write actual: " + err.Error() + ")"
	}
	return path
}

func readSmokeAvatarFixture(t *testing.T, name string) *image.RGBA {
	t.Helper()
	path := filepath.Join(goldenDir, "audiobook", "avatars", name)
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open audiobook smoke avatar fixture %s: %v", path, err)
	}
	defer f.Close()
	src, err := png.Decode(f)
	if err != nil {
		t.Fatalf("decode audiobook smoke avatar fixture %s: %v", path, err)
	}
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			dst.Set(x, y, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

func writePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

func readPNG(path string) (*image.RGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	src, err := png.Decode(f)
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			dst.Set(x, y, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst, nil
}

// gradientPlate synthesises a deterministic 1920×1080 background plate: a
// vertical gradient plus seeded noise. Stands in for the AI-generated scene
// backgrounds so the style test never touches the network and stays bit-stable.
func gradientPlate(top, bot color.RGBA, seed int64) *image.RGBA {
	const w, h = 1920, 1080
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewSource(seed))
	for y := 0; y < h; y++ {
		ty := float64(y) / float64(h-1)
		base := color.RGBA{
			uint8(float64(top.R)*(1-ty) + float64(bot.R)*ty),
			uint8(float64(top.G)*(1-ty) + float64(bot.G)*ty),
			uint8(float64(top.B)*(1-ty) + float64(bot.B)*ty),
			0xff,
		}
		for x := 0; x < w; x++ {
			n := int(rng.Int31n(24)) - 12
			i := img.PixOffset(x, y)
			img.Pix[i] = clipByte(int(base.R) + n)
			img.Pix[i+1] = clipByte(int(base.G) + n)
			img.Pix[i+2] = clipByte(int(base.B) + n)
			img.Pix[i+3] = 0xff
		}
	}
	return img
}

func clipByte(v int) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}
