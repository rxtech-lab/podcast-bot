package video

import (
	"context"
	"strings"
	"sync"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/debate"
	"github.com/sirily11/debate-bot/internal/eventbus"
)

// PuzzleStage drives the encoder for content of type "situation-puzzle"
// (海龜湯). Layout-wise it shares the Encoder/Renderer with DebateStage but
// remaps the panels: the puzzle host (出題者) sits alone on the left side, the
// players (解題者) on the right, and the soup-surface text (湯面) is placed
// in the left panel's footer slot so it stays visible the whole round.
//
// Type gating mirrors DebateStage: the stage only acts while the most recent
// TopicMsg.Type is situation-puzzle. Other content idles it. Two stages run
// per channel; whichever matches the active topic drives the encoder.
//
// Subtitle handling differs from debate in one respect: there is no
// affirmative/negative side, so the speaker pill doesn't try to colour-code
// by side — the puzzle host's role string ("puzzle-host") and the players'
// role string ("player") flow straight through to the renderer, and any
// future role-specific styling lives in render.go's roleColor.
type PuzzleStage struct {
	enc       *Encoder
	channelID string

	mu         sync.Mutex
	active     bool
	curSpeaker string
	curRole    string
	body       strings.Builder
}

// NewPuzzleStage creates a sequential-mode PuzzleStage (no channel filter).
func NewPuzzleStage(enc *Encoder) *PuzzleStage {
	return &PuzzleStage{enc: enc, active: true}
}

// NewPuzzleChannelStage creates a PuzzleStage that only reacts to events
// whose ChannelID matches. The stage starts idle and activates on the first
// situation-puzzle TopicMsg.
func NewPuzzleChannelStage(enc *Encoder, channelID string) *PuzzleStage {
	return &PuzzleStage{enc: enc, channelID: channelID}
}

// Run subscribes to bus and dispatches puzzle events to the encoder. Returns
// when ctx is cancelled or the bus closes.
func (s *PuzzleStage) Run(ctx context.Context, bus *eventbus.Bus) {
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
			if m, ok := v.(debate.TopicMsg); ok {
				if isPuzzleType(m.Type) {
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
				s.enc.SetPhase(puzzlePhaseLabel(m.Phase))
			case debate.TickMsg:
				s.enc.SetClock(m.Elapsed, m.Elapsed+m.Remaining)
			}
		}
	}
}

func isPuzzleType(t string) bool {
	return t == config.ContentTypeSituationPuzzle
}

// puzzlePhaseLabel re-skins the shared agent.Phase strings for the puzzle
// flow. The phase enum is reused (PhaseOpening/PhaseFreeSpeech/etc) but the
// audience-facing labels diverge from the debate version's "立論 / 自由辯論
// / 結辯 / 判決".
func puzzlePhaseLabel(p agent.Phase) string {
	switch p {
	case agent.PhaseOpening:
		return "出題"
	case agent.PhaseFreeSpeech:
		return "問答"
	case agent.PhaseVerdict:
		return "揭曉"
	case agent.PhaseEnded, agent.PhaseConclusion:
		return "總結"
	}
	return p.String()
}

func (s *PuzzleStage) activate() {
	s.mu.Lock()
	s.active = true
	s.mu.Unlock()
}

func (s *PuzzleStage) idle() {
	s.mu.Lock()
	s.active = false
	s.curSpeaker, s.curRole = "", ""
	s.body.Reset()
	s.mu.Unlock()
}

func (s *PuzzleStage) isActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

func (s *PuzzleStage) accepts(v any) bool {
	if s.channelID == "" {
		return true
	}
	id := debate.MsgChannelID(v)
	return id == "" || id == s.channelID
}

// handleTopic primes the encoder with the puzzle's framing. AffNames/NegNames
// come from buildTopicMsg already mapped (host on the left, players on the
// right); AffPosition carries the soup-surface (湯面) so viewers can read the
// scenario the whole round. We deliberately do NOT pass the truth (湯底) to
// any rendering surface — only the puzzle host's LLM prompt sees it.
func (s *PuzzleStage) handleTopic(m debate.TopicMsg) {
	s.enc.SetTopic(m.Title)
	s.enc.SetSides(m.AffNames, m.NegNames)
	s.enc.SetPositions(m.AffPosition, m.NegPosition)
	s.mu.Lock()
	s.curSpeaker, s.curRole = "", ""
	s.body.Reset()
	s.mu.Unlock()
	s.enc.SetSpeaker("", "", "")
	s.enc.SetBody("", 0)
}

// handleTranscript paints the active speaker's subtitle. For the puzzle the
// "side" coordinate is meaningless (no aff/neg), so we keep the side empty
// and let the renderer fall through to the role-color path. The puzzle host's
// 是/不是/與此無關 utterances arrive as ordinary transcript fragments — they
// flow through unchanged.
func (s *PuzzleStage) handleTranscript(m debate.TranscriptMsg) {
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

	s.mu.Lock()
	defer s.mu.Unlock()

	speakerKey := m.Speaker + "|" + string(m.Role)
	curKey := s.curSpeaker + "|" + s.curRole

	if speakerKey != curKey && m.Speaker != "" {
		s.curSpeaker = m.Speaker
		s.curRole = string(m.Role)
		s.body.Reset()
		s.enc.SetSpeaker(m.Speaker, string(m.Role), "")
		s.enc.SetBody("", 0)
	}

	if m.Text != "" {
		s.body.Reset()
		s.body.WriteString(m.Text)
		s.enc.SetBody(s.body.String(), m.AudioDuration)
	}
}
