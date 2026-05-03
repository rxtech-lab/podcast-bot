package video

import (
	"context"
	"strings"
	"sync"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/debate"
	"github.com/sirily11/debate-bot/internal/eventbus"
)

const (
	bodyMaxCols  = 50
	bodyMaxLines = 10
)

// Stage subscribes to the event bus and updates the encoder's drawtext source
// files so the live debate text shows up baked into the video stream.
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

	// Speaker change: reset the body and update the tag. We treat a Done event
	// from a different speaker (e.g. user lines that arrive Done=true) as a
	// transition too.
	if speakerKey != curKey && m.Speaker != "" {
		s.curSpeaker = m.Speaker
		s.curRole = string(m.Role)
		s.curSide = m.Side
		s.body.Reset()
		_ = s.enc.UpdateTag(formatTag(m))
	}

	if m.Text != "" {
		if s.body.Len() > 0 {
			s.body.WriteByte(' ')
		}
		s.body.WriteString(m.Text)
		_ = s.enc.UpdateBody(wrapLines(s.body.String(), bodyMaxCols, bodyMaxLines))
	}
}

func formatTag(m debate.TranscriptMsg) string {
	switch m.Role {
	case agent.RoleHost:
		return "HOST"
	case agent.RoleAffirmative:
		return "AFFIRMATIVE — " + m.Speaker
	case agent.RoleNegative:
		return "NEGATIVE — " + m.Speaker
	case agent.RoleJudge:
		return "JUDGE"
	case agent.RoleViewer:
		return "VIEWER — " + m.Speaker
	}
	if m.Speaker != "" {
		return strings.ToUpper(m.Speaker)
	}
	return " "
}

// wrapLines word-wraps s to maxCols columns and keeps the last maxLines lines.
// Callers feed it as drawtext body text.
func wrapLines(s string, maxCols, maxLines int) string {
	if s == "" {
		return " "
	}
	words := strings.Fields(s)
	var lines []string
	var cur strings.Builder
	for _, w := range words {
		switch {
		case cur.Len() == 0:
			cur.WriteString(w)
		case cur.Len()+1+len(w) > maxCols:
			lines = append(lines, cur.String())
			cur.Reset()
			cur.WriteString(w)
		default:
			cur.WriteByte(' ')
			cur.WriteString(w)
		}
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}
