package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	contentcreator "github.com/sirily11/debate-bot/internal/content_creator"
	"github.com/sirily11/debate-bot/internal/mq"
	"github.com/sirily11/debate-bot/internal/summarizer"
)

const mindmapBackgroundTimeout = 10 * time.Minute

// StartMindmapGeneration marks the mindmap as generating synchronously, then
// runs the single-shot generator in the background. It is shared by automatic
// post-podcast generation and the manual API path. Reuses the summary deps,
// input, storage rows (doc_type "mindmap"), and events.
func StartMindmapGeneration(ctx context.Context, deps SummaryGenerationDeps, input SummaryGenerationInput) (*SummaryMeta, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if deps.Discussions == nil || deps.Env == nil || strings.TrimSpace(input.DiscussionID) == "" {
		return nil, ErrSummaryNotConfigured
	}
	if len(input.Lines) == 0 {
		return nil, ErrSummaryNoTranscript
	}
	docType := SummaryDocTypeMindmap
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

	model := summarizer.NewMindmapGenerator(deps.Env).Model()
	if err := deps.Discussions.BeginSummary(ctx, input.DiscussionID, docType, model); err != nil {
		return nil, err
	}
	meta, err := deps.Discussions.SummaryMetaFor(ctx, input.DiscussionID, docType)
	if err != nil {
		return nil, err
	}
	publishMindmapEvent(deps, input.JobID, input.DiscussionID, string(SummaryGenerating))
	if err := publishSummaryTask(ctx, deps, mq.TaskMindmap, docType, input); err != nil {
		return nil, err
	}
	return meta, nil
}

// RunMindmapGenerationTask executes one queued mindmap attempt. Same retry
// contract as RunSummaryGenerationTask: points settle inside the attempt,
// the document stays `generating` on failure, and the dispatch layer owns
// retry vs terminal.
func RunMindmapGenerationTask(deps SummaryGenerationDeps, input SummaryGenerationInput, owner string) error {
	ctx, cancel := context.WithTimeout(context.Background(), mindmapBackgroundTimeout)
	defer cancel()
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}
	logger := log.With("job", input.JobID, "discussion_id", input.DiscussionID, "task", "mindmap")
	docType := SummaryDocTypeMindmap
	model := summarizer.NewMindmapGenerator(deps.Env).Model()

	var reserved, reserveLedgerID int64
	if deps.Points != nil {
		r, ledgerID, ok, rerr := deps.Points.ReserveSummary(ctx, deps.Env, owner, input.DiscussionID)
		if rerr != nil {
			logger.Warn("mindmap reserve failed", "err", rerr)
		}
		if !ok {
			return mq.Permanent(errors.New("insufficient points for mindmap"))
		}
		reserved, reserveLedgerID = r, ledgerID
	}

	meter := &summaryUsageMeter{}
	spec, err := summarizer.NewMindmapGenerator(deps.Env).WithUsageRecorder(meter.record).Generate(ctx, summarizer.Input{
		Title:    input.Title,
		Topic:    input.Topic,
		Language: input.Language,
		Lines:    input.Lines,
	})
	var data []byte
	if err == nil {
		data, err = json.Marshal(spec)
	}
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

	if err := deps.Discussions.SaveSummary(ctx, input.DiscussionID, docType, string(data), model, SummaryUsage{
		PromptTokens:     sum.PromptTokens,
		CompletionTokens: sum.CompletionTokens,
		TotalTokens:      sum.TotalTokens,
		LLMCostUSD:       sum.CostUSD,
	}); err != nil {
		return fmt.Errorf("store mindmap: %w", err)
	}

	logger.Info("mindmap ready",
		"model", model,
		"prompt_tokens", sum.PromptTokens,
		"completion_tokens", sum.CompletionTokens,
		"total_tokens", sum.TotalTokens,
		"cost_usd", sum.CostUSD)
	publishMindmapEvent(deps, input.JobID, input.DiscussionID, string(SummaryReadyState))
	return nil
}

func publishMindmapEvent(deps SummaryGenerationDeps, jobID, discussionID, status string) {
	if deps.Bus == nil {
		return
	}
	deps.Bus.Publish(contentcreator.StampChannelID(contentcreator.SummaryReadyMsg{
		DocType: SummaryDocTypeMindmap,
		Status:  status,
	}, jobID))
	PublishDiscussionResourceUpdated(deps.Bus, deps.Env, jobID, discussionID, "Mindmap "+status, "mindmap")
}
