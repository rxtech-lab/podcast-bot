package jobworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sirily11/debate-bot/internal/mq"
	"github.com/sirily11/debate-bot/internal/server"
	"github.com/sirily11/debate-bot/internal/videojob"
)

// podcastStaleClaim is how old a "running" claim must be before a
// redelivered attempt may take the job over — the consumer that held it is
// presumed dead. Sized to the longest plausible render (matches the
// generation queue's broker-side consumer timeout).
const podcastStaleClaim = 3 * time.Hour

// videojobDeps builds the per-task videojob dependency set.
func (w *Worker) videojobDeps(discussionID string) videojob.Deps {
	return videojob.Deps{
		Env:          w.d.Env,
		MCPCfg:       w.d.MCPCfg,
		Bus:          w.d.Bus,
		Jobs:         w.d.Jobs,
		Discussions:  w.d.Discussions,
		Points:       w.d.Points,
		APNS:         w.d.APNS,
		MQ:           w.d.MQ,
		Log:          w.d.Log,
		Uploader:     w.d.Uploader,
		DiscussionID: discussionID,
	}
}

// podcastRunner handles TaskPodcastGenerate: one queued render attempt for
// any content type (debate, discussion, audio-book, series).
func (w *Worker) podcastRunner() runner {
	decode := func(t mq.Task) (videojob.PodcastGeneratePayload, error) {
		var p videojob.PodcastGeneratePayload
		if err := json.Unmarshal(t.Payload, &p); err != nil {
			return p, mq.Permanent(fmt.Errorf("decode podcast payload: %w", err))
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
			if !w.d.Jobs.ClaimRun(p.JobID, t.Attempt, podcastStaleClaim) {
				w.d.Log.Info("podcast generation delivery already handled; skipping",
					"job", p.JobID, "attempt", t.Attempt)
				return nil
			}
			if err := videojob.RunFromTask(ctx, w.videojobDeps(p.DiscussionID), p); err != nil {
				return err
			}
			// The podcast just became ready: vectorize its transcript + sources
			// for semantic search / Q&A. Fire-and-forget — StartDiscussionIndexing
			// skips unless content actually changed, and the precheck backfill
			// catches anything missed here.
			if w.d.Srv != nil && p.DiscussionID != "" {
				if err := w.d.Srv.StartDiscussionIndexing(ctx, p.DiscussionID); err != nil && !errors.Is(err, server.ErrIndexingNotConfigured) {
					w.d.Log.Warn("post-generation index enqueue failed", "discussion_id", p.DiscussionID, "err", err)
				}
			}
			return nil
		},
		retrying: func(ctx context.Context, t mq.Task, err error, delay time.Duration) {
			p, derr := decode(t)
			if derr != nil {
				return
			}
			videojob.MarkRetryScheduled(w.videojobDeps(p.DiscussionID), p.JobID, t.Attempt, delay, err)
		},
		terminal: func(ctx context.Context, t mq.Task, err error) {
			p, _ := decode(t)
			videojob.FailTerminal(w.videojobDeps(p.DiscussionID), p.JobID, err)
		},
	}
}
