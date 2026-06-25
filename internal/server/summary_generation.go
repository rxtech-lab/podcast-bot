package server

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/sirily11/debate-bot/internal/config"
	contentcreator "github.com/sirily11/debate-bot/internal/content_creator"
	"github.com/sirily11/debate-bot/internal/eventbus"
	"github.com/sirily11/debate-bot/internal/llm"
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
	publishSummaryEvent(deps, input.JobID, docType, string(SummaryGenerating))
	go runSummaryGeneration(deps, input, owner, model)
	return meta, nil
}

func runSummaryGeneration(deps SummaryGenerationDeps, input SummaryGenerationInput, owner, model string) {
	ctx, cancel := context.WithTimeout(context.Background(), summaryBackgroundTimeout)
	defer cancel()
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}
	logger := log.With("job", input.JobID, "discussion_id", input.DiscussionID, "task", "summary")
	docType := SummaryDocTypeSummary

	var reserved, reserveLedgerID int64
	if deps.Points != nil {
		r, ledgerID, ok, rerr := deps.Points.ReserveSummary(ctx, deps.Env, owner, input.DiscussionID)
		if rerr != nil {
			logger.Warn("summary reserve failed", "err", rerr)
		}
		if !ok {
			logger.Info("summary skipped: insufficient points")
			_ = deps.Discussions.FailSummary(ctx, input.DiscussionID, docType, "insufficient points for summary")
			publishSummaryEvent(deps, input.JobID, docType, string(SummaryFailed))
			return
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
		logger.Warn("summary generation failed", "err", err)
		_ = deps.Discussions.FailSummary(ctx, input.DiscussionID, docType, err.Error())
		publishSummaryEvent(deps, input.JobID, docType, string(SummaryFailed))
		return
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
		logger.Warn("summary persist failed", "err", err)
		_ = deps.Discussions.FailSummary(ctx, input.DiscussionID, docType, "failed to store summary")
		publishSummaryEvent(deps, input.JobID, docType, string(SummaryFailed))
		return
	}

	logger.Info("summary ready",
		"model", model,
		"prompt_tokens", sum.PromptTokens,
		"completion_tokens", sum.CompletionTokens,
		"total_tokens", sum.TotalTokens,
		"cost_usd", sum.CostUSD)
	notifySummaryReady(ctx, deps, input.DiscussionID)
	publishSummaryEvent(deps, input.JobID, docType, string(SummaryReadyState))
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

func publishSummaryEvent(deps SummaryGenerationDeps, jobID, docType, status string) {
	if deps.Bus == nil {
		return
	}
	deps.Bus.Publish(contentcreator.StampChannelID(contentcreator.SummaryReadyMsg{
		DocType: docType,
		Status:  status,
	}, jobID))
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
