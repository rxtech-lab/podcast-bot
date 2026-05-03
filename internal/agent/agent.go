package agent

import (
	"context"
	"time"

	"github.com/sirily11/debate-bot/internal/llm"
	"github.com/sirily11/debate-bot/internal/tts"
)

// Role names a debate participant's category.
type Role string

const (
	RoleHost        Role = "host"
	RoleAffirmative Role = "affirmative"
	RoleNegative    Role = "negative"
	RoleJudge       Role = "judge"
	RoleViewer      Role = "viewer"
)

// Side returns "affirmative" or "negative" for candidate roles, otherwise "".
func (r Role) Side() string {
	switch r {
	case RoleAffirmative:
		return "affirmative"
	case RoleNegative:
		return "negative"
	}
	return ""
}

// Phase is the current debate phase.
type Phase int

const (
	PhaseSetup Phase = iota
	PhaseOpening
	PhaseFreeSpeech
	PhaseClosing
	PhaseVerdict
	PhaseConclusion
	PhaseEnded
)

func (p Phase) String() string {
	switch p {
	case PhaseSetup:
		return "setup"
	case PhaseOpening:
		return "opening"
	case PhaseFreeSpeech:
		return "free-debate"
	case PhaseClosing:
		return "closing"
	case PhaseVerdict:
		return "verdict"
	case PhaseConclusion:
		return "conclusion"
	case PhaseEnded:
		return "ended"
	}
	return "?"
}

// TranscriptLine is one entry in the running debate transcript.
// Pending marks audience lines that the host has not yet acknowledged on-air;
// such lines are kept out of agent prompts so candidates don't pre-empt the
// host's introduction of the question.
type TranscriptLine struct {
	Speaker string
	Role    Role
	Side    string
	Text    string
	At      time.Time
	Pending bool
}

// SpeakPrompt is the orchestrator's request to an Agent for one segment.
type SpeakPrompt struct {
	Phase         Phase
	SegmentNo     int
	SecondsBudget int
	Recent        []TranscriptLine
	Memory        string
	Instructions  string // host directive: "opening", "rebut:Linda", "closing", "address-user:<text>", ...
	TopicTitle    string
	TopicLanguage string
	Side          string // for candidates only; convenience copy of agent's side
}

// Agent is the OOP contract every participant satisfies.
type Agent interface {
	Name() string
	SafeName() string
	Role() Role
	Side() string
	Model() string
	Voice() tts.Voice
	SetVoice(v tts.Voice)
	Speak(ctx context.Context, p SpeakPrompt) (*llm.Stream, error)
	Listen(ctx context.Context, line TranscriptLine) error
	Compress(ctx context.Context) error
}

// Registry holds every agent constructed for a debate run.
type Registry struct {
	Host       Agent
	Judge      Agent
	Affirmatve []Agent
	Negative   []Agent
	Viewers    []Agent
}

// All returns every agent in a deterministic order.
func (r *Registry) All() []Agent {
	out := make([]Agent, 0, 2+len(r.Affirmatve)+len(r.Negative)+len(r.Viewers))
	if r.Host != nil {
		out = append(out, r.Host)
	}
	if r.Judge != nil {
		out = append(out, r.Judge)
	}
	out = append(out, r.Affirmatve...)
	out = append(out, r.Negative...)
	out = append(out, r.Viewers...)
	return out
}

// FindByName looks up an agent by display name.
func (r *Registry) FindByName(name string) Agent {
	for _, a := range r.All() {
		if a.Name() == name {
			return a
		}
	}
	return nil
}
