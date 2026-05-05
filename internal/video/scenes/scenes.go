// Package scenes builds the per-phase background images that PuzzleStage
// fades behind the subtitle. One PuzzleScenes is generated per puzzle topic.
// The two long phases (surface/briefing and conclusion) get multiple distinct
// variants each so the renderer can rotate between them and keep the visual
// story moving; the short pivot phases (qa, reveal) get a single image. All
// images are cached on disk so a re-run hits the cache instead of regenerating.
package scenes

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/video/imagegen"
)

// DefaultPerFrameTimeout is the default budget for one image-gen call when
// GenerateOptions.PerFrameTimeout is zero. Long enough to cover a slow
// gateway round-trip plus a couple of internal retries; short enough that a
// single hung frame can't stall the surface batch indefinitely (the failure
// mode that left a debate "pending" for 8 minutes waiting on surface-v33).
const DefaultPerFrameTimeout = 120 * time.Second

// GenerateOptions tunes per-frame behaviour for GenerateWithOptions.
//
// PerFrameTimeout caps how long a single loadOrGenerate call may run; zero
// means use DefaultPerFrameTimeout. OnFrame, if non-nil, is invoked after
// every job completes — caller can attach successful frames to a live stage
// as they arrive instead of waiting for the whole batch. err is non-nil
// when the frame failed (timeout / permanent / exhausted retries) and img
// is nil in that case. OnFrame must be non-blocking — it runs on the
// gen goroutine for that frame.
//
// SurfacePriorityCount, when > 0, gates non-priority surface variants from
// starting until every variant in [0, SurfacePriorityCount) has completed
// (success or failure). The host narrates surface frames in strict order and
// emits `<scene N/>` markers tied to specific variant indices, so blocking
// the show on "first N specific variants" rather than "any N% complete"
// prevents a low-index straggler from making the host emit a marker for a
// frame that hasn't arrived yet. Other phases (qa, reveal, conclusion) are
// unaffected.
type GenerateOptions struct {
	PerFrameTimeout      time.Duration
	OnFrame              func(name string, variant int, img *image.RGBA, err error)
	SurfacePriorityCount int
}

// Scene names used for cache filenames and prompt selection.
const (
	SceneSurface    = "surface"
	SceneQA         = "qa"
	SceneReveal     = "reveal"
	SceneConclusion = "conclusion"
)

// SurfaceVariantCount and ConclusionVariantCount are the static-fallback
// frame counts used when scenes.Plan returns no plan AND the heuristic
// fallback can't produce one either. Surface aligns with planner.go's
// minSurfaceFrames so the static path doesn't downgrade the floor that
// the dynamic path enforces. Conclusion's 4 stays — it's only rotated by
// a 30s timer, so the count just bounds how long before the loop visibly
// repeats.
const (
	SurfaceVariantCount    = 6
	ConclusionVariantCount = 4
)

// surfaceBatchSize is the maximum number of surface frames generated in
// parallel. Sized to maxSurfaceFrames so the busiest puzzle's surface
// gen runs in a single fan-out — the gateway happily handles this batch
// width and the wall-clock for first-paint drops accordingly.
const surfaceBatchSize = maxSurfaceFrames

// genSize is what we ask the gateway for. Gemini flash-image accepts
// 1024×1024; we resample to the renderer's 1280×720 below. Picking square
// gives the model the most freedom — landscape crops cleanly because
// prompts ask for a quiet bottom third.
const genSize = "1024x1024"

// frameW/frameH match the renderer's internal composition resolution
// (Encoder.videoWidth/Height in internal/video/encoder.go). The renderer
// blits scene bgs straight into the frame without scaling.
const (
	frameW = 1280
	frameH = 720
)

// PuzzleScenes is the set of pre-generated bgs for one puzzle topic. Surface
// and Conclusion are slices because those phases rotate through several
// distinct frames; QA and Reveal are single images. Any field/element may be
// nil if generation failed — callers should fall back to the renderer's
// default bg in that case.
type PuzzleScenes struct {
	Surface    []*image.RGBA
	QA         *image.RGBA
	Reveal     *image.RGBA
	Conclusion []*image.RGBA
}

// ByName returns the first available image for the given scene name, or nil.
// Convenience for callers that don't rotate (e.g., the smoke renderer
// stamping a single still per scene). For rotation, use ByNameIdx.
func (s *PuzzleScenes) ByName(name string) *image.RGBA {
	return s.ByNameIdx(name, 0)
}

// ByNameIdx returns the variant image for the given scene name modulo the
// available variant count. idx is folded into [0,len) so callers can advance
// a free-running counter without bookkeeping. Returns nil when the slot is
// empty (singleton scene with no variants, or generation failure).
func (s *PuzzleScenes) ByNameIdx(name string, idx int) *image.RGBA {
	if s == nil {
		return nil
	}
	pickFromSlice := func(xs []*image.RGBA) *image.RGBA {
		// Skip nil entries so a partial generation failure on one variant
		// doesn't strobe to a black frame mid-rotation.
		n := len(xs)
		if n == 0 {
			return nil
		}
		if idx < 0 {
			idx = 0
		}
		for i := 0; i < n; i++ {
			img := xs[(idx+i)%n]
			if img != nil {
				return img
			}
		}
		return nil
	}
	switch name {
	case SceneSurface:
		return pickFromSlice(s.Surface)
	case SceneQA:
		return s.QA
	case SceneReveal:
		return s.Reveal
	case SceneConclusion:
		return pickFromSlice(s.Conclusion)
	}
	return nil
}

// ByNameIdxExact returns the image at the EXACT slot — unlike ByNameIdx,
// it does NOT fall through to the next non-nil variant when the requested
// slot is empty. Use this when the caller wants to honour the host's
// explicit `<scene N/>` marker but tolerate the frame not being generated
// yet (PuzzleStage falls back to leaving the current background in place,
// rather than jumping to a different beat). For QA/Reveal singletons this
// is identical to ByNameIdx.
func (s *PuzzleScenes) ByNameIdxExact(name string, idx int) *image.RGBA {
	if s == nil {
		return nil
	}
	pickExact := func(xs []*image.RGBA) *image.RGBA {
		if idx < 0 || idx >= len(xs) {
			return nil
		}
		return xs[idx]
	}
	switch name {
	case SceneSurface:
		return pickExact(s.Surface)
	case SceneQA:
		return s.QA
	case SceneReveal:
		return s.Reveal
	case SceneConclusion:
		return pickExact(s.Conclusion)
	}
	return nil
}

// VariantCount reports how many variants were generated for the named scene
// (1 for singleton scenes, the slice length for rotating ones, 0 if unknown).
// PuzzleStage uses this to decide whether to start a rotation goroutine.
func (s *PuzzleScenes) VariantCount(name string) int {
	if s == nil {
		return 0
	}
	switch name {
	case SceneSurface:
		return len(s.Surface)
	case SceneQA:
		if s.QA != nil {
			return 1
		}
		return 0
	case SceneReveal:
		if s.Reveal != nil {
			return 1
		}
		return 0
	case SceneConclusion:
		return len(s.Conclusion)
	}
	return 0
}

// Generate produces every scene image in parallel. The two long phases
// (surface, conclusion) get SurfaceVariantCount / ConclusionVariantCount
// distinct variants; QA and Reveal get one each. cacheDir, if non-empty, is
// where each scene PNG is written; on a subsequent call with the same prompt
// content the cached image is loaded instead of regenerated.
//
// The returned *PuzzleScenes is always non-nil, even on partial failure —
// callers should rely on per-field nil checks. The returned error joins all
// per-job failures so callers can log them and still proceed with whatever
// images succeeded.
//
// To split surface and conclusion timing, see GeneratePhase.
func Generate(ctx context.Context, client *imagegen.Client, topic *config.DebateTopic, cacheDir string) (*PuzzleScenes, error) {
	return GenerateWithPlan(ctx, client, topic, nil, cacheDir, "")
}

// GenerateWithPlan is Generate with an optional ScenePlan that overrides the
// per-phase variant count and direction. A nil plan keeps the static
// SurfaceVariantCount / ConclusionVariantCount and the built-in
// surfaceVariantDirections / conclusionVariantDirections. A non-nil plan
// uses len(plan.Surface) / len(plan.Conclusion) and folds each entry's
// directional sentence into the variant prompt.
//
// phases, when non-empty, restricts generation to the listed scene names
// (any subset of SceneSurface / SceneQA / SceneReveal / SceneConclusion).
// Empty means "every phase" — used by Generate. Restricting lets the caller
// stage generation: surface + qa + reveal first (so the podcast can start),
// then conclusion in the background.
func GenerateWithPlan(ctx context.Context, client *imagegen.Client, topic *config.DebateTopic,
	plan *ScenePlan, cacheDir string, phases ...string,
) (*PuzzleScenes, error) {
	return GenerateWithOptions(ctx, client, topic, plan, cacheDir, GenerateOptions{}, phases...)
}

// GenerateWithOptions is GenerateWithPlan with extra knobs (per-frame
// timeout, per-frame completion callback). See GenerateOptions. Existing
// callers should keep using GenerateWithPlan; new callers that want
// streaming attach or a tighter per-frame budget reach for this entry
// point.
func GenerateWithOptions(ctx context.Context, client *imagegen.Client, topic *config.DebateTopic,
	plan *ScenePlan, cacheDir string, opts GenerateOptions, phases ...string,
) (*PuzzleScenes, error) {
	perFrame := opts.PerFrameTimeout
	if perFrame <= 0 {
		perFrame = DefaultPerFrameTimeout
	}
	if cacheDir != "" {
		_ = os.MkdirAll(cacheDir, 0o755)
	}

	surfaceN := SurfaceVariantCount
	if n := plan.SurfaceCount(); n > 0 {
		surfaceN = n
	}
	conclusionN := ConclusionVariantCount
	if n := plan.ConclusionCount(); n > 0 {
		conclusionN = n
	}

	out := &PuzzleScenes{
		Surface:    make([]*image.RGBA, surfaceN),
		Conclusion: make([]*image.RGBA, conclusionN),
	}

	type job struct {
		name      string
		variant   int
		cacheName string
		assign    func(*image.RGBA)
	}
	wantPhase := func(name string) bool {
		if len(phases) == 0 {
			return true
		}
		for _, p := range phases {
			if p == name {
				return true
			}
		}
		return false
	}
	var jobs []job
	if wantPhase(SceneSurface) {
		for i := 0; i < surfaceN; i++ {
			i := i
			jobs = append(jobs, job{
				name:      SceneSurface,
				variant:   i,
				cacheName: fmt.Sprintf("%s-v%d", SceneSurface, i),
				assign:    func(img *image.RGBA) { out.Surface[i] = img },
			})
		}
	}
	if wantPhase(SceneQA) {
		jobs = append(jobs, job{
			name:      SceneQA,
			cacheName: SceneQA,
			assign:    func(img *image.RGBA) { out.QA = img },
		})
	}
	if wantPhase(SceneReveal) {
		jobs = append(jobs, job{
			name:      SceneReveal,
			cacheName: SceneReveal,
			assign:    func(img *image.RGBA) { out.Reveal = img },
		})
	}
	if wantPhase(SceneConclusion) {
		for i := 0; i < conclusionN; i++ {
			i := i
			jobs = append(jobs, job{
				name:      SceneConclusion,
				variant:   i,
				cacheName: fmt.Sprintf("%s-v%d", SceneConclusion, i),
				assign:    func(img *image.RGBA) { out.Conclusion[i] = img },
			})
		}
	}

	// surfaceBatchSemaphore caps how many surface frames generate
	// concurrently. The gateway's Gemini chat-completions image path takes
	// a single prompt per call, so the only knob we have is goroutine
	// concurrency. 13 was picked deliberately by the operator — gives
	// good throughput on long surface plans (up to maxSurfaceFrames) while
	// staying under the upstream gateway's per-IP fan-out budget. Other
	// scene phases (qa / reveal / conclusion) are not gated; they're at
	// most 1+1+conclusionN jobs total, so concurrent saturation stays
	// bounded.
	surfaceSem := make(chan struct{}, surfaceBatchSize)

	// Priority gate: non-priority surface variants block on priorityDone
	// until every variant in [0, priorityN) has completed (success OR
	// failure — counting both avoids a deadlock when the gateway returns
	// a permanent failure for a priority slot). priorityJobs is the
	// actual priority count after intersecting with the active phase
	// set, so a phases=[SceneConclusion] call doesn't dangle on a
	// never-decremented counter.
	priorityN := opts.SurfacePriorityCount
	if priorityN < 0 {
		priorityN = 0
	}
	priorityJobs := 0
	if priorityN > 0 {
		for _, j := range jobs {
			if j.name == SceneSurface && j.variant < priorityN {
				priorityJobs++
			}
		}
	}
	priorityDone := make(chan struct{})
	var priorityWg sync.WaitGroup
	if priorityJobs > 0 {
		priorityWg.Add(priorityJobs)
		go func() {
			priorityWg.Wait()
			close(priorityDone)
		}()
	} else {
		close(priorityDone)
	}

	var (
		wg     sync.WaitGroup
		errsMu sync.Mutex
		errs   []string
	)
	for _, j := range jobs {
		wg.Add(1)
		go func(j job) {
			defer wg.Done()
			isPrioritySurface := j.name == SceneSurface && j.variant < priorityN
			if isPrioritySurface {
				defer priorityWg.Done()
			} else if j.name == SceneSurface && priorityJobs > 0 {
				// Hold non-priority surface variants out of the gateway
				// queue entirely until the priority batch finishes — this
				// is what gives the first N variants real scheduling
				// priority, not just bookkeeping priority.
				select {
				case <-ctx.Done():
					if opts.OnFrame != nil {
						opts.OnFrame(j.name, j.variant, nil, ctx.Err())
					}
					return
				case <-priorityDone:
				}
			}
			if j.name == SceneSurface {
				surfaceSem <- struct{}{}
				defer func() { <-surfaceSem }()
			}
			prompt := buildPromptVariantWithPlan(j.name, topic, j.variant, plan)
			// Per-frame timeout caps any single hung gen call. Derived
			// from ctx so a parent cancel still aborts immediately.
			frameCtx, cancel := context.WithTimeout(ctx, perFrame)
			img, err := loadOrGenerate(frameCtx, client, j.cacheName, prompt, cacheDir)
			cancel()
			if err != nil {
				errsMu.Lock()
				errs = append(errs, fmt.Sprintf("%s: %v", j.cacheName, err))
				errsMu.Unlock()
				if opts.OnFrame != nil {
					opts.OnFrame(j.name, j.variant, nil, err)
				}
				return
			}
			j.assign(img)
			if opts.OnFrame != nil {
				opts.OnFrame(j.name, j.variant, img, nil)
			}
		}(j)
	}
	wg.Wait()

	if len(errs) > 0 {
		return out, fmt.Errorf("scene generation: %s", strings.Join(errs, "; "))
	}
	return out, nil
}

// loadOrGenerate hits the disk cache first (keyed by sha1 of the prompt so
// prompt edits force a fresh generation), then calls the gateway.
func loadOrGenerate(ctx context.Context, client *imagegen.Client, name, prompt, cacheDir string) (*image.RGBA, error) {
	cachePath := ""
	if cacheDir != "" {
		cachePath = filepath.Join(cacheDir, name+"-"+promptKey(prompt)+".png")
		if img, err := readCachedRGBA(cachePath); err == nil {
			return img, nil
		}
	}

	if client == nil {
		return nil, fmt.Errorf("no imagegen client and cache miss")
	}
	raw, err := client.Generate(ctx, imagegen.Request{
		Model:  imagegen.PuzzleSceneModel,
		Prompt: prompt,
		Size:   genSize,
	})
	if err != nil {
		return nil, err
	}
	img, err := imagegen.DecodeAndResize(raw, frameW, frameH)
	if err != nil {
		return nil, err
	}
	if cachePath != "" {
		if err := writeRGBA(cachePath, img); err != nil {
			return img, nil
		}
	}
	return img, nil
}

func promptKey(s string) string {
	h := sha1.Sum([]byte(s))
	return hex.EncodeToString(h[:])[:12]
}

func readCachedRGBA(path string) (*image.RGBA, error) {
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
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			out.Set(x, y, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return out, nil
}

func writeRGBA(path string, img *image.RGBA) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := &png.Encoder{CompressionLevel: png.BestCompression}
	return enc.Encode(f, img)
}

// BuildPrompt is exported so the smoke test and tests can probe the prompt
// for a given scene without invoking generation. Returns the variant-0 prompt
// — use BuildPromptVariant for a specific variant index.
func BuildPrompt(name string, topic *config.DebateTopic) string {
	return buildPromptVariant(name, topic, 0)
}

// BuildPromptVariant returns the prompt for a specific variant of a scene.
// Surface and Conclusion are the only scenes that have multiple variants;
// for QA and Reveal the variant index is ignored.
func BuildPromptVariant(name string, topic *config.DebateTopic, variant int) string {
	return buildPromptVariant(name, topic, variant)
}

// buildPromptVariantWithPlan dispatches to the plan-driven prompt builder
// when a ScenePlan provides a custom direction for the requested variant,
// and falls back to the static-direction builder otherwise. Empty / out-of-
// range plan entries trigger the fallback.
func buildPromptVariantWithPlan(name string, topic *config.DebateTopic, variant int, plan *ScenePlan) string {
	if plan != nil {
		var directions []string
		switch name {
		case SceneSurface:
			directions = plan.Surface
		case SceneConclusion:
			directions = plan.Conclusion
		}
		if variant >= 0 && variant < len(directions) && strings.TrimSpace(directions[variant]) != "" {
			return buildPromptWithDirection(name, topic, directions[variant])
		}
	}
	return buildPromptVariant(name, topic, variant)
}

// buildPromptWithDirection constructs the per-frame prompt using a
// caller-provided direction sentence (from a ScenePlan) instead of the
// static rotation in surfaceVariantDirections / conclusionVariantDirections.
// Style + safety boilerplate matches buildPromptVariant exactly so the two
// paths produce visually consistent images.
func buildPromptWithDirection(name string, topic *config.DebateTopic, direction string) string {
	surface := strings.TrimSpace(topic.Surface)
	const styleSuffix = `
Style: ANIME cinematic illustration. Hand-drawn, soft cell-shading,
expressive lighting and color, in the sensibility of Makoto Shinkai /
Studio Ghibli / Kyoto Animation. Painterly skies, delicate linework,
atmospheric mood. NOT photoreal, NOT 3D-rendered, NOT realistic photo.
No text, no letters, no captions, no subtitles, no logos, no UI overlays.
No faces speaking close-up. Wide cinematic 16:9 framing.`

	switch name {
	case SceneSurface:
		return strings.TrimSpace(fmt.Sprintf(`
Anime cinematic illustration for this scenario:

%s

Per-frame direction (this is one of several frames cut together — make this
specific variant visually distinct from the others):
%s

Moody, evocative, atmospheric. Capture the situation as a frozen tableau
— the viewer should sense the mystery without being told the answer.
%s`, surface, direction, styleSuffix))

	case SceneConclusion:
		return strings.TrimSpace(fmt.Sprintf(`
Anime quiet, contemplative aftermath scene reflecting on this situation:

%s

Per-frame direction (this is one of several frames cut together — make this
specific variant visually distinct from the others):
%s

Soft warm golden-hour light, gentle stillness, sense of closure. The
mystery has been revealed and the moment lingers.
%s`, surface, direction, styleSuffix))
	}
	return buildPromptVariant(name, topic, 0)
}

// surfaceVariantDirections steers each Surface variant toward a different
// composition / framing so the four images read as a deliberate edit
// sequence rather than near-duplicates. Index modulo len() so callers can
// pass a free-running counter without bounds checks.
var surfaceVariantDirections = []string{
	"Wide cinematic establishing shot. The full setting in one frame, " +
		"a quiet ominous tableau — the moment just before the story begins.",
	"Intimate close detail. Focus on a single significant object, gesture, " +
		"or piece of the environment from the scenario. Soft focus around " +
		"it; the rest of the world is suggestion.",
	"Mid-shot from a different angle than the establishing frame. " +
		"Emphasise atmosphere and posture; the silhouette of a figure in " +
		"context, never a recognisable face.",
	"Pure environment piece — no figures at all. The location alone, " +
		"holding the emotional weight. A different time of day or weather " +
		"than the establishing shot so the four frames read as separate " +
		"beats.",
}

// conclusionVariantDirections does the same for the Conclusion phase. Each
// frame should feel like the same world a moment later — the puzzle has
// just been resolved and the camera lingers in different corners of the
// aftermath.
var conclusionVariantDirections = []string{
	"Wide reflective tableau of the place after the truth has been said. " +
		"Stillness, soft warm light, no figures speaking — just the space.",
	"Departing-figure silhouette. A small figure walking away into dusk " +
		"or distance, back to camera, scale dwarfed by the environment.",
	"Quiet still-life detail. A small object from the scenario rests in " +
		"soft golden light, rich with meaning, gently out of focus around " +
		"the edges.",
	"Wide exterior landscape. Time has passed; the world continues. " +
		"Tranquil, gentle, almost a postcard — closure rather than tension.",
}

func buildPromptVariant(name string, topic *config.DebateTopic, variant int) string {
	surface := strings.TrimSpace(topic.Surface)
	truth := strings.TrimSpace(topic.Truth)

	// Style direction shared across every scene: anime cinematic
	// illustration in the Makoto Shinkai / Studio Ghibli idiom — soft
	// cell-shading, atmospheric lighting, expressive but never photoreal.
	// Plus the practical constraint: no UI/text in the picture so the
	// chrome we composite on top doesn't fight with painted glyphs.
	const styleSuffix = `
Style: ANIME cinematic illustration. Hand-drawn, soft cell-shading,
expressive lighting and color, in the sensibility of Makoto Shinkai /
Studio Ghibli / Kyoto Animation. Painterly skies, delicate linework,
atmospheric mood. NOT photoreal, NOT 3D-rendered, NOT realistic photo.
No text, no letters, no captions, no subtitles, no logos, no UI overlays.
No faces speaking close-up. Wide cinematic 16:9 framing.`

	switch name {
	case SceneSurface:
		direction := surfaceVariantDirections[((variant%len(surfaceVariantDirections))+len(surfaceVariantDirections))%len(surfaceVariantDirections)]
		return strings.TrimSpace(fmt.Sprintf(`
Anime cinematic illustration for this scenario:

%s

Variant direction (this is one of several frames cut together — make this
specific variant visually distinct from the others):
%s

Moody, evocative, atmospheric. Capture the situation as a frozen tableau
— the viewer should sense the mystery without being told the answer.
%s`, surface, direction, styleSuffix))

	case SceneQA:
		return strings.TrimSpace(fmt.Sprintf(`
Anime atmospheric, contemplative scene that sets the mood for a yes/no
question-and-answer investigation of this situation:

%s

Soft focus, thoughtful, cool palette, gentle blues and teals. Suggest
curiosity and uncertainty without revealing what happened.
%s`, surface, styleSuffix))

	case SceneReveal:
		// Reveal is the only scene that has access to the truth.
		return strings.TrimSpace(fmt.Sprintf(`
Anime dramatic, revelatory cinematic illustration of the underlying truth
behind this situation:

%s

Heavy chiaroscuro, dramatic rim lighting, shocked stillness, emotional
weight. Saturated accent color (deep red or amber) cutting through the
darkness. Convey the realization moment — the painful clarity of what
really happened.
%s`, truth, styleSuffix))

	case SceneConclusion:
		direction := conclusionVariantDirections[((variant%len(conclusionVariantDirections))+len(conclusionVariantDirections))%len(conclusionVariantDirections)]
		return strings.TrimSpace(fmt.Sprintf(`
Anime quiet, contemplative aftermath scene reflecting on this situation:

%s

Variant direction (this is one of several frames cut together — make this
specific variant visually distinct from the others):
%s

Soft warm golden-hour light, gentle stillness, sense of closure. The
mystery has been revealed and the moment lingers.
%s`, surface, direction, styleSuffix))
	}
	return ""
}
