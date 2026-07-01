// Package audiobook wires the content_creator orchestrator for an
// audio-book run: it generates the music bed + per-chapter stinger clips
// and (when image generation is enabled) a small set of illustration
// images that are surfaced in the chat transcript, the companion
// text-based document, and the rendered video.
//
// It mirrors internal/series and internal/discussion's prepare layer:
// everything is best-effort (errors are logged, never propagated) so a
// degraded run still produces a playable audiobook. Lives in its own
// package to avoid an import cycle (it imports content_creator + audio +
// video, none of which may import it).
package audiobook

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sirily11/debate-bot/internal/audio/musicgen"
	"github.com/sirily11/debate-bot/internal/config"
	contentcreator "github.com/sirily11/debate-bot/internal/content_creator"
	"github.com/sirily11/debate-bot/internal/storage"
	"github.com/sirily11/debate-bot/internal/video/imagegen"
)

// illustrationSize is the requested image size — 16:9 so the same PNG feeds
// the chat transcript and the 1080p video frame without cropping.
const illustrationSize = "1920x1080"

// illustrationURLTTL is the lifetime of the presigned image URLs embedded in
// the chat + companion text. When a public CDN base URL is configured the URL
// is permanent and the TTL is ignored; otherwise SigV4 caps presigned URLs at
// 7 days, which comfortably covers viewing the run and its summary.
const illustrationURLTTL = 7 * 24 * time.Hour

// maxIllustrations caps how many images an audiobook generates. The user
// asked for "just a few", and each image costs a Gemini generation + an S3
// upload, so we keep the count small regardless of chapter count.
const maxIllustrations = 5

// stingerSeconds is the length of each chapter intro stinger. Short — it
// layers over the running bed at the chapter open, it isn't a full track.
const stingerSeconds = 8

// PrepareAudio generates the audiobook's music bed + chapter stingers and
// pushes them onto the orchestrator before Run. Image generation is handled
// by PrepareImages (called separately so the audio-only and video paths can
// share the audio prep). Best-effort throughout.
func PrepareAudio(ctx context.Context, log *slog.Logger, env *config.Env,
	topic *config.DebateTopic, orch *contentcreator.Orchestrator,
) {
	status := func(text string) {
		if orch != nil && orch.Send != nil {
			orch.Send(contentcreator.StatusMsg{Text: text})
		}
	}
	if topic == nil || orch == nil {
		return
	}

	cacheDir := filepath.Join(env.OutDir, "audiobook")
	musicCacheDir := filepath.Join(cacheDir, "music")
	soundsCacheDir := filepath.Join(cacheDir, "sounds")
	for _, p := range []string{musicCacheDir, soundsCacheDir} {
		_ = os.MkdirAll(p, 0o755)
	}

	// Music bed + chapter stingers run concurrently — the stingers don't
	// depend on the bed.
	status("starting audiobook music generation…")
	bedCh := make(chan string, 1)
	go func() { bedCh <- generateBed(ctx, log, topic, musicCacheDir, orch.RecordMusicGeneration) }()

	if dirs, paths := generateStingers(ctx, log, topic, soundsCacheDir, orch.RecordMusicGeneration); len(paths) > 0 {
		orch.SetSeriesSoundPlan(dirs, paths)
		status(fmt.Sprintf("chapter stingers ready (%d)", len(paths)))
	}

	if bed := <-bedCh; bed != "" {
		orch.SetSeriesMusic(bed)
		status("music bed ready")
	} else {
		status("music bed unavailable (continuing dry)")
	}
}

func generateBed(ctx context.Context, log *slog.Logger, topic *config.DebateTopic,
	cacheDir string, rec func(),
) string {
	client, err := musicgen.New("")
	if err != nil {
		log.Warn("audiobook music gen disabled", "err", err)
		return ""
	}
	client = client.WithUsageRecorder(rec)
	prompt := fmt.Sprintf(
		"Quiet, warm instrumental underscore for a narrated audiobook titled %q. Soft, unobtrusive, low in the mix so it never competes with a speaking voice. Gentle piano and strings, slow tempo. Instrumental only — absolutely no vocals.",
		topic.Title)
	t0 := time.Now()
	path, err := musicgen.GenerateClip(ctx, client, prompt, cacheDir, "bed", 90)
	if err != nil {
		log.Warn("audiobook music gen failed",
			"elapsed", time.Since(t0).Round(time.Millisecond), "err", err)
		return ""
	}
	log.Info("audiobook music bed ready", "path", path,
		"elapsed", time.Since(t0).Round(time.Millisecond))
	return path
}

// generateStingers produces one short intro stinger per chapter, anchored to
// the chapter title so the host fires `<sound-overlapped-N/>` as it opens
// that chapter. The clips layer over the running bed (mode=overlap).
func generateStingers(ctx context.Context, log *slog.Logger, topic *config.DebateTopic,
	cacheDir string, rec func(),
) ([]contentcreator.SoundCueDirection, []string) {
	chapters := topic.AudioBookChapters
	if len(chapters) == 0 {
		return nil, nil
	}
	client, err := musicgen.New("")
	if err != nil {
		log.Warn("audiobook stinger gen disabled", "err", err)
		return nil, nil
	}
	client = client.WithUsageRecorder(rec)

	dirs := make([]contentcreator.SoundCueDirection, 0, len(chapters))
	paths := make([]string, 0, len(chapters))
	for i, ch := range chapters {
		prompt := fmt.Sprintf(
			"Short instrumental transition stinger introducing an audiobook chapter titled %q. Mood: %s. A brief, cinematic flourish (a few seconds) that signals a new chapter, then settles. Instrumental only, no vocals.",
			strings.TrimSpace(ch.Title), chapterMood(ch))
		path, gerr := musicgen.GenerateClip(ctx, client, prompt, cacheDir,
			fmt.Sprintf("stinger-%d", i), stingerSeconds)
		if gerr != nil {
			log.Warn("audiobook stinger failed", "chapter", i, "err", gerr)
			continue
		}
		dirs = append(dirs, contentcreator.SoundCueDirection{
			Mode:            "overlap",
			Prompt:          prompt,
			Anchor:          strings.TrimSpace(ch.Title),
			DurationSeconds: stingerSeconds,
		})
		paths = append(paths, path)
	}
	return dirs, paths
}

// PrepareImages generates a small set of illustration images (one per chapter,
// capped at maxIllustrations), saves them to disk for the video stage, uploads
// each to object storage for the chat transcript + companion text, and installs
// the scene plan + image set on the orchestrator before Run. Best-effort: any
// failure (no image creds, generation error, upload error) is logged and the
// run continues without that image.
func PrepareImages(ctx context.Context, log *slog.Logger, env *config.Env,
	topic *config.DebateTopic, orch *contentcreator.Orchestrator, uploader *storage.Uploader,
) {
	status := func(text string) {
		if orch != nil && orch.Send != nil {
			orch.Send(contentcreator.StatusMsg{Text: text})
		}
	}
	if topic == nil || orch == nil || len(topic.AudioBookChapters) == 0 {
		return
	}
	client, err := imagegen.New("")
	if err != nil {
		log.Warn("audiobook image gen disabled", "err", err)
		status("illustrations disabled (no image creds)")
		return
	}

	scenesDir := filepath.Join(env.OutDir, "audiobook", "scenes")
	_ = os.MkdirAll(scenesDir, 0o755)

	// One beat per chapter, capped. beats/anchors drive the host's <scene N/>
	// markers; the anchor is the chapter title the host opens each chapter with.
	chapters := topic.AudioBookChapters
	if len(chapters) > maxIllustrations {
		chapters = chapters[:maxIllustrations]
	}
	beats := make([]string, len(chapters))
	anchors := make([]string, len(chapters))
	prompts := make([]string, len(chapters))
	for i, ch := range chapters {
		title := strings.TrimSpace(ch.Title)
		beats[i] = title
		anchors[i] = title
		speakers := "narrator only"
		if len(ch.Speakers) > 0 {
			speakers = strings.Join(ch.Speakers, ", ")
		}
		prompts[i] = fmt.Sprintf(
			"A tasteful editorial illustration for the audiobook %q. Chapter %d of %d: %q. Chapter focus: %s. Narration mode: %s. Featured voices: %s. Make this chapter visually distinct from the other chapter illustrations with a unique composition, subject, setting, and color accents. Cinematic editorial artwork, atmospheric, no text, no captions, no watermark.",
			topic.Title, i+1, len(chapters), title, chapterMood(ch), strings.TrimSpace(ch.Mode), speakers)
	}

	status(fmt.Sprintf("generating %d illustration(s)…", len(chapters)))
	imgs := make([]contentcreator.AudioBookImage, len(chapters))
	var wg sync.WaitGroup
	for i := range chapters {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			raw, gerr := client.Generate(ctx, imagegen.Request{
				Model:  imagegen.PuzzleSceneModel,
				Prompt: prompts[i],
				Size:   illustrationSize,
			})
			if gerr != nil {
				log.Warn("audiobook illustration failed", "beat", i, "err", gerr)
				return
			}
			path := filepath.Join(scenesDir, fmt.Sprintf("narration-v%d.png", i))
			if werr := os.WriteFile(path, raw, 0o644); werr != nil {
				log.Warn("audiobook illustration write failed", "beat", i, "err", werr)
				return
			}
			img := contentcreator.AudioBookImage{Beat: i, Path: path, Caption: beats[i]}
			if uploader.Enabled() {
				key := uploader.Key(fmt.Sprintf("%s-illustration-%d.png", topicSlug(topic), i))
				ct := http.DetectContentType(raw)
				if uerr := uploader.UploadBytes(ctx, key, ct, raw); uerr != nil {
					log.Warn("audiobook illustration upload failed", "beat", i, "err", uerr)
				} else if url, derr := uploader.DownloadURL(ctx, key, illustrationURLTTL); derr == nil {
					img.URL = url
				} else {
					log.Warn("audiobook illustration url failed", "beat", i, "err", derr)
				}
			}
			imgs[i] = img
		}(i)
	}
	wg.Wait()

	// Keep only beats whose image actually generated, renumbering so the
	// scene-plan indices stay contiguous (the host emits <scene 0/>..<scene
	// N-1/> against this compacted list).
	var (
		keptBeats   []string
		keptAnchors []string
		keptImgs    []contentcreator.AudioBookImage
	)
	for i, img := range imgs {
		if img.Path == "" {
			continue
		}
		img.Beat = len(keptImgs)
		keptBeats = append(keptBeats, beats[i])
		keptAnchors = append(keptAnchors, anchors[i])
		keptImgs = append(keptImgs, img)
	}
	if len(keptImgs) == 0 {
		status("illustrations unavailable (continuing without)")
		return
	}
	orch.SetSeriesPlan(keptBeats, keptAnchors, nil)
	orch.SetAudioBookImages(keptImgs)
	status(fmt.Sprintf("illustrations ready (%d)", len(keptImgs)))
}

// topicSlug builds a filesystem/url-safe slug from the audiobook title so
// uploaded illustration keys are human-recognisable.
func topicSlug(topic *config.DebateTopic) string {
	s := strings.ToLower(strings.TrimSpace(topic.Title))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('-')
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "audiobook"
	}
	if len(slug) > 48 {
		slug = slug[:48]
	}
	return slug
}

func chapterMood(ch config.AudioBookChapter) string {
	s := strings.TrimSpace(ch.Summary)
	if s == "" {
		return "calm and contemplative"
	}
	// Use the first sentence of the chapter summary as a mood hint.
	if idx := strings.IndexAny(s, ".!?。！？"); idx > 0 {
		s = s[:idx]
	}
	return s
}
