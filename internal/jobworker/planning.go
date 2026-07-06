package jobworker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sirily11/debate-bot/internal/mq"
	"github.com/sirily11/debate-bot/internal/server"
)

// planningRunner handles TaskPlanningTurn. There is no DB claim here: the
// Redis Active record (checked inside RunPlanningTurnTask against the
// payload's RunID) is the distributed claim, and its TTL bounds abandoned
// runs.
func (w *Worker) planningRunner() runner {
	decode := func(t mq.Task) (server.PlanningTurnPayload, error) {
		var p server.PlanningTurnPayload
		if err := json.Unmarshal(t.Payload, &p); err != nil {
			return p, mq.Permanent(fmt.Errorf("decode planning payload: %w", err))
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
			return w.d.Srv.RunPlanningTurnTask(ctx, p)
		},
		retrying: func(ctx context.Context, t mq.Task, err error, delay time.Duration) {
			p, derr := decode(t)
			if derr != nil {
				return
			}
			w.d.Srv.PlanningTurnRetrying(p, t.Attempt, delay)
		},
		terminal: func(ctx context.Context, t mq.Task, err error) {
			p, derr := decode(t)
			if derr != nil {
				return
			}
			w.d.Srv.FailPlanningTurnTask(p, err)
		},
	}
}
