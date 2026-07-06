package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/sirily11/debate-bot/internal/config"
	contentcreator "github.com/sirily11/debate-bot/internal/content_creator"
	"github.com/sirily11/debate-bot/internal/eventbus"
	"github.com/sirily11/debate-bot/internal/llm"
	"github.com/sirily11/debate-bot/internal/mq"
	"github.com/sirily11/debate-bot/internal/summarizer"
)

const summaryBackgroundTimeout = 10 * time.Minute

var (
	ErrSummaryNotConfigured = errors.New("summary generation is not configured")
	ErrSummaryNoTranscript  = errors.New("summary generation requires a transcript")
)

type SummaryGenerationDeps struct {
	Env         *config.Env
	Bus         *eventbus.Bus
	Discussions *DiscussionStore
	Points      *PointsStore
	APNS        *APNSClient
	Log         *slog.Logger
	// MQ carries the queued generation attempt (with retry); Start…
	// functions publish to it instead of spawning goroutines.
	MQ mq.Client
}

// SummaryTaskPayload is the wire payload of a queued summary or mindmap
// generation. Identifiers only — the consumer rebuilds the transcript input
// from the database (it is persisted before generation starts).
type SummaryTaskPayload struct {
	DiscussionID string `json:"discussion_id"`
	JobID        string `json:"job_id,omitempty"`
}

// publishSummaryTask enqueues one summary-family generation task after
// BeginSummary has marked the document generating. On a publish failure the
// document is failed immediately so the client isn't left on a spinner that
// no consumer will ever resolve.
func publishSummaryTask(ctx context.Context, deps SummaryGenerationDeps, taskType mq.TaskType, docType string, input SummaryGenerationInput) error {
	if deps.MQ == nil {
		_ = deps.Discussions.FailSummary(ctx, input.DiscussionID, docType, "generation queue is not configured")
		publishSummaryDocEvent(deps, input.JobID, input.DiscussionID, docType, string(SummaryFailed))
		return ErrSummaryNotConfigured
	}
	task, err := mq.NewTask(taskType, input.DiscussionID, SummaryTaskPayload{
		DiscussionID: input.DiscussionID,
		JobID:        input.JobID,
	})
	if err == nil {
		err = deps.MQ.Publish(ctx, mq.QueueDocs, task)
	}
	if err != nil {
		_ = deps.Discussions.FailSummary(ctx, input.DiscussionID, docType, "failed to enqueue generation")
		publishSummaryDocEvent(deps, input.JobID, input.DiscussionID, docType, string(SummaryFailed))
		return err
	}
	return nil
}

// publishSummaryDocEvent routes to the doc type's event helper.
func publishSummaryDocEvent(deps SummaryGenerationDeps, jobID, discussionID, docType, status string) {
	if docType == SummaryDocTypeMindmap {
		publishMindmapEvent(deps, jobID, discussionID, status)
		return
	}
	publishSummaryEvent(deps, jobID, discussionID, docType, status)
}

// FailSummaryGenerationTask records the terminal failure of a queued
// summary-family generation: the document flips to failed and clients are
// notified. Non-terminal attempts deliberately leave the document
// `generating` so the pending state survives the backoff window.
func FailSummaryGenerationTask(deps SummaryGenerationDeps, discussionID, jobID, docType string, cause error) {
	ctx := context.Background()
	msg := "generation failed"
	if cause != nil {
		msg = cause.Error()
	}
	_ = deps.Discussions.FailSummary(ctx, discussionID, docType, msg)
	publishSummaryDocEvent(deps, jobID, discussionID, docType, string(SummaryFailed))
}

type SummaryGenerationInput struct {
	DiscussionID string
	JobID        string
	Title        string
	Topic        string
	Language     string
	Lines        []summarizer.Line
}

func SummaryGenerationInputFromDiscussion(d *Discussion) SummaryGenerationInput {
	if d == nil {
		return SummaryGenerationInput{}
	}
	lines := make([]summarizer.Line, 0, len(d.Lines))
	for _, l := range d.Lines {
		text := strings.TrimSpace(l.Text)
		if text == "" {
			continue
		}
		role := strings.TrimSpace(l.Role)
		if role == "" && l.IsUser {
			role = "user"
		}
		lines = append(lines, summarizer.Line{
			Speaker: l.Speaker,
			Role:    role,
			Text:    text,
		})
	}
	return SummaryGenerationInput{
		DiscussionID: d.ID,
		JobID:        d.JobID,
		Title:        d.Title,
		Topic:        d.Topic,
		Language:     d.Language,
		Lines:        lines,
	}
}

// StartSummaryGeneration marks the summary as generating synchronously, then
// runs the expensive agent loop in the background. It is shared by automatic
// post-podcast generation and the manual API retry path.
func StartSummaryGeneration(ctx context.Context, deps SummaryGenerationDeps, input SummaryGenerationInput) (*SummaryMeta, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if deps.Discussions == nil || deps.Env == nil || strings.TrimSpace(input.DiscussionID) == "" {
		return nil, ErrSummaryNotConfigured
	}
	if len(input.Lines) == 0 {
		return nil, ErrSummaryNoTranscript
	}
	docType := SummaryDocTypeSummary
	if status, exists, err := deps.Discussions.SummaryStatusFor(ctx, input.DiscussionID, docType); err != nil {
		return nil, err
	} else if exists && (status == SummaryReadyState || status == SummaryGenerating) {
		return deps.Discussions.SummaryMetaFor(ctx, input.DiscussionID, docType)
	}

	owner, err := deps.Discussions.OwnerOf(ctx, input.DiscussionID)
	if err != nil {
		return nil, err
	}
	if owner == "" {
		return nil, nil
	}

	gen := summarizer.New(deps.Env)
	model := gen.Model()
	if err := deps.Discussions.BeginSummary(ctx, input.DiscussionID, docType, model); err != nil {
		return nil, err
	}
	meta, err := deps.Discussions.SummaryMetaFor(ctx, input.DiscussionID, docType)
	if err != nil {
		return nil, err
	}
	publishSummaryEvent(deps, input.JobID, input.DiscussionID, docType, string(SummaryGenerating))
	if err := publishSummaryTask(ctx, deps, mq.TaskSummary, docType, input); err != nil {
		return nil, err
	}
	return meta, nil
}

// RunSummaryGenerationTask executes one queued summary attempt. Points are
// reserved and settled entirely inside the attempt (a failed attempt already
// refunds to zero), so retries need no cross-attempt bookkeeping. A non-nil
// return is the attempt's failure: the document stays `generating` and the
// dispatch layer decides retry vs terminal (FailSummaryGenerationTask).
func RunSummaryGenerationTask(deps SummaryGenerationDeps, input SummaryGenerationInput, owner string) error {
	ctx, cancel := context.WithTimeout(context.Background(), summaryBackgroundTimeout)
	defer cancel()
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}
	logger := log.With("job", input.JobID, "discussion_id", input.DiscussionID, "task", "summary")
	docType := SummaryDocTypeSummary
	model := summarizer.New(deps.Env).Model()

	var reserved, reserveLedgerID int64
	if deps.Points != nil {
		r, ledgerID, ok, rerr := deps.Points.ReserveSummary(ctx, deps.Env, owner, input.DiscussionID)
		if rerr != nil {
			logger.Warn("summary reserve failed", "err", rerr)
		}
		if !ok {
			return mq.Permanent(errors.New("insufficient points for summary"))
		}
		reserved, reserveLedgerID = r, ledgerID
	}

	meter := &summaryUsageMeter{}
	res, err := summarizer.New(deps.Env).WithUsageRecorder(meter.record).Generate(ctx, summarizer.Input{
		Title:    input.Title,
		Topic:    input.Topic,
		Language: input.Language,
		Lines:    input.Lines,
	})
	if err != nil {
		if deps.Points != nil {
			_ = deps.Points.SettleSummary(ctx, owner, input.DiscussionID, reserveLedgerID, reserved, 0, PointsUsageDetail{})
		}
		return err
	}

	sum := meter.snapshot()
	if deps.Points != nil {
		actual := deps.Points.SummaryPoints(deps.Env, sum.CostUSD)
		_ = deps.Points.SettleSummary(ctx, owner, input.DiscussionID, reserveLedgerID, reserved, actual, PointsUsageDetail{
			PromptTokens:     sum.PromptTokens,
			CompletionTokens: sum.CompletionTokens,
			TotalTokens:      sum.TotalTokens,
			LLMCostUSD:       sum.CostUSD,
			LLMCostKnown:     sum.CostKnown,
			CostUSD:          sum.CostUSD,
		})
	}

	if err := deps.Discussions.SaveSummary(ctx, input.DiscussionID, docType, res.Markdown, model, SummaryUsage{
		PromptTokens:     sum.PromptTokens,
		CompletionTokens: sum.CompletionTokens,
		TotalTokens:      sum.TotalTokens,
		LLMCostUSD:       sum.CostUSD,
	}); err != nil {
		return fmt.Errorf("store summary: %w", err)
	}

	logger.Info("summary ready",
		"model", model,
		"prompt_tokens", sum.PromptTokens,
		"completion_tokens", sum.CompletionTokens,
		"total_tokens", sum.TotalTokens,
		"cost_usd", sum.CostUSD)
	notifySummaryReady(ctx, deps, input.DiscussionID)
	publishSummaryEvent(deps, input.JobID, input.DiscussionID, docType, string(SummaryReadyState))
	return nil
}

func notifySummaryReady(ctx context.Context, deps SummaryGenerationDeps, discussionID string) {
	if deps.APNS == nil || deps.Discussions == nil {
		return
	}
	d, err := deps.Discussions.GetForNotification(ctx, discussionID)
	if err != nil || d == nil {
		if err != nil && deps.Log != nil {
			deps.Log.Warn("summary ready push discussion lookup failed", "discussion_id", discussionID, "err", err)
		}
		return
	}
	SendPushNotification(ctx, deps.Discussions, deps.APNS, d.OwnerUserID, PushNotification{
		Kind:         PushKindSummaryReady,
		DiscussionID: d.ID,
		Title:        "Summary ready",
		Body:         pushDiscussionTitle(d, "Your podcast summary is ready."),
		URL:          DiscussionDeepLink(FrontendBaseURL(deps.Env), d.ID),
	}, deps.Log)
}

func publishSummaryEvent(deps SummaryGenerationDeps, jobID, discussionID, docType, status string) {
	if deps.Bus == nil {
		return
	}
	deps.Bus.Publish(contentcreator.StampChannelID(contentcreator.SummaryReadyMsg{
		DocType: docType,
		Status:  status,
	}, jobID))
	PublishDiscussionResourceUpdated(deps.Bus, deps.Env, jobID, discussionID, "Summary "+status, "summary")
}

type summaryUsageMeter struct {
	mu  sync.Mutex
	sum llm.UsageSummary
}

func (m *summaryUsageMeter) record(u llm.Usage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sum.PromptTokens += u.PromptTokens
	m.sum.CompletionTokens += u.CompletionTokens
	m.sum.TotalTokens += u.TotalTokens
	if u.CostKnown {
		m.sum.CostUSD += u.CostUSD
		m.sum.CostKnown = true
	}
}

func (m *summaryUsageMeter) snapshot() llm.UsageSummary {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sum
}
