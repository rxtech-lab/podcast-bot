package contentcreator

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/video/imagegen"
)

// directorTickInterval is how often the silent commander reconsiders the
// background image + music. Generation is throttled further by an in-flight
// guard so a slow image-gen round-trip can't stack requests.
const directorTickInterval = 45 * time.Second

// directorStartDelay holds the commander quiet for the first stretch of the
// show so the pre-generated palette + opening bed get their moment before any
// on-the-fly swap.
const directorStartDelay = 30 * time.Second

// DiscussionDirector is the runtime behind the silent commander. It polls the
// running transcript, asks the commander LLM whether to change the mood, and
// acts on the cue: generating a fresh background image asynchronously (emitted
// as a DynamicSceneMsg the discussion stage paints) and/or crossfading the
// music bed (emitted as a replace SoundCueMsg the pipeline mixer dispatches).
// It never schedules a spoken turn — the commander is silent.
type DiscussionDirector struct {
	commander  *agent.Commander
	transcript *Transcript
	send       func(any)
	img        *imagegen.Client
	log        *slog.Logger
	musicBeds  int

	mu         sync.Mutex
	generating bool
}

// NewDiscussionDirector builds a director. img may be nil (image generation
// disabled — the director then only crossfades the pre-generated beds).
func NewDiscussionDirector(commander *agent.Commander, tr *Transcript, send func(any),
	img *imagegen.Client, musicBeds int, log *slog.Logger) *DiscussionDirector {
	return &DiscussionDirector{
		commander:  commander,
		transcript: tr,
		send:       send,
		img:        img,
		musicBeds:  musicBeds,
		log:        log,
	}
}

// Run blocks until ctx is cancelled, ticking the director on a timer. Intended
// to run in its own goroutine alongside the pipeline.
func (d *DiscussionDirector) Run(ctx context.Context) {
	if d == nil || d.commander == nil {
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(directorStartDelay):
	}
	t := time.NewTicker(directorTickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.tick(ctx)
		}
	}
}

func (d *DiscussionDirector) tick(ctx context.Context) {
	var recent []agent.TranscriptLine
	if d.transcript != nil {
		recent = d.transcript.RecentN(30)
	}
	if len(recent) == 0 {
		return
	}
	// Light up the commander node in the live diagram while it deliberates;
	// it otherwise works silently and would appear idle the whole show.
	d.commander.EmitActivity(string(ActivityDirecting), "")
	cueCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	cue, err := d.commander.Direct(cueCtx, recent)
	cancel()
	if err != nil {
		d.commander.EmitActivity(string(ActivityIdle), "")
		d.log.Warn("commander direct failed", "err", err)
		return
	}
	d.log.Info("commander cue",
		"action", cue.Action,
		"music_index", cue.MusicIndex,
		"reason", cue.Reason)

	// Crossfade music among the pre-generated beds.
	if cue.MusicIndex >= 0 && cue.MusicIndex < d.musicBeds {
		d.send(SoundCueMsg{Index: cue.MusicIndex, Mode: SoundCueReplace})
	}

	// Generate a fresh background on the fly. When generation starts it keeps
	// the node lit and resets to idle from its own goroutine; otherwise we
	// settle the node back to idle here.
	if strings.EqualFold(strings.TrimSpace(cue.Action), "generate") &&
		strings.TrimSpace(cue.ScenePrompt) != "" && d.maybeGenerate(ctx, cue.ScenePrompt) {
		return
	}
	d.commander.EmitActivity(string(ActivityIdle), "")
}

// maybeGenerate kicks off one background generation if none is in flight. It
// reports whether a generation goroutine was started; when true, that goroutine
// owns resetting the commander node back to idle once the image is ready.
func (d *DiscussionDirector) maybeGenerate(ctx context.Context, prompt string) bool {
	if d.img == nil {
		return false
	}
	d.mu.Lock()
	if d.generating {
		d.mu.Unlock()
		return false
	}
	d.generating = true
	d.mu.Unlock()

	go func() {
		defer func() {
			d.mu.Lock()
			d.generating = false
			d.mu.Unlock()
			d.commander.EmitActivity(string(ActivityIdle), "")
		}()
		genCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		t0 := time.Now()
		raw, err := d.img.Generate(genCtx, imagegen.Request{
			Model:  imagegen.PuzzleSceneModel,
			Prompt: prompt,
			Size:   "1024x1024",
		})
		if err != nil {
			d.log.Warn("commander image gen failed", "err", err)
			return
		}
		rgba, err := imagegen.DecodeAndResize(raw, 1920, 1080)
		if err != nil {
			d.log.Warn("commander image decode failed", "err", err)
			return
		}
		d.log.Info("commander background ready",
			"elapsed", time.Since(t0).Round(time.Millisecond))
		d.send(DynamicSceneMsg{Img: rgba})
	}()
	return true
}
