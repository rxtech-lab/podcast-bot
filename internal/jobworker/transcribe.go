package jobworker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sirily11/debate-bot/internal/mq"
	"github.com/sirily11/debate-bot/internal/server"
)

// transcribeRunner handles TaskAudioTranscribe: speech-to-text of an
// uploaded-audio discussion. Idempotency lives in RunAudioTranscribeTask (a
// stored transcript short-circuits a redelivery), so no DB claim is needed.
func (w *Worker) transcribeRunner() runner {
	decode := func(t mq.Task) (server.AudioTranscribePayload, error) {
		var p server.AudioTranscribePayload
		if err := json.Unmarshal(t.Payload, &p); err != nil {
			return p, mq.Permanent(fmt.Errorf("decode transcribe payload: %w", err))
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
			return w.d.Srv.RunAudioTranscribeTask(ctx, p)
		},
		retrying: func(ctx context.Context, t mq.Task, err error, delay time.Duration) {
			p, derr := decode(t)
			if derr != nil {
				return
			}
			w.d.Srv.AudioTranscribeRetrying(p, t.Attempt, delay)
		},
		terminal: func(ctx context.Context, t mq.Task, err error) {
			p, derr := decode(t)
			if derr != nil {
				return
			}
			w.d.Srv.FailAudioTranscribeTask(p, err)
		},
	}
}
