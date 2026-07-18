package contentcreator

import (
	"fmt"
	"strings"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/tools"
)

// isDiscussionFamily reports whether ct runs on the discussion live runtime
// surfaces (userQueue audience flow, commander/director, session music beds,
// Background/SourceDocuments grounding): panel discussions and news
// broadcasts. Note the news AI flow itself is dedicated — scripted segments,
// no judgement, no research tools; see news_planner.go / agent/news.go.
func isDiscussionFamily(ct string) bool {
	return ct == config.ContentTypeDiscussion || ct == config.ContentTypeNews
}

// newsRundown renders the ordered story list (headline + summary + key facts)
// for the script writer and the anchor's live-turn prompt. Without it the
// anchor has no reliable source for what is on today's show — at intro time
// the transcript is still empty.
func newsRundown(t *config.DebateTopic) string {
	if t == nil || len(t.NewsStories) == 0 {
		return ""
	}
	var b strings.Builder
	for i, s := range t.NewsStories {
		fmt.Fprintf(&b, "%d. %s\n", i+1, strings.TrimSpace(s.Headline))
		if sum := strings.TrimSpace(s.Summary); sum != "" {
			fmt.Fprintf(&b, "   Summary: %s\n", sum)
		}
		for _, f := range s.KeyFacts {
			if f = strings.TrimSpace(f); f != "" {
				fmt.Fprintf(&b, "   - %s\n", f)
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// newsHeadlines returns only the ordered headline names. The script writer uses
// this detail-free list for multi-story sign-offs so closing copy cannot drift
// into re-reporting summaries or key facts.
func newsHeadlines(t *config.DebateTopic) []string {
	if t == nil {
		return nil
	}
	out := make([]string, 0, len(t.NewsStories))
	for _, story := range t.NewsStories {
		if headline := strings.TrimSpace(story.Headline); headline != "" {
			out = append(out, headline)
		}
	}
	return out
}

// newsAnchorName resolves the anchor's display name with its default.
func newsAnchorName(t *config.DebateTopic) string {
	if name := strings.TrimSpace(t.Host.Name); name != "" {
		return name
	}
	return "Anchor"
}

// newsBeats lists the co-hosts (name + beat) for the script writer.
func newsBeats(t *config.DebateTopic) []agent.NewsBeat {
	out := make([]agent.NewsBeat, 0, len(t.Discussants))
	for _, d := range t.Discussants {
		out = append(out, agent.NewsBeat{Name: d.Name, Beat: strings.TrimSpace(d.Aspect)})
	}
	return out
}

// buildNewsAgents constructs the dedicated news roster: the anchor, the
// co-host commentators, and the silent commander (visual/music director).
// The speakers get an EMPTY tool registry — their turns are scripted ahead
// of air by the NewsScriptWriter, and the remaining live turns (listener
// answers) must never stall the broadcast on research tool loops. There is
// deliberately NO judgement agent: a news desk reads the news, it does not
// fact-check-debate its own speakers on air.
func (o *Orchestrator) buildNewsAgents() error {
	roster := discussionRoster(o.Topic)
	rundown := newsRundown(o.Topic)
	noTools := tools.New()

	anchorBase := o.newAgentBase(
		config.AgentSpec{
			Name:    newsAnchorName(o.Topic),
			Model:   o.Topic.Host.Model,
			BaseURL: o.Topic.Host.BaseURL,
			APIKey:  o.Topic.Host.APIKey,
		},
		agent.RoleHost, o.Env.HostModel, noTools)
	o.Registry.Host = agent.NewNewsAnchor(anchorBase, roster, rundown)

	for _, s := range o.Topic.Discussants {
		base := o.newAgentBase(s, agent.RoleDiscussant, o.Env.HostModel, noTools)
		o.Registry.Discussants = append(o.Registry.Discussants,
			agent.NewNewsCommentator(base, s.Aspect, roster))
	}

	commanderName := o.Topic.Commander.Name
	if commanderName == "" {
		commanderName = "Commander"
	}
	o.Registry.Commander = o.makeAgent(
		config.AgentSpec{
			Name:    commanderName,
			Model:   o.Topic.Commander.Model,
			BaseURL: o.Topic.Commander.BaseURL,
			APIKey:  o.Topic.Commander.APIKey,
		},
		agent.RoleCommander, o.Env.HostModel)
	return nil
}

// newNewsPlanner constructs the scripted news planner: the script writer
// pre-generates each broadcast segment on the anchor's model, and the planner
// replays the lines with zero per-turn model latency.
func (o *Orchestrator) newNewsPlanner() Planner {
	writer := agent.NewNewsScriptWriter(
		o.newAgentLLMClient(config.AgentSpec{
			Model:   o.Topic.Host.Model,
			BaseURL: o.Topic.Host.BaseURL,
			APIKey:  o.Topic.Host.APIKey,
		}, o.Env.HostModel),
		newsAnchorName(o.Topic),
		newsBeats(o.Topic),
		o.Topic.Language,
		o.Topic.Background,
		o.Topic.SourceDocuments,
		newsRundown(o.Topic),
		newsHeadlines(o.Topic),
	)
	return NewNewsPlanner(o.Topic, o.Tracker, o.Registry, o.Queue, o.Transcript, writer)
}
