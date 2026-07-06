package jobworker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sirily11/debate-bot/internal/mq"
	"github.com/sirily11/debate-bot/internal/server"
)

// videoRenderStaleClaim mirrors podcastStaleClaim: an audiobook video render
// (Ken Burns frame piping over an hour-long narration) can legitimately run
// for a long time before its claim may be presumed dead.
const videoRenderStaleClaim = 3 * time.Hour

// videoRenderRunner handles TaskVideoRender: the audiobook slideshow-video
// post-pass, queued both automatically after a finished audiobook podcast
// and manually via POST /api/discussions/{id}/video/generate.
func (w *Worker) videoRenderRunner() runner {
	decode := func(t mq.Task) (server.VideoRenderPayload, error) {
		var p server.VideoRenderPayload
		if err := json.Unmarshal(t.Payload, &p); err != nil {
			return p, mq.Permanent(fmt.Errorf("decode video render payload: %w", err))
		}
		if p.JobID == "" {
			p.JobID = t.Key
		}
		return p, nil
	}
	return runner{
		run: func(ctx context.Context, t mq.Task) error {
			p, err := decode(t)
			if err != nil {
				return err
			}
			if !w.d.Jobs.ClaimVideoRender(p.JobID, t.Attempt, videoRenderStaleClaim) {
				w.d.Log.Info("video render delivery already handled; skipping",
					"job", p.JobID, "attempt", t.Attempt)
				return nil
			}
			return w.d.Srv.RunAudioBookVideoRenderTask(ctx, p.JobID, p.DiscussionID)
		},
		retrying: func(ctx context.Context, t mq.Task, err error, delay time.Duration) {
			p, derr := decode(t)
			if derr != nil {
				return
			}
			w.d.Srv.MarkVideoRenderRetryScheduled(p.JobID, t.Attempt, delay)
		},
		terminal: func(ctx context.Context, t mq.Task, err error) {
			p, _ := decode(t)
			w.d.Srv.MarkVideoRenderFailed(p.JobID, err)
		},
	}
}
