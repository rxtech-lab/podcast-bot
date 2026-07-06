package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	// The durable audiobook illustrations are webp; register the decoder so
	// ensureAudioBookIllustrations can transcode them to the PNGs the render
	// pipeline expects.
	_ "golang.org/x/image/webp"

	"github.com/sirily11/debate-bot/internal/config"
	contentcreator "github.com/sirily11/debate-bot/internal/content_creator"
	"github.com/sirily11/debate-bot/internal/mq"
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

// VideoRenderPayload is the wire payload of a queued audiobook video render.
// It carries identifiers only — the consuming pod rebuilds every render
// input from the database, local disk, and object storage.
type VideoRenderPayload struct {
	JobID        string `json:"job_id"`
	DiscussionID string `json:"discussion_id"`
}

// AudioBookTimingsObjectName is the deterministic object key (pre-prefix)
// under which the render pipeline stages the timings.json sidecar so a
// consuming pod other than the one that generated the audio can reproduce
// beat-accurate image motion.
func AudioBookTimingsObjectName(jobID string) string {
	return path.Join("audiobook-timings", jobID+".json")
}

// PublishAudioBookVideoRender resets the video attempt lifecycle on the job
// row and enqueues the render task. Shared by the manual endpoint and the
// automatic post-podcast pass (internal/videojob).
func PublishAudioBookVideoRender(ctx context.Context, jobs *JobRegistry, mqc mq.Client, jobID, discussionID string) error {
	if mqc == nil {
		return fmt.Errorf("video generation queue is not configured")
	}
	jobs.Update(jobID, func(j *Job) {
		j.Phase = "video-queued"
		j.PhaseLabel = "Video queued"
		j.VideoAttempts = 0
	})
	task, err := mq.NewTask(mq.TaskVideoRender, jobID, VideoRenderPayload{JobID: jobID, DiscussionID: discussionID})
	if err != nil {
		return fmt.Errorf("encode video render task: %w", err)
	}
	if err := mqc.Publish(ctx, mq.QueueGeneration, task); err != nil {
		jobs.Update(jobID, func(j *Job) {
			j.Phase = "video-failed"
			j.PhaseLabel = "Video enqueue failed"
		})
		return fmt.Errorf("enqueue video render: %w", err)
	}
	return nil
}

func (s *Server) enqueueDiscussionAudioBookVideo(ctx context.Context, d *Discussion) error {
	if s.d.Jobs == nil || s.d.UploadRoot == "" || s.d.Uploader == nil || !s.d.Uploader.Enabled() {
		return fmt.Errorf("video generation is not configured")
	}
	jobID := strings.TrimSpace(d.JobID)
	if jobID == "" {
		return fmt.Errorf("discussion has no source job")
	}
	return PublishAudioBookVideoRender(ctx, s.d.Jobs, s.d.MQ, jobID, d.ID)
}

// MarkVideoRenderRetryScheduled resets the phase so the next attempt's
// claim succeeds and surfaces the pending retry to clients.
func (s *Server) MarkVideoRenderRetryScheduled(jobID string, attempt int, delay time.Duration) {
	s.d.Jobs.Update(jobID, func(j *Job) {
		j.Phase = "video-queued"
		j.PhaseLabel = fmt.Sprintf("Video retrying (attempt %d/%d)", attempt+1, mq.MaxAttempts)
	})
	s.d.Jobs.AppendLog(jobID, "status",
		fmt.Sprintf("video render attempt %d/%d failed — retrying in %s", attempt, mq.MaxAttempts, delay.Round(time.Second)), nil)
}

// MarkVideoRenderFailed records the terminal failure of the video post-pass.
func (s *Server) MarkVideoRenderFailed(jobID string, cause error) {
	s.logger().Warn("audiobook video render failed terminally", "job", jobID, "err", cause)
	s.d.Jobs.Update(jobID, func(j *Job) {
		j.Phase = "video-failed"
		j.PhaseLabel = "Video failed"
	})
	s.d.Jobs.AppendLog(jobID, "error", fmt.Sprintf("video render failed: %v", cause), nil)
}

// RunAudioBookVideoRenderTask executes one queued render attempt: rebuild
// every input (audio, subtitles, timings, illustrations) from DB/disk/S3 —
// the consuming pod may not be the pod that generated the podcast — then
// render, upload, persist the video key, and notify. The returned error is
// the attempt's failure; the dispatch layer owns retry vs terminal.
func (s *Server) RunAudioBookVideoRenderTask(ctx context.Context, jobID, discussionID string) error {
	log := s.logger().With("job", jobID, "discussion", discussionID)
	if s.d.Uploader == nil || !s.d.Uploader.Enabled() {
		return mq.Permanent(fmt.Errorf("video generation is not configured"))
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
	d, err := s.d.Discussions.GetForNotification(ctx, discussionID)
	if err != nil {
		return fmt.Errorf("load discussion: %w", err)
	}
	if d == nil || d.Script == nil {
		return mq.Permanent(fmt.Errorf("discussion %s not found or has no script", discussionID))
	}
	if lines, lerr := s.d.Discussions.LinesByJob(ctx, jobID); lerr == nil {
		d.Lines = lines
	}

	audioPath, err := s.ensureAudioBookAudio(ctx, jobID, jobDir, job)
	if err != nil {
		return err
	}
	// The automatic pass publishes right after the audio recorder drains;
	// guard against reading a file whose realtime-paced tail is still being
	// flushed on this pod.
	waitForStableAudioFile(ctx, audioPath)
	vttPath := s.ensureAudioBookSubtitles(ctx, jobDir, job)
	s.ensureAudioBookTimings(ctx, jobID, jobDir)
	s.ensureAudioBookIllustrationsSidecar(ctx, jobDir, job)
	if err := s.ensureAudioBookIllustrations(ctx, jobDir); err != nil {
		log.Warn("audiobook illustrations materialization failed", "err", err)
	}
	imagePaths := audioBookIllustrationPaths(jobDir)
	if len(imagePaths) == 0 {
		return mq.Permanent(fmt.Errorf("no audiobook illustrations found for video"))
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
		return mq.Permanent(fmt.Errorf("no audiobook illustrations found for video"))
	}
	res := video.Resolution(d.Script.Resolution)
	if res == "" {
		res = video.Resolution1080p
	}

	log.Info("audiobook video render starting", "images", len(imagePaths), "resolution", string(res))
	if err := video.RenderAudioBookVideoWithOptions(outPath, audioPath, vttPath, imagePaths, res, opts); err != nil {
		return fmt.Errorf("render audiobook video: %w", err)
	}
	key := s.d.Uploader.Key(jobID + "-video.mp4")
	s.d.Jobs.Update(jobID, func(j *Job) {
		j.VideoPath = outPath
		j.HasVideo = true
		j.Phase = "video-uploading"
		j.PhaseLabel = "Uploading video"
	})
	if err := s.d.Uploader.Upload(ctx, outPath, key); err != nil {
		return fmt.Errorf("upload audiobook video: %w", err)
	}
	if err := s.d.Discussions.SetVideoKey(ctx, discussionID, key); err != nil {
		log.Warn("audiobook video key persist failed", "key", key, "err", err)
	} else {
		log.Info("audiobook video key persisted", "key", key)
	}
	s.d.Jobs.Update(jobID, func(j *Job) {
		j.Phase = "video-ready"
		j.PhaseLabel = "Video ready"
	})
	s.publishDiscussionResourceUpdated(jobID, discussionID, "Video ready", "video")
	s.notifyAudioBookVideoReady(ctx, d, log)
	return nil
}

// waitForStableAudioFile blocks briefly until the audio file stops growing
// (2s quiet, 30s cap) so the render never probes a still-draining recording.
func waitForStableAudioFile(ctx context.Context, path string) {
	const (
		poll  = 500 * time.Millisecond
		quiet = 2 * time.Second
		max   = 30 * time.Second
	)
	deadline := time.Now().Add(max)
	lastSize := int64(-1)
	stableSince := time.Now()
	for {
		if info, err := os.Stat(path); err == nil {
			if info.Size() != lastSize {
				lastSize = info.Size()
				stableSince = time.Now()
			} else if time.Since(stableSince) >= quiet {
				return
			}
		}
		if time.Now().After(deadline) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
	}
}

// notifyAudioBookVideoReady pushes a "video ready" notification so the owner
// can open the freshly rendered video from the context menu.
func (s *Server) notifyAudioBookVideoReady(ctx context.Context, d *Discussion, log *slog.Logger) {
	if s.apns == nil || d == nil {
		return
	}
	title := strings.TrimSpace(d.Title)
	body := "Your audiobook video is ready to watch."
	if title != "" {
		body = title
	}
	SendPushNotification(ctx, s.d.Discussions, s.apns, d.OwnerUserID, PushNotification{
		Kind:         PushKindPodcastVideoReady,
		DiscussionID: d.ID,
		Title:        "Video ready",
		Body:         body,
		URL:          DiscussionDeepLink(FrontendBaseURL(s.d.Env), d.ID),
	}, log)
}

// ensureAudioBookTimings downloads the timings.json sidecar the generating
// pod staged in object storage when the local copy is absent (cross-pod
// render). Best-effort — the render falls back to the illustrations
// timeline, then to an even split.
func (s *Server) ensureAudioBookTimings(ctx context.Context, jobID, jobDir string) {
	local := filepath.Join(jobDir, "audiobook", "scenes", "timings.json")
	if info, err := os.Stat(local); err == nil && info.Size() > 0 {
		return
	}
	data, err := s.d.Uploader.Download(ctx, s.d.Uploader.Key(AudioBookTimingsObjectName(jobID)))
	if err != nil || len(data) == 0 {
		return
	}
	_ = os.MkdirAll(filepath.Dir(local), 0o755)
	_ = os.WriteFile(local, data, 0o644)
}

// ensureAudioBookIllustrationsSidecar downloads the illustrations timeline
// sidecar when the local copy is absent (cross-pod render). Best-effort.
func (s *Server) ensureAudioBookIllustrationsSidecar(ctx context.Context, jobDir string, job *Job) {
	local := filepath.Join(jobDir, PodcastAudioDir, PodcastIllustrationsFilename)
	if info, err := os.Stat(local); err == nil && info.Size() > 0 {
		return
	}
	if job == nil || strings.TrimSpace(job.IllustrationsS3Key) == "" {
		return
	}
	data, err := s.d.Uploader.Download(ctx, strings.TrimSpace(job.IllustrationsS3Key))
	if err != nil || len(data) == 0 {
		return
	}
	_ = os.MkdirAll(filepath.Dir(local), 0o755)
	_ = os.WriteFile(local, data, 0o644)
}

// ensureAudioBookIllustrations materialises the scene PNGs from the durable
// per-cue image keys in the illustrations sidecar when the local glob is
// empty — required when the rendering pod is not the pod that generated
// them. Durable images are webp; they are decoded and re-encoded as the
// narration-v<beat>.png files the render pipeline globs.
func (s *Server) ensureAudioBookIllustrations(ctx context.Context, jobDir string) error {
	if len(audioBookIllustrationPaths(jobDir)) > 0 {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(jobDir, PodcastAudioDir, PodcastIllustrationsFilename))
	if err != nil {
		return nil // no sidecar → nothing to materialise
	}
	var sidecar struct {
		Illustrations []contentcreator.IllustrationCue `json:"illustrations"`
	}
	if json.Unmarshal(data, &sidecar) != nil || len(sidecar.Illustrations) == 0 {
		return nil
	}
	scenesDir := filepath.Join(jobDir, "audiobook", "scenes")
	if err := os.MkdirAll(scenesDir, 0o755); err != nil {
		return fmt.Errorf("create scenes dir: %w", err)
	}
	var restored int
	for _, cue := range sidecar.Illustrations {
		key := strings.TrimSpace(cue.ImageKey)
		if key == "" {
			continue
		}
		beat, ok := audioBookImageKeyBeat(key)
		if !ok {
			continue
		}
		dst := filepath.Join(scenesDir, fmt.Sprintf("narration-v%d.png", beat))
		if info, serr := os.Stat(dst); serr == nil && info.Size() > 0 {
			restored++
			continue
		}
		raw, derr := s.d.Uploader.Download(ctx, key)
		if derr != nil {
			return fmt.Errorf("download illustration %s: %w", key, derr)
		}
		img, _, ierr := image.Decode(bytes.NewReader(raw))
		if ierr != nil {
			return fmt.Errorf("decode illustration %s: %w", key, ierr)
		}
		var buf bytes.Buffer
		if perr := png.Encode(&buf, img); perr != nil {
			return fmt.Errorf("encode illustration %s: %w", key, perr)
		}
		if werr := os.WriteFile(dst, buf.Bytes(), 0o644); werr != nil {
			return fmt.Errorf("write illustration %s: %w", dst, werr)
		}
		restored++
	}
	s.logger().Info("audiobook illustrations materialised from object storage", "count", restored)
	return nil
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
