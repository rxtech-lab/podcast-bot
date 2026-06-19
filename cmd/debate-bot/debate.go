package main

import (
	"github.com/sirily11/debate-bot/internal/content_creator"
)

// buildDebateTopicMsg fills in the debate-specific fields of a TopicMsg:
// affirmative + negative roster names plus their position statements.
func buildDebateTopicMsg(d loadedDebate, msg contentcreator.TopicMsg) contentcreator.TopicMsg {
	msg.AffNames = agentNames(d.topic.Affirmative)
	msg.NegNames = agentNames(d.topic.Negative)
	msg.AffPosition = d.topic.AffirmativePos
	msg.NegPosition = d.topic.NegativePos
	return msg
}
