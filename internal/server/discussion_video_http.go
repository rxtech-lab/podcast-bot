package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/sirily11/debate-bot/internal/config"
	contentcreator "github.com/sirily11/debate-bot/internal/content_creator"
	"github.com/sirily11/debate-bot/internal/video"
)

func (s *Server) handleDiscussionVideoGenerate(w http.ResponseWriter, r *http.Request) {
	user := s.requestUser(r)
	id := r.PathValue("id")
	d, err := s.d.Discussions.Get(r.Context(), user.ID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil || d.Script == nil {
		http.NotFound(w, r)
		return
	}
	if !discussionIsAudioBook(d) {
		http.Error(w, "video generation is only available for audiobooks", http.StatusBadRequest)
		return
	}
	if d.Status != DiscussionReady {
		http.Error(w, "podcast must be ready before video generation", http.StatusConflict)
		return
	}
	if videoKey := s.discussionVideoKey(r, d); strings.TrimSpace(videoKey) != "" {
		s.sanitizeDiscussionUsage(d)
		writeJSON(w, d)
		return
	}
	if s.audioBookVideoRendering(d) {
		s.sanitizeDiscussionUsage(d)
		writeJSON(w, d)
		return
	}
	if err := s.enqueueDiscussionAudioBookVideo(r.Context(), d); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.sanitizeDiscussionUsage(d)
	writeJSON(w, d)
}

func (s *Server) enqueueDiscussionAudioBookVideo(ctx context.Context, d *Discussion) error {
	if s.d.Jobs == nil || s.d.UploadRoot == "" || s.d.Uploader == nil || !s.d.Uploader.Enabled() {
		return fmt.Errorf("video generation is not configured")
	}
	jobID := strings.TrimSpace(d.JobID)
	if jobID == "" {
		return fmt.Errorf("discussion has no source job")
	}
	jobDir := s.jobArtifactDir(jobID)
	if jobDir == "" {
		jobDir = filepath.Join(filepath.Dir(s.d.UploadRoot), "jobs", jobID)
		if err := os.MkdirAll(jobDir, 0o755); err != nil {
			return fmt.Errorf("create job dir: %w", err)
		}
	}
	job := s.d.Jobs.Get(jobID)
	if job == nil {
		job = s.recoverJob(jobID)
	}
	audioPath, err := s.ensureAudioBookAudio(ctx, jobID, jobDir, job)
	if err != nil {
		return err
	}
	vttPath := s.ensureAudioBookSubtitles(ctx, jobDir, job)
	imagePaths := audioBookIllustrationPaths(jobDir)
	if len(imagePaths) == 0 {
		return fmt.Errorf("no audiobook illustrations found for video")
	}
	outPath := filepath.Join(jobDir, "video.mp4")
	opts := discussionAudioBookVideoOptions(d.Script, d.Lines, audioBookAvatarPaths(jobDir))
	anims, offsets, beats := audioBookVideoTimings(jobDir, len(imagePaths))
	if len(beats) == 0 && len(offsets) == 0 {
		// Books rendered before the beats-aware timings sidecar existed have
		// their real per-beat offsets in the illustrations timeline sidecar —
		// use those rather than even-splitting every generated image.
		offsets, beats = audioBookOffsetsFromIllustrations(jobDir)
		// Legacy animation lists are indexed by beat (one entry per planned
		// image); re-select them to parallel the recovered beats.
		if len(beats) > 0 && len(anims) > 0 {
			picked := make([]string, len(beats))
			for i, b := range beats {
				if b >= 0 && b < len(anims) {
					picked[i] = anims[b]
				}
			}
			anims = picked
		}
	}
	imagePaths, opts.Animations, opts.ImageOffsets = applyAudioBookTimingBeats(imagePaths, anims, offsets, beats)
	if len(imagePaths) == 0 {
		return fmt.Errorf("no audiobook illustrations found for video")
	}
	res := video.Resolution(d.Script.Resolution)
	if res == "" {
		res = video.Resolution1080p
	}

	s.d.Jobs.Update(jobID, func(j *Job) {
		j.Phase = "video-queued"
		j.PhaseLabel = "Video queued"
	})
	go s.renderDiscussionAudioBookVideo(context.Background(), jobID, d.ID, outPath, audioPath, vttPath, imagePaths, res, opts)
	return nil
}

func (s *Server) renderDiscussionAudioBookVideo(ctx context.Context, jobID, discussionID, outPath, audioPath, vttPath string, imagePaths []string, res video.Resolution, opts video.AudioBookVideoOptions) {
	log := s.logger().With("job", jobID, "discussion", discussionID)
	s.d.Jobs.Update(jobID, func(j *Job) {
		j.Phase = "video-rendering"
		j.PhaseLabel = "Rendering video"
	})
	if err := video.RenderAudioBookVideoWithOptions(outPath, audioPath, vttPath, imagePaths, res, opts); err != nil {
		log.Warn("manual audiobook video render failed", "err", err)
		s.d.Jobs.Update(jobID, func(j *Job) {
			j.Phase = "video-failed"
			j.PhaseLabel = "Video failed"
		})
		return
	}
	key := s.d.Uploader.Key(jobID + "-video.mp4")
	s.d.Jobs.Update(jobID, func(j *Job) {
		j.VideoPath = outPath
		j.HasVideo = true
		j.Phase = "video-uploading"
		j.PhaseLabel = "Uploading video"
	})
	if err := s.d.Uploader.Upload(ctx, outPath, key); err != nil {
		log.Warn("manual audiobook video upload failed", "key", key, "err", err)
		s.d.Jobs.Update(jobID, func(j *Job) {
			j.Phase = "video-failed"
			j.PhaseLabel = "Video upload failed"
		})
		return
	}
	if err := s.d.Discussions.SetVideoKey(ctx, discussionID, key); err != nil {
		log.Warn("manual audiobook video key persist failed", "key", key, "err", err)
	} else {
		log.Info("manual audiobook video key persisted", "key", key)
	}
	s.d.Jobs.Update(jobID, func(j *Job) {
		j.Phase = "video-ready"
		j.PhaseLabel = "Video ready"
	})
	s.publishDiscussionResourceUpdated(jobID, discussionID, "Video ready", "video")
}

func (s *Server) ensureAudioBookAudio(ctx context.Context, jobID, jobDir string, job *Job) (string, error) {
	candidates := []string{}
	if job != nil && strings.TrimSpace(job.AudioPath) != "" {
		candidates = append(candidates, strings.TrimSpace(job.AudioPath))
	}
	candidates = append(candidates, podcastAudioPath(jobDir), legacyPodcastAudioPath(jobDir))
	for _, path := range candidates {
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			return path, nil
		}
	}
	if job == nil || strings.TrimSpace(job.AudioS3Key) == "" {
		return "", fmt.Errorf("audio artifact is not available")
	}
	data, err := s.d.Uploader.Download(ctx, strings.TrimSpace(job.AudioS3Key))
	if err != nil {
		return "", fmt.Errorf("download audio artifact: %w", err)
	}
	path := podcastAudioPath(jobDir)
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("write audio artifact: %w", err)
	}
	return path, nil
}

func (s *Server) ensureAudioBookSubtitles(ctx context.Context, jobDir string, job *Job) string {
	path := firstExistingNonEmpty(podcastSubtitlesPath(jobDir), legacyPodcastSubtitlesPath(jobDir))
	if path != "" {
		return path
	}
	path = podcastSubtitlesPath(jobDir)
	if job == nil || strings.TrimSpace(job.SubtitlesS3Key) == "" {
		return path
	}
	data, err := s.d.Uploader.Download(ctx, strings.TrimSpace(job.SubtitlesS3Key))
	if err != nil || len(data) == 0 {
		return path
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, data, 0o644)
	return path
}

func audioBookIllustrationPaths(jobDir string) []string {
	paths, _ := filepath.Glob(filepath.Join(jobDir, "audiobook", "scenes", "narration-v*.png"))
	// Numeric sort on the beat index — a plain string sort puts
	// narration-v10.png before narration-v2.png once the dense plan pushes
	// past 9 images, scrambling the slideshow order.
	sort.Slice(paths, func(i, j int) bool {
		return audioBookIllustrationBeat(paths[i]) < audioBookIllustrationBeat(paths[j])
	})
	return paths
}

// audioBookIllustrationBeat extracts N from a ".../narration-vN.png" path;
// non-matching names sort last.
func audioBookIllustrationBeat(path string) int {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	numStr := strings.TrimPrefix(base, "narration-v")
	n, err := strconv.Atoi(numStr)
	if err != nil {
		return int(^uint(0) >> 1)
	}
	return n
}

// audioBookVideoTimings loads the animation + audio-offset sidecar the
// original render job wrote next to the scene PNGs (timings.json). Falls
// back to the prepare step's plan.json for animations alone. Empty slices
// mean "no metadata" — the renderer then uses its fallback motion cycle and
// even image spacing, which is also the compat path for audiobooks rendered
// before this metadata existed.
//
// beats, when present in the sidecar, names the narration beat behind each
// parallel animations/offsets entry: the original render may have dropped
// illustrations whose scene marker never fired (never-narrated beats), so
// the entries no longer line up 1:1 with the globbed scene PNGs — the caller
// narrows the image list via applyAudioBookTimingBeats. Legacy sidecars have
// no beats field; there offsets are only trusted when they cover every image.
func audioBookVideoTimings(jobDir string, imageCount int) (anims []string, offsets []float64, beats []int) {
	scenesDir := filepath.Join(jobDir, "audiobook", "scenes")
	var timings struct {
		Animations   []string  `json:"animations"`
		ImageOffsets []float64 `json:"image_offsets"`
		Beats        []int     `json:"beats"`
	}
	if data, err := os.ReadFile(filepath.Join(scenesDir, "timings.json")); err == nil {
		if json.Unmarshal(data, &timings) == nil {
			anims = timings.Animations
			switch {
			case len(timings.Beats) > 0:
				beats = timings.Beats
				if len(timings.ImageOffsets) == len(beats) {
					offsets = timings.ImageOffsets
				}
			case len(timings.ImageOffsets) == imageCount:
				offsets = timings.ImageOffsets
			}
		}
	}
	if len(anims) == 0 {
		var plan struct {
			NarrationAnimations []string `json:"narration_animations"`
		}
		if data, err := os.ReadFile(filepath.Join(scenesDir, "plan.json")); err == nil {
			if json.Unmarshal(data, &plan) == nil {
				anims = plan.NarrationAnimations
			}
		}
	}
	return anims, offsets, beats
}

// audioBookOffsetsFromIllustrations derives per-beat audio offsets from the
// illustrations timeline sidecar (podcast-audio/illustrations.json), which
// records when each fired illustration actually appeared on the audio
// timeline. The beat is recovered from the durable image key
// ("…/image-K.webp" is beat K-1 — see audiobook/prepare.go). Cues without a
// parseable key are skipped; returns nils when the sidecar is missing or
// yields nothing. Cue order (sorted by start time) is preserved so the
// offsets stay non-decreasing for the renderer's validation.
func audioBookOffsetsFromIllustrations(jobDir string) (offsets []float64, beats []int) {
	data, err := os.ReadFile(filepath.Join(jobDir, PodcastAudioDir, PodcastIllustrationsFilename))
	if err != nil {
		return nil, nil
	}
	var sidecar struct {
		Illustrations []contentcreator.IllustrationCue `json:"illustrations"`
	}
	if json.Unmarshal(data, &sidecar) != nil {
		return nil, nil
	}
	for _, cue := range sidecar.Illustrations {
		key := cue.ImageKey
		if strings.TrimSpace(key) == "" {
			key = cue.ImageURL
		}
		beat, ok := audioBookImageKeyBeat(key)
		if !ok {
			continue
		}
		beats = append(beats, beat)
		offsets = append(offsets, float64(cue.StartMS)/1000)
	}
	return offsets, beats
}

// audioBookImageKeyBeat extracts the 0-based beat index from a durable
// audiobook image key or URL of the form "…/image-K.webp" (K is 1-based).
func audioBookImageKeyBeat(key string) (int, bool) {
	base := filepath.Base(strings.TrimSpace(key))
	base = strings.TrimSuffix(base, filepath.Ext(base))
	numStr := strings.TrimPrefix(base, "image-")
	if numStr == base {
		return 0, false
	}
	n, err := strconv.Atoi(numStr)
	if err != nil || n < 1 {
		return 0, false
	}
	return n - 1, true
}

// applyAudioBookTimingBeats narrows the globbed scene PNGs to the beats the
// timings sidecar recorded, keeping the animation/offset slices parallel to
// the returned paths. A beat with no matching PNG on disk drops its timing
// entries too so the three slices stay aligned. nil beats (legacy sidecar or
// no sidecar) returns the inputs unchanged.
func applyAudioBookTimingBeats(imagePaths, anims []string, offsets []float64, beats []int) ([]string, []string, []float64) {
	if len(beats) == 0 {
		return imagePaths, anims, offsets
	}
	byBeat := make(map[int]string, len(imagePaths))
	for _, p := range imagePaths {
		byBeat[audioBookIllustrationBeat(p)] = p
	}
	outPaths := make([]string, 0, len(beats))
	outAnims := make([]string, 0, len(beats))
	outOffsets := make([]float64, 0, len(beats))
	for i, b := range beats {
		p, ok := byBeat[b]
		if !ok {
			continue
		}
		outPaths = append(outPaths, p)
		if i < len(anims) {
			outAnims = append(outAnims, anims[i])
		} else {
			outAnims = append(outAnims, "")
		}
		if i < len(offsets) {
			outOffsets = append(outOffsets, offsets[i])
		}
	}
	if len(outOffsets) != len(outPaths) {
		// Offsets were absent (or a PNG gap desynced them) — even split.
		outOffsets = nil
	}
	return outPaths, outAnims, outOffsets
}

func audioBookAvatarPaths(jobDir string) []video.AudioBookVideoAvatar {
	paths, _ := filepath.Glob(filepath.Join(jobDir, "audiobook", "scenes", "avatars", "*.png"))
	sort.Strings(paths)
	out := make([]video.AudioBookVideoAvatar, 0, len(paths))
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		out = append(out, video.AudioBookVideoAvatar{Name: name, Path: path})
	}
	return out
}

func discussionAudioBookVideoOptions(topic *config.DebateTopic, lines []DiscussionLine, avatars []video.AudioBookVideoAvatar) video.AudioBookVideoOptions {
	if topic == nil {
		return video.AudioBookVideoOptions{Avatars: avatars}
	}
	host := strings.TrimSpace(topic.AudioBookHost.Name)
	if host == "" {
		host = "Narrator"
	}
	speakers := make([]string, 0, 1+len(topic.AudioBookSpeakers))
	speakers = append(speakers, host)
	for _, s := range topic.AudioBookSpeakers {
		if name := strings.TrimSpace(s.Name); name != "" {
			speakers = append(speakers, name)
		}
	}
	outLines := make([]video.AudioBookVideoLine, 0, len(lines))
	for _, line := range lines {
		text := strings.TrimSpace(line.Text)
		if text == "" {
			continue
		}
		speaker := strings.TrimSpace(line.Speaker)
		if speaker == "" {
			speaker = host
		}
		outLines = append(outLines, video.AudioBookVideoLine{Speaker: speaker, Text: text})
	}
	return video.AudioBookVideoOptions{
		Style:    string(topic.AudioBookStyle),
		Title:    strings.TrimSpace(topic.Title),
		Language: strings.TrimSpace(topic.Language),
		Host:     host,
		Speakers: speakers,
		Lines:    outLines,
		Avatars:  avatars,
	}
}
