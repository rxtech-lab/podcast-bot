package video

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/debate"
	"github.com/sirily11/debate-bot/internal/eventbus"
	"github.com/sirily11/debate-bot/internal/video/scenes"
)

// sceneRotationInterval is how long each variant of a multi-variant scene
// (surface / conclusion) stays on screen before the stage swaps to the next
// one. Tuned so a 240s briefing turn cycles through all four surface frames
// roughly twice and shorter conclusion sequences still see at least one swap.
const sceneRotationInterval = 30 * time.Second

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

	// Scene backgrounds for the active puzzle topic. Generated async by
	// the caller (cmd/debate-bot) via internal/video/scenes and handed
	// over via AttachScenes when ready. nil until ready; setSceneFor
	// silently no-ops on nil so the renderer keeps its default bg until
	// generation completes.
	sceneScenes *scenes.PuzzleScenes
	curScene    string
	curSceneIdx int

	// rotateCancel stops the goroutine that swaps multi-variant scenes
	// (surface, conclusion) on a timer. nil when no rotation is active.
	// rotateGen is bumped on every (re)start so a stale goroutine that
	// loses the cancel race notices its generation no longer matches and
	// exits without applying a stale image.
	rotateCancel context.CancelFunc
	rotateGen    int
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
				s.enc.SetPhase(phaseChipText(m))
				s.setSceneFor(phaseToScene(m.Phase))
			case debate.TickMsg:
				s.enc.SetClock(m.Elapsed, m.Elapsed+m.Remaining)
			}
		}
	}
}

func isPuzzleType(t string) bool {
	return t == config.ContentTypeSituationPuzzle
}

// phaseChipText returns the on-frame label for the phase pill. Prefers
// the server-stamped human label (PhaseMsg.Label) so the on-air chip and
// the SSE phase event always agree. Falls back to the raw phase id for
// the (rare) case of an unstamped event — the renderer's phaseLabel()
// will translate a raw id into Chinese on the way out.
func phaseChipText(m debate.PhaseMsg) string {
	if m.Label != "" {
		return m.Label
	}
	return m.Phase.String()
}

func (s *PuzzleStage) activate() {
	s.mu.Lock()
	s.active = true
	s.mu.Unlock()
	s.enc.SetPuzzleMode(true)
}

func (s *PuzzleStage) idle() {
	s.stopSceneRotation()
	s.mu.Lock()
	s.active = false
	s.curSpeaker, s.curRole = "", ""
	s.curScene = ""
	s.curSceneIdx = 0
	s.body.Reset()
	s.mu.Unlock()
	// Reset puzzle layout so a subsequent debate topic on the same encoder
	// renders with the standard CNN chrome.
	s.enc.SetPuzzleMode(false)
	s.enc.SetSceneBackground(nil)
}

// AttachScenes hands pre-generated scene images to the stage. Caller is
// cmd/debate-bot, which kicks off scenes.Generate asynchronously when a
// puzzle topic is admitted and calls AttachScenes on completion. Safe to
// call before or after the topic activates — the surface scene is applied
// immediately if the stage is currently active.
func (s *PuzzleStage) AttachScenes(sc *scenes.PuzzleScenes) {
	s.mu.Lock()
	s.sceneScenes = sc
	active := s.active
	cur := s.curScene
	s.mu.Unlock()
	if active {
		// Apply the appropriate scene for the current phase. If a phase
		// has already been seen, use it; otherwise default to surface.
		name := cur
		if name == "" {
			name = scenes.SceneSurface
		}
		s.applyScene(name, 0)
		s.maybeStartSceneRotation(name)
	}
}

// setSceneFor applies the scene image keyed by name to the encoder if
// scenes are loaded. Records the name so AttachScenes called later can
// pick the right one even if PhaseMsg arrived before generation finished.
// On a real scene change (different name from the current one) the variant
// counter resets to 0 and any prior rotation is stopped — the new phase
// gets its own rotation if it's a multi-variant one.
func (s *PuzzleStage) setSceneFor(name string) {
	if name == "" {
		return
	}
	s.mu.Lock()
	if s.curScene == name {
		s.mu.Unlock()
		return
	}
	s.curScene = name
	s.curSceneIdx = 0
	s.mu.Unlock()
	s.stopSceneRotation()
	s.applyScene(name, 0)
	s.maybeStartSceneRotation(name)
}

// applyScene blits the indexed variant of the named scene through the
// encoder. Silently no-ops if scenes haven't been attached yet or the
// variant slot is empty.
func (s *PuzzleStage) applyScene(name string, idx int) {
	s.mu.Lock()
	sc := s.sceneScenes
	s.mu.Unlock()
	if sc == nil {
		return
	}
	img := sc.ByNameIdx(name, idx)
	if img == nil {
		return
	}
	s.enc.SetSceneBackground(img)
}

// maybeStartSceneRotation kicks off a goroutine that swaps to the next
// variant of name every sceneRotationInterval, but only if the scene has
// more than one variant. Scenes with a single image (qa, reveal) skip
// rotation entirely. Caller is responsible for having called
// stopSceneRotation first if a different scene was previously active.
func (s *PuzzleStage) maybeStartSceneRotation(name string) {
	s.mu.Lock()
	sc := s.sceneScenes
	s.mu.Unlock()
	if sc == nil {
		return
	}
	count := sc.VariantCount(name)
	if count <= 1 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	if s.rotateCancel != nil {
		s.rotateCancel()
	}
	s.rotateCancel = cancel
	s.rotateGen++
	gen := s.rotateGen
	s.mu.Unlock()
	go s.rotateSceneLoop(ctx, gen, name, count)
}

// stopSceneRotation halts any active rotation goroutine. Idempotent. The
// generation counter on the stage is what makes a stale goroutine that
// already read its tick channel exit on its next pass — cancel is just
// the fast path.
func (s *PuzzleStage) stopSceneRotation() {
	s.mu.Lock()
	cancel := s.rotateCancel
	s.rotateCancel = nil
	s.rotateGen++
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// rotateSceneLoop is the goroutine body started by maybeStartSceneRotation.
// On every tick it advances the variant index by one (mod count) and
// applies it. The gen check guards against a race where stopSceneRotation
// fires between the tick and the apply; without it the new scene's first
// frame could be clobbered by the previous scene's stale tick.
func (s *PuzzleStage) rotateSceneLoop(ctx context.Context, gen int, name string, count int) {
	t := time.NewTicker(sceneRotationInterval)
	defer t.Stop()
	idx := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			idx = (idx + 1) % count
			s.mu.Lock()
			if s.rotateGen != gen || s.curScene != name {
				s.mu.Unlock()
				return
			}
			s.curSceneIdx = idx
			s.mu.Unlock()
			s.applyScene(name, idx)
		}
	}
}

// phaseToScene maps planner phases to scene names. Mirrors the four
// scenes generated by internal/video/scenes.Generate.
func phaseToScene(p agent.Phase) string {
	switch p {
	case agent.PhaseSetup, agent.PhaseOpening:
		return scenes.SceneSurface
	case agent.PhaseFreeSpeech:
		return scenes.SceneQA
	case agent.PhaseVerdict:
		return scenes.SceneReveal
	case agent.PhaseEnded, agent.PhaseConclusion:
		return scenes.SceneConclusion
	}
	return ""
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
	// Default to the surface scene on topic admission. If scenes haven't
	// been generated yet, this no-ops and PhaseMsg/AttachScenes pick it up.
	s.setSceneFor(scenes.SceneSurface)
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

	// Done markers are sent immediately after produce() returns, so they can
	// arrive ahead of (and interleaved with) the AfterFunc-scheduled
	// sentence TranscriptMsgs of the same or next turn. If we let a Done
	// for turn N+1 reach the speaker-change branch below, it flips the
	// active speaker before turn N's last sentence text has even fired,
	// clearing the body and making the puzzle Q&A subtitle visibly
	// disappear mid-audio. They carry no Text and no AudioDuration —
	// nothing for the on-air layout to do — so drop them here. Other bus
	// subscribers (transcript log, web feed) still see the marker.
	if m.Done {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	speakerKey := m.Speaker + "|" + string(m.Role)
	curKey := s.curSpeaker + "|" + s.curRole

	speakerChanged := speakerKey != curKey && m.Speaker != ""
	if speakerChanged {
		s.curSpeaker = m.Speaker
		s.curRole = string(m.Role)
		s.body.Reset()
		s.enc.SetSpeaker(m.Speaker, string(m.Role), "")
		// Skip clearing the body when this same call carries the new
		// sentence text — the SetBody below installs it atomically. The
		// older "SetBody("", 0) then SetBody(text, dur)" pattern produced
		// a microsecond window of empty body that an unlucky 30 fps
		// Frame() could capture, presenting as the QA subtitle blinking
		// out for one frame on every speaker swap.
		if m.Text == "" {
			s.enc.SetBody("", 0)
		}
	}

	if m.Text != "" {
		// Each TranscriptMsg is scheduled by the producer to fire when
		// this sentence's first audio byte reaches the listener (see
		// pipeline.synthSentence's playhead). Replacing the body each
		// time keeps the visible subtitle showing exactly the sentence
		// currently being spoken — the splitter's MinChars=6 guard
		// prevents single-character flicker for the puzzle host's
		// "是。"/"不是。" prefix.
		s.body.Reset()
		s.body.WriteString(m.Text)
		s.enc.SetBody(s.body.String(), m.AudioDuration)
	}
}
