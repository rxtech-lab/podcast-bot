package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/content_creator"
	"github.com/sirily11/debate-bot/internal/discussion"
)

// prepareDiscussionAssets gets the show started as fast as possible by
// delegating to the shared discussion asset-prep (background palette + music
// beds). Kept as a thin channel-runtime wrapper so main's run loop reads the
// same as the puzzle/series prep calls; the heavy lifting lives in
// internal/discussion so the video-job runner can reuse it.
func prepareDiscussionAssets(ctx context.Context, log *slog.Logger, env *config.Env,
	ch *channelRuntime, d loadedDebate, orch *contentcreator.Orchestrator) {
	fmt.Fprintf(os.Stdout, "▶ ch %d [%s] generating discussion backgrounds + music (first run only; cached afterwards)\n",
		ch.def.Number, ch.def.ID)
	discussion.PrepareAssets(ctx, log, env.OutDir, ch.discussionStage, d.topic, orch)
}

// buildDiscussionTopicMsg fills the panel roster into the TopicMsg. The
// discussants populate the left panel; the moderator the right. Aspects feed
// the left-panel footer so viewers can see each participant's angle.
func buildDiscussionTopicMsg(d loadedDebate, msg contentcreator.TopicMsg) contentcreator.TopicMsg {
	msg.AffNames = agentNames(d.topic.Discussants)
	hostName := d.topic.Host.Name
	if hostName == "" {
		hostName = "Host"
	}
	msg.NegNames = []string{hostName}
	msg.AffPosition = d.topic.Background
	msg.NegPosition = ""
	return msg
}
