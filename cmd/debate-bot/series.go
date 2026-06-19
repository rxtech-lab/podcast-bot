package main

import (
	"context"
	"log/slog"

	"github.com/sirily11/debate-bot/internal/config"
	contentcreator "github.com/sirily11/debate-bot/internal/content_creator"
	"github.com/sirily11/debate-bot/internal/series"
)

// prepareSeriesAssets thin-wraps series.PrepareEpisode so the channel
// runner has a uniform call shape with preparePuzzleAssets. The heavy
// lifting (recap LLM, image-ref resolution, scene plan, music + narration
// gen) lives in internal/series so the cmd/series-smoke binary can share
// the exact same code path.
func prepareSeriesAssets(ctx context.Context, log *slog.Logger, env *config.Env,
	ch *channelRuntime, d loadedDebate, orch *contentcreator.Orchestrator) {
	series.PrepareEpisode(ctx, log, env, ch.seriesStage, d.topic, orch)
}

// finishSeriesEpisode thin-wraps series.FinishEpisode for symmetry.
func finishSeriesEpisode(log *slog.Logger, env *config.Env, d loadedDebate) {
	series.FinishEpisode(log, env, d.topic)
}

// buildSeriesTopicMsg fills the series-specific TopicMsg fields. Host on
// the left panel, synopsis as AffPosition, no NegNames.
func buildSeriesTopicMsg(d loadedDebate, msg contentcreator.TopicMsg) contentcreator.TopicMsg {
	hostName := d.topic.SeriesHost.Name
	if hostName == "" {
		hostName = "Narrator"
	}
	msg.AffNames = []string{hostName}
	msg.NegNames = nil
	msg.AffPosition = d.topic.Surface
	msg.NegPosition = ""
	msg.Show = d.topic.Show
	msg.Season = d.topic.Season
	msg.Episode = d.topic.Episode
	return msg
}
