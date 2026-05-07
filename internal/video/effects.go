package video

import (
	"image"
	"image/draw"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/math/f64"
)

// MovementKind enumerates supported camera moves. String values are stable
// across the codebase: planner JSON, render state, and smoke tests all use
// the same lower-case tokens.
type MovementKind string

const (
	MoveStall     MovementKind = "stall"
	MovePanLeft   MovementKind = "panleft"
	MovePanRight  MovementKind = "panright"
	MovePanTop    MovementKind = "pantop"
	MovePanBottom MovementKind = "panbottom"
	MoveZoomIn    MovementKind = "zoomin"
	MoveZoomOut   MovementKind = "zoomout"
)

// movementZoom is the default fractional viewport size at the most-zoomed
// point of a zoom move (0.7 → push in 30%). Same magnitude is reused as
// the pan headroom so a pan move travels 30% of the source over its
// trajectory while keeping a 0.7× viewport.
const movementZoom = 0.7

// CameraMovement is a Ken-Burns-style pan/zoom applied to a still image
// source. It is a value type with no internal clock — the caller passes
// progress p ∈ [0,1] each frame, so the same instance can drive multiple
// sources in parallel and the renderer keeps the time bookkeeping.
//
// Value carries movement-specific parameters:
//   - pan*  → Value[0] = pan magnitude as fraction of source (default 0.30
//     when zero). Value[1] is reserved.
//   - zoom* → Value[0] = start scale, Value[1] = end scale. When both are
//     zero the defaults kick in: zoomin 1.0→0.7, zoomout 0.7→1.0.
//   - stall → Value is ignored; render is identity.
type CameraMovement struct {
	Kind  MovementKind
	Value [2]float64
}

// Render draws src into dst applying the camera move at progress p ∈ [0,1].
// p must already be eased — Render does not apply any easing of its own so
// callers can choose linear, cubic, ease-out etc.
//
// Implementation uses xdraw.CatmullRom.Transform with a sub-pixel-precise
// affine matrix. Both viewport position and viewport size are floats end-
// to-end, so a slow zoom no longer snaps the source rect by whole pixels
// between adjacent frames — the earthquake shimmer that integer rect
// rounding produced is gone.
//
// No-op when src or dst is nil. Empty bounds are treated as no-op so a
// 1×1 placeholder source doesn't crash the resampler.
func (m CameraMovement) Render(dst *image.RGBA, src *image.RGBA, p float64) {
	if dst == nil || src == nil {
		return
	}
	sb := src.Bounds()
	if sb.Empty() {
		return
	}
	if p < 0 {
		p = 0
	}
	if p > 1 {
		p = 1
	}

	// Stall fast path: when there's no motion AND the source already
	// matches the dst size pixel-for-pixel, a single draw.Draw with
	// draw.Src is a memcpy — no resampler kernel, no allocation. This
	// is the steady-state path the renderer hits on every frame for
	// most scenes (qa, reveal, conclusion, and any surface beat with
	// no planned move), so it's worth the special case.
	db := dst.Bounds()
	if (m.Kind == "" || m.Kind == MoveStall) && sb.Dx() == db.Dx() && sb.Dy() == db.Dy() {
		draw.Draw(dst, db, src, sb.Min, draw.Src)
		return
	}

	// Compute viewport in source space (origin + size). Origin is allowed
	// to be fractional — that's exactly what frees us from per-frame snap.
	sw := float64(sb.Dx())
	sh := float64(sb.Dy())

	var vx, vy, vw, vh float64
	switch m.Kind {
	case MoveZoomIn:
		startScale, endScale := m.zoomScales(1.0, movementZoom)
		s := startScale + (endScale-startScale)*p
		vw, vh = sw*s, sh*s
		vx = (sw - vw) / 2
		vy = (sh - vh) / 2
	case MoveZoomOut:
		startScale, endScale := m.zoomScales(movementZoom, 1.0)
		s := startScale + (endScale-startScale)*p
		vw, vh = sw*s, sh*s
		vx = (sw - vw) / 2
		vy = (sh - vh) / 2
	case MovePanLeft, MovePanRight, MovePanTop, MovePanBottom:
		mag := m.Value[0]
		if mag <= 0 {
			mag = 1.0 - movementZoom
		}
		// Viewport is movementZoom × source; origin walks across the
		// (1-movementZoom) headroom along one axis, centred on the other.
		vw = sw * movementZoom
		vh = sh * movementZoom
		hx := (sw - vw)
		hy := (sh - vh)
		travelX := sw * mag
		travelY := sh * mag
		switch m.Kind {
		case MovePanLeft:
			// Camera left → viewport drifts hx → (hx - travelX), clamped ≥ 0.
			endX := hx - travelX
			if endX < 0 {
				endX = 0
			}
			vx = hx + (endX-hx)*p
			vy = hy / 2
		case MovePanRight:
			endX := travelX
			if endX > hx {
				endX = hx
			}
			vx = endX * p
			vy = hy / 2
		case MovePanTop:
			endY := hy - travelY
			if endY < 0 {
				endY = 0
			}
			vx = hx / 2
			vy = hy + (endY-hy)*p
		case MovePanBottom:
			endY := travelY
			if endY > hy {
				endY = hy
			}
			vx = hx / 2
			vy = endY * p
		}
	default:
		// Stall (or unknown): identity — full source fills dst with no motion.
		vx, vy = 0, 0
		vw, vh = sw, sh
	}

	dw := float64(db.Dx())
	dh := float64(db.Dy())
	if dw <= 0 || dh <= 0 || vw <= 0 || vh <= 0 {
		return
	}

	// Affine: dst = M · src such that the viewport (vx, vy)..(vx+vw, vy+vh)
	// fills the dst bounds. We anchor src at sb.Min so the matrix accounts
	// for any non-zero source origin (RGBA buffers built from sub-images).
	kx := dw / vw
	ky := dh / vh
	a := f64.Aff3{
		kx, 0, float64(db.Min.X) - kx*(float64(sb.Min.X)+vx),
		0, ky, float64(db.Min.Y) - ky*(float64(sb.Min.Y)+vy),
	}
	// ApproxBiLinear is ~3–4× faster than CatmullRom on a full-frame image
	// and the visual difference for slow Ken-Burns motion is negligible —
	// the move only ever zooms ≤ 30% so we never up-sample aggressively.
	// CatmullRom at 30 fps blew the per-frame budget and made the encoder
	// buffer; bilinear is fast enough to leave headroom for the rest of
	// Frame() (subtitle, lower-third, ticker, etc).
	xdraw.ApproxBiLinear.Transform(dst, a, src, sb, xdraw.Over, nil)
}

// zoomScales picks the start/end scale factors for a zoom move. Zero values
// in m.Value fall back to the (defStart, defEnd) caller defaults.
func (m CameraMovement) zoomScales(defStart, defEnd float64) (float64, float64) {
	s, e := m.Value[0], m.Value[1]
	if s == 0 && e == 0 {
		return defStart, defEnd
	}
	if s == 0 {
		s = defStart
	}
	if e == 0 {
		e = defEnd
	}
	return s, e
}

// Transition composes two animated sources with a transition between them.
// Like CameraMovement it carries no clock — the caller drives progress per
// frame and the type itself is just a configured renderer.
type Transition struct {
	// Kind selects the transition style. Today only "crossfade" is
	// implemented; "" defaults to crossfade. Future: "cut", "dissolve",
	// "dip-to-black".
	Kind string
}

// Render draws a transition from srcA to srcB into dst at progress p ∈ [0,1].
// Both sources may carry their own camera movement and per-source progress
// so a crossfade between two animated images keeps both layers in motion
// (each on its own clock) instead of freezing one.
//
// Pass srcA = nil when the transition has no outgoing layer (first scene
// fade-in). Pass srcB = nil when there's no incoming layer (rare, but
// supported). When both are nil the call is a no-op.
//
// p is the (eased) transition progress; p=0 reads as srcA fully visible,
// p=1 reads as srcB fully visible.
func (t Transition) Render(
	dst *image.RGBA,
	srcA *image.RGBA, moveA CameraMovement, progressA float64,
	srcB *image.RGBA, moveB CameraMovement, progressB float64,
	p float64,
) {
	if dst == nil {
		return
	}
	if p < 0 {
		p = 0
	}
	if p > 1 {
		p = 1
	}

	// Steady-state fast path: no incoming layer to blend in, just paint
	// the outgoing one. This is the every-frame path between transitions
	// once the renderer has cleared prevSceneBg.
	if srcB == nil || p <= 0 {
		if srcA != nil {
			moveA.Render(dst, srcA, progressA)
		}
		return
	}
	// Fade-complete fast path: incoming is fully opaque, outgoing
	// contributes nothing — skip its render and the temp allocation.
	if p >= 1 {
		moveB.Render(dst, srcB, progressB)
		return
	}
	// Mid-fade slow path: paint outgoing into dst, then composite the
	// incoming layer on top via a same-size temp at α = p.
	if srcA != nil {
		moveA.Render(dst, srcA, progressA)
	}
	tmp := image.NewRGBA(dst.Bounds())
	moveB.Render(tmp, srcB, progressB)
	blitWithGlobalAlpha(dst, tmp, p)
}
