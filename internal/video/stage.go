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
type Stage struct {
	enc *Encoder

	mu         sync.Mutex
	curSpeaker string
	curRole    string
	curSide    string
	body       strings.Builder
}

// NewStage creates a Stage bound to enc and primes the topic title plus the
// affirmative / negative roster panels on the left and right of the frame.
func NewStage(enc *Encoder, topicTitle string, affNames, negNames []string) *Stage {
	enc.SetTopic(topicTitle)
	enc.SetSides(affNames, negNames)
	return &Stage{enc: enc}
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
			switch m := v.(type) {
			case debate.TranscriptMsg:
				s.handleTranscript(m)
			case debate.PhaseMsg:
				s.enc.SetPhase(m.Phase.String())
			case debate.TickMsg:
				s.enc.SetClock(m.Elapsed, m.Elapsed+m.Remaining)
			}
		}
	}
}

func (s *Stage) handleTranscript(m debate.TranscriptMsg) {
	// Chat lines from the user role are routed to the transient overlay so
	// they appear briefly without replacing the speaker subtitle. The
	// orchestrator emits them via PushUserMessage with Role="user".
	if string(m.Role) == "user" {
		if m.Text != "" {
			s.enc.ShowUserMessage(m.Text)
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
		// CJK text comes in without spaces between sentences; only inject a
		// separator when the existing buffer ends in non-CJK content.
		if s.body.Len() > 0 && needsSeparator(s.body.String(), m.Text) {
			s.body.WriteByte(' ')
		}
		s.body.WriteString(m.Text)
		s.enc.SetBody(s.body.String())
	}
}

// needsSeparator decides whether to insert a space between two transcript
// fragments. Latin sentences need a space; CJK runs do not.
func needsSeparator(prev, next string) bool {
	if prev == "" || next == "" {
		return false
	}
	last := lastRune(prev)
	first := firstRune(next)
	if isCJKRune(last) || isCJKRune(first) {
		return false
	}
	return true
}

func lastRune(s string) rune {
	if s == "" {
		return 0
	}
	r, _ := decodeLastRune(s)
	return r
}

func firstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}

func decodeLastRune(s string) (rune, int) {
	for i := len(s) - 1; i >= 0; i-- {
		if (s[i] & 0xc0) != 0x80 {
			r := []rune(s[i:])
			if len(r) == 0 {
				return 0, 0
			}
			return r[0], len(s) - i
		}
	}
	return 0, 0
}

func isCJKRune(r rune) bool {
	switch {
	case r >= 0x3000 && r <= 0x303f,
		r >= 0x3400 && r <= 0x4dbf,
		r >= 0x4e00 && r <= 0x9fff,
		r >= 0xff00 && r <= 0xffef,
		r >= 0x3040 && r <= 0x30ff,
		r >= 0xac00 && r <= 0xd7af:
		return true
	}
	return false
}
