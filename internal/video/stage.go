package video

import (
	"context"
	"strings"
	"sync"

	"github.com/sirily11/debate-bot/internal/debate"
	"github.com/sirily11/debate-bot/internal/eventbus"
)

// Stage subscribes to the event bus and updates the encoder's renderer state
// so the live debate text shows up baked into the video stream.
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

// NewStage creates a Stage bound to enc.
func NewStage(enc *Encoder) *Stage { return &Stage{enc: enc} }

// Run subscribes to bus and dispatches transcript events. Returns when ctx is
// cancelled or the bus closes.
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
			if m, ok := v.(debate.TranscriptMsg); ok {
				s.handleTranscript(m)
			}
		}
	}
}

func (s *Stage) handleTranscript(m debate.TranscriptMsg) {
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
		_ = s.enc.UpdateBody("")
	}

	if m.Text != "" {
		if s.body.Len() > 0 {
			s.body.WriteByte(' ')
		}
		s.body.WriteString(m.Text)
		_ = s.enc.UpdateBody(s.body.String())
	}
}
