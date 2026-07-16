package contentcreator

import (
	"context"
	"fmt"
	"strings"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/video/imagegen"
)

// discussionRoster renders the moderator and every discussant (with their
// assigned angle) as a name list for the host's and discussants' system
// prompts. Without it the host has no reliable source for participant names —
// at intro time the transcript is still empty — so it invents or mangles them.
func discussionRoster(t *config.DebateTopic) string {
	if t == nil || len(t.Discussants) == 0 {
		return ""
	}
	var b strings.Builder
	host := strings.TrimSpace(t.Host.Name)
	if host == "" {
		host = "Host"
	}
	fmt.Fprintf(&b, "Moderator: %s\nParticipants:\n", host)
	for _, d := range t.Discussants {
		if a := strings.TrimSpace(d.Aspect); a != "" {
			fmt.Fprintf(&b, "- %s — angle: %s\n", d.Name, a)
		} else {
			fmt.Fprintf(&b, "- %s\n", d.Name)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// buildDiscussionAgents constructs the moderator (host), the discussants, and
// the silent commander for the panel-discussion format. Viewers are shared
// with all formats and are populated by buildAgents before this is called.
func (o *Orchestrator) buildDiscussionAgents() error {
	hostName := o.Topic.Host.Name
	if hostName == "" {
		hostName = "Host"
	}
	o.Registry.Host = o.makeAgent(
		config.AgentSpec{
			Name:    hostName,
			Model:   o.Topic.Host.Model,
			BaseURL: o.Topic.Host.BaseURL,
			APIKey:  o.Topic.Host.APIKey,
		},
		agent.RoleHost, o.Env.HostModel)

	for _, s := range o.Topic.Discussants {
		o.Registry.Discussants = append(o.Registry.Discussants,
			o.makeAgent(s, agent.RoleDiscussant, ""))
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

	o.Registry.Judgement = o.makeAgent(
		config.AgentSpec{
			Name:  "Judgement",
			Model: o.Env.JudgementModel,
		},
		agent.RoleJudgement, o.Env.JudgementModel)
	return nil
}

// newDiscussionPlanner constructs the discussion-format planner used by the
// base orchestrator's newPlanner dispatcher.
func (o *Orchestrator) newDiscussionPlanner() Planner {
	return NewDiscussionPlanner(o.Topic, o.Tracker, o.Registry, o.Queue, o.Transcript)
}

// SetDiscussionAudio installs the pre-generated music for a discussion before
// Run. beds is the session-bed map (folded into the pipeline's MusicPaths;
// use key "session" for the always-on bed). sounds + moods are index-aligned:
// sounds[i] is the mp3 path of a bed the commander can crossfade to and
// moods[i] is the short description fed to the commander's prompt for that
// same index. Must be called before Run (the commander captures moods at
// construction time in Setup).
func (o *Orchestrator) SetDiscussionAudio(beds map[string]string, sounds, moods []string) {
	o.discussionMusic = beds
	o.discussionSounds = append([]string(nil), sounds...)
	o.discussionMusicMoods = append([]string(nil), moods...)
}

// SetDisableImages suppresses all on-the-fly image generation during Run (the
// discussion director's background generation). Used by the audio-only feed,
// where generated images would be unused but still cost provider calls. Call
// before Run. Music crossfade behaviour is unaffected.
func (o *Orchestrator) SetDisableImages(v bool) {
	o.disableImages = v
}

// SetAudioOnly marks the run as an audio-only feed. The recorded audio.mp3 is
// captured straight from the LiveStream at t=0 with no stitch StartOffset trim,
// so the sidecar VTT must skip vttBias (which only realigns cues against the
// trimmed mp4). Call before Run.
func (o *Orchestrator) SetAudioOnly(v bool) {
	o.audioOnly = v
}

// startDiscussionDirector builds and launches the silent commander loop. The
// image client is best-effort: if no API key is configured the director still
// crossfades the pre-generated beds, it just can't generate fresh backgrounds.
func (o *Orchestrator) startDiscussionDirector(ctx context.Context) {
	cmd, ok := o.Registry.Commander.(*agent.Commander)
	if !ok || cmd == nil {
		o.Log.Warn("discussion commander missing — director disabled")
		return
	}
	var imgClient *imagegen.Client
	if o.disableImages {
		// Audio-only feed: keep the music crossfade loop, never generate
		// backgrounds (there's no stage to paint them and they cost provider
		// calls).
		o.Log.Info("discussion image gen suppressed (audio-only) — commander will only crossfade music")
	} else if c, err := imagegen.New(""); err != nil {
		o.Log.Warn("discussion image gen disabled — commander will only crossfade music", "err", err)
	} else {
		imgClient = c
	}
	o.discussionDirector = NewDiscussionDirector(
		cmd, o.Transcript, o.Send, imgClient, len(o.discussionSounds), o.Log)
	go o.discussionDirector.Run(ctx)
}
