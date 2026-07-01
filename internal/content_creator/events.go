package contentcreator

import (
	"image"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
)

// PhaseLabel returns the human-readable phase name (Traditional Chinese) for
// the given content type. Single source of truth for the video renderer's
// on-frame chip and the default PhaseMsg.Label stamp. The language-aware
// variant PhaseLabelLang (i18n.go) backs the per-connection SSE/WS labels;
// this wrapper preserves the original Traditional-only callers unchanged.
func PhaseLabel(contentType string, p agent.Phase) string {
	return PhaseLabelLang(contentType, p, LangHant)
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
	ChannelID        string
	Speaker          string
	Role             agent.Role
	Side             string
	Text             string
	Done             bool
	IsUserMessage    bool
	SenderUserID     string
	AudioURL         string
	AudioDuration    time.Duration
	Sources          []agent.TranscriptSource
	JudgementComment string
	// ImageURL, when set, carries a generated illustration to show inline in
	// the chat transcript (audiobook content). Such a message has no spoken
	// Text — it's an image-only bubble the client renders at this point in the
	// stream. Empty for ordinary sentence messages.
	ImageURL string
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

// SummaryReadyMsg announces that a podcast's generated summary document has
// finished (or failed) so connected clients can refresh the discussion detail
// and pick up the new summary metadata. It carries no content — the client
// re-fetches the summary body from the content endpoint when its view mounts.
type SummaryReadyMsg struct {
	ChannelID string
	DocType   string
	Status    string // "generating", "ready", or "failed"
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

// DynamicSceneMsg hands a freshly-generated background image to the active
// visual stage. Unlike SceneAdvanceMsg (which selects a pre-generated palette
// frame by index), this carries the decoded image itself — emitted by the
// discussion commander/director when it generates a new background on the fly.
// Stages that don't paint AI backgrounds (debate) treat it as a no-op.
type DynamicSceneMsg struct {
	ChannelID string
	Img       *image.RGBA
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

// AgentActivity enumerates what an agent is doing at a moment in time, so the
// dashboard can light up the corresponding node in its live diagram.
type AgentActivity string

const (
	ActivitySearching AgentActivity = "searching" // calling a web/research tool
	ActivityMemory    AgentActivity = "memory"    // taking notes / data-store
	ActivitySpeaking  AgentActivity = "speaking"  // producing its turn
	ActivityDirecting AgentActivity = "directing" // commander deciding/rendering visuals + music
	ActivityIdle      AgentActivity = "idle"      // waiting
)

// AgentActivityMsg reports a per-agent status change. It rides the same bus as
// every other event and is exposed to both SSE and WebSocket clients, so the
// dashboard's read-only diagram can show which agent is searching, taking
// memory, or speaking in realtime.
type AgentActivityMsg struct {
	ChannelID string
	Agent     string
	Role      string
	Activity  AgentActivity
	Detail    string
}

// MsgChannelID extracts the channel id from any debate event message. Returns
// "" for unknown types (which are treated as broadcast by per-channel filters).
func MsgChannelID(v any) string {
	switch m := v.(type) {
	case AgentActivityMsg:
		return m.ChannelID
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
	case SummaryReadyMsg:
		return m.ChannelID
	case TopicsChangedMsg:
		return ""
	case SceneAdvanceMsg:
		return m.ChannelID
	case DynamicSceneMsg:
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
	case AgentActivityMsg:
		m.ChannelID = id
		return m
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
	case SummaryReadyMsg:
		m.ChannelID = id
		return m
	case TopicsChangedMsg:
		return m
	case SceneAdvanceMsg:
		m.ChannelID = id
		return m
	case DynamicSceneMsg:
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
