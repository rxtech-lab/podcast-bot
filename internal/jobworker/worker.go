// Package jobworker is the dispatch layer between the mq transport and the
// generation code: it routes queued tasks to their run functions and owns
// the retry policy (max 3 attempts with exponential backoff, terminal
// failure paths per task type). The mq consumer always acks; everything
// retry-shaped happens here.
package jobworker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/eventbus"
	"github.com/sirily11/debate-bot/internal/mq"
	"github.com/sirily11/debate-bot/internal/server"
	"github.com/sirily11/debate-bot/internal/storage"
)

// Prefetch per queue: the per-pod concurrency bound for that queue's
// consumers. Generation uses --max-concurrency (encoder-bound, matches the
// old in-memory pool); docs and planning are lighter LLM/IO work.
const (
	docsPrefetch     = 4
	planningPrefetch = 8
)

// Deps carries everything the task handlers need. Fields used only by
// not-yet-migrated task types may be nil until their migration lands.
type Deps struct {
	Env         *config.Env
	MCPCfg      *config.MCPConfig
	Bus         *eventbus.Bus
	Jobs        *server.JobRegistry
	Discussions *server.DiscussionStore
	Points      *server.PointsStore
	Planning    *server.PlanningStore
	Srv         *server.Server
	APNS        *server.APNSClient
	Uploader    *storage.Uploader
	Log         *slog.Logger
	MQ          mq.Client

	// MaxConcurrency bounds concurrent heavy renders per pod (the
	// generation queue's prefetch), mirroring the old in-memory pool size.
	MaxConcurrency int
}

// runner binds a task type to its attempt logic and terminal-failure path.
type runner struct {
	// run executes one attempt. A nil error completes the task; an error
	// wrapped with mq.Permanent skips remaining attempts.
	run func(ctx context.Context, t mq.Task) error
	// retrying, when set, runs after a failed non-final attempt and before
	// the retry is scheduled: reset the domain object so the next
	// attempt's claim succeeds (status back to pending, progress note).
	retrying func(ctx context.Context, t mq.Task, err error, delay time.Duration)
	// terminal runs after the final failed attempt (or a permanent error):
	// mark the domain object failed, emit events, settle points.
	terminal func(ctx context.Context, t mq.Task, err error)
}

// Worker registers the queue consumers and dispatches tasks.
type Worker struct {
	d Deps
	// baseCtx parents every handler context so SIGTERM cancels in-flight
	// work during graceful shutdown.
	baseCtx context.Context
	runners map[string]map[mq.TaskType]runner
}

// New builds the worker and its task-type routing tables.
func New(ctx context.Context, d Deps) *Worker {
	w := &Worker{
		d:       d,
		baseCtx: ctx,
		runners: map[string]map[mq.TaskType]runner{
			mq.QueueGeneration: {},
			mq.QueueDocs:       {},
			mq.QueuePlanning:   {},
		},
	}
	w.registerRunners()
	return w
}

// RegisterAll starts consuming every queue this worker has runners for.
func (w *Worker) RegisterAll() error {
	prefetch := map[string]int{
		mq.QueueGeneration: w.d.MaxConcurrency,
		mq.QueueDocs:       docsPrefetch,
		mq.QueuePlanning:   planningPrefetch,
	}
	for queue, runners := range w.runners {
		if len(runners) == 0 {
			continue
		}
		if err := w.d.MQ.RegisterHandler(queue, prefetch[queue], w.dispatch(queue)); err != nil {
			return fmt.Errorf("jobworker: register %s: %w", queue, err)
		}
	}
	return nil
}

// dispatch routes one delivery to its runner and applies the retry policy:
// failed attempts below mq.MaxAttempts are rescheduled with backoff; the
// last attempt (or a permanent error, or a failed reschedule) runs the
// task's terminal path.
func (w *Worker) dispatch(queue string) mq.Handler {
	return func(_ context.Context, t mq.Task) error {
		r, ok := w.runners[queue][t.Type]
		if !ok {
			w.d.Log.Warn("jobworker: no runner for task type; dropping", "queue", queue, "type", t.Type, "key", t.Key)
			return nil
		}
		ctx := w.baseCtx
		err := w.runAttempt(ctx, r, t)
		if err == nil {
			return nil
		}
		if t.Attempt < mq.MaxAttempts && !mq.IsPermanent(err) {
			next := t
			next.Attempt++
			delay := mq.ComputeRetryDelay(next.Attempt)
			w.d.Log.Warn("jobworker: attempt failed, scheduling retry",
				"type", t.Type, "key", t.Key, "attempt", t.Attempt, "max", mq.MaxAttempts, "delay", delay, "err", err)
			if r.retrying != nil {
				r.retrying(ctx, t, err, delay)
			}
			// Publish outside the handler context: during graceful shutdown
			// the base context is already canceled — precisely the moment an
			// interrupted attempt must still get its retry scheduled.
			if perr := w.d.MQ.PublishRetry(context.WithoutCancel(ctx), queue, next, delay); perr != nil {
				w.d.Log.Error("jobworker: retry publish failed, failing terminally",
					"type", t.Type, "key", t.Key, "err", perr)
				w.runTerminal(ctx, r, t, err)
				return errors.Join(err, perr)
			}
			return err
		}
		w.d.Log.Error("jobworker: terminal failure",
			"type", t.Type, "key", t.Key, "attempt", t.Attempt, "err", err)
		w.runTerminal(ctx, r, t, err)
		return err
	}
}

// runAttempt executes one attempt, converting a panicking run into an
// ordinary failed attempt so the retry policy still applies.
func (w *Worker) runAttempt(ctx context.Context, r runner, t mq.Task) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("%s task panic: %v", t.Type, rec)
		}
	}()
	return r.run(ctx, t)
}

func (w *Worker) runTerminal(ctx context.Context, r runner, t mq.Task, err error) {
	if r.terminal == nil {
		return
	}
	defer func() {
		if rec := recover(); rec != nil {
			w.d.Log.Error("jobworker: terminal handler panic", "type", t.Type, "key", t.Key, "panic", rec)
		}
	}()
	r.terminal(ctx, t, err)
}

// registerRunners wires each migrated task type into its queue's routing
// table. Task types still on their legacy in-process paths simply don't
// appear here yet.
func (w *Worker) registerRunners() {
	w.runners[mq.QueueGeneration][mq.TaskPodcastGenerate] = w.podcastRunner()
	w.runners[mq.QueueGeneration][mq.TaskVideoRender] = w.videoRenderRunner()
	w.runners[mq.QueueDocs][mq.TaskSummary] = w.summaryDocRunner(server.SummaryDocTypeSummary, server.RunSummaryGenerationTask)
	w.runners[mq.QueueDocs][mq.TaskMindmap] = w.summaryDocRunner(server.SummaryDocTypeMindmap, server.RunMindmapGenerationTask)
	w.runners[mq.QueueDocs][mq.TaskPPTExport] = w.summaryExportRunner()
	w.runners[mq.QueueDocs][mq.TaskPDFExport] = w.summaryExportRunner()
	w.runners[mq.QueuePlanning][mq.TaskPlanningTurn] = w.planningRunner()
	w.runners[mq.QueuePlanning][mq.TaskAudioTranscribe] = w.transcribeRunner()
}
