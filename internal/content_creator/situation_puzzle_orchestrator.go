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

// SetSurfacePlan records the visual director's surface beat directions for
// the upcoming run. Each entry is a one-sentence direction describing what
// the matching cached image (surface-vN) depicts; the puzzle host's system
// prompt enumerates them as "Beat N: <direction>" so the host can emit
// "<scene N/>" markers locked to the planner's beats. Caller
// (cmd/debate-bot) sets this from scenes.Plan / scenes.FallbackPlan output
// after planning completes and before Run is called. No-op for empty plans.
func (o *Orchestrator) SetSurfacePlan(plan []string) {
	if len(plan) == 0 {
		return
	}
	o.surfacePlan = append([]string(nil), plan...)
}

// SetSurfaceAnchors records the planner's per-beat verbatim anchor list
// (parallel to SurfacePlan). The puzzle host's system prompt embeds each
// anchor under its beat so the host can string-match its narration
// position against the surface and drop "<scene N/>" markers exactly
// at the planner's intended boundaries — replaces the old "count
// paragraph breaks" heuristic that drifted in long narrations. No-op
// when the slice is empty (host falls back to its own paragraph
// judgement).
func (o *Orchestrator) SetSurfaceAnchors(anchors []string) {
	if len(anchors) == 0 {
		return
	}
	o.surfaceAnchors = append([]string(nil), anchors...)
}

// SetConclusionPlan is the same as SetSurfacePlan for the conclusion phase.
// Conclusion uses scene-marker advancement (not a wall-clock timer) so the
// host needs to know what each numbered beat depicts to emit the right
// markers in the right order.
func (o *Orchestrator) SetConclusionPlan(plan []string) {
	if len(plan) == 0 {
		return
	}
	o.conclusionPlan = append([]string(nil), plan...)
}

// SetSoundPlan installs the planner's sound-cue list and the parallel
// list of generated clip paths. Index N of either slice must describe
// the same cue; mismatched lengths are tolerated by trimming both to
// the shorter length so a partial generation failure (one clip out of
// five) doesn't pin a stray index on the wrong path. No-op when either
// list is empty — the host's prompt then omits the sound section so
// the LLM never emits a sound marker. Caller invokes this after
// musicgen finishes generating each clip and before Run.
func (o *Orchestrator) SetSoundPlan(plan []SoundCueDirection, paths []string) {
	if len(plan) == 0 || len(paths) == 0 {
		return
	}
	n := len(plan)
	if len(paths) < n {
		n = len(paths)
	}
	o.soundPlan = append([]SoundCueDirection(nil), plan[:n]...)
	o.soundPaths = append([]string(nil), paths[:n]...)
}
