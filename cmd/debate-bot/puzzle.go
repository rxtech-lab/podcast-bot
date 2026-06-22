package main

import (
	"context"
	"fmt"
	"image"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sirily11/debate-bot/internal/audio/musicgen"
	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/content_creator"
	"github.com/sirily11/debate-bot/internal/llm"
	"github.com/sirily11/debate-bot/internal/video"
	"github.com/sirily11/debate-bot/internal/video/imagegen"
	"github.com/sirily11/debate-bot/internal/video/scenes"
)

// preparePuzzleAssets BLOCKS on the assets the host needs to start narrating
// — surface + qa + reveal scenes plus the music bed — then kicks off the
// conclusion-image generation in the background. The conclusion only paints
// at the end of the show, so deferring it shaves up to ~30s off the start-of-
// podcast wait on a first run. Cached runs hit disk in <100ms so even the
// conclusion lands before the user notices.
func preparePuzzleAssets(ctx context.Context, log *slog.Logger, env *config.Env,
	ch *channelRuntime, d loadedDebate, orch *contentcreator.Orchestrator) {
	scenesCacheDir := filepath.Join(env.OutDir, "puzzle-bgs")
	musicCacheDir := filepath.Join(env.OutDir, "puzzle-music")
	fmt.Fprintf(os.Stdout, "▶ ch %d [%s] planning + generating scenes + music in parallel (this can take ~30-60s on first run; conclusion runs concurrently)\n",
		ch.def.Number, ch.def.ID)

	// Music generation does not depend on the scene plan, so kick it off
	// immediately — runs concurrently with planning AND scene generation
	// below. Saves the plan latency (~4s) off the music critical path.
	musicCh := make(chan *musicgen.PuzzleMusic, 1)
	go func() {
		musicCh <- generatePuzzleMusic(ctx, log, d.topic, musicCacheDir, orch.RecordMusicGeneration)
	}()

	// Plan synchronously; phase-1 + conclusion both need it.
	plan := planPuzzleScenes(ctx, log, env, d.topic)
	// Hand the host the planner's per-frame direction lists so its system
	// prompt can enumerate beats and emit numbered "<scene N/>" markers
	// locked to the cached images. The anchor list is parallel to Surface
	// and gives the host a verbatim string-match trigger for each marker —
	// without it the host counts paragraph breaks and drifts off the
	// planner's intent. Must happen before orch.Run, which constructs the
	// host agent in Setup. No-op when plan is nil.
	if plan != nil {
		orch.SetSurfacePlan(plan.Surface)
		orch.SetSurfaceAnchors(plan.SurfaceAnchors)
		orch.SetConclusionPlan(plan.Conclusion)
		// Per-frame surface animations live on the PuzzleStage rather
		// than the orchestrator: the renderer needs them at the same
		// moment SceneAdvance fires, and the stage already owns the
		// image swap path.
		ch.puzzleStage.AttachSurfaceAnimations(plan.SurfaceAnimations)
		// Persist the plan next to the rest of the per-debate artefacts
		// so a post-mortem viewer can see exactly which beats / anchors
		// / sound cues the planner picked for this puzzle. Non-fatal on
		// failure — the show runs fine without the file.
		planPath := filepath.Join(env.OutDir, "scene-plan.json")
		if err := scenes.WritePlan(plan, planPath); err != nil {
			log.Warn("scene plan write failed", "path", planPath, "err", err)
		}
	}

	// Generate any planner-requested sound cues in the background. The
	// host system prompt lists the cues by index and the pipeline
	// dispatches them via mixer.OverlapClip / ReplaceMusic when the
	// host emits the matching `<sound-…/>` marker. Wait synchronously
	// for completion so SetSoundPlan lands before orch.Run constructs
	// the host (the prompt content is captured at construction time);
	// a typical 1–2 cue plan finishes well within the surface-priority
	// gate. Failures are non-fatal — Setup still runs without sound.
	soundDirs, soundPaths := generatePuzzleSounds(ctx, log, d.topic, plan, musicCacheDir, orch.RecordMusicGeneration)
	if len(soundDirs) > 0 && len(soundPaths) > 0 {
		orch.SetSoundPlan(soundDirs, soundPaths)
	}

	// Surface gen has full priority: every imagegen call slot goes to
	// surface frames first, and qa+reveal+conclusion gen is deferred until
	// the surface batch fully drains. Otherwise the three pools compete for
	// gateway capacity and surface (the longest list, which actually gates
	// show start) ends up the slowest — see the 13:28 session that hung 8
	// minutes on surface-v33 while qa+reveal+conclusion had long since
	// finished.
	//
	// Show start unblocks once the first surfacePriorityCount variants
	// have completed (success or failure). The host narrates surface
	// frames in strict order and emits `<scene N/>` markers tied to
	// specific variant indices, so the old "any 80% complete" rule could
	// pass with low-index frames still missing — the host would then emit
	// markers for variants that hadn't arrived and PuzzleStage's
	// ByNameIdxExact would no-op them. Gating on the first N variants
	// specifically guarantees the opening narration paints. Remaining
	// variants (10..N) generate in the background and hot-swap in via
	// AttachSurfaceFrame as they land.
	surfaceReady := make(chan struct{})
	surfaceDone := make(chan struct{})
	go func() {
		defer close(surfaceDone)
		generatePuzzleSurfaceStreaming(ctx, log, ch.puzzleStage,
			d.topic, plan, scenesCacheDir, surfaceReady)
	}()
	// qa+reveal+conclusion runs only AFTER the surface batch finishes
	// (success or partial failure — surfaceDone fires in either case).
	// Backgrounded so it never blocks the show.
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-surfaceDone:
		}
		generatePuzzleScenePhases(ctx, log, ch.puzzleStage,
			d.topic, plan, scenesCacheDir, "qa+reveal",
			scenes.SceneQA, scenes.SceneReveal)
		generatePuzzleConclusion(ctx, log, ch.puzzleStage, d.topic, plan, scenesCacheDir)
	}()

	// Block on the assets the podcast needs to actually start — the
	// first surfacePriorityCount surface variants + music. The remaining
	// surface frames continue streaming in via AttachSurfaceFrame after
	// Run starts.
	music := <-musicCh
	<-surfaceReady

	if music != nil {
		m := map[string]string{}
		if music.SurfacePath != "" {
			m[musicgen.PhaseSurface] = music.SurfacePath
		}
		if music.RevealPath != "" {
			m[musicgen.PhaseReveal] = music.RevealPath
		}
		orch.SetPuzzleMusic(m)
	}
}

// generatePuzzleScenePhases generates the listed scene phases and attaches
// them to the channel's PuzzleStage as they land (AttachScenes is now
// additive, so partial fills don't clobber prior attaches). Blocks until
// generation finishes — caller decides whether to await this goroutine
// or let it run in the background.
//
// Used in two roles by preparePuzzleAssets:
//   - "surface only" — blocks the show start so the host has imagery for
//     the opening narration.
//   - "qa + reveal" — runs in the background; attaches when ready, well
//     before the qa/reveal phases of the show begin (the surface
//     narration runs ~3-5 min, plenty of slack).
//
// Logs but never propagates errors — missing scenes leave the renderer on
// its default bg, which is acceptable degradation.
func generatePuzzleScenePhases(ctx context.Context, log *slog.Logger,
	ps *video.PuzzleStage, topic *config.DebateTopic, plan *scenes.ScenePlan,
	scenesCacheDir, label string, phases ...string) {
	client, err := imagegen.New("")
	if err != nil {
		log.Warn("puzzle scene gen disabled", "label", label, "err", err)
		return
	}
	t0 := time.Now()
	sc, err := scenes.GenerateWithPlan(ctx, client, topic, plan, scenesCacheDir, phases...)
	if err != nil {
		log.Warn("puzzle scene gen partial",
			"label", label,
			"title", topic.Title,
			"elapsed", time.Since(t0).Round(time.Millisecond),
			"err", err)
	} else {
		log.Info("puzzle scenes ready",
			"label", label,
			"title", topic.Title,
			"elapsed", time.Since(t0).Round(time.Millisecond))
	}
	if sc != nil {
		ps.AttachScenes(sc)
	}
}

// surfacePriorityCount is the number of low-index surface variants that
// gate the show start. Variants in [0, surfacePriorityCount) are scheduled
// first (every gateway slot goes to them) and the show blocks until each
// one completes — success or failure. Remaining variants only enter the
// gateway queue after the priority batch finishes and hot-swap in via
// AttachSurfaceFrame as they land. 10 covers the host's opening narration
// with margin; tighten if the host reaches scene 10's marker before
// variant 10 lands in practice.
const surfacePriorityCount = 10

// generatePuzzleSurfaceStreaming generates the surface frames with the
// streaming attach path: every successful frame is forwarded to the stage
// as soon as it lands, and readyCh fires the moment every variant in
// [0, surfacePriorityCount) has completed (success or failure). Caller
// blocks on readyCh before starting the show — frames generated after
// that go straight onto the live stage via AttachSurfaceFrame.
//
// readyCh is always closed exactly once on return: on the priority-
// completion event when reached, otherwise on function exit (no matter
// how many frames succeeded) so the caller never deadlocks if the
// gateway flakes out mid-batch.
func generatePuzzleSurfaceStreaming(ctx context.Context, log *slog.Logger,
	ps *video.PuzzleStage, topic *config.DebateTopic, plan *scenes.ScenePlan,
	scenesCacheDir string, readyCh chan<- struct{}) {
	var readyOnce sync.Once
	signalReady := func() { readyOnce.Do(func() { close(readyCh) }) }
	// The defer guarantees readyCh closes even if New() fails, the gen
	// returns zero successful frames, or ctx is canceled.
	defer signalReady()

	client, err := imagegen.New("")
	if err != nil {
		log.Warn("puzzle surface gen disabled", "err", err)
		return
	}

	target := plan.SurfaceCount()
	if target <= 0 {
		target = scenes.SurfaceVariantCount
	}
	priority := surfacePriorityCount
	if priority > target {
		priority = target
	}
	if priority < 1 {
		priority = 1
	}

	var (
		mu              sync.Mutex
		succeeded       int
		priorityDoneSet = make(map[int]bool, priority)
	)

	t0 := time.Now()
	opts := scenes.GenerateOptions{
		PerFrameTimeout:      scenes.DefaultPerFrameTimeout,
		SurfacePriorityCount: priority,
		OnFrame: func(name string, variant int, img *image.RGBA, frameErr error) {
			if name != scenes.SceneSurface {
				return
			}
			if img != nil {
				ps.AttachSurfaceFrame(variant, img)
			}
			mu.Lock()
			if img != nil {
				succeeded++
			}
			// Counting both success AND failure ensures readyCh fires even
			// if a priority slot returns a permanent error — the host
			// emits its `<scene N/>` marker either way and the renderer
			// falls back to the prior frame for the missing slot.
			if variant >= 0 && variant < priority {
				priorityDoneSet[variant] = true
			}
			hit := len(priorityDoneSet) >= priority
			mu.Unlock()
			if hit {
				signalReady()
			}
		},
	}

	sc, err := scenes.GenerateWithOptions(ctx, client, topic, plan, scenesCacheDir, opts, scenes.SceneSurface)
	if err != nil {
		log.Warn("puzzle scene gen partial",
			"label", "surface",
			"title", topic.Title,
			"succeeded", succeeded,
			"target", target,
			"priority", priority,
			"elapsed", time.Since(t0).Round(time.Millisecond),
			"err", err)
	} else {
		log.Info("puzzle scenes ready",
			"label", "surface",
			"title", topic.Title,
			"succeeded", succeeded,
			"target", target,
			"priority", priority,
			"elapsed", time.Since(t0).Round(time.Millisecond))
	}
	if sc != nil {
		// Final canonical attach is mostly idempotent (each frame already
		// streamed through AttachSurfaceFrame) but covers the unlikely
		// edge case where OnFrame raced with a stage idle/reset.
		ps.AttachScenes(sc)
	}
}

// generatePuzzleSounds produces the planner's per-cue sound clips in
// parallel and returns the SoundCueDirection list + parallel path
// slice. plan is the scene plan whose Sounds field carries the cues;
// nil / empty plan returns (nil, nil). On a per-clip generation
// failure the corresponding path is left empty — the orchestrator's
// SetSoundPlan trims back to a uniform-length pair so the host's
// indices line up with available files.
//
// Errors are logged but never propagated; missing clips degrade
// gracefully (the host's marker dispatches a "sound cue path empty"
// warning at runtime).
func generatePuzzleSounds(ctx context.Context, log *slog.Logger,
	topic *config.DebateTopic, plan *scenes.ScenePlan,
	musicCacheDir string, rec func()) ([]contentcreator.SoundCueDirection, []string) {
	if plan == nil || len(plan.Sounds) == 0 {
		return nil, nil
	}
	client, err := musicgen.New("")
	if err != nil {
		log.Warn("puzzle sound gen disabled", "err", err)
		return nil, nil
	}
	client = client.WithUsageRecorder(rec)
	dirs := make([]contentcreator.SoundCueDirection, len(plan.Sounds))
	paths := make([]string, len(plan.Sounds))
	var wg sync.WaitGroup
	t0 := time.Now()
	for i, s := range plan.Sounds {
		dirs[i] = contentcreator.SoundCueDirection{
			Mode:            s.Mode,
			Prompt:          s.Prompt,
			Anchor:          s.Anchor,
			DurationSeconds: s.DurationSeconds,
		}
		wg.Add(1)
		go func(i int, prompt string, dur int) {
			defer wg.Done()
			path, gerr := musicgen.GenerateClip(ctx, client, prompt, musicCacheDir, "sound", dur)
			if gerr != nil {
				log.Warn("puzzle sound clip failed",
					"index", i, "title", topic.Title, "err", gerr)
				return
			}
			paths[i] = path
		}(i, s.Prompt, s.DurationSeconds)
	}
	wg.Wait()
	have := 0
	for _, p := range paths {
		if p != "" {
			have++
		}
	}
	log.Info("puzzle sounds ready",
		"title", topic.Title,
		"have", have,
		"target", len(plan.Sounds),
		"elapsed", time.Since(t0).Round(time.Millisecond))
	// Compact: the orchestrator pairs dirs[i] with paths[i] strictly,
	// so a hole in the middle would shift the host's indices. Drop any
	// entry where the clip didn't generate.
	outDirs := dirs[:0]
	outPaths := make([]string, 0, len(paths))
	for i, p := range paths {
		if p == "" {
			continue
		}
		outDirs = append(outDirs, dirs[i])
		outPaths = append(outPaths, p)
	}
	return outDirs, outPaths
}

// generatePuzzleMusic generates the music bed for a puzzle topic. Returns
// the music paths or nil if generation failed. Designed to run as a
// goroutine — the caller publishes the result via orch.SetPuzzleMusic
// once both music and phase-1 scenes are ready.
//
// Music does NOT depend on the scene plan, so this can start as soon as
// the topic is admitted, in parallel with planPuzzleScenes. That trims
// the planning latency (~4s) off the music critical path on first runs.
func generatePuzzleMusic(ctx context.Context, log *slog.Logger,
	topic *config.DebateTopic, musicCacheDir string, rec func()) *musicgen.PuzzleMusic {
	client, err := musicgen.New("")
	if err != nil {
		log.Warn("puzzle music gen disabled", "err", err)
		return nil
	}
	client = client.WithUsageRecorder(rec)
	t0 := time.Now()
	pm, err := musicgen.Generate(ctx, client, topic, musicCacheDir)
	if err != nil {
		log.Warn("puzzle music gen partial",
			"title", topic.Title,
			"elapsed", time.Since(t0).Round(time.Millisecond),
			"err", err)
	} else {
		log.Info("puzzle music ready",
			"title", topic.Title,
			"elapsed", time.Since(t0).Round(time.Millisecond))
	}
	return pm
}

// planPuzzleScenes asks the host LLM to design the variant-direction list
// for the surface and conclusion image phases — see scenes.Plan. Returns
// nil on any failure, in which case the downstream generators fall back to
// the static SurfaceVariantCount / ConclusionVariantCount and the built-in
// rotation directions. Reuses the host model + endpoint so we don't need a
// separate API key wired through env.
func planPuzzleScenes(ctx context.Context, log *slog.Logger, env *config.Env, topic *config.DebateTopic) *scenes.ScenePlan {
	if env == nil || env.OpenAIBaseURL == "" || env.OpenAIKey == "" {
		// No LLM creds → skip the LLM path entirely and use the heuristic
		// fallback so the surface still gets story-ordered chunks.
		if fb := scenes.FallbackPlan(topic); fb != nil {
			log.Info("scene plan fallback ready (no LLM creds)",
				"title", topic.Title,
				"surface_frames", fb.SurfaceCount(),
				"conclusion_frames", fb.ConclusionCount())
			return fb
		}
		return nil
	}
	// Prefer the dedicated scene-planner model (SCENE_PLANNER_MODEL) when
	// configured; LoadEnv falls back to HostModel if unset. The planner
	// only runs once per puzzle so a higher-quality model is cheap here.
	model := env.ScenePlannerModel
	if model == "" {
		model = env.HostModel
	}
	if model == "" {
		if fb := scenes.FallbackPlan(topic); fb != nil {
			log.Info("scene plan fallback ready (no model configured)",
				"title", topic.Title,
				"surface_frames", fb.SurfaceCount(),
				"conclusion_frames", fb.ConclusionCount())
			return fb
		}
		return nil
	}
	client := llm.New(env.OpenAIBaseURL, env.OpenAIKey, model)
	log.Info("scene plan llm",
		"title", topic.Title,
		"model", model)
	t0 := time.Now()
	plan, err := scenes.Plan(ctx, client, topic)
	if err != nil || plan == nil {
		log.Warn("scene plan llm call failed, using heuristic fallback",
			"title", topic.Title,
			"elapsed", time.Since(t0).Round(time.Millisecond),
			"err", err)
		if fb := scenes.FallbackPlan(topic); fb != nil {
			log.Info("scene plan fallback ready",
				"title", topic.Title,
				"surface_frames", fb.SurfaceCount(),
				"conclusion_frames", fb.ConclusionCount())
			return fb
		}
		return nil
	}
	log.Info("scene plan ready",
		"title", topic.Title,
		"surface_frames", plan.SurfaceCount(),
		"conclusion_frames", plan.ConclusionCount(),
		"elapsed", time.Since(t0).Round(time.Millisecond))
	return plan
}

// generatePuzzleConclusion runs the conclusion image generation that
// preparePuzzleAssets deliberately deferred. Called from a background
// goroutine after the podcast has started; on completion the rendered
// images are handed to the channel's PuzzleStage via AttachConclusion so
// the conclusion phase paints with fresh frames if/when the show reaches
// it. Errors are logged but never bubbled up — the conclusion phase
// gracefully falls back to the renderer's default bg.
func generatePuzzleConclusion(ctx context.Context, log *slog.Logger,
	ps *video.PuzzleStage, topic *config.DebateTopic, plan *scenes.ScenePlan,
	scenesCacheDir string) {
	client, err := imagegen.New("")
	if err != nil {
		log.Warn("puzzle conclusion gen disabled", "err", err)
		return
	}
	t0 := time.Now()
	sc, err := scenes.GenerateWithPlan(ctx, client, topic, plan, scenesCacheDir, scenes.SceneConclusion)
	if err != nil {
		log.Warn("puzzle conclusion gen partial",
			"title", topic.Title,
			"elapsed", time.Since(t0).Round(time.Millisecond),
			"err", err)
	} else {
		log.Info("puzzle conclusion ready",
			"title", topic.Title,
			"elapsed", time.Since(t0).Round(time.Millisecond))
	}
	if sc != nil {
		ps.AttachConclusion(sc.Conclusion)
	}
}

// buildPuzzleTopicMsg fills in the puzzle-specific fields of a TopicMsg.
// Repurposes the debate panels: the puzzle host appears alone on the left
// (AffNames), players on the right (NegNames), and the surface story
// doubles as the left-panel position text so on-screen viewers see the
// puzzle prompt while the host is reading it. Truth (湯底) is intentionally
// NOT sent — that would defeat the game.
func buildPuzzleTopicMsg(d loadedDebate, msg contentcreator.TopicMsg) contentcreator.TopicMsg {
	hostName := d.topic.PuzzleHost.Name
	if hostName == "" {
		hostName = "Host"
	}
	msg.AffNames = []string{hostName}
	msg.NegNames = agentNames(d.topic.Players)
	msg.AffPosition = d.topic.Surface
	msg.NegPosition = ""
	return msg
}
