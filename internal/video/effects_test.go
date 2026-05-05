package video

import (
	"image"
	"image/color"
	"testing"
)

// makeGradientRGBA returns a w×h RGBA filled with a horizontal red gradient
// (0..255 across X) and a vertical green gradient (0..255 across Y). Letting
// the test inspect known pixel values at known coordinates after a camera
// move tells us whether the viewport landed where it should.
func makeGradientRGBA(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r := uint8(255 * x / max(w-1, 1))
			g := uint8(255 * y / max(h-1, 1))
			img.Set(x, y, color.RGBA{R: r, G: g, B: 0, A: 0xff})
		}
	}
	return img
}

// brightR returns the average R channel of a small box around (cx, cy) in dst.
// Box averaging hides the Catmull-Rom resampler's ±1 ringing so the test
// asserts on the underlying gradient value, not on per-pixel kernel artifacts.
func brightR(dst *image.RGBA, cx, cy int) int {
	const r = 3
	var sum, n int
	b := dst.Bounds()
	for y := cy - r; y <= cy+r; y++ {
		for x := cx - r; x <= cx+r; x++ {
			if x < b.Min.X || x >= b.Max.X || y < b.Min.Y || y >= b.Max.Y {
				continue
			}
			sum += int(dst.RGBAAt(x, y).R)
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / n
}

func brightG(dst *image.RGBA, cx, cy int) int {
	const r = 3
	var sum, n int
	b := dst.Bounds()
	for y := cy - r; y <= cy+r; y++ {
		for x := cx - r; x <= cx+r; x++ {
			if x < b.Min.X || x >= b.Max.X || y < b.Min.Y || y >= b.Max.Y {
				continue
			}
			sum += int(dst.RGBAAt(x, y).G)
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / n
}

func TestCameraMovementStallIsIdentity(t *testing.T) {
	src := makeGradientRGBA(640, 360)
	dst := image.NewRGBA(image.Rect(0, 0, 640, 360))
	CameraMovement{Kind: MoveStall}.Render(dst, src, 0.5)
	// At the center we expect R=127 (mid-X), G=127 (mid-Y).
	if got := brightR(dst, 320, 180); got < 110 || got > 145 {
		t.Errorf("stall center R = %d, want ≈127", got)
	}
	if got := brightG(dst, 320, 180); got < 110 || got > 145 {
		t.Errorf("stall center G = %d, want ≈127", got)
	}
}

func TestCameraMovementZoomInStartFillsFullFrame(t *testing.T) {
	src := makeGradientRGBA(1000, 1000)
	dst := image.NewRGBA(image.Rect(0, 0, 500, 500))
	// p=0 → start scale (1.0) → full source fills dst.
	CameraMovement{Kind: MoveZoomIn}.Render(dst, src, 0)
	// dst (0,0) should map to src (0,0) → R=0, G=0
	if got := brightR(dst, 5, 5); got > 30 {
		t.Errorf("zoomin p=0 top-left R = %d, want low (~0)", got)
	}
	// dst (490,490) ~ src (980,980) → R≈250, G≈250
	if got := brightR(dst, 490, 490); got < 220 {
		t.Errorf("zoomin p=0 bottom-right R = %d, want high (~250)", got)
	}
}

func TestCameraMovementZoomInEndShowsCenterOnly(t *testing.T) {
	src := makeGradientRGBA(1000, 1000)
	dst := image.NewRGBA(image.Rect(0, 0, 500, 500))
	// p=1 → end scale (0.7) → centered 700×700 of src fills dst.
	// That window is src x ∈ [150, 850]. dst (0,0) ≈ src (150,150) → R≈38.
	// dst (250,250) ≈ src (500,500) → R≈127.
	CameraMovement{Kind: MoveZoomIn}.Render(dst, src, 1.0)
	if got := brightR(dst, 5, 5); got < 25 || got > 70 {
		t.Errorf("zoomin p=1 top-left R = %d, want ≈38 (in 25..70)", got)
	}
	if got := brightR(dst, 250, 250); got < 110 || got > 145 {
		t.Errorf("zoomin p=1 center R = %d, want ≈127", got)
	}
}

func TestCameraMovementPanRightShiftsViewportOrigin(t *testing.T) {
	src := makeGradientRGBA(1000, 1000)
	dst := image.NewRGBA(image.Rect(0, 0, 500, 500))
	// p=1 with default mag 0.30 → viewport size 700×700 with origin
	// at x=300 (vw=700 fills dst, origin shifted right by sw*0.30=300).
	// Vertical centring → origin y = (1000-700)/2 = 150.
	// dst (0,0) maps to src (300, 150) → R≈76, G≈38.
	CameraMovement{Kind: MovePanRight}.Render(dst, src, 1.0)
	if got := brightR(dst, 5, 5); got < 60 || got > 100 {
		t.Errorf("panright p=1 top-left R = %d, want ≈76 (in 60..100)", got)
	}
	if got := brightG(dst, 5, 5); got < 25 || got > 60 {
		t.Errorf("panright p=1 top-left G = %d, want ≈38 (in 25..60)", got)
	}
}

func TestCameraMovementUnknownKindIsStall(t *testing.T) {
	src := makeGradientRGBA(640, 360)
	dst := image.NewRGBA(image.Rect(0, 0, 640, 360))
	CameraMovement{Kind: "totally-bogus"}.Render(dst, src, 0.5)
	// Should be identity → center R=127.
	if got := brightR(dst, 320, 180); got < 110 || got > 145 {
		t.Errorf("unknown kind treated as stall: center R = %d, want ≈127", got)
	}
}

func TestCameraMovementSubPixelMonotonicity(t *testing.T) {
	// Regression for the "earthquake" bug: as p sweeps a small range, the
	// rendered output should change in a smooth direction — not jitter
	// back and forth as integer rect rounding kicks in/out. We measure
	// the R channel at a fixed dst pixel through a fine-grained zoom
	// sweep and require the sequence to be (approximately) monotonic.
	src := makeGradientRGBA(1000, 1000)
	dst := image.NewRGBA(image.Rect(0, 0, 500, 500))
	const steps = 20
	prev := -1
	dir := 0 // +1 increasing, -1 decreasing, 0 unset
	jitter := 0
	for i := 0; i <= steps; i++ {
		p := float64(i) / float64(steps)
		// Reset dst between steps so each render is fresh (Render uses Over).
		for j := range dst.Pix {
			dst.Pix[j] = 0
		}
		CameraMovement{Kind: MoveZoomIn}.Render(dst, src, p)
		v := brightR(dst, 5, 5)
		if prev < 0 {
			prev = v
			continue
		}
		if v == prev {
			continue
		}
		stepDir := 1
		if v < prev {
			stepDir = -1
		}
		if dir == 0 {
			dir = stepDir
		} else if stepDir != dir {
			// Allow ≤ 2 reversals across the sweep — Catmull-Rom kernel
			// edges can produce a sub-1-unit wobble. Anything more was
			// the integer-rect-snap shimmer we just fixed.
			jitter++
		}
		prev = v
	}
	if jitter > 2 {
		t.Errorf("zoomin sub-pixel sweep had %d direction reversals (want ≤ 2)", jitter)
	}
}

func TestTransitionCrossfadeAtZero(t *testing.T) {
	// p=0 → only srcA visible, srcB suppressed.
	srcA := makeGradientRGBA(640, 360)
	srcB := image.NewRGBA(image.Rect(0, 0, 640, 360))
	for i := 0; i < len(srcB.Pix); i += 4 {
		srcB.Pix[i] = 0
		srcB.Pix[i+1] = 0
		srcB.Pix[i+2] = 0xff
		srcB.Pix[i+3] = 0xff
	}
	dst := image.NewRGBA(image.Rect(0, 0, 640, 360))
	Transition{Kind: "crossfade"}.Render(dst,
		srcA, CameraMovement{Kind: MoveStall}, 0,
		srcB, CameraMovement{Kind: MoveStall}, 0,
		0)
	// Expect the gradient (R≈127 at center) — no blue from srcB.
	if got := brightR(dst, 320, 180); got < 110 || got > 145 {
		t.Errorf("crossfade p=0 R = %d, want srcA gradient ≈127", got)
	}
}

func TestTransitionCrossfadeAtOne(t *testing.T) {
	// p=1 → only srcB visible.
	srcA := makeGradientRGBA(640, 360)
	srcB := image.NewRGBA(image.Rect(0, 0, 640, 360))
	for i := 0; i < len(srcB.Pix); i += 4 {
		srcB.Pix[i] = 0
		srcB.Pix[i+1] = 0
		srcB.Pix[i+2] = 0xff
		srcB.Pix[i+3] = 0xff
	}
	dst := image.NewRGBA(image.Rect(0, 0, 640, 360))
	Transition{Kind: "crossfade"}.Render(dst,
		srcA, CameraMovement{Kind: MoveStall}, 0,
		srcB, CameraMovement{Kind: MoveStall}, 0,
		1.0)
	// Expect blue (B≈255).
	if got := dst.RGBAAt(320, 180).B; got < 200 {
		t.Errorf("crossfade p=1 B = %d, want srcB blue ≈255", got)
	}
	if got := dst.RGBAAt(320, 180).R; got > 30 {
		t.Errorf("crossfade p=1 R = %d, want srcA suppressed ≈0", got)
	}
}

func TestTransitionCrossfadeMidpointMixes(t *testing.T) {
	srcA := makeGradientRGBA(640, 360)
	srcB := image.NewRGBA(image.Rect(0, 0, 640, 360))
	for i := 0; i < len(srcB.Pix); i += 4 {
		srcB.Pix[i] = 0
		srcB.Pix[i+1] = 0
		srcB.Pix[i+2] = 0xff
		srcB.Pix[i+3] = 0xff
	}
	dst := image.NewRGBA(image.Rect(0, 0, 640, 360))
	Transition{Kind: "crossfade"}.Render(dst,
		srcA, CameraMovement{Kind: MoveStall}, 0,
		srcB, CameraMovement{Kind: MoveStall}, 0,
		0.5)
	c := dst.RGBAAt(320, 180)
	if c.B < 100 {
		t.Errorf("crossfade midpoint B = %d, want partial blue (>= 100)", c.B)
	}
	if c.R < 40 {
		t.Errorf("crossfade midpoint R = %d, want partial gradient (>= 40)", c.R)
	}
}
