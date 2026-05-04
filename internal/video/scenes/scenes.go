// Package scenes builds the per-phase background images that PuzzleStage
// fades behind the subtitle. One PuzzleScenes is generated per puzzle topic
// — four images keyed by phase (surface / qa / reveal / conclusion) — and
// cached on disk so a re-run hits the cache instead of regenerating.
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

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/video/imagegen"
)

// Scene names used for cache filenames and prompt selection.
const (
	SceneSurface    = "surface"
	SceneQA         = "qa"
	SceneReveal     = "reveal"
	SceneConclusion = "conclusion"
)

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

// PuzzleScenes is the set of pre-generated bgs for one puzzle topic. Any
// field may be nil if generation failed — callers should fall back to the
// renderer's default bg in that case.
type PuzzleScenes struct {
	Surface    *image.RGBA
	QA         *image.RGBA
	Reveal     *image.RGBA
	Conclusion *image.RGBA
}

// ByName returns the scene image for the given scene name, or nil.
func (s *PuzzleScenes) ByName(name string) *image.RGBA {
	if s == nil {
		return nil
	}
	switch name {
	case SceneSurface:
		return s.Surface
	case SceneQA:
		return s.QA
	case SceneReveal:
		return s.Reveal
	case SceneConclusion:
		return s.Conclusion
	}
	return nil
}

// Generate produces all four scenes in parallel. cacheDir, if non-empty, is
// where each scene PNG is written; on a subsequent call with the same
// prompt content the cached image is loaded instead of regenerated.
//
// The returned *PuzzleScenes is always non-nil, even on partial failure —
// callers should rely on per-field nil checks. The returned error is the
// first generation/decode failure encountered (joined with the rest).
func Generate(ctx context.Context, client *imagegen.Client, topic *config.DebateTopic, cacheDir string) (*PuzzleScenes, error) {
	if cacheDir != "" {
		_ = os.MkdirAll(cacheDir, 0o755)
	}

	jobs := []struct {
		name string
		ptr  **image.RGBA
	}{
		{SceneSurface, nil},
		{SceneQA, nil},
		{SceneReveal, nil},
		{SceneConclusion, nil},
	}
	out := &PuzzleScenes{}
	jobs[0].ptr = &out.Surface
	jobs[1].ptr = &out.QA
	jobs[2].ptr = &out.Reveal
	jobs[3].ptr = &out.Conclusion

	var (
		wg     sync.WaitGroup
		errsMu sync.Mutex
		errs   []string
	)
	for _, j := range jobs {
		wg.Add(1)
		go func(name string, ptr **image.RGBA) {
			defer wg.Done()
			prompt := buildPrompt(name, topic)
			img, err := loadOrGenerate(ctx, client, name, prompt, cacheDir)
			if err != nil {
				errsMu.Lock()
				errs = append(errs, fmt.Sprintf("%s: %v", name, err))
				errsMu.Unlock()
				return
			}
			*ptr = img
		}(j.name, j.ptr)
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
// for a given scene without invoking generation.
func BuildPrompt(name string, topic *config.DebateTopic) string {
	return buildPrompt(name, topic)
}

func buildPrompt(name string, topic *config.DebateTopic) string {
	surface := strings.TrimSpace(topic.Surface)
	truth := strings.TrimSpace(topic.Truth)

	// Style direction shared across every scene: anime cinematic
	// illustration in the Makoto Shinkai / Studio Ghibli idiom — soft
	// cell-shading, atmospheric lighting, expressive but never photoreal.
	// Plus the practical constraints: no UI/text in the picture, quiet
	// bottom band so the subtitle card sits cleanly on top.
	const styleSuffix = `
Style: ANIME cinematic illustration. Hand-drawn, soft cell-shading,
expressive lighting and color, in the sensibility of Makoto Shinkai /
Studio Ghibli / Kyoto Animation. Painterly skies, delicate linework,
atmospheric mood. NOT photoreal, NOT 3D-rendered, NOT realistic photo.
No text, no letters, no captions, no subtitles, no logos, no UI overlays.
No faces speaking close-up. Wide cinematic 16:9 framing. Leave the
BOTTOM 35% of the frame visually quiet (low contrast, soft, uncluttered)
so a subtitle card can be overlaid on top without clashing.`

	switch name {
	case SceneSurface:
		return strings.TrimSpace(fmt.Sprintf(`
Anime cinematic establishing shot illustrating this scenario:

%s

Moody, evocative, atmospheric. Capture the situation as a frozen tableau
— the viewer should sense the mystery without being told the answer.
%s`, surface, styleSuffix))

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
		return strings.TrimSpace(fmt.Sprintf(`
Anime quiet, contemplative aftermath scene reflecting on this situation:

%s

Soft warm golden-hour light, gentle stillness, sense of closure. The
mystery has been revealed and the moment lingers.
%s`, surface, styleSuffix))
	}
	return ""
}
