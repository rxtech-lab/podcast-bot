package video

import (
	"context"
	"strings"
	"sync"

	"github.com/sirily11/debate-bot/internal/debate"
	"github.com/sirily11/debate-bot/internal/eventbus"
)

// Stage subscribes to the event bus and drives the encoder's renderer state
// so the live debate text shows up baked into the video stream.
//
// It owns three pieces of UI state:
//   - the topic title (set once at construction)
//   - the current phase (updated from PhaseMsg)
//   - the active-speaker subtitle (built from TranscriptMsg)
//
// One Stage per Encoder. Cheap to construct; Run blocks until ctx is done.
//
// In parallel (channel) mode, each Stage is bound to a specific channel id and
// ignores events whose ChannelID doesn't match. An empty channel id means
// "accept all events" — the sequential default.
type Stage struct {
	enc       *Encoder
	channelID string

	mu         sync.Mutex
	curSpeaker string
	curRole    string
	curSide    string
	body       strings.Builder
}

// NewStage creates a Stage bound to enc that consumes every event on the bus
// (sequential mode). The topic title and side panels are installed dynamically
// when the orchestrator publishes a TopicMsg, so the same Stage/Encoder pair
// is reused across sequential topics.
func NewStage(enc *Encoder) *Stage {
	return &Stage{enc: enc}
}

// NewChannelStage creates a Stage that only reacts to events whose ChannelID
// matches the given id. Use this in parallel mode so every channel's Encoder
// only sees its own debate's transcript / phase / topic events.
func NewChannelStage(enc *Encoder, channelID string) *Stage {
	return &Stage{enc: enc, channelID: channelID}
}

// Run subscribes to bus and dispatches transcript + phase events. Returns
// when ctx is cancelled or the bus closes.
func (s *Stage) Run(ctx context.Context, bus *eventbus.Bus) {
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
			switch m := v.(type) {
			case debate.TranscriptMsg:
				s.handleTranscript(m)
			case debate.PhaseMsg:
				s.enc.SetPhase(m.Phase.String())
			case debate.TickMsg:
				s.enc.SetClock(m.Elapsed, m.Elapsed+m.Remaining)
			case debate.TopicMsg:
				s.handleTopic(m)
			}
		}
	}
}

// accepts returns true when the Stage should process v. Sequential Stages
// (channelID=="") see everything; channel-bound Stages drop events whose
// ChannelID is set and doesn't match.
func (s *Stage) accepts(v any) bool {
	if s.channelID == "" {
		return true
	}
	id := debate.MsgChannelID(v)
	// id=="" means broadcast — keep so global StatusMsgs still show up.
	return id == "" || id == s.channelID
}

func (s *Stage) handleTopic(m debate.TopicMsg) {
	s.enc.SetTopic(m.Title)
	s.enc.SetSides(m.AffNames, m.NegNames)
	s.mu.Lock()
	s.curSpeaker, s.curRole, s.curSide = "", "", ""
	s.body.Reset()
	s.mu.Unlock()
	s.enc.SetSpeaker("", "", "")
	s.enc.SetBody("")
}

func (s *Stage) handleTranscript(m debate.TranscriptMsg) {
	// Chat lines from the user role are routed to the transient overlay so
	// they appear briefly without replacing the speaker subtitle. The
	// orchestrator emits them via PushUserMessage with Role="user".
	if string(m.Role) == "user" {
		if m.Text != "" {
			// m.Speaker carries the viewer's chosen username (PushUserMessage
			// falls back to "user" when blank). Pass it through so the ticker
			// can label the message — empty stays empty for old payloads.
			username := m.Speaker
			if username == "user" {
				username = ""
			}
			s.enc.ShowUserMessage(m.Text, username)
		}
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	speakerKey := m.Speaker + "|" + string(m.Role) + "|" + m.Side
	curKey := s.curSpeaker + "|" + s.curRole + "|" + s.curSide

	// Speaker change: reset the body and update the encoder's speaker state
	// so the renderer redraws the role-coloured pill.
	if speakerKey != curKey && m.Speaker != "" {
		s.curSpeaker = m.Speaker
		s.curRole = string(m.Role)
		s.curSide = m.Side
		s.body.Reset()
		s.enc.SetSpeaker(m.Speaker, string(m.Role), m.Side)
		s.enc.SetBody("")
	}

	if m.Text != "" {
		// Each TranscriptMsg is scheduled by the producer (pipeline.synthSentence)
		// to fire when this sentence's first audio byte is about to play, so the
		// subtitle follows the audio if we REPLACE the visible body each time
		// instead of appending. With the renderer's page-based subtitle, this
		// means viewers see exactly the sentence they're currently hearing.
		// (The cumulative transcript that gets persisted to disk is built from
		// t.TextOut in updateMemories — independent of what we display here.)
		s.body.Reset()
		s.body.WriteString(m.Text)
		s.enc.SetBody(s.body.String())
	}
}

