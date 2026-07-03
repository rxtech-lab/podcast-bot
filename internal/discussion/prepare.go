// Package discussion holds the shared asset-prep for the panel-discussion
// content type: it renders the pre-generated background palette and the short
// music bed used under the discussion.
//
// The logic lived in cmd/debate-bot (stream mode) only, which is why
// discussion jobs submitted to video mode rendered over a bare background with
// no imagery — the video-job runner had no way to call it. Pulling it into an
// internal package lets both the stream runner (cmd/debate-bot) and the
// upload-and-render runner (internal/videojob) share one implementation.
package discussion

import (
	"context"
	"fmt"
	"image"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/sirily11/debate-bot/internal/audio/musicgen"
	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/video/imagegen"
)

// PaletteStage is the slice of *video.DiscussionStage the prep needs: a sink
// for background frames as they finish generating. Taking an interface keeps
// this package from importing internal/video (which would otherwise be an
// import cycle through content_creator).
type PaletteStage interface {
	AttachPaletteFrame(*image.RGBA)
}

// AudioSink installs the generated music bed before the orchestrator runs.
// *contentcreator.Orchestrator satisfies this via SetDiscussionAudio.
type AudioSink interface {
	SetDiscussionAudio(beds map[string]string, sounds, moods []string)
}

// paletteMoods are the moods of the pre-generated background palette. The
// first frame to finish paints immediately so the show starts fast; the rest
// stream in and join the stage's rotation. The silent commander layers fresh,
// on-the-fly backgrounds over these as the conversation evolves.
var paletteMoods = []string{
	"calm, neutral establishing mood",
	"warm, optimistic mood",
	"cool, serious / analytical mood",
	"tense, high-stakes mood",
}

// bedDurationSeconds is the length of each generated music bed. The mixer
// loops the bed, so a short clip is plenty — and Lyria renders a short clip
// far faster than a 1-2 minute one, which is the dominant first-run cost.
const bedDurationSeconds = 45

// bedSpecs are the short beds generated for a discussion. Keep this to one
// session bed so each discussion spends one billed Lyria call instead of two.
var bedSpecs = []struct{ label, prompt, mood string }{
	{"calm", "A calm, warm, instrumental ambient bed for a thoughtful panel discussion. Soft pads, gentle and unobtrusive, no drums, loops cleanly.", "calm, reflective ambient bed"},
}

// Music bundles the music wiring handed to the orchestrator.
type Music struct {
	Beds   map[string]string // session-bed map for the pipeline (key "session")
	Sounds []string          // index-aligned beds the commander crossfades to
	Moods  []string          // descriptions for the commander's prompt
}

// PrepareAssets gets the show started as fast as possible: it blocks only on
// the FIRST background frame plus the music bed (the session bed must be set
// before orch.Run), and streams the remaining palette frames onto the live
// stage afterward. The silent commander (started inside orch.Run) then
// generates fresh backgrounds on the fly.
//
// Everything degrades gracefully: failed image/music gen just means fewer
// frames / no bed, and the show still runs. outDir is the per-run output
// directory used to cache the generated music.
// musicRecorder is invoked once per billed Lyria generation so callers can fold
// music cost into a run total. nil disables metering (e.g. smoke tools).
type musicRecorder = func()

func PrepareAssets(ctx context.Context, log *slog.Logger, outDir string,
	stage PaletteStage, topic *config.DebateTopic, orch AudioSink, rec musicRecorder) Music {
	// Palette: stream frames onto the stage; signal as soon as the first lands.
	firstReady := make(chan struct{})
	go GeneratePalette(ctx, log, stage, topic, firstReady)

	// Music: generate the beds in parallel (they're the long pole on a cold run).
	musicCh := make(chan Music, 1)
	go func() { musicCh <- GenerateMusic(ctx, log, topic, outDir, rec) }()

	// Block only on what's needed to start: the first background + music.
	select {
	case <-firstReady:
	case <-ctx.Done():
		return Music{}
	}
	music := <-musicCh
	if len(music.Beds) > 0 || len(music.Sounds) > 0 {
		orch.SetDiscussionAudio(music.Beds, music.Sounds, music.Moods)
		log.Info("discussion music ready", "beds", len(music.Sounds), "title", topic.Title)
	}
	return music
}

// PrepareAudioOnly is the image-free variant of PrepareAssets for the
// audio-only feed: it generates only the music beds (the audible part) and
// installs them on the orchestrator, skipping the background palette
// entirely (no imagegen calls). Mirrors the music half of PrepareAssets but
// blocks on the beds synchronously since there is no live stage to race
// against. Degrades gracefully: failed music gen just means the discussion
// plays dry.
func PrepareAudioOnly(ctx context.Context, log *slog.Logger, outDir string,
	topic *config.DebateTopic, orch AudioSink, rec musicRecorder) Music {
	music := GenerateMusic(ctx, log, topic, outDir, rec)
	if len(music.Beds) > 0 || len(music.Sounds) > 0 {
		orch.SetDiscussionAudio(music.Beds, music.Sounds, music.Moods)
		log.Info("discussion music ready (audio-only)",
			"beds", len(music.Sounds), "title", topic.Title)
	}
	return music
}

// GeneratePalette renders the mood palette concurrently, attaching each frame
// to the stage the moment it lands and closing readyCh once the first frame is
// up (or all frames have failed, so the caller never blocks forever).
func GeneratePalette(ctx context.Context, log *slog.Logger,
	stage PaletteStage, topic *config.DebateTopic, readyCh chan<- struct{}) {
	var once sync.Once
	signal := func() { once.Do(func() { close(readyCh) }) }
	defer signal() // guarantees the caller unblocks even on total failure

	client, err := imagegen.New("")
	if err != nil {
		log.Warn("discussion palette gen disabled", "err", err)
		return
	}
	t0 := time.Now()
	var wg sync.WaitGroup
	var have int
	var mu sync.Mutex
	for i, mood := range paletteMoods {
		wg.Add(1)
		go func(i int, mood string) {
			defer wg.Done()
			prompt := fmt.Sprintf(
				"A cinematic, photographic background scene evoking a thoughtful panel discussion about %q. %s. No people in the foreground, no text or captions, gentle composition with a darker, calmer lower third so white subtitles stay legible.",
				topic.Title, mood)
			raw, gerr := client.Generate(ctx, imagegen.Request{
				Model:  imagegen.PuzzleSceneModel,
				Prompt: prompt,
				Size:   "1024x1024",
			})
			if gerr != nil {
				log.Warn("discussion palette frame failed", "index", i, "err", gerr)
				return
			}
			rgba, derr := imagegen.DecodeAndResize(raw, 1920, 1080)
			if derr != nil {
				log.Warn("discussion palette frame decode failed", "index", i, "err", derr)
				return
			}
			if stage != nil {
				stage.AttachPaletteFrame(rgba)
			}
			mu.Lock()
			have++
			mu.Unlock()
			signal() // first successful frame unblocks the show
		}(i, mood)
	}
	wg.Wait()
	log.Info("discussion palette generated",
		"have", have, "target", len(paletteMoods),
		"elapsed", time.Since(t0).Round(time.Millisecond))
}

// GenerateMusic renders the short looping beds. Uses GenerateClip with an
// explicit short duration so Lyria returns quickly (the bed loops, so length
// doesn't matter for playback). Returns empty fields on failure.
func GenerateMusic(ctx context.Context, log *slog.Logger,
	topic *config.DebateTopic, outDir string, rec musicRecorder) Music {
	client, err := musicgen.New("")
	if err != nil {
		log.Warn("discussion music gen disabled", "err", err)
		return Music{}
	}
	client = client.WithUsageRecorder(rec)
	cacheDir := filepath.Join(outDir, "raw-music", "discussion")
	t0 := time.Now()
	paths := make([]string, len(bedSpecs))
	var wg sync.WaitGroup
	for i, spec := range bedSpecs {
		wg.Add(1)
		go func(i int, label, prompt string) {
			defer wg.Done()
			p, gerr := musicgen.GenerateClip(ctx, client, prompt, cacheDir, label, bedDurationSeconds)
			if gerr != nil {
				log.Warn("discussion music bed failed", "label", label, "err", gerr)
				return
			}
			paths[i] = p
		}(i, spec.label, spec.prompt)
	}
	wg.Wait()

	out := Music{Beds: map[string]string{}}
	for i, p := range paths {
		if p == "" {
			continue
		}
		if _, ok := out.Beds["session"]; !ok {
			out.Beds["session"] = p // first available bed plays continuously
		}
		out.Sounds = append(out.Sounds, p)
		out.Moods = append(out.Moods, bedSpecs[i].mood)
	}
	log.Info("discussion beds generated",
		"have", len(out.Sounds), "target", len(bedSpecs),
		"elapsed", time.Since(t0).Round(time.Millisecond))
	return out
}
