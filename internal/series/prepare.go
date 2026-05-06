// Package series wires the content_creator orchestrator + the SeriesStage
// for a TV-series episode: walks prior-episode archives, builds the
// "previously on …" recap via the compression LLM, plans + generates
// per-beat narration imagery, kicks off music + sound clip generation,
// and post-archives the run output.
//
// Lives in its own package (rather than under content_creator or
// cmd/debate-bot) so the cmd/series-smoke binary can reuse the same code
// path the production server boots through. Imports both video + audio +
// content_creator packages, so it can't be a sub-package of any of them
// without creating an import cycle.
package series

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sirily11/debate-bot/internal/audio/musicgen"
	"github.com/sirily11/debate-bot/internal/config"
	contentcreator "github.com/sirily11/debate-bot/internal/content_creator"
	"github.com/sirily11/debate-bot/internal/llm"
	"github.com/sirily11/debate-bot/internal/video"
	"github.com/sirily11/debate-bot/internal/video/imagegen"
	"github.com/sirily11/debate-bot/internal/video/scenes"
)

// priorityCount is the number of low-index narration variants the show
// blocks on before starting. Mirrors surfacePriorityCount on the puzzle
// side. Variants in [0, priorityCount) are scheduled first; the show
// gates on each one completing (success or failure) so the host's
// opening narration always paints.
const priorityCount = 6

// PrepareEpisode runs the full series-episode preparation pipeline:
//
//  1. Ensures the on-disk archive directory exists.
//  2. Walks prior episodes for recap input + reuse catalog.
//  3. Calls the compression LLM (when available) for the "previously
//     on …" preamble; episode 1 / no creds → empty recap.
//  4. Plans the narration beats via scenes.PlanSeries (or fallback).
//  5. Persists the plan to scene-plan.json.
//  6. Trims the catalog to highlight ids surfaced by the recap.
//  7. Decodes prior-episode PNGs into the cross-episode resolver map
//     and hands them to the SeriesStage.
//  8. Pushes the prepared inputs onto the orchestrator BEFORE Run.
//  9. Generates music + sounds + per-beat narration PNGs in parallel,
//     blocking on the first priorityCount narration variants + the
//     music bed.
//
// Errors anywhere in the pipeline are logged but never propagated —
// degraded paths are designed to still produce a runnable episode (no
// recap, fallback plan, dry TTS, etc.). The function returns once the
// orchestrator has everything it needs to start emitting turns.
func PrepareEpisode(ctx context.Context, log *slog.Logger, env *config.Env,
	stage *video.SeriesStage, topic *config.DebateTopic, orch *contentcreator.Orchestrator,
) {
	// status pushes a one-line note onto the bus so the SPA progress
	// log can render scene-prep milestones interleaved with the
	// orchestrator's events. No-op when the orchestrator was built
	// without a Send callback (defensive — production wires one).
	status := func(text string) {
		if orch != nil && orch.Send != nil {
			orch.Send(contentcreator.StatusMsg{Text: text})
		}
	}

	episodeDir, err := contentcreator.EnsureEpisodeDir(env.PersistentRoot, topic.Show, topic.Season, topic.Episode)
	if err != nil {
		log.Warn("series episode dir prep failed", "err", err)
		return
	}
	log.Info("series episode dir ready", "path", episodeDir)
	scenesCacheDir := filepath.Join(episodeDir, "scenes")
	musicCacheDir := filepath.Join(episodeDir, "music")
	soundsCacheDir := filepath.Join(episodeDir, "sounds")
	for _, p := range []string{scenesCacheDir, musicCacheDir, soundsCacheDir} {
		_ = os.MkdirAll(p, 0o755)
	}

	status("loading prior episodes…")
	priors, perr := contentcreator.LoadPriorEpisodes(env.PersistentRoot, topic.Show, topic.Season, topic.Episode)
	if perr != nil {
		log.Warn("series prior-episode load failed", "err", perr)
	}
	if len(priors) == 0 {
		status("no prior episodes (starting fresh)")
	} else {
		status(fmt.Sprintf("found %d prior episode(s)", len(priors)))
	}
	candidates := buildImageRefCandidates(priors)

	var recap string
	var highlightIDs []string
	if len(priors) > 0 && env.CompressionBaseURL != "" && env.CompressionKey != "" && env.CompressionModel != "" {
		status("generating recap narrative (compression LLM)…")
		comp := llm.New(env.CompressionBaseURL, env.CompressionKey, env.CompressionModel)
		t0 := time.Now()
		r, ids, rerr := contentcreator.BuildRecap(ctx, comp, priors, topic.Show)
		if rerr != nil {
			log.Warn("series recap failed", "elapsed", time.Since(t0).Round(time.Millisecond), "err", rerr)
			status("recap narrative failed (continuing without)")
		} else {
			log.Info("series recap ready", "len", len(r), "highlights", len(ids),
				"elapsed", time.Since(t0).Round(time.Millisecond))
			recap = r
			highlightIDs = ids
			status(fmt.Sprintf("recap narrative ready · %d chars · %d highlights · %s",
				len(r), len(ids), time.Since(t0).Round(time.Second)))
		}
	}

	status("planning narration scenes…")
	plan := planScenes(ctx, log, env, topic, candidates)
	if plan != nil {
		planPath := filepath.Join(episodeDir, "scene-plan.json")
		if err := scenes.WritePlan(plan, planPath); err != nil {
			log.Warn("series scene plan write failed", "path", planPath, "err", err)
		}
		status(fmt.Sprintf("scene plan generated · %d narration frame(s)", plan.NarrationCount()))
	} else {
		status("scene plan unavailable (using fallback)")
	}

	catalog, refPaths := buildArchiveCatalog(priors)
	if len(highlightIDs) > 0 {
		highlightSet := map[string]bool{}
		for _, id := range highlightIDs {
			highlightSet[id] = true
		}
		filtered := make([]contentcreator.SeriesImageRefCatalogEntry, 0, len(highlightIDs))
		for _, e := range catalog {
			if highlightSet[contentcreator.ImageRefKey(e.Season, e.Episode, e.Beat)] {
				filtered = append(filtered, e)
			}
		}
		catalog = filtered
		trimmed := map[string]string{}
		for _, e := range filtered {
			k := contentcreator.ImageRefKey(e.Season, e.Episode, e.Beat)
			if p, ok := refPaths[k]; ok {
				trimmed[k] = p
			}
		}
		refPaths = trimmed
	}
	imgs, lerr := loadImageRefImages(refPaths)
	if lerr != nil {
		log.Warn("series cross-episode image load partial", "err", lerr)
	}
	if stage != nil {
		stage.AttachImageRefs(imgs)
	}

	orch.SetSeriesPreviouslyOn(recap)
	if plan != nil {
		orch.SetSeriesPlan(plan.Narration, plan.NarrationAnchors, plan.NarrationAnimations)
		if stage != nil {
			stage.AttachAnimations(plan.NarrationAnimations)
		}
		if len(plan.Characters) > 0 {
			cast := make([]contentcreator.SeriesCharacter, len(plan.Characters))
			for i, c := range plan.Characters {
				cast[i] = contentcreator.SeriesCharacter{
					Name:        c.Name,
					Gender:      c.Gender,
					VoiceHint:   c.VoiceHint,
					Description: c.Description,
				}
			}
			orch.SetSeriesCharacters(cast)
			log.Info("series cast ready", "count", len(cast))
		}
	}
	orch.SetSeriesImageRefs(catalog, refPaths)

	status("starting music + narration audio bed generation…")
	musicCh := make(chan string, 1)
	go func() { musicCh <- generateMusic(ctx, log, topic, musicCacheDir) }()

	if plan != nil && len(plan.Sounds) > 0 {
		status(fmt.Sprintf("generating %d sound clip(s)…", len(plan.Sounds)))
	}
	if soundDirs, soundPaths := generateSounds(ctx, log, topic, plan, soundsCacheDir); len(soundDirs) > 0 {
		orch.SetSeriesSoundPlan(soundDirs, soundPaths)
		status(fmt.Sprintf("sound clips generated (%d)", len(soundPaths)))
	}

	if plan != nil && plan.NarrationCount() > 0 {
		status(fmt.Sprintf("starting image generation · %d narration frame(s)", plan.NarrationCount()))
	}
	readyCh := make(chan struct{})
	go func() {
		generateNarrationStreaming(ctx, log, status, stage, topic, plan, scenesCacheDir, readyCh)
	}()

	music := <-musicCh
	<-readyCh
	if music != "" {
		orch.SetSeriesMusic(music)
		status("music bed ready")
	} else {
		status("music bed unavailable (continuing dry)")
	}
}

// FinishEpisode mirrors the puzzle pipeline's archival step: copies the
// per-run script + audio + subtitles from env.OutDir into the persistent
// episode directory so the next episode's recap engine can read them.
// Best-effort: errors are logged but never propagated.
func FinishEpisode(log *slog.Logger, env *config.Env, topic *config.DebateTopic) {
	if topic == nil || topic.Type != config.ContentTypeSeries {
		return
	}
	dir := contentcreator.EpisodeDir(env.PersistentRoot, topic.Show, topic.Season, topic.Episode)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Warn("series finish: mkdir archive failed", "path", dir, "err", err)
		return
	}
	matches, _ := filepath.Glob(filepath.Join(env.OutDir, "turn_*.script.txt"))
	sortAlpha(matches)
	if len(matches) > 0 {
		var sb strings.Builder
		for _, m := range matches {
			data, err := os.ReadFile(m)
			if err != nil {
				continue
			}
			sb.Write(data)
			sb.WriteString("\n\n")
		}
		_ = os.WriteFile(filepath.Join(dir, "script.txt"), []byte(sb.String()), 0o644)
	}
	for src, dst := range map[string]string{
		filepath.Join(env.OutDir, "debate.mp3"):     filepath.Join(dir, "episode.mp3"),
		filepath.Join(env.OutDir, "subtitles.vtt"):  filepath.Join(dir, "subtitles.vtt"),
		filepath.Join(env.OutDir, "transcript.txt"): filepath.Join(dir, "transcript.txt"),
	} {
		_ = copyFile(src, dst)
	}
	log.Info("series finish: episode archived", "path", dir)
}

func planScenes(ctx context.Context, log *slog.Logger, env *config.Env,
	topic *config.DebateTopic, candidates []scenes.SeriesImageRefCandidate,
) *scenes.ScenePlan {
	if env == nil || env.OpenAIBaseURL == "" || env.OpenAIKey == "" {
		if fb := scenes.FallbackSeriesPlan(topic); fb != nil {
			log.Info("series scene plan fallback (no creds)",
				"narration_frames", fb.NarrationCount())
			return fb
		}
		return nil
	}
	model := env.ScenePlannerModel
	if model == "" {
		model = env.HostModel
	}
	if model == "" {
		return scenes.FallbackSeriesPlan(topic)
	}
	client := llm.New(env.OpenAIBaseURL, env.OpenAIKey, model)
	t0 := time.Now()
	plan, err := scenes.PlanSeries(ctx, client, topic, candidates)
	if err != nil || plan == nil {
		log.Warn("series scene plan llm failed, using fallback",
			"elapsed", time.Since(t0).Round(time.Millisecond), "err", err)
		return scenes.FallbackSeriesPlan(topic)
	}
	log.Info("series scene plan ready",
		"narration_frames", plan.NarrationCount(),
		"elapsed", time.Since(t0).Round(time.Millisecond))
	return plan
}

func buildImageRefCandidates(priors []contentcreator.PriorEpisodeContent) []scenes.SeriesImageRefCandidate {
	var out []scenes.SeriesImageRefCandidate
	for _, p := range priors {
		if p.Plan == nil {
			continue
		}
		for i, dir := range p.Plan.Narration {
			out = append(out, scenes.SeriesImageRefCandidate{
				Key:         contentcreator.ImageRefKey(p.Season, p.Episode, i),
				Season:      p.Season,
				Episode:     p.Episode,
				Beat:        i,
				Description: strings.TrimSpace(dir),
			})
		}
	}
	return out
}

func buildArchiveCatalog(priors []contentcreator.PriorEpisodeContent,
) (catalog []contentcreator.SeriesImageRefCatalogEntry, paths map[string]string) {
	paths = map[string]string{}
	for _, p := range priors {
		if p.Plan == nil {
			continue
		}
		for i, dir := range p.Plan.Narration {
			matches, err := filepath.Glob(filepath.Join(p.Dir, "scenes", fmt.Sprintf("narration-v%d-*.png", i)))
			if err != nil || len(matches) == 0 {
				continue
			}
			catalog = append(catalog, contentcreator.SeriesImageRefCatalogEntry{
				Season:      p.Season,
				Episode:     p.Episode,
				Beat:        i,
				Description: strings.TrimSpace(dir),
			})
			paths[contentcreator.ImageRefKey(p.Season, p.Episode, i)] = matches[0]
		}
	}
	return
}

func loadImageRefImages(paths map[string]string) (map[string]*image.RGBA, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	out := make(map[string]*image.RGBA, len(paths))
	var firstErr error
	for k, p := range paths {
		img, err := loadPNG(p)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		out[k] = img
	}
	return out, firstErr
}

func loadPNG(path string) (*image.RGBA, error) {
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

func generateMusic(ctx context.Context, log *slog.Logger, topic *config.DebateTopic, cacheDir string) string {
	client, err := musicgen.New("")
	if err != nil {
		log.Warn("series music gen disabled", "err", err)
		return ""
	}
	prompt := fmt.Sprintf(
		"Atmospheric instrumental score for a serialized narrated podcast episode. Show: %s. Calm, contemplative, late-night documentary feel. No vocals.",
		topic.Show)
	t0 := time.Now()
	path, err := musicgen.GenerateClip(ctx, client, prompt, cacheDir, "narration", 90)
	if err != nil {
		log.Warn("series music gen failed",
			"elapsed", time.Since(t0).Round(time.Millisecond), "err", err)
		return ""
	}
	log.Info("series music ready", "path", path,
		"elapsed", time.Since(t0).Round(time.Millisecond))
	return path
}

func generateSounds(ctx context.Context, log *slog.Logger, topic *config.DebateTopic,
	plan *scenes.ScenePlan, cacheDir string,
) ([]contentcreator.SoundCueDirection, []string) {
	if plan == nil || len(plan.Sounds) == 0 {
		return nil, nil
	}
	client, err := musicgen.New("")
	if err != nil {
		log.Warn("series sound gen disabled", "err", err)
		return nil, nil
	}
	dirs := make([]contentcreator.SoundCueDirection, len(plan.Sounds))
	paths := make([]string, len(plan.Sounds))
	var wg sync.WaitGroup
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
			path, gerr := musicgen.GenerateClip(ctx, client, prompt, cacheDir, "sound", dur)
			if gerr != nil {
				log.Warn("series sound clip failed", "index", i, "err", gerr)
				return
			}
			paths[i] = path
		}(i, s.Prompt, s.DurationSeconds)
	}
	wg.Wait()
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

func generateNarrationStreaming(ctx context.Context, log *slog.Logger,
	status func(string),
	stage *video.SeriesStage, topic *config.DebateTopic, plan *scenes.ScenePlan,
	scenesCacheDir string, readyCh chan<- struct{},
) {
	var readyOnce sync.Once
	signalReady := func() { readyOnce.Do(func() { close(readyCh) }) }
	defer signalReady()

	if status == nil {
		status = func(string) {}
	}

	client, err := imagegen.New("")
	if err != nil {
		log.Warn("series narration gen disabled", "err", err)
		status("image generation disabled (no Gemini creds)")
		return
	}
	target := 0
	if plan != nil {
		target = plan.NarrationCount()
	}
	if target <= 0 {
		log.Warn("series narration plan empty — skipping image gen")
		status("narration plan empty — skipping image gen")
		return
	}
	priority := priorityCount
	if priority > target {
		priority = target
	}
	var (
		mu              sync.Mutex
		succeeded       int
		priorityDoneSet = make(map[int]bool, priority)
		priorityHit     bool
	)
	t0 := time.Now()
	opts := scenes.GenerateOptions{
		PerFrameTimeout:      scenes.DefaultPerFrameTimeout,
		SurfacePriorityCount: priority,
		OnFrame: func(name string, variant int, img *image.RGBA, _ error) {
			if name != scenes.SceneNarration {
				return
			}
			if img != nil && stage != nil {
				stage.AttachNarrationFrame(variant, img)
			}
			mu.Lock()
			if img != nil {
				succeeded++
			}
			if variant >= 0 && variant < priority {
				priorityDoneSet[variant] = true
			}
			hit := len(priorityDoneSet) >= priority
			done := succeeded
			justHit := hit && !priorityHit
			if hit {
				priorityHit = true
			}
			mu.Unlock()
			// Per-frame status would flood the SSE stream; throttle
			// to: priority-batch hit (show start) + every 5th image
			// + the final image. Keeps the SPA log readable.
			if img != nil {
				if justHit {
					status(fmt.Sprintf("priority images ready (%d/%d) — show can start",
						done, target))
				} else if done == target || done%5 == 0 {
					status(fmt.Sprintf("generated %d/%d narration image(s)", done, target))
				}
			}
			if hit {
				signalReady()
			}
		},
	}
	sc, err := scenes.GenerateWithOptions(ctx, client, topic, plan, scenesCacheDir, opts, scenes.SceneNarration)
	if err != nil {
		log.Warn("series narration gen partial",
			"succeeded", succeeded, "target", target,
			"elapsed", time.Since(t0).Round(time.Millisecond), "err", err)
		status(fmt.Sprintf("image generation partial · %d/%d · %s",
			succeeded, target, time.Since(t0).Round(time.Second)))
	} else {
		log.Info("series narration ready",
			"succeeded", succeeded, "target", target,
			"elapsed", time.Since(t0).Round(time.Millisecond))
		status(fmt.Sprintf("image generation done · %d/%d · %s",
			succeeded, target, time.Since(t0).Round(time.Second)))
	}
	if sc != nil && stage != nil {
		stage.AttachScenes(sc)
	}
}

func sortAlpha(xs []string) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j-1] > xs[j]; j-- {
			xs[j-1], xs[j] = xs[j], xs[j-1]
		}
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	buf := make([]byte, 64*1024)
	for {
		n, rerr := in.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if rerr != nil {
			break
		}
	}
	return nil
}

// BuildTopicMsg shapes the per-content-type TopicMsg for series. Mirrors
// cmd/debate-bot's buildSeriesTopicMsg so the smoke binary doesn't have
// to duplicate it.
func BuildTopicMsg(topic *config.DebateTopic, id, title string, index, total int) contentcreator.TopicMsg {
	hostName := topic.SeriesHost.Name
	if hostName == "" {
		hostName = "Narrator"
	}
	return contentcreator.TopicMsg{
		ID:          id,
		Title:       title,
		Type:        topic.Type,
		Index:       index,
		Total:       total,
		AffNames:    []string{hostName},
		AffPosition: topic.Surface,
		Show:        topic.Show,
		Season:      topic.Season,
		Episode:     topic.Episode,
	}
}
