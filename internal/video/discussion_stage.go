package video

import (
	"context"
	"image"
	"strings"
	"sync"
	"time"

	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/content_creator"
	"github.com/sirily11/debate-bot/internal/eventbus"
	"github.com/sirily11/debate-bot/internal/video/scenes"
)

// DiscussionStage drives the encoder for content of type "discussion". It
// reuses the debate-style lower-third subtitle (speaker pill + body) but
// composites it over an AI-generated background that the silent commander
// swaps on the fly. Backgrounds arrive two ways:
//   - a pre-generated palette installed via AttachPalette before the show and
//     selected by index through SceneAdvanceMsg, and
//   - fresh images generated mid-show, delivered as DynamicSceneMsg.
//
// Type gating mirrors the other stages: the stage only acts while the most
// recent TopicMsg.Type is discussion; other content idles it.
type DiscussionStage struct {
	enc       *Encoder
	channelID string

	mu         sync.Mutex
	active     bool
	curSpeaker string
	curRole    string
	body       strings.Builder

	// frames is the background pool: the pre-generated palette followed by
	// any commander-generated images appended at runtime. curIdx is the
	// frame currently shown.
	frames []*image.RGBA
	curIdx int
}

// NewDiscussionStage creates a sequential-mode stage (no channel filter).
func NewDiscussionStage(enc *Encoder) *DiscussionStage {
	return &DiscussionStage{enc: enc, active: true}
}

// NewDiscussionChannelStage creates a stage that only reacts to events whose
// ChannelID matches. It starts idle and activates on the first discussion
// TopicMsg.
func NewDiscussionChannelStage(enc *Encoder, channelID string) *DiscussionStage {
	return &DiscussionStage{enc: enc, channelID: channelID}
}

// discussionRotationInterval is how long each background stays on screen
// before the stage rotates to the next one. Keeps the frame alive between
// the commander's on-the-fly background swaps so a static plate doesn't sit
// for minutes while the discussion runs.
const discussionRotationInterval = 25 * time.Second

// AttachPalette installs the pre-generated background palette. Safe to call
// before or after the topic activates; the first frame paints on activation.
func (s *DiscussionStage) AttachPalette(frames []*image.RGBA) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Prepend the palette ahead of any already-appended dynamic frames so
	// palette indices the commander/host reference stay stable.
	s.frames = append(append([]*image.RGBA(nil), frames...), s.frames...)
	if s.active && s.enc != nil && len(s.frames) > 0 && s.curIdx < len(s.frames) {
		s.enc.SetSceneBackground(s.frames[s.curIdx])
	}
}

// AttachPaletteFrame appends a single background as it finishes generating
// (streaming path). The first frame to land paints immediately so the show
// can start without waiting for the whole palette; later frames join the
// rotation pool.
func (s *DiscussionStage) AttachPaletteFrame(img *image.RGBA) {
	if img == nil {
		return
	}
	s.mu.Lock()
	s.frames = append(s.frames, img)
	var show *image.RGBA
	if s.active && len(s.frames) == 1 {
		s.curIdx = 0
		show = img
	}
	s.mu.Unlock()
	if show != nil {
		s.enc.SetSceneBackground(show)
	}
}

// rotate advances to the next background in the pool. No-op until there are
// at least two frames to cycle between.
func (s *DiscussionStage) rotate() {
	s.mu.Lock()
	if !s.active || len(s.frames) <= 1 {
		s.mu.Unlock()
		return
	}
	s.curIdx = (s.curIdx + 1) % len(s.frames)
	img := s.frames[s.curIdx]
	s.mu.Unlock()
	if img != nil {
		s.enc.SetSceneBackground(img)
	}
}

// Run subscribes to the bus and drives the encoder until ctx is cancelled.
func (s *DiscussionStage) Run(ctx context.Context, bus *eventbus.Bus) {
	ch, cancel := bus.Subscribe(128)
	defer cancel()
	rot := time.NewTicker(discussionRotationInterval)
	defer rot.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-rot.C:
			s.rotate()
		case v, ok := <-ch:
			if !ok {
				return
			}
			if !s.accepts(v) {
				continue
			}
			if m, ok := v.(contentcreator.TopicMsg); ok {
				if isDiscussionType(m.Type) {
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
			case contentcreator.TranscriptMsg:
				s.handleTranscript(m)
			case contentcreator.PhaseMsg:
				s.enc.SetPhase(phaseChipText(m))
			case contentcreator.TickMsg:
				s.enc.SetClock(m.Elapsed, m.Elapsed+m.Remaining)
			case contentcreator.SceneAdvanceMsg:
				s.applySceneAdvance(m.Index)
			case contentcreator.DynamicSceneMsg:
				s.applyDynamicScene(m.Img)
			}
		}
	}
}

func isDiscussionType(t string) bool { return t == config.ContentTypeDiscussion }

func (s *DiscussionStage) activate() {
	s.mu.Lock()
	s.active = true
	s.mu.Unlock()
	// Cinematic layout: AI background + caption card. SceneQA gives the
	// slab-and-rule caption look (readable speaker + body), unlike the
	// outline-only surface style.
	s.enc.SetPuzzleMode(true)
	s.enc.SetPuzzleSceneName(scenes.SceneQA)
	// Override the puzzle idle pill so the warmup card reads as a discussion,
	// not "TODAY'S PUZZLE".
	s.enc.SetPuzzleIdleLabel("討論  ·  DISCUSSION")
}

func (s *DiscussionStage) idle() {
	s.mu.Lock()
	s.active = false
	s.curSpeaker, s.curRole = "", ""
	s.body.Reset()
	s.mu.Unlock()
	s.enc.SetPuzzleMode(false)
	// Restore the default idle label so a puzzle topic that follows on the
	// same channel encoder doesn't inherit the discussion pill.
	s.enc.SetPuzzleIdleLabel("")
}

func (s *DiscussionStage) isActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

func (s *DiscussionStage) accepts(v any) bool {
	if s.channelID == "" {
		return true
	}
	id := contentcreator.MsgChannelID(v)
	return id == "" || id == s.channelID
}

func (s *DiscussionStage) handleTopic(m contentcreator.TopicMsg) {
	s.enc.SetTopic(m.Title)
	s.mu.Lock()
	s.curSpeaker, s.curRole = "", ""
	s.body.Reset()
	s.curIdx = 0
	var first *image.RGBA
	if len(s.frames) > 0 {
		first = s.frames[0]
	}
	s.mu.Unlock()
	s.enc.SetSpeaker("", "", "")
	s.enc.SetBody("", 0)
	if first != nil {
		s.enc.SetSceneBackground(first)
	}
}

// applySceneAdvance selects a palette/background frame by absolute index.
// Out-of-range indices are ignored so a stray marker can't blank the screen.
func (s *DiscussionStage) applySceneAdvance(idx int) {
	s.mu.Lock()
	if idx < 0 || idx >= len(s.frames) {
		s.mu.Unlock()
		return
	}
	s.curIdx = idx
	img := s.frames[idx]
	s.mu.Unlock()
	if img != nil {
		s.enc.SetSceneBackground(img)
	}
}

// applyDynamicScene appends a freshly-generated background and shows it.
func (s *DiscussionStage) applyDynamicScene(img *image.RGBA) {
	if img == nil {
		return
	}
	s.mu.Lock()
	s.frames = append(s.frames, img)
	s.curIdx = len(s.frames) - 1
	s.mu.Unlock()
	s.enc.SetSceneBackground(img)
}

func (s *DiscussionStage) handleTranscript(m contentcreator.TranscriptMsg) {
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
	if m.Done {
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
		s.enc.SetSpeaker(m.Speaker, string(m.Role), m.Side)
		if m.Text == "" {
			s.enc.SetBody("", 0)
		}
	}
	if m.Text != "" {
		s.body.Reset()
		s.body.WriteString(m.Text)
		s.enc.SetBody(s.body.String(), m.AudioDuration)
	}
}
