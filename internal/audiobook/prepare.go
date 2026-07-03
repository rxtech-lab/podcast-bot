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
	"os/exec"
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

const avatarSize = "1024x1024"

// stingerSeconds is the length of each chapter intro stinger. Short — it
// layers over the running bed at the chapter open, it isn't a full track.
const stingerSeconds = 8

// replacementCueSeconds is the requested length for chapter-level replacement
// beds. The mixer loops replacement clips indefinitely after cross-fading to
// them, so this only needs enough musical material to avoid an obvious loop.
const replacementCueSeconds = 90

type audioBookCueSpec struct {
	mode            string
	prompt          string
	anchor          string
	durationSeconds int
	cacheLabel      string
}

// PrepareAudio generates the audiobook's music bed + chapter music cues and
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

	// Music bed + chapter cues run concurrently — the cues don't
	// depend on the bed.
	status("starting audiobook music generation…")
	bedCh := make(chan string, 1)
	go func() { bedCh <- generateBed(ctx, log, topic, musicCacheDir, orch.RecordMusicGeneration) }()

	if dirs, paths := generateStingers(ctx, log, topic, soundsCacheDir, orch.RecordMusicGeneration); len(paths) > 0 {
		orch.SetSeriesSoundPlan(dirs, paths)
		status(fmt.Sprintf("chapter music cues ready (%d)", len(paths)))
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

// generateStingers produces chapter music cues anchored to chapter titles.
// Each chapter gets a short overlap stinger and, after the opening chapter, a
// sustained replacement bed option. The audiobook host chooses which marker to
// fire at narration time: overlap temporarily ducks the current bed and falls
// back afterward; replace cross-fades to the new bed and keeps it playing.
func generateStingers(ctx context.Context, log *slog.Logger, topic *config.DebateTopic,
	cacheDir string, rec func(),
) ([]contentcreator.SoundCueDirection, []string) {
	specs := buildAudioBookCueSpecs(topic)
	if len(specs) == 0 {
		return nil, nil
	}
	client, err := musicgen.New("")
	if err != nil {
		log.Warn("audiobook music cue gen disabled", "err", err)
		return nil, nil
	}
	client = client.WithUsageRecorder(rec)

	dirs := make([]contentcreator.SoundCueDirection, 0, len(specs))
	paths := make([]string, 0, len(specs))
	for i, spec := range specs {
		path, gerr := musicgen.GenerateClip(ctx, client, spec.prompt, cacheDir,
			spec.cacheLabel, spec.durationSeconds)
		if gerr != nil {
			log.Warn("audiobook music cue failed", "cue", i, "mode", spec.mode, "err", gerr)
			continue
		}
		dirs = append(dirs, contentcreator.SoundCueDirection{
			Mode:            spec.mode,
			Prompt:          spec.prompt,
			Anchor:          spec.anchor,
			DurationSeconds: spec.durationSeconds,
		})
		paths = append(paths, path)
	}
	return dirs, paths
}

func buildAudioBookCueSpecs(topic *config.DebateTopic) []audioBookCueSpec {
	if topic == nil || len(topic.AudioBookChapters) == 0 {
		return nil
	}
	specs := make([]audioBookCueSpec, 0, len(topic.AudioBookChapters)*2-1)
	for i, ch := range topic.AudioBookChapters {
		title := strings.TrimSpace(ch.Title)
		if title == "" {
			continue
		}
		mood := chapterMood(ch)
		specs = append(specs, audioBookCueSpec{
			mode: "overlap",
			prompt: fmt.Sprintf(
				"Short instrumental transition stinger introducing an audiobook chapter titled %q. Mood: %s. A brief cinematic flourish, a few seconds long, that signals a new chapter, then gets out of the way. Instrumental only, no vocals.",
				title, mood),
			anchor:          title,
			durationSeconds: stingerSeconds,
			cacheLabel:      fmt.Sprintf("stinger-%d", i),
		})
		if i == 0 {
			continue
		}
		specs = append(specs, audioBookCueSpec{
			mode: "replace",
			prompt: fmt.Sprintf(
				"Sustained instrumental background bed for an audiobook chapter titled %q. Mood: %s. It should work as the main underscore for the chapter after a cross-fade, with a stable low-intensity pulse, gentle musical identity, and no sharp ending. Instrumental only, no vocals, no lyrics, no spoken word.",
				title, mood),
			anchor:          title,
			durationSeconds: replacementCueSeconds,
			cacheLabel:      fmt.Sprintf("replace-%d", i),
		})
	}
	return specs
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
	visualGuide := audioBookIllustrationVisualGuide(topic)
	for i, ch := range chapters {
		title := strings.TrimSpace(ch.Title)
		beats[i] = title
		anchors[i] = title
		prompts[i] = audioBookIllustrationPrompt(topic, ch, i, len(chapters), visualGuide)
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
	if avatars := generateSpeakerAvatars(ctx, log, client, topic, scenesDir); len(avatars) > 0 {
		orch.SetAudioBookAvatars(avatars)
		status(fmt.Sprintf("speaker avatars ready (%d)", len(avatars)))
	}
	orch.SetSeriesPlan(keptBeats, keptAnchors, nil)
	orch.SetAudioBookImages(keptImgs)
	status(fmt.Sprintf("illustrations ready (%d)", len(keptImgs)))
}

func audioBookIllustrationPrompt(topic *config.DebateTopic, ch config.AudioBookChapter,
	chapterIndex, chapterCount int, visualGuide string,
) string {
	title := strings.TrimSpace(ch.Title)
	mode := strings.TrimSpace(ch.Mode)
	if mode == "" {
		mode = config.AudioBookModeNarration
	}
	speakers := "narrator only"
	if len(ch.Speakers) > 0 {
		speakers = strings.Join(ch.Speakers, ", ")
	}
	return fmt.Sprintf(`Create one 16:9 animated-film illustration for this audiobook video.
Audiobook: %q.
%s
Chapter %d of %d: %q.
Chapter focus: %s.
Narration mode: %s. Featured voices: %s.

Continuity requirements:
- This image is one frame from the same animated feature film as every other chapter image.
- Keep the main character's face, hair, wardrobe, silhouette, proportions, and color palette exactly the same across all images.
- Keep recurring speaker designs exactly the same whenever they appear.
- Change the setting, camera angle, action, and mood to fit this chapter, but do not redesign the main character or the film's art direction.

Style:
Polished animated feature film still, expressive 2D/cel-shaded illustration, clean readable silhouettes, warm cinematic lighting, hand-painted background depth, cohesive color script, subtle painterly texture, no photorealism.

Composition:
Show a specific story moment or metaphor from the chapter. Prefer the recurring main character in-frame unless the chapter is clearly abstract or location-focused. Leave the lower third calm enough for subtitles.

Constraints:
No text, no captions, no watermark, no logos, no UI, no speech bubbles.`,
		strings.TrimSpace(topic.Title),
		visualGuide,
		chapterIndex+1,
		chapterCount,
		title,
		chapterMood(ch),
		mode,
		speakers)
}

func audioBookIllustrationVisualGuide(topic *config.DebateTopic) string {
	host := strings.TrimSpace(topic.AudioBookHost.Name)
	if host == "" {
		host = "Narrator"
	}
	looks := audioBookCharacterLooks(topic)
	lines := []string{
		"Shared visual bible:",
		fmt.Sprintf("- Main character: %s, the audiobook narrator and recurring on-screen guide. Design: %s.", host, looks[characterLookKey(host)]),
	}
	for i, s := range topic.AudioBookSpeakers {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			continue
		}
		desc := strings.TrimSpace(s.Description)
		if desc == "" {
			desc = "recurring supporting speaker"
		}
		gender := strings.TrimSpace(s.Gender)
		if gender == "" {
			gender = "neutral presentation"
		}
		look := looks[characterLookKey(name)]
		if look == "" {
			look = audioBookCharacterLook(name, i+1)
		}
		lines = append(lines, fmt.Sprintf("- Recurring speaker: %s, %s, %s. Design: %s.", name, gender, desc, look))
		if len(lines) == 5 {
			break
		}
	}
	lines = append(lines, "- Overall world: consistent animated-film art direction across every chapter image; same character models, line weight, lighting grammar, and palette family.")
	return strings.Join(lines, "\n")
}

func audioBookCharacterLooks(topic *config.DebateTopic) map[string]string {
	looks := make(map[string]string, len(topic.AudioBookSpeakers)+1)
	host := strings.TrimSpace(topic.AudioBookHost.Name)
	if host == "" {
		host = "Narrator"
	}
	looks[characterLookKey(host)] = audioBookCharacterLook(host, 0)
	for i, s := range topic.AudioBookSpeakers {
		name := strings.TrimSpace(s.Name)
		key := characterLookKey(name)
		if key == "" || looks[key] != "" {
			continue
		}
		looks[key] = audioBookCharacterLook(name, i+1)
	}
	return looks
}

func characterLookKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func audioBookCharacterLook(name string, offset int) string {
	hair := []string{
		"short dark wavy hair",
		"shoulder-length black bob",
		"close-cropped silver hair",
		"curly brown hair",
		"smooth auburn hair",
	}
	wardrobe := []string{
		"deep teal jacket with a warm ochre scarf",
		"cranberry coat over a navy shirt",
		"indigo blazer with a cream turtleneck",
		"forest green cardigan over a slate shirt",
		"charcoal jacket with a small copper lapel pin",
	}
	silhouette := []string{
		"calm upright posture and rounded friendly features",
		"thoughtful posture with angular glasses",
		"open expressive posture and soft square features",
		"reserved posture with a clean oval face",
		"energetic posture with a strong simple silhouette",
	}
	idx := audioBookStableVisualIndex(name, offset)
	return fmt.Sprintf("%s, %s, %s", silhouette[idx%len(silhouette)], hair[(idx/3)%len(hair)], wardrobe[(idx/5)%len(wardrobe)])
}

func audioBookStableVisualIndex(name string, offset int) int {
	sum := offset*97 + 31
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		sum += int(r)
	}
	if sum < 0 {
		return -sum
	}
	return sum
}

func generateSpeakerAvatars(ctx context.Context, log *slog.Logger, client *imagegen.Client,
	topic *config.DebateTopic, cacheDir string,
) []contentcreator.AudioBookAvatar {
	if topic == nil || client == nil || !usesConversationalAudioBookLayout(topic.AudioBookStyle) {
		return nil
	}
	speakers := audioBookAvatarSpeakers(topic)
	if len(speakers) == 0 {
		return nil
	}
	avatarDir := filepath.Join(cacheDir, "avatars")
	if err := os.MkdirAll(avatarDir, 0o755); err != nil {
		log.Warn("audiobook avatar dir failed", "err", err)
		return nil
	}

	out := make([]contentcreator.AudioBookAvatar, len(speakers))
	var wg sync.WaitGroup
	for i, sp := range speakers {
		wg.Add(1)
		go func(i int, sp audioBookAvatarSpeaker) {
			defer wg.Done()
			raw, gerr := client.Generate(ctx, imagegen.Request{
				Model:  imagegen.PuzzleSceneModel,
				Prompt: audioBookAvatarPrompt(topic, sp),
				Size:   avatarSize,
			})
			if gerr != nil {
				log.Warn("audiobook avatar generation failed", "speaker", sp.Name, "err", gerr)
				return
			}
			base := fmt.Sprintf("%02d-%s", i, speakerSlug(sp.Name))
			greenPath := filepath.Join(avatarDir, base+"-green.png")
			alphaPath := filepath.Join(avatarDir, base+".png")
			if werr := os.WriteFile(greenPath, raw, 0o644); werr != nil {
				log.Warn("audiobook avatar write failed", "speaker", sp.Name, "err", werr)
				return
			}
			if cerr := chromaKeyAvatar(ctx, greenPath, alphaPath); cerr != nil {
				log.Warn("audiobook avatar chromakey failed", "speaker", sp.Name, "err", cerr)
				return
			}
			out[i] = contentcreator.AudioBookAvatar{Name: sp.Name, Path: alphaPath}
		}(i, sp)
	}
	wg.Wait()

	kept := out[:0]
	for _, avatar := range out {
		if strings.TrimSpace(avatar.Name) == "" || avatar.Path == "" {
			continue
		}
		kept = append(kept, avatar)
	}
	return kept
}

type audioBookAvatarSpeaker struct {
	Name        string
	Description string
	Gender      string
	Host        bool
	Look        string
}

func audioBookAvatarSpeakers(topic *config.DebateTopic) []audioBookAvatarSpeaker {
	host := strings.TrimSpace(topic.AudioBookHost.Name)
	if host == "" {
		host = "Narrator"
	}
	looks := audioBookCharacterLooks(topic)
	seen := map[string]bool{}
	add := func(sp audioBookAvatarSpeaker) []audioBookAvatarSpeaker {
		name := strings.TrimSpace(sp.Name)
		if name == "" {
			return nil
		}
		key := strings.ToLower(name)
		if seen[key] {
			return nil
		}
		seen[key] = true
		sp.Name = name
		if sp.Look == "" {
			sp.Look = looks[characterLookKey(name)]
		}
		return []audioBookAvatarSpeaker{sp}
	}
	var out []audioBookAvatarSpeaker
	out = append(out, add(audioBookAvatarSpeaker{
		Name:        host,
		Description: "the main host and narrator",
		Host:        true,
		Look:        looks[characterLookKey(host)],
	})...)
	for _, s := range topic.AudioBookSpeakers {
		out = append(out, add(audioBookAvatarSpeaker{
			Name:        s.Name,
			Description: s.Description,
			Gender:      s.Gender,
			Look:        looks[characterLookKey(s.Name)],
		})...)
	}
	if len(out) > 2 {
		out = out[:2]
	}
	return out
}

func audioBookAvatarPrompt(topic *config.DebateTopic, sp audioBookAvatarSpeaker) string {
	role := "guest speaker"
	if sp.Host {
		role = "main host and narrator"
	}
	desc := strings.TrimSpace(sp.Description)
	if desc == "" {
		desc = role
	}
	gender := strings.TrimSpace(sp.Gender)
	if gender == "" {
		gender = "neutral presentation"
	}
	look := strings.TrimSpace(sp.Look)
	if look == "" {
		look = audioBookCharacterLook(sp.Name, 0)
	}
	return fmt.Sprintf(`Create an animated-film style speaker avatar for an audiobook conversation video.
Subject: %s, %s. Role: %s. Description: %s.
Project: %s.
Character continuity: use this exact character design so the avatar matches the chapter illustrations: %s.
Style: polished animated feature film character, 2D cel-shaded avatar, clean vector-like shapes, simple readable silhouette, waist-up or full-body framing, facing camera, expressive but natural, bold clean outline, flat color regions, no props that touch the frame edge.
Hair: simplified cartoon hair made from solid opaque shapes with clean edges. No individual hair strands, no wispy flyaway hair, no semi-transparent hair, no green rim light, no green highlights.
Background: perfectly flat solid #00ff00 chroma-key background.
Constraints: the background must be one uniform #00ff00 color with no shadows, gradients, texture, floor plane, reflections, or lighting variation. Keep the subject fully separated from the background with crisp edges and generous padding. Do not use #00ff00 or any green hue in the subject, clothing, hair, outline, shadows, or highlights. No photorealism, no photographic texture, no cast shadow, no contact shadow, no reflection, no text, no captions, no watermark.`,
		sp.Name, gender, role, desc, strings.TrimSpace(topic.Title), look)
}

func chromaKeyAvatar(ctx context.Context, inPath, outPath string) error {
	args := []string{
		"-y",
		"-loglevel", "error",
		"-i", inPath,
		"-vf", "chromakey=0x00ff00:0.22:0.10,format=rgba",
		outPath,
	}
	out, err := exec.CommandContext(ctx, "ffmpeg", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg chromakey: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func usesConversationalAudioBookLayout(style string) bool {
	switch strings.ToLower(strings.TrimSpace(style)) {
	case config.AudioBookStyleConversational, config.AudioBookStylePodcast, config.AudioBookStyleMeeting, config.AudioBookStyleNews:
		return true
	default:
		return false
	}
}

// topicSlug builds a filesystem/url-safe slug from the audiobook title so
// uploaded illustration keys are human-recognisable.
func topicSlug(topic *config.DebateTopic) string {
	s := strings.ToLower(strings.TrimSpace(topic.Title))
	return slugText(s, "audiobook", 48)
}

func speakerSlug(name string) string {
	return slugText(strings.ToLower(strings.TrimSpace(name)), "speaker", 36)
}

func slugText(s, fallback string, maxLen int) string {
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
		return fallback
	}
	if len(slug) > maxLen {
		slug = slug[:maxLen]
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
