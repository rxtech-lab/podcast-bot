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
	opts.Animations, opts.ImageOffsets = audioBookVideoTimings(jobDir, len(imagePaths))
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
func audioBookVideoTimings(jobDir string, imageCount int) (anims []string, offsets []float64) {
	scenesDir := filepath.Join(jobDir, "audiobook", "scenes")
	var timings struct {
		Animations   []string  `json:"animations"`
		ImageOffsets []float64 `json:"image_offsets"`
	}
	if data, err := os.ReadFile(filepath.Join(scenesDir, "timings.json")); err == nil {
		if json.Unmarshal(data, &timings) == nil {
			anims = timings.Animations
			if len(timings.ImageOffsets) == imageCount {
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
	return anims, offsets
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
