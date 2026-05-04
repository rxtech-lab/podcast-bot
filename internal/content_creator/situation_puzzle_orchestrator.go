package contentcreator

import (
	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
)

// buildPuzzleAgents constructs the puzzle host + players roster for the
// situation-puzzle format. Viewers are shared with all formats and are
// populated by buildAgents in the base orchestrator before this is called.
func (o *Orchestrator) buildPuzzleAgents() error {
	hostName := o.Topic.PuzzleHost.Name
	if hostName == "" {
		hostName = "Host"
	}
	o.Registry.PuzzleHost = o.makeAgent(
		config.AgentSpec{
			Name:    hostName,
			Model:   o.Topic.PuzzleHost.Model,
			BaseURL: o.Topic.PuzzleHost.BaseURL,
			APIKey:  o.Topic.PuzzleHost.APIKey,
		},
		agent.RolePuzzleHost, o.Env.HostModel)
	for _, s := range o.Topic.Players {
		o.Registry.Players = append(o.Registry.Players,
			o.makeAgent(s, agent.RolePlayer, ""))
	}
	return nil
}

// newPuzzlePlanner constructs the situation-puzzle planner used by the base
// orchestrator's newPlanner dispatcher.
func (o *Orchestrator) newPuzzlePlanner() Planner {
	return NewPuzzlePlanner(o.Topic, o.Tracker, o.Registry, o.Queue, o.Transcript)
}

// SetPuzzleMusic installs the per-directive music file map for the
// upcoming pipeline run. Caller (cmd/debate-bot) populates this after
// musicgen.Generate finishes so the surface and reveal turns mix the
// generated bed under the host's TTS. No-op if music is empty or nil.
// Must be called before Run.
func (o *Orchestrator) SetPuzzleMusic(music map[string]string) {
	if len(music) == 0 {
		return
	}
	o.puzzleMusic = music
}

// SetSurfaceFrames records the visual director's surface frame count for
// the upcoming run so the puzzle host's system prompt can demand exactly
// surfaceFrames-1 scene markers and the pipeline can cap excess
// SceneAdvanceMsg events. Caller (cmd/debate-bot) sets this from
// scenes.Plan / scenes.FallbackPlan output after planning completes and
// before Run is called. No-op for n <= 0.
func (o *Orchestrator) SetSurfaceFrames(n int) {
	if n <= 0 {
		return
	}
	o.surfaceFrames = n
}

// SetConclusionFrames is the same as SetSurfaceFrames for the conclusion
// phase. Conclusion now uses scene-marker advancement (not a wall-clock
// timer) so the host needs to know how many markers to emit and the
// pipeline needs to know how many to cap at.
func (o *Orchestrator) SetConclusionFrames(n int) {
	if n <= 0 {
		return
	}
	o.conclusionFrames = n
}
