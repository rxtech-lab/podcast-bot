package contentcreator

import (
	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
)

// buildDebateAgents constructs the host + judge + affirmative + negative
// roster for the debate format. Viewers are shared with all formats and are
// populated by buildAgents in the base orchestrator before this is called.
func (o *Orchestrator) buildDebateAgents() error {
	o.Registry.Host = o.makeAgent(
		config.AgentSpec{Name: "Host", Model: o.Env.HostModel},
		agent.RoleHost, o.Env.HostModel)
	o.Registry.Judge = o.makeAgent(
		config.AgentSpec{
			Name:    "Judge",
			Model:   o.Topic.Judge.Model,
			BaseURL: o.Topic.Judge.BaseURL,
			APIKey:  o.Topic.Judge.APIKey,
		},
		agent.RoleJudge, o.Env.HostModel)
	for _, s := range o.Topic.Affirmative {
		o.Registry.Affirmatve = append(o.Registry.Affirmatve,
			o.makeAgent(s, agent.RoleAffirmative, ""))
	}
	for _, s := range o.Topic.Negative {
		o.Registry.Negative = append(o.Registry.Negative,
			o.makeAgent(s, agent.RoleNegative, ""))
	}
	return nil
}

// newDebatePlanner constructs the debate-format planner used by the base
// orchestrator's newPlanner dispatcher.
func (o *Orchestrator) newDebatePlanner() Planner {
	return NewDebatePlanner(o.Topic, o.Tracker, o.Registry, o.Queue, o.Transcript)
}
