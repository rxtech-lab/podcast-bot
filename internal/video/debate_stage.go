package video

import (
	"context"
	"strings"
	"sync"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/debate"
	"github.com/sirily11/debate-bot/internal/eventbus"
)

// DebateStage drives the encoder's renderer for content of type "debate".
// It tracks the topic title, the phase, and the active-speaker subtitle.
// Bound to one Encoder; cheap to construct; Run blocks until ctx is done.
//
// In parallel (channel) mode, each DebateStage is bound to a specific channel
// id and ignores events whose ChannelID doesn't match. An empty channel id
// means "accept all events" — the sequential default.
//
// Type gating: the stage only acts while the most recent TopicMsg's Type is
// debate (or unset, for back-compat with topics produced before the field
// existed). When a non-debate topic becomes active, the stage goes idle so
// the matching stage (e.g. PuzzleStage) can drive the encoder unopposed.
type DebateStage struct {
	enc       *Encoder
	channelID string

	mu         sync.Mutex
	active     bool // true while the current TopicMsg.Type is debate
	curSpeaker string
	curRole    string
	curSide    string
	body       strings.Builder
}

// NewDebateStage creates a DebateStage that consumes every event on the bus
// (sequential mode). The topic title and side panels are installed dynamically
// when the orchestrator publishes a debate-type TopicMsg, so the same Stage/
// Encoder pair is reused across sequential debate topics.
func NewDebateStage(enc *Encoder) *DebateStage {
	return &DebateStage{enc: enc, active: true}
}

// NewDebateChannelStage creates a DebateStage that only reacts to events
// whose ChannelID matches the given id. Use this in parallel mode so every
// channel's Encoder only sees its own debate's transcript / phase / topic
// events. The stage starts idle and activates on the first matching debate
// TopicMsg.
func NewDebateChannelStage(enc *Encoder, channelID string) *DebateStage {
	return &DebateStage{enc: enc, channelID: channelID}
}

// Run subscribes to bus and dispatches transcript + phase events. Returns
// when ctx is cancelled or the bus closes.
func (s *DebateStage) Run(ctx context.Context, bus *eventbus.Bus) {
	ch, cancel := bus.Subscribe(128)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case v, ok := <-ch:
			if !ok {
				return
			}
			if !s.accepts(v) {
				continue
			}
			// TopicMsg flips activation regardless of current state — a debate
			// topic activates us; any other type idles us.
			if m, ok := v.(debate.TopicMsg); ok {
				if isDebateType(m.Type) {
					s.activate()
					s.handleTopic(m)
				} else {
					s.idle()
				}
				continue
			}
			if !s.isActive() {
				continue
			}
			switch m := v.(type) {
			case debate.TranscriptMsg:
				s.handleTranscript(m)
			case debate.PhaseMsg:
				s.enc.SetPhase(phaseChipText(m))
			case debate.TickMsg:
				s.enc.SetClock(m.Elapsed, m.Elapsed+m.Remaining)
			}
		}
	}
}

// isDebateType returns true for debate content. Empty Type is treated as
// debate so older event payloads (and tests that don't bother setting Type)
// keep working.
func isDebateType(t string) bool {
	return t == "" || t == config.ContentTypeDebate
}

func (s *DebateStage) activate() {
	s.mu.Lock()
	s.active = true
	s.mu.Unlock()
}

func (s *DebateStage) idle() {
	s.mu.Lock()
	s.active = false
	s.curSpeaker, s.curRole, s.curSide = "", "", ""
	s.body.Reset()
	s.mu.Unlock()
}

func (s *DebateStage) isActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// accepts returns true when the Stage should process v. Sequential Stages
// (channelID=="") see everything; channel-bound Stages drop events whose
// ChannelID is set and doesn't match.
func (s *DebateStage) accepts(v any) bool {
	if s.channelID == "" {
		return true
	}
	id := debate.MsgChannelID(v)
	// id=="" means broadcast — keep so global StatusMsgs still show up.
	return id == "" || id == s.channelID
}

func (s *DebateStage) handleTopic(m debate.TopicMsg) {
	s.enc.SetTopic(m.Title)
	s.enc.SetSides(m.AffNames, m.NegNames)
	s.enc.SetPositions(m.AffPosition, m.NegPosition)
	s.mu.Lock()
	s.curSpeaker, s.curRole, s.curSide = "", "", ""
	s.body.Reset()
	s.mu.Unlock()
	s.enc.SetSpeaker("", "", "")
	s.enc.SetBody("", 0)
}

func (s *DebateStage) handleTranscript(m debate.TranscriptMsg) {
	// Chat lines from the user role are routed to the transient overlay so
	// they appear briefly without replacing the speaker subtitle. The
	// orchestrator emits them via PushUserMessage with Role="user".
	if string(m.Role) == "user" {
		if m.Text != "" {
			username := m.Speaker
			if username == "user" {
				username = ""
			}
			s.enc.ShowUserMessage(m.Text, username)
		}
		return
	}

	// Done markers are sent right after produce() returns and can race
	// ahead of (or interleave with) the AfterFunc-scheduled sentence
	// TranscriptMsgs. Letting one reach the speaker-change branch below
	// flips the active speaker before the last sentence's text has fired
	// and clears the body mid-audio. They carry no Text — nothing for the
	// on-air layout to do — so drop them here.
	if m.Done {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	speakerKey := m.Speaker + "|" + string(m.Role) + "|" + m.Side
	curKey := s.curSpeaker + "|" + s.curRole + "|" + s.curSide

	if speakerKey != curKey && m.Speaker != "" {
		s.curSpeaker = m.Speaker
		s.curRole = string(m.Role)
		s.curSide = m.Side
		s.body.Reset()
		s.enc.SetSpeaker(m.Speaker, string(m.Role), m.Side)
		// Skip clearing the body when this same call carries the new
		// sentence text — the SetBody below installs it atomically. The
		// older "clear then set" pattern produced a microsecond window
		// of empty body that an unlucky frame could capture as a blink.
		if m.Text == "" {
			s.enc.SetBody("", 0)
		}
	}

	if m.Text != "" {
		// Each TranscriptMsg is scheduled by the producer to fire when this
		// sentence's first audio byte is about to play, so the subtitle follows
		// the audio if we REPLACE the visible body each time instead of
		// appending. Viewers see exactly the sentence they're currently hearing.
		s.body.Reset()
		s.body.WriteString(m.Text)
		s.enc.SetBody(s.body.String(), m.AudioDuration)
	}
}
