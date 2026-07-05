package videojob

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
	contentcreator "github.com/sirily11/debate-bot/internal/content_creator"
	"github.com/sirily11/debate-bot/internal/server"
	"github.com/sirily11/debate-bot/internal/video"
)

// scheduleAudioBookVideo enqueues a background pass that renders the audiobook's
// 1080p video (illustration slideshow + narration audio + soft captions) once
// the audio has finished streaming, uploads it to object storage, records the
// key on the discussion so the context menu can offer "View Video", and pushes
// a notification. No-op when there are no illustrations to show.
//
// Image paths are snapshotted synchronously before enqueuing so the task never
// touches the orchestrator after it has been shut down.
//
// recDone closes once the audio.mp3 recorder has flushed the full LiveStream.
// The queued render waits on it (bounded) and then confirms the file has
// stopped growing before probing its duration: rendering against a
// still-draining recording produced a video sized to a stale (short) probe,
// which is how audio tails got truncated. nil recDone skips the wait.
func scheduleAudioBookVideo(deps Deps, jobID string, sub server.JobSubmission,
	topic *config.DebateTopic, orch *contentcreator.Orchestrator, audioPath, jobOutDir string,
	recDone <-chan struct{},
) {
	logger := slog.Default().With("job", jobID)
	if deps.Log != nil {
		logger = deps.Log.With("job", jobID)
	}
	if deps.Queue == nil || orch == nil {
		logger.Info("audiobook video skipped", "queue_configured", deps.Queue != nil, "orchestrator_configured", orch != nil)
		return
	}
	imgs := orch.AudioBookImages()
	sort.Slice(imgs, func(i, j int) bool { return imgs[i].Beat < imgs[j].Beat })
	offsets := orch.AudioBookImageOffsets()
	paths, anims, starts, beats, skipped := snapshotAudioBookVideoImages(imgs, offsets)
	if len(paths) == 0 {
		// No illustrations → no slideshow to render. The audio + text doc still
		// stand on their own.
		logger.Info("audiobook video skipped", "reason", "no illustrations")
		return
	}
	if skipped > 0 {
		logger.Info("audiobook video: dropping beats whose scene marker never fired",
			"kept", len(paths), "skipped", skipped)
	}
	opts := audioBookVideoOptions(topic, orch.Transcript.Snapshot(), orch.AudioBookAvatars())
	opts.Animations = anims
	opts.ImageOffsets = starts
	writeAudioBookVideoTimings(logger, jobOutDir, anims, starts, beats)

	res := video.Resolution(topic.Resolution)
	if sub.Resolution != "" {
		res = video.Resolution(sub.Resolution)
	}
	if res == "" {
		res = video.Resolution1080p
	}
	vttPath := existingPodcastSubtitlesPath(jobOutDir)
	outPath := filepath.Join(jobOutDir, "video.mp4")

	deps.Jobs.Update(jobID, func(j *server.Job) {
		j.Phase = "video-queued"
		j.PhaseLabel = "Video queued"
	})
	deps.Queue.Add(context.Background(), func(runCtx context.Context) {
		defer func() {
			if v := recover(); v != nil {
				logger.Error("audiobook video render panic", "panic", v)
				deps.Jobs.Update(jobID, func(j *server.Job) {
					j.Phase = "video-failed"
					j.PhaseLabel = "Video failed"
				})
			}
		}()
		// The recorder is normally drained before this task is scheduled;
		// this guards the drain-timeout path where audio.mp3 may still be
		// receiving the realtime-paced tail.
		if recDone != nil {
			select {
			case <-recDone:
			case <-time.After(3 * time.Minute):
				logger.Warn("audiobook video: audio recorder still draining — verifying file stability")
				waitForStableFile(runCtx, logger, audioPath, 2*time.Second, 30*time.Second)
			case <-runCtx.Done():
				return
			}
		}
		logger.Info("audiobook video render starting", "images", len(paths), "resolution", string(res), "style", opts.Style)
		deps.Jobs.Update(jobID, func(j *server.Job) {
			j.Phase = "video-rendering"
			j.PhaseLabel = "Rendering video"
		})
		if err := video.RenderAudioBookVideoWithOptions(outPath, audioPath, vttPath, paths, res, opts); err != nil {
			logger.Warn("audiobook video render failed", "err", err)
			deps.Jobs.Update(jobID, func(j *server.Job) {
				j.Phase = "video-failed"
				j.PhaseLabel = "Video failed"
			})
			return
		}
		deps.Jobs.Update(jobID, func(j *server.Job) {
			j.VideoPath = outPath
			j.HasVideo = true
		})
		if !deps.Uploader.Enabled() {
			logger.Info("audiobook video upload skipped", "reason", "s3 uploader disabled", "path", outPath)
		} else {
			key := deps.Uploader.Key(jobID + "-video.mp4")
			logger.Info("audiobook video upload starting", "key", key, "path", outPath)
			deps.Jobs.Update(jobID, func(j *server.Job) {
				j.Phase = "video-uploading"
				j.PhaseLabel = "Uploading video"
			})
			if err := deps.Uploader.Upload(runCtx, outPath, key); err != nil {
				logger.Warn("audiobook video upload failed", "key", key, "err", err)
				deps.Jobs.Update(jobID, func(j *server.Job) {
					j.Phase = "video-failed"
					j.PhaseLabel = "Video upload failed"
				})
				return
			}
			logger.Info("audiobook video uploaded", "key", key)
			persistAudioBookVideoKey(runCtx, deps, logger, jobID, key)
		}
		deps.Jobs.Update(jobID, func(j *server.Job) {
			j.Phase = "video-ready"
			j.PhaseLabel = "Video ready"
		})
		logger.Info("audiobook video ready", "path", outPath)
		server.PublishDiscussionResourceUpdated(deps.Bus, deps.Env, jobID, deps.DiscussionID, "Video ready", "video")
		notifyAudioBookVideoReady(runCtx, deps, logger)
	})
}

// snapshotAudioBookVideoImages selects which illustrations go into the video
// and with what timing. imgs must be sorted by Beat; offsets is the per-beat
// audio position captured from the live run's scene markers.
//
// When any offsets were recorded, only beats whose marker actually fired are
// kept — a chapter-limited narration generates illustrations for the whole
// outline, but images past the narrated range have no place on this audio's
// timeline. (The previous all-or-nothing check discarded EVERY offset when
// one was missing, so the renderer even-split all images across the audio
// and the slideshow ran ahead of the narration.) When no offsets exist at
// all (legacy runs, no markers fired), every image is kept and starts is nil
// so the renderer falls back to its even split.
func snapshotAudioBookVideoImages(imgs []contentcreator.AudioBookImage, offsets map[int]float64,
) (paths, anims []string, starts []float64, beats []int, skipped int) {
	for _, im := range imgs {
		if im.Path == "" {
			continue
		}
		off, ok := offsets[im.Beat]
		if !ok && len(offsets) > 0 {
			skipped++
			continue
		}
		paths = append(paths, im.Path)
		anims = append(anims, im.Animation)
		starts = append(starts, off)
		beats = append(beats, im.Beat)
	}
	if len(offsets) == 0 {
		starts = nil
	}
	return paths, anims, starts, beats, skipped
}

// writeAudioBookVideoTimings persists the per-image animation + audio-offset
// snapshot next to the generated scene PNGs so the manual re-render endpoint
// can rebuild the same motion-timed video after the orchestrator is gone.
// beats identifies which narration-vN.png each entry describes — the
// re-render endpoint globs every scene PNG, so without it a filtered
// snapshot couldn't be matched back to files. offsets may be nil (unknown
// timing → even split on re-render).
func writeAudioBookVideoTimings(logger *slog.Logger, jobOutDir string, anims []string, offsets []float64, beats []int) {
	payload := struct {
		Animations   []string  `json:"animations"`
		ImageOffsets []float64 `json:"image_offsets,omitempty"`
		Beats        []int     `json:"beats,omitempty"`
	}{Animations: anims, ImageOffsets: offsets, Beats: beats}
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		logger.Warn("audiobook video timings marshal failed", "err", err)
		return
	}
	path := filepath.Join(jobOutDir, "audiobook", "scenes", "timings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		logger.Warn("audiobook video timings dir failed", "err", err)
		return
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
		logger.Warn("audiobook video timings write failed", "err", err)
	}
}

func persistAudioBookVideoKey(ctx context.Context, deps Deps, logger *slog.Logger, jobID, key string) {
	if deps.Discussions == nil {
		logger.Warn("audiobook video key persist skipped", "discussion_configured", false)
		return
	}
	if deps.DiscussionID != "" {
		if err := deps.Discussions.SetVideoKey(ctx, deps.DiscussionID, key); err == nil {
			logger.Info("audiobook video key persisted", "discussion", deps.DiscussionID, "key", key, "method", "discussion_id")
			return
		} else {
			logger.Warn("audiobook video key persist by discussion failed", "discussion", deps.DiscussionID, "err", err)
		}
	}
	if err := deps.Discussions.SetVideoKeyForJob(ctx, jobID, key); err != nil {
		logger.Warn("audiobook video key persist by job failed", "job", jobID, "err", err)
		return
	}
	logger.Info("audiobook video key persisted", "job", jobID, "key", key, "method", "job_id")
}

func audioBookVideoOptions(topic *config.DebateTopic, lines []agent.TranscriptLine,
	avatars []contentcreator.AudioBookAvatar,
) video.AudioBookVideoOptions {
	if topic == nil {
		return video.AudioBookVideoOptions{}
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
		outLines = append(outLines, video.AudioBookVideoLine{
			Speaker: speaker,
			Text:    text,
		})
	}
	outAvatars := make([]video.AudioBookVideoAvatar, 0, len(avatars))
	for _, avatar := range avatars {
		name := strings.TrimSpace(avatar.Name)
		if name == "" || avatar.Path == "" {
			continue
		}
		outAvatars = append(outAvatars, video.AudioBookVideoAvatar{
			Name: name,
			Path: avatar.Path,
		})
	}
	return video.AudioBookVideoOptions{
		Style:    topic.AudioBookStyle,
		Title:    topic.Title,
		Language: topic.Language,
		Host:     host,
		Speakers: speakers,
		Lines:    outLines,
		Avatars:  outAvatars,
	}
}

// notifyAudioBookVideoReady pushes a "video ready" notification so the owner
// can open the freshly rendered video from the context menu.
func notifyAudioBookVideoReady(ctx context.Context, deps Deps, log *slog.Logger) {
	if deps.APNS == nil || deps.Discussions == nil || deps.DiscussionID == "" {
		return
	}
	d, err := deps.Discussions.GetForNotification(ctx, deps.DiscussionID)
	if err != nil || d == nil {
		return
	}
	server.SendPushNotification(ctx, deps.Discussions, deps.APNS, d.OwnerUserID, server.PushNotification{
		Kind:         server.PushKindPodcastVideoReady,
		DiscussionID: d.ID,
		Title:        "Video ready",
		Body:         pushDiscussionTitle(d, "Your audiobook video is ready to watch."),
		URL:          server.DiscussionDeepLink(server.FrontendBaseURL(deps.Env), d.ID),
	}, log)
}
