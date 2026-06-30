package videojob

import (
	"context"
	"log/slog"
	"path/filepath"
	"sort"

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
func scheduleAudioBookVideo(deps Deps, jobID string, sub server.JobSubmission,
	topic *config.DebateTopic, orch *contentcreator.Orchestrator, audioPath, jobOutDir string,
) {
	if deps.Queue == nil || orch == nil {
		return
	}
	imgs := orch.AudioBookImages()
	sort.Slice(imgs, func(i, j int) bool { return imgs[i].Beat < imgs[j].Beat })
	paths := make([]string, 0, len(imgs))
	for _, im := range imgs {
		if im.Path != "" {
			paths = append(paths, im.Path)
		}
	}
	if len(paths) == 0 {
		// No illustrations → no slideshow to render. The audio + text doc still
		// stand on their own.
		return
	}

	res := video.Resolution(topic.Resolution)
	if sub.Resolution != "" {
		res = video.Resolution(sub.Resolution)
	}
	if res == "" {
		res = video.Resolution1080p
	}
	vttPath := filepath.Join(jobOutDir, "subtitles.vtt")
	outPath := filepath.Join(jobOutDir, "video.mp4")
	logger := deps.Log.With("job", jobID)

	deps.Queue.Add(context.Background(), func(runCtx context.Context) {
		defer func() {
			if v := recover(); v != nil {
				logger.Error("audiobook video render panic", "panic", v)
			}
		}()
		logger.Info("audiobook video render starting", "images", len(paths), "resolution", string(res))
		if err := video.RenderAudioBookVideo(outPath, audioPath, vttPath, paths, res); err != nil {
			logger.Warn("audiobook video render failed", "err", err)
			return
		}
		deps.Jobs.Update(jobID, func(j *server.Job) {
			j.VideoPath = outPath
			j.HasVideo = true
		})
		if deps.Uploader.Enabled() {
			key := deps.Uploader.Key(jobID + "-video.mp4")
			if err := deps.Uploader.Upload(runCtx, outPath, key); err != nil {
				logger.Warn("audiobook video upload failed", "key", key, "err", err)
				return
			}
			if deps.Discussions != nil && deps.DiscussionID != "" {
				if err := deps.Discussions.SetVideoKey(runCtx, deps.DiscussionID, key); err != nil {
					logger.Warn("audiobook video key persist failed", "err", err)
				}
			}
		}
		logger.Info("audiobook video ready", "path", outPath)
		notifyAudioBookVideoReady(runCtx, deps, logger)
	})
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
		Kind:         server.PushKindPodcastReady,
		DiscussionID: d.ID,
		Title:        "Video ready",
		Body:         pushDiscussionTitle(d, "Your audiobook video is ready to watch."),
		URL:          server.DiscussionDeepLink(server.FrontendBaseURL(deps.Env), d.ID),
	}, log)
}
