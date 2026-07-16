// Package mq provides the durable generation-job queue: a small RabbitMQ
// client (publish, consume, TTL+DLX retry scheduling) plus an in-process
// fallback used when no broker is configured.
//
// Retry model (ported from linda-assistant's audio pipeline): the consumer
// ALWAYS acks, even when the handler fails — an unacked message would be
// redelivered immediately, bypassing both the backoff and the attempt
// counter. The attempt counter travels in the message payload, and the
// dispatch layer (internal/jobworker) republishes failed tasks to a holding
// queue whose per-message TTL dead-letters back into the work queue after
// the backoff delay.
//
// Known limitation shared with the reference implementation: the holding
// queue expires head-first, so a long-TTL message at the head delays
// shorter-TTL messages behind it. Acceptable at this scale.
package mq

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// TaskType identifies the kind of generation work carried by a Task.
type TaskType string

const (
	TaskPodcastGenerate TaskType = "podcast-generate" // all content types via videojob
	TaskVideoRender     TaskType = "video-render"     // audiobook slideshow mp4
	TaskPlanningTurn    TaskType = "planning-turn"
	TaskAudioTranscribe TaskType = "audio-transcribe" // uploaded-audio speech-to-text
	TaskSummary         TaskType = "summary"
	TaskMindmap         TaskType = "mindmap"
	TaskTranslation     TaskType = "translation"
	TaskPPTExport       TaskType = "ppt-export"
	TaskPDFExport       TaskType = "pdf-export"
)

// Work queues, grouped by weight. Prefetch is the per-pod concurrency knob,
// so heavy renders, light doc generation, and latency-sensitive planning
// turns each get their own queue rather than sharing one prefetch budget.
const (
	QueueGeneration = "podcast.generation" // podcast-generate, video-render
	QueueDocs       = "podcast.docs"       // summary, mindmap, ppt/pdf export
	QueuePlanning   = "podcast.planning"   // planning-turn, audio-transcribe
)

// RetryExchange is the shared direct exchange that routes retry messages
// into each queue's holding queue (routing key = physical queue name).
const RetryExchange = "podcast.retry-x"

// Retry policy: attempts are 1-based and capped at MaxAttempts; the delay
// before attempt N is RetryBase * 2^(N-2), capped at RetryMax.
const (
	MaxAttempts = 3
	RetryBase   = 60 * time.Second
	RetryMax    = 600 * time.Second
)

// generationConsumerTimeout raises RabbitMQ's delivery-ack timeout (default
// 30 minutes) on the heavy queue. A podcast render routinely runs for hours;
// without this the broker force-closes the channel mid-run and redelivers,
// double-running the job.
const generationConsumerTimeout = 3 * time.Hour

// prefixedQueueExpiry garbage-collects queues created with a queue prefix
// (E2E runs) once they sit unused, so a persistent broker on a self-hosted
// runner doesn't accumulate per-run queues.
const prefixedQueueExpiry = 30 * time.Minute

// Task is the wire envelope for one unit of generation work.
type Task struct {
	Type       TaskType        `json:"type"`
	Key        string          `json:"key"`     // jobID / discussionID / conversationID
	Attempt    int             `json:"attempt"` // 1-based, carried in the payload
	EnqueuedAt int64           `json:"enqueued_at"`
	Payload    json.RawMessage `json:"payload"`
}

// Handler processes one task. A returned error signals a failed attempt; the
// dispatch layer decides between scheduling a retry and running the task's
// terminal-failure path. Wrap an error with Permanent to skip retries.
type Handler func(ctx context.Context, t Task) error

// Client is the transport seam: an AMQP implementation when RABBITMQ_URL is
// configured, an in-process fallback otherwise. Both provide identical
// publish/retry/consume semantics so the dispatch layer above is shared.
type Client interface {
	// Publish enqueues a task onto a work queue.
	Publish(ctx context.Context, queue string, t Task) error
	// PublishRetry schedules a task to re-enter the work queue after delay.
	PublishRetry(ctx context.Context, queue string, t Task, delay time.Duration) error
	// RegisterHandler starts consuming queue with up to prefetch tasks
	// in flight concurrently. Handler errors are the caller's concern
	// (dispatch layer); the message is acked regardless.
	RegisterHandler(queue string, prefetch int, h Handler) error
	// Connected reports broker connectivity for health checks.
	Connected(ctx context.Context) error
	// Close stops consuming, waits briefly for in-flight handlers, and
	// releases the connection.
	Close() error
}

// ComputeRetryDelay returns the backoff before the given (1-based) next
// attempt: RetryBase for attempt 2, doubling per attempt, capped at RetryMax.
func ComputeRetryDelay(nextAttempt int) time.Duration {
	return ComputeRetryDelayWith(RetryBase, RetryMax, nextAttempt)
}

// ComputeRetryDelayWith is ComputeRetryDelay with injectable bounds (tests).
func ComputeRetryDelayWith(base, max time.Duration, nextAttempt int) time.Duration {
	exp := nextAttempt - 2
	if exp < 0 {
		exp = 0
	}
	d := base << uint(exp)
	if d > max || d <= 0 {
		return max
	}
	return d
}

// permanentError marks a failure that must not be retried.
type permanentError struct{ err error }

func (p permanentError) Error() string { return p.err.Error() }
func (p permanentError) Unwrap() error { return p.err }

// Permanent wraps err so the dispatch layer skips remaining attempts and
// goes straight to the terminal-failure path.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err: err}
}

// IsPermanent reports whether err (or anything it wraps) came from Permanent.
func IsPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}

// NewTask builds a first-attempt task envelope, JSON-encoding payload.
func NewTask(taskType TaskType, key string, payload any) (Task, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Task{}, err
	}
	return Task{
		Type:       taskType,
		Key:        key,
		Attempt:    1,
		EnqueuedAt: time.Now().UnixMilli(),
		Payload:    raw,
	}, nil
}
