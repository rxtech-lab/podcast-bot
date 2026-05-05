package musicgen

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sirily11/debate-bot/internal/config"
)

// PuzzleMusic holds disk paths to the per-phase mp3 clips for one
// puzzle topic. Either field may be empty if generation failed —
// callers should fall through to dry TTS in that case.
type PuzzleMusic struct {
	SurfacePath string
	RevealPath  string
}

// ByPhase returns the file path for the given phase name, or "".
func (m *PuzzleMusic) ByPhase(phase string) string {
	if m == nil {
		return ""
	}
	switch phase {
	case PhaseSurface:
		return m.SurfacePath
	case PhaseReveal:
		return m.RevealPath
	}
	return ""
}

// Generate produces both phases' music in parallel. cacheDir, if
// non-empty, is where each clip is written; on a subsequent call with
// the same prompt content the cached file is returned instead of
// regenerated.
//
// The returned *PuzzleMusic is always non-nil even on partial failure —
// callers rely on per-field empty-string checks. The returned error is
// the joined per-phase failure list.
func Generate(ctx context.Context, client *Client, topic *config.DebateTopic, cacheDir string) (*PuzzleMusic, error) {
	if cacheDir != "" {
		_ = os.MkdirAll(cacheDir, 0o755)
	}

	jobs := []struct {
		name string
		out  *string
	}{
		{PhaseSurface, nil},
		{PhaseReveal, nil},
	}
	pm := &PuzzleMusic{}
	jobs[0].out = &pm.SurfacePath
	jobs[1].out = &pm.RevealPath

	var (
		wg     sync.WaitGroup
		errsMu sync.Mutex
		errs   []string
	)
	for _, j := range jobs {
		wg.Add(1)
		go func(name string, out *string) {
			defer wg.Done()
			prompt := buildPrompt(name, topic)
			path, err := loadOrGenerate(ctx, client, name, prompt, cacheDir)
			if err != nil {
				errsMu.Lock()
				errs = append(errs, fmt.Sprintf("%s: %v", name, err))
				errsMu.Unlock()
				return
			}
			*out = path
		}(j.name, j.out)
	}
	wg.Wait()

	if len(errs) > 0 {
		return pm, fmt.Errorf("music generation: %s", strings.Join(errs, "; "))
	}
	return pm, nil
}

// loadOrGenerate hits the disk cache first (keyed by sha1 of the prompt
// so prompt edits force a fresh generation), then calls the API and
// writes the bytes to disk. Returns the on-disk path so the amix stage
// can hand it to ffmpeg as `-i`.
func loadOrGenerate(ctx context.Context, client *Client, phase, prompt, cacheDir string) (string, error) {
	cachePath := ""
	if cacheDir != "" {
		cachePath = filepath.Join(cacheDir, phase+"-"+promptKey(prompt)+".mp3")
		if _, err := os.Stat(cachePath); err == nil {
			return cachePath, nil
		}
	}

	if client == nil {
		return "", fmt.Errorf("no musicgen client and cache miss")
	}
	raw, err := client.Generate(ctx, Request{Prompt: prompt})
	if err != nil {
		return "", err
	}
	if cachePath == "" {
		// Without a cache dir there's nowhere persistent to put the
		// bytes; create a tempfile so ffmpeg has something to read.
		f, terr := os.CreateTemp("", phase+"-*.mp3")
		if terr != nil {
			return "", fmt.Errorf("tempfile: %w", terr)
		}
		if _, werr := f.Write(raw); werr != nil {
			f.Close()
			return "", werr
		}
		_ = f.Close()
		return f.Name(), nil
	}
	if err := os.WriteFile(cachePath, raw, 0o644); err != nil {
		return "", fmt.Errorf("write cache: %w", err)
	}
	return cachePath, nil
}

func promptKey(s string) string {
	h := sha1.Sum([]byte(s))
	return hex.EncodeToString(h[:])[:12]
}

// GenerateClip produces a single sound clip for the planner's per-puzzle
// sound list (separate from the long phase-bed clips Generate handles).
// label is a short cache-friendly tag like "sound" — the on-disk filename
// is "<label>-<sha1(prompt+duration)>.mp3" so two puzzles with the same
// prompt + duration share the same cache file (a different requested
// duration cuts a fresh clip). Returns the disk path; caller hands it
// to the mixer as an `-i` input. Empty cacheDir falls back to a tempfile.
//
// durationSeconds, when > 0, is forwarded to Lyria as an explicit length
// hint via Request.DurationSeconds. 0 lets the model decide.
func GenerateClip(ctx context.Context, client *Client, prompt, cacheDir, label string, durationSeconds int) (string, error) {
	if strings.TrimSpace(prompt) == "" {
		return "", fmt.Errorf("musicgen: empty prompt")
	}
	if label == "" {
		label = "sound"
	}
	if cacheDir != "" {
		_ = os.MkdirAll(cacheDir, 0o755)
	}
	cacheKey := prompt
	if durationSeconds > 0 {
		cacheKey = fmt.Sprintf("%s|dur=%d", prompt, durationSeconds)
	}
	cachePath := ""
	if cacheDir != "" {
		cachePath = filepath.Join(cacheDir, label+"-"+promptKey(cacheKey)+".mp3")
		if _, err := os.Stat(cachePath); err == nil {
			return cachePath, nil
		}
	}
	if client == nil {
		return "", fmt.Errorf("no musicgen client and cache miss")
	}
	raw, err := client.Generate(ctx, Request{
		Prompt:          prompt,
		DurationSeconds: durationSeconds,
	})
	if err != nil {
		return "", err
	}
	if cachePath == "" {
		f, terr := os.CreateTemp("", label+"-*.mp3")
		if terr != nil {
			return "", fmt.Errorf("tempfile: %w", terr)
		}
		if _, werr := f.Write(raw); werr != nil {
			f.Close()
			return "", werr
		}
		_ = f.Close()
		return f.Name(), nil
	}
	if err := os.WriteFile(cachePath, raw, 0o644); err != nil {
		return "", fmt.Errorf("write cache: %w", err)
	}
	return cachePath, nil
}
