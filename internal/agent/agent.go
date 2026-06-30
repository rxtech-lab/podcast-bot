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
	RolePuzzleHost  Role = "puzzle-host"
	RolePlayer      Role = "player"
	// RoleSeriesHost is the single narrator on a host-only TV-series episode.
	// Series episodes have no debate, no Q&A, no audience interjection — the
	// host reads the prepared synopsis and (optionally) a "previously on …"
	// preamble. See agent/series_host.go.
	RoleSeriesHost Role = "series-host"
	// RoleDiscussant is one participant in a panel discussion. Each speaks
	// from an assigned aspect/perspective and responds to the others. See
	// agent/discussant.go.
	RoleDiscussant Role = "discussant"
	// RoleCommander is the silent visual/music director of a discussion. It
	// never takes a spoken turn; a background loop calls its Direct method to
	// decide background-image / music changes. See agent/commander.go.
	RoleCommander Role = "commander"
	// RoleJudgement is a silent fact-checker for discussion turns. It never
	// speaks in the schedule; the pipeline calls it after a discussant turn to
	// decide whether a short evidence warning should be attached.
	RoleJudgement Role = "judgement"
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
// host's introduction of the question. Addressed marks audience lines that
// have already been answered on-air (host's address-user / candidate's
// answer-user turn finished). Once addressed, the audience-steering block
// stops re-firing on every subsequent agent turn — without this flag every
// player kept opening with "since the audience asked..." for the rest of
// the round.
type TranscriptLine struct {
	Speaker          string
	Role             Role
	Side             string
	Text             string
	ImageURL         string
	At               time.Time
	Pending          bool
	Addressed        bool
	Sources          []TranscriptSource
	JudgementComment string
}

// TranscriptSource is a compact public reference attached to one transcript
// turn when a speaker used web/research tools while composing it.
type TranscriptSource struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
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
	ToolResult    func(name, jsonArgs, result string)
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

// Registry holds every agent constructed for one content run. Debate-mode
// content uses Host/Judge/Affirmatve/Negative; situation-puzzle content uses
// PuzzleHost/Players. Viewers are shared across both formats.
type Registry struct {
	Host       Agent
	Judge      Agent
	Affirmatve []Agent
	Negative   []Agent

	PuzzleHost Agent
	Players    []Agent

	// SeriesHost is the single narrator agent for a TV-series episode.
	// Series content uses this role exclusively — no players, no judge,
	// no other speakers.
	SeriesHost Agent

	// Discussion content type: Discussants speak in round-robin; Host (above)
	// moderates; Commander is the silent visual/music director (it never
	// appears in the speaking rotation, but lives here so the orchestrator
	// can reach it to drive the background loop).
	Discussants []Agent
	Commander   Agent
	Judgement   Agent

	Viewers []Agent
}

// All returns every agent in a deterministic order.
func (r *Registry) All() []Agent {
	out := make([]Agent, 0, 5+len(r.Affirmatve)+len(r.Negative)+len(r.Players)+len(r.Discussants)+len(r.Viewers))
	if r.Host != nil {
		out = append(out, r.Host)
	}
	if r.Commander != nil {
		out = append(out, r.Commander)
	}
	if r.Judgement != nil {
		out = append(out, r.Judgement)
	}
	if r.Judge != nil {
		out = append(out, r.Judge)
	}
	if r.PuzzleHost != nil {
		out = append(out, r.PuzzleHost)
	}
	if r.SeriesHost != nil {
		out = append(out, r.SeriesHost)
	}
	out = append(out, r.Affirmatve...)
	out = append(out, r.Negative...)
	out = append(out, r.Players...)
	out = append(out, r.Discussants...)
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
