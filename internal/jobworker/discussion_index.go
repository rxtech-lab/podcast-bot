package jobworker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sirily11/debate-bot/internal/mq"
	"github.com/sirily11/debate-bot/internal/server"
)

// discussionIndexRunner handles TaskDiscussionIndex: one queued chunk+embed
// pass over a finished podcast's transcript and sources. Attempts are cheap
// and idempotent (ReplaceChunks swaps atomically), so there is no claim row —
// a duplicate delivery just re-embeds and overwrites the same chunks.
func (w *Worker) discussionIndexRunner() runner {
	decode := func(t mq.Task) (server.DiscussionIndexPayload, error) {
		var p server.DiscussionIndexPayload
		if err := json.Unmarshal(t.Payload, &p); err != nil {
			return p, mq.Permanent(fmt.Errorf("decode index payload: %w", err))
		}
		if p.DiscussionID == "" {
			p.DiscussionID = t.Key
		}
		return p, nil
	}
	return runner{
		run: func(ctx context.Context, t mq.Task) error {
			p, err := decode(t)
			if err != nil {
				return err
			}
			return w.d.Srv.RunDiscussionIndexTask(ctx, p)
		},
		terminal: func(_ context.Context, t mq.Task, err error) {
			p, derr := decode(t)
			if derr == nil {
				w.d.Srv.FailDiscussionIndexTask(p, err)
			}
		},
	}
}
