package jobworker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sirily11/debate-bot/internal/mq"
	"github.com/sirily11/debate-bot/internal/server"
)

// qaRunner handles TaskQATurn. Like planningRunner, the Redis Active record
// (checked inside RunQATurnTask against the payload's RunID) is the
// distributed claim.
func (w *Worker) qaRunner() runner {
	decode := func(t mq.Task) (server.QATurnPayload, error) {
		var p server.QATurnPayload
		if err := json.Unmarshal(t.Payload, &p); err != nil {
			return p, mq.Permanent(fmt.Errorf("decode qa payload: %w", err))
		}
		if p.RunID == "" {
			p.RunID = t.Key
		}
		return p, nil
	}
	return runner{
		run: func(ctx context.Context, t mq.Task) error {
			p, err := decode(t)
			if err != nil {
				return err
			}
			return w.d.Srv.RunQATurnTask(ctx, p)
		},
		retrying: func(ctx context.Context, t mq.Task, err error, delay time.Duration) {
			p, derr := decode(t)
			if derr != nil {
				return
			}
			w.d.Srv.QATurnRetrying(p, t.Attempt, delay)
		},
		terminal: func(ctx context.Context, t mq.Task, err error) {
			p, derr := decode(t)
			if derr != nil {
				return
			}
			w.d.Srv.FailQATurnTask(p, err)
		},
	}
}
