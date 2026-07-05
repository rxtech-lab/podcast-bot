package videojob

import (
	"context"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/server"
)

// startMindmapGeneration kicks off — in the background — the generator that
// builds the discussion's mindmap node tree once generation has finished.
// Fire-and-forget like startSummaryGeneration; only discussion-type podcasts
// get a mindmap. The client is notified via a summary_ready event with
// doc_type "mindmap" when it lands.
func startMindmapGeneration(deps Deps, jobID string, topic *config.DebateTopic, lines []agent.TranscriptLine) {
	if deps.Discussions == nil || deps.DiscussionID == "" || deps.Env == nil {
		return
	}
	if topic == nil || topic.Type != config.ContentTypeDiscussion {
		return
	}
	summaryLines := toSummaryLines(lines)
	if len(summaryLines) == 0 {
		return
	}
	_, err := server.StartMindmapGeneration(context.Background(), server.SummaryGenerationDeps{
		Env:         deps.Env,
		Bus:         deps.Bus,
		Discussions: deps.Discussions,
		Points:      deps.Points,
		APNS:        deps.APNS,
		Log:         deps.Log,
	}, server.SummaryGenerationInput{
		DiscussionID: deps.DiscussionID,
		JobID:        jobID,
		Title:        topic.Title,
		Language:     topic.Language,
		Lines:        summaryLines,
	})
	if err != nil && deps.Log != nil {
		deps.Log.Warn("mindmap start failed", "job", jobID, "discussion_id", deps.DiscussionID, "err", err)
	}
}
