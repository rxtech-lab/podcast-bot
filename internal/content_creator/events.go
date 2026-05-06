package contentcreator

import (
	"strings"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
)

// PhaseLabel returns the human-readable phase name for the given content
// type. Single source of truth for both the video renderer's on-frame
// chip and the SSE PhaseMsg.Label field, so frontends never need to map
// phase IDs to text themselves.
func PhaseLabel(contentType string, p agent.Phase) string {
	switch contentType {
	case config.ContentTypeSituationPuzzle:
		switch p {
		case agent.PhaseSetup, agent.PhaseOpening:
			return "出題"
		case agent.PhaseFreeSpeech:
			return "問答"
		case agent.PhaseVerdict:
			return "揭曉"
		case agent.PhaseEnded, agent.PhaseConclusion:
			return "總結"
		}
	default:
		// Debate (and unknown types — match the existing on-frame chip).
		switch p {
		case agent.PhaseOpening:
			return "立論"
		case agent.PhaseFreeSpeech:
			return "自由辯論"
		case agent.PhaseClosing:
			return "結辯"
		case agent.PhaseVerdict:
			return "判決"
		case agent.PhaseConclusion:
			return "總結"
		}
	}
	return strings.ToUpper(p.String())
}

// Tea-style messages the orchestrator pushes to the TUI via Send.
// They are defined here (not in tui/) so the orchestrator does not depend on tui.
//
// Every message carries a ChannelID so the bus can fan one shared event stream
// out to per-channel subscribers in parallel mode. Empty ChannelID means
// "broadcast" — sequential mode emits empty IDs, and per-channel filters
// treat empty as a match so today's behavior is preserved.

// TranscriptMsg is one sentence (or fragment) of one turn.
//
// AudioDuration is the wall-clock length of the synthesized audio for Text,
// measured from the bytes the TTS provider produced. Subscribers that drive
// time-based UI (subtitle scrolling) use it to align motion with the audio.
// Zero means "unknown" — emitters that don't have measured audio (e.g. the
// final Done=true marker) leave it unset.
type TranscriptMsg struct {
	ChannelID     string
	Speaker       string
	Role          agent.Role
	Side          string
	Text          string
	Done          bool
	AudioDuration time.Duration
}

// TickMsg updates the elapsed/remaining clock display.
type TickMsg struct {
	ChannelID string
	Elapsed   time.Duration
	Remaining time.Duration
}

// PhaseMsg announces a phase change.
//
// Label is the human-readable phase name, content-type aware so the
// frontend can show "問答" during a puzzle's Q&A round and "自由辯論"
// during a debate's free-speech round without baking that mapping into
// the client. The pipeline stamps it at emit time using the topic's
// content type. Type carries the content discriminator (mirrors
// TopicMsg.Type) so the frontend can also adjust styling by format.
type PhaseMsg struct {
	ChannelID string
	Phase     agent.Phase
	Label     string
	Type      string
}

// StatusMsg pushes a status-line note (e.g. "MCP server X connected").
type StatusMsg struct {
	ChannelID string
	Text      string
}

// ErrorMsg surfaces a non-fatal error.
type ErrorMsg struct {
	ChannelID string
	Err       error
}

// EndedMsg tells the TUI the orchestrator has finished and it should quit.
type EndedMsg struct {
	ChannelID      string
	TranscriptPath string
	AudioPath      string
}

// TopicMsg announces that the active debate topic has changed (sequential
// multi-topic runs). The Stage uses it to reset the encoder title + side
// panels; the web UI uses it to clear the live transcript and refresh the
// topic list. In parallel mode, ID and ChannelID are equal — each channel
// emits its own TopicMsg at start.
type TopicMsg struct {
	ChannelID string
	ID        string
	Title     string
	// Type carries the content-type discriminator (config.ContentTypeDebate /
	// config.ContentTypeSituationPuzzle). Stage subscribers gate on it so each
	// content kind has its own video-generation flow.
	Type     string
	Index    int // 0-based position in the queue
	Total    int // total topics in the queue
	AffNames []string
	NegNames []string

	// Position statements rendered as small footer text inside each side
	// panel so viewers can see what each side argues. For debate, these are
	// the affirmative / negative position summaries. For situation-puzzle,
	// AffPosition holds the soup-surface (湯面); NegPosition stays empty.
	// May be empty when the topic .md omits the section.
	AffPosition string
	NegPosition string

	// Series-only metadata. The renderer paints "Show\nS{Season} · E{Episode}"
	// as a small top-left label that fades out a few seconds in, mirroring
	// the way regular TV episodes show their identification. Empty / zero
	// for non-series content.
	Show    string
	Season  int
	Episode int
}

// TopicsChangedMsg signals that the channel/debate list has changed (e.g. a
// new debate.md was discovered by the folder watcher and added to a channel's
// queue). The frontend reacts by re-fetching /api/topics. Broadcast only —
// ChannelID is intentionally empty so every connected SSE client gets it
// regardless of which channel they're tuned to.
type TopicsChangedMsg struct{}

// SceneAdvanceMsg asks any active visual stage to swap to a specific scene
// variant for the current phase. Emitted by the producer when the speaker's
// streamed text contains a scene-switch marker (today: the puzzle host's
// surface and conclusion narration use `<scene N/>` to flag a beat boundary
// so images follow the audio beats instead of a fixed timer).
//
// Index is the 0-based absolute frame the renderer should jump to. A
// negative Index (markerIdxNoNumber) preserves the legacy "advance by one"
// semantics for unnumbered `<scene/>` markers — the renderer increments
// curSceneIdx by one mod count.
//
// Stages without a multi-variant scene active treat it as a no-op.
type SceneAdvanceMsg struct {
	ChannelID string
	Index     int
}

// ImageRefMsg asks the active stage to swap to a specific cross-episode
// archived image. Emitted by the producer when the series host stream
// contains a `<season-S-episode-E-image-N/>` marker. Key is the canonical
// image-reference id (see contentcreator.ImageRefKey) — the stage holds a
// resolver map from key → in-memory *image.RGBA loaded from the prior
// episode's archive at startup. Stages without that resolver populated
// (debate, situation-puzzle, or a series stage that didn't preload any
// prior imagery) treat this as a no-op.
type ImageRefMsg struct {
	ChannelID string
	Key       string
}

// SoundCueMsg asks the audio mixer to dispatch one of the planner's
// pre-generated sound clips. Emitted by the producer when the host
// stream contains a `<sound-overlapped-N/>` or `<sound-replace-N/>`
// marker. Index is the 0-based slot of the clip in the puzzle's sound
// plan; Mode picks between additive overlay (overlap) and a cross-fade
// of the running music bed (replace). Stages / runtimes that don't have
// a sound mixer attached treat this as a no-op.
type SoundCueMsg struct {
	ChannelID string
	Index     int
	Mode      SoundCueMode
}

// MsgChannelID extracts the channel id from any debate event message. Returns
// "" for unknown types (which are treated as broadcast by per-channel filters).
func MsgChannelID(v any) string {
	switch m := v.(type) {
	case TranscriptMsg:
		return m.ChannelID
	case TickMsg:
		return m.ChannelID
	case PhaseMsg:
		return m.ChannelID
	case StatusMsg:
		return m.ChannelID
	case ErrorMsg:
		return m.ChannelID
	case EndedMsg:
		return m.ChannelID
	case TopicMsg:
		return m.ChannelID
	case TopicsChangedMsg:
		return ""
	case SceneAdvanceMsg:
		return m.ChannelID
	case SoundCueMsg:
		return m.ChannelID
	case ImageRefMsg:
		return m.ChannelID
	}
	return ""
}

// StampChannelID returns a copy of v with its ChannelID set. Used by the
// per-channel send wrapper so orchestrators don't need to know their own id.
// Unknown types are returned unchanged.
func StampChannelID(v any, id string) any {
	switch m := v.(type) {
	case TranscriptMsg:
		m.ChannelID = id
		return m
	case TickMsg:
		m.ChannelID = id
		return m
	case PhaseMsg:
		m.ChannelID = id
		return m
	// Note: Label / Type fields on PhaseMsg are content-derived and don't
	// need touching here — orchestrator stamps them at emit time.
	case StatusMsg:
		m.ChannelID = id
		return m
	case ErrorMsg:
		m.ChannelID = id
		return m
	case EndedMsg:
		m.ChannelID = id
		return m
	case TopicMsg:
		m.ChannelID = id
		return m
	case TopicsChangedMsg:
		return m
	case SceneAdvanceMsg:
		m.ChannelID = id
		return m
	case SoundCueMsg:
		m.ChannelID = id
		return m
	case ImageRefMsg:
		m.ChannelID = id
		return m
	}
	return v
}
