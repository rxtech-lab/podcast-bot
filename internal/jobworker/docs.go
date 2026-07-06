package jobworker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sirily11/debate-bot/internal/mq"
	"github.com/sirily11/debate-bot/internal/server"
)

// docsStaleClaim is the crash-takeover threshold for the docs queue. Every
// doc task bounds itself with a ~10-minute internal timeout, so a claim
// older than this belongs to a dead consumer.
const docsStaleClaim = 15 * time.Minute

func (w *Worker) summaryGenerationDeps() server.SummaryGenerationDeps {
	return server.SummaryGenerationDeps{
		Env:         w.d.Env,
		Bus:         w.d.Bus,
		Discussions: w.d.Discussions,
		Points:      w.d.Points,
		APNS:        w.d.APNS,
		Log:         w.d.Log,
		MQ:          w.d.MQ,
	}
}

func decodeSummaryTask(t mq.Task) (server.SummaryTaskPayload, error) {
	var p server.SummaryTaskPayload
	if err := json.Unmarshal(t.Payload, &p); err != nil {
		return p, mq.Permanent(fmt.Errorf("decode summary task payload: %w", err))
	}
	if p.DiscussionID == "" {
		p.DiscussionID = t.Key
	}
	return p, nil
}

// summaryExportRunner handles TaskPPTExport / TaskPDFExport: claim the
// export doc row, then render + upload via the server's export task.
func (w *Worker) summaryExportRunner() runner {
	decode := func(t mq.Task) (server.SummaryExportPayload, string, error) {
		var p server.SummaryExportPayload
		if err := json.Unmarshal(t.Payload, &p); err != nil {
			return p, "", mq.Permanent(fmt.Errorf("decode export payload: %w", err))
		}
		if p.DiscussionID == "" {
			p.DiscussionID = t.Key
		}
		docType, err := server.SummaryExportDocTypeFor(p.Kind)
		if err != nil {
			return p, "", mq.Permanent(err)
		}
		return p, docType, nil
	}
	return runner{
		run: func(ctx context.Context, t mq.Task) error {
			p, docType, err := decode(t)
			if err != nil {
				return err
			}
			claimed, err := w.d.Discussions.ClaimSummaryRun(ctx, p.DiscussionID, docType, t.Attempt, docsStaleClaim)
			if err != nil {
				return fmt.Errorf("claim export run: %w", err)
			}
			if !claimed {
				w.d.Log.Info("export delivery already handled; skipping",
					"doc_type", docType, "discussion", p.DiscussionID, "attempt", t.Attempt)
				return nil
			}
			return w.d.Srv.RunSummaryExportTask(ctx, p)
		},
		terminal: func(ctx context.Context, t mq.Task, err error) {
			p, _, derr := decode(t)
			if derr != nil {
				return
			}
			w.d.Srv.FailSummaryExportTask(p, err)
		},
	}
}

// summaryDocRunner is the shared runner shape for the summary-family doc
// types (summary, mindmap): claim the doc row, rebuild the transcript input
// from the database, run the generator; terminal failures flip the doc to
// failed while non-terminal attempts leave it generating through the
// backoff window.
func (w *Worker) summaryDocRunner(docType string,
	runTask func(deps server.SummaryGenerationDeps, input server.SummaryGenerationInput, owner string) error,
) runner {
	return runner{
		run: func(ctx context.Context, t mq.Task) error {
			p, err := decodeSummaryTask(t)
			if err != nil {
				return err
			}
			claimed, err := w.d.Discussions.ClaimSummaryRun(ctx, p.DiscussionID, docType, t.Attempt, docsStaleClaim)
			if err != nil {
				return fmt.Errorf("claim %s run: %w", docType, err)
			}
			if !claimed {
				w.d.Log.Info("doc generation delivery already handled; skipping",
					"doc_type", docType, "discussion", p.DiscussionID, "attempt", t.Attempt)
				return nil
			}
			d, err := w.d.Discussions.DiscussionWithTranscript(ctx, p.DiscussionID)
			if err != nil {
				return fmt.Errorf("load discussion: %w", err)
			}
			if d == nil {
				return mq.Permanent(fmt.Errorf("discussion %s not found", p.DiscussionID))
			}
			input := server.SummaryGenerationInputFromDiscussion(d)
			if input.JobID == "" {
				input.JobID = p.JobID
			}
			if len(input.Lines) == 0 {
				return mq.Permanent(fmt.Errorf("discussion %s has no transcript", p.DiscussionID))
			}
			owner, err := w.d.Discussions.OwnerOf(ctx, p.DiscussionID)
			if err != nil {
				return fmt.Errorf("resolve owner: %w", err)
			}
			if owner == "" {
				return mq.Permanent(fmt.Errorf("discussion %s has no owner", p.DiscussionID))
			}
			return runTask(w.summaryGenerationDeps(), input, owner)
		},
		terminal: func(ctx context.Context, t mq.Task, err error) {
			p, _ := decodeSummaryTask(t)
			server.FailSummaryGenerationTask(w.summaryGenerationDeps(), p.DiscussionID, p.JobID, docType, err)
		},
	}
}
