package videojob

import (
	"context"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/server"
	"github.com/sirily11/debate-bot/internal/summarizer"
)

// startSummaryGeneration kicks off — in the background — the agent that writes
// the podcast's Markdown summary document once generation has finished. It is
// fire-and-forget: the run's finalisation (stitch/upload) continues immediately,
// and the client is notified via a summary_ready event when the summary lands.
//
// Billing mirrors planning: the creator's balance is reserved against an
// estimate before the agent runs and settled to actual usage on completion, so
// the summary's LLM cost counts toward the discussion's final point total.
func startSummaryGeneration(deps Deps, jobID string, topic *config.DebateTopic, lines []agent.TranscriptLine) {
	if deps.Discussions == nil || deps.DiscussionID == "" || deps.Env == nil {
		return
	}
	if topic != nil && topic.Type == config.ContentTypeAudioBook {
		return
	}
	summaryLines := toSummaryLines(lines)
	if len(summaryLines) == 0 {
		return
	}
	title, language := "", ""
	if topic != nil {
		title = topic.Title
		language = topic.Language
	}
	_, err := server.StartSummaryGeneration(context.Background(), server.SummaryGenerationDeps{
		Env:         deps.Env,
		Bus:         deps.Bus,
		Discussions: deps.Discussions,
		Points:      deps.Points,
		APNS:        deps.APNS,
		Log:         deps.Log,
		MQ:          deps.MQ,
	}, server.SummaryGenerationInput{
		DiscussionID: deps.DiscussionID,
		JobID:        jobID,
		Title:        title,
		Language:     language,
		Lines:        summaryLines,
	})
	if err != nil && deps.Log != nil {
		deps.Log.Warn("summary start failed", "job", jobID, "discussion_id", deps.DiscussionID, "err", err)
	}
}

// toSummaryLines projects the orchestrator transcript into the summarizer's
// minimal line shape, dropping empty turns.
func toSummaryLines(lines []agent.TranscriptLine) []summarizer.Line {
	out := make([]summarizer.Line, 0, len(lines))
	for _, l := range lines {
		out = append(out, summarizer.Line{
			Speaker: l.Speaker,
			Role:    string(l.Role),
			Text:    l.Text,
		})
	}
	return out
}
