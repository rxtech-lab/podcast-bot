package video

import (
	"context"
	"image"
	"strings"
	"sync"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/content_creator"
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

	// qaSurfaceMode is true once a `<scene N/>` marker during the QA
	// phase has switched the displayed background from the dedicated QA
	// mood image to a surface-bank variant. The default on QA entry is
	// false (show s.QA); the host flips it by emitting a marker tied to
	// the current question's topic. setSceneFor resets it on every
	// phase change so re-entering QA later starts from the default again.
	qaSurfaceMode bool

	// surfaceAnimations is the planner's per-surface-frame camera move
	// list (parallel to sceneScenes.Surface). applySceneAdvance reads
	// the slot at the new index and forwards the value to the encoder
	// so each surface beat's image plays with its planned pan / zoom.
	// Empty / shorter than Surface means "no plan for that beat" — the
	// renderer holds the still image (stall semantics).
	surfaceAnimations []string

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
			if m, ok := v.(contentcreator.TopicMsg); ok {
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
			case contentcreator.TranscriptMsg:
				s.handleTranscript(m)
			case contentcreator.PhaseMsg:
				s.enc.SetPhase(phaseChipText(m))
				s.setSceneFor(phaseToScene(m.Phase))
			case contentcreator.TickMsg:
				s.enc.SetClock(m.Elapsed, m.Elapsed+m.Remaining)
			case contentcreator.SceneAdvanceMsg:
				s.applySceneAdvance(m.Index)
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
func phaseChipText(m contentcreator.PhaseMsg) string {
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
	s.qaSurfaceMode = false
	s.body.Reset()
	s.mu.Unlock()
	// Reset puzzle layout so a subsequent debate topic on the same encoder
	// renders with the standard CNN chrome.
	s.enc.SetPuzzleMode(false)
	s.enc.SetSceneBackground(nil)
}

// AttachScenes hands pre-generated scene images to the stage. Caller is
// cmd/debate-bot, which kicks off scene generation asynchronously when a
// puzzle topic is admitted and calls AttachScenes on completion. Safe to
// call before or after the topic activates — the active scene is applied
// immediately if the stage is currently active.
//
// Additive merge: each call writes only the non-nil entries of sc into
// the stage's accumulated PuzzleScenes. Per-index merge for the slice
// fields (Surface / Conclusion) so a streaming caller that already
// installed individual frames via AttachSurfaceFrame doesn't have those
// frames clobbered by a later wholesale attach with mostly-nil slots.
func (s *PuzzleStage) AttachScenes(sc *scenes.PuzzleScenes) {
	if sc == nil {
		return
	}
	s.mu.Lock()
	if s.sceneScenes == nil {
		s.sceneScenes = &scenes.PuzzleScenes{}
	}
	if n := len(sc.Surface); n > 0 {
		if n > len(s.sceneScenes.Surface) {
			grown := make([]*image.RGBA, n)
			copy(grown, s.sceneScenes.Surface)
			s.sceneScenes.Surface = grown
		}
		for i, img := range sc.Surface {
			if img != nil {
				s.sceneScenes.Surface[i] = img
			}
		}
	}
	if sc.QA != nil {
		s.sceneScenes.QA = sc.QA
	}
	if sc.Reveal != nil {
		s.sceneScenes.Reveal = sc.Reveal
	}
	if n := len(sc.Conclusion); n > 0 {
		if n > len(s.sceneScenes.Conclusion) {
			grown := make([]*image.RGBA, n)
			copy(grown, s.sceneScenes.Conclusion)
			s.sceneScenes.Conclusion = grown
		}
		for i, img := range sc.Conclusion {
			if img != nil {
				s.sceneScenes.Conclusion[i] = img
			}
		}
	}
	active := s.active
	cur := s.curScene
	curIdx := s.curSceneIdx
	s.mu.Unlock()
	if active {
		// Apply the appropriate scene for the current phase. If a phase
		// has already been seen, use it; otherwise default to surface.
		// Preserve curSceneIdx — resetting to 0 here would yank the
		// surface scene back to its first variant every time a downstream
		// phase's images stream in, producing a visible jump-cut and
		// crossfade-flicker mid-narration.
		name := cur
		if name == "" {
			name = scenes.SceneSurface
		}
		idx := curIdx
		if name != cur {
			idx = 0
		}
		s.applyScene(name, idx)
		s.maybeStartSceneRotation(name)
	}
}

// AttachSurfaceFrame installs a single surface variant produced by the
// streaming gen path. Used by cmd/debate-bot to hand frames to the stage as
// they finish so the show can start once the first N priority variants land
// without waiting for the slowest frames in the tail. Out-of-range indices
// grow the underlying slice (subsequent indices remain nil — ByNameIdx skips
// them, ByNameIdxExact returns nil so applySceneAdvance leaves the current
// background in place). No-op for a nil image.
func (s *PuzzleStage) AttachSurfaceFrame(variant int, img *image.RGBA) {
	if img == nil || variant < 0 {
		return
	}
	s.mu.Lock()
	if s.sceneScenes == nil {
		s.sceneScenes = &scenes.PuzzleScenes{}
	}
	if variant >= len(s.sceneScenes.Surface) {
		grown := make([]*image.RGBA, variant+1)
		copy(grown, s.sceneScenes.Surface)
		s.sceneScenes.Surface = grown
	}
	s.sceneScenes.Surface[variant] = img
	active := s.active
	cur := s.curScene
	curIdx := s.curSceneIdx
	qaSurface := s.qaSurfaceMode
	s.mu.Unlock()
	if !active {
		return
	}
	// If the renderer is currently parked on this exact slot (because an
	// earlier <scene N/> marker landed before the image had finished
	// generating), repaint now that the frame is available. Same logic
	// applies during QA-surface-mode: a marker may have parked QA on
	// surface[variant] before that frame finished generating.
	if cur == scenes.SceneSurface && curIdx == variant {
		s.applyScene(scenes.SceneSurface, variant)
	}
	if cur == scenes.SceneQA && qaSurface && curIdx == variant {
		s.applyScene(scenes.SceneQA, variant)
	}
}

// AttachConclusion fills in the conclusion variant slice on a previously-
// attached PuzzleScenes. Called by cmd/debate-bot when the deferred
// conclusion-image generation finishes — the podcast can already be in
// flight at that point because surface assets unblock the run. If the stage
// is already in the conclusion phase, paints frame 0; subsequent frames
// are advanced by `<scene/>` markers in the host's conclusion narration
// (see advanceScene), so no timer rotation is started here.
func (s *PuzzleStage) AttachConclusion(imgs []*image.RGBA) {
	s.mu.Lock()
	if s.sceneScenes == nil {
		s.sceneScenes = &scenes.PuzzleScenes{}
	}
	if n := len(imgs); n > 0 {
		if n > len(s.sceneScenes.Conclusion) {
			grown := make([]*image.RGBA, n)
			copy(grown, s.sceneScenes.Conclusion)
			s.sceneScenes.Conclusion = grown
		}
		for i, img := range imgs {
			if img != nil {
				s.sceneScenes.Conclusion[i] = img
			}
		}
	}
	active := s.active
	cur := s.curScene
	s.mu.Unlock()
	if !active || cur != scenes.SceneConclusion {
		return
	}
	s.applyScene(scenes.SceneConclusion, 0)
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
	s.qaSurfaceMode = false
	s.mu.Unlock()
	s.stopSceneRotation()
	s.applyScene(name, 0)
	s.maybeStartSceneRotation(name)
}

// applySceneAdvance honours a SceneAdvanceMsg from the producer. When idx
// >= 0 the stage jumps directly to that absolute variant index (clamped
// into [0, count-1]) so numbered `<scene N/>` markers from the host land
// on the planner-aligned frame even if the host skips, repeats, or
// reorders beats. When idx < 0 (legacy unnumbered marker) the stage
// falls back to incrementing the current variant by one — preserving
// the original "advance by one" semantics. The Reveal singleton ignores
// the call. The QA scene routes the marker into the surface bank so the
// host can switch the background to track the current question's topic
// (see applyQASurfaceAdvance).
func (s *PuzzleStage) applySceneAdvance(idx int) {
	s.mu.Lock()
	sc := s.sceneScenes
	name := s.curScene
	s.mu.Unlock()
	if sc == nil || name == "" {
		return
	}
	if name == scenes.SceneQA {
		s.applyQASurfaceAdvance(idx, sc)
		return
	}
	count := sc.VariantCount(name)
	if count <= 1 {
		return
	}
	s.mu.Lock()
	switch {
	case idx >= 0:
		// Clamp into range so an LLM emitting `<scene 99/>` against a
		// 14-frame plan doesn't crash — clamp to the last available
		// frame instead.
		if idx >= count {
			idx = count - 1
		}
		s.curSceneIdx = idx
	default:
		s.curSceneIdx = (s.curSceneIdx + 1) % count
	}
	applyIdx := s.curSceneIdx
	s.mu.Unlock()
	// Honour the exact requested slot. When streaming surface gen is
	// still running the targeted frame may not exist yet — in that case
	// keep whatever background is currently on screen instead of jumping
	// to a different beat. AttachSurfaceFrame repaints once the frame
	// lands.
	anim := s.animationFor(name, applyIdx)
	if img := sc.ByNameIdxExact(name, applyIdx); img != nil {
		s.enc.SetPuzzleSceneName(name)
		s.enc.SetSceneBackground(img)
		s.enc.SetSceneAnimation(anim)
	} else {
		s.enc.SetPuzzleSceneName(name)
	}
}

// applyQASurfaceAdvance switches the QA background from the dedicated
// QA mood image to surface-bank variant idx. The Q&A loop runs minutes
// long; without this the same atmospheric still sits behind every
// question. The host (or orchestrator) emits `<scene N/>` when the
// question shifts to a new aspect of the scenario and the renderer
// jumps to that surface variant, then sticks until the next marker.
//
// idx < 0 (legacy unnumbered marker) advances by one through the
// surface bank. Out-of-range explicit indices are clamped. The puzzle
// scene name stays "qa" so the renderer keeps the slab-and-rule
// subtitle treatment — only the picture changes. Surface camera-move
// animations are not applied during QA: the bank's pan/zoom is paced
// for the surface narration's tempo, which doesn't fit the shorter
// question/answer beats.
func (s *PuzzleStage) applyQASurfaceAdvance(idx int, sc *scenes.PuzzleScenes) {
	count := len(sc.Surface)
	if count == 0 {
		return
	}
	s.mu.Lock()
	switch {
	case idx >= 0:
		if idx >= count {
			idx = count - 1
		}
		s.curSceneIdx = idx
	default:
		// Wrap forward through the surface bank. Resets to slot 0 when
		// we've already iterated through every variant — keeps the
		// rotation closed even on a long Q&A loop.
		s.curSceneIdx = (s.curSceneIdx + 1) % count
	}
	s.qaSurfaceMode = true
	applyIdx := s.curSceneIdx
	s.mu.Unlock()
	s.enc.SetPuzzleSceneName(scenes.SceneQA)
	if img := sc.ByNameIdxExact(scenes.SceneSurface, applyIdx); img != nil {
		s.enc.SetSceneBackground(img)
		s.enc.SetSceneAnimation("")
	}
}

// applyScene blits the indexed variant of the named scene through the
// encoder. Silently no-ops if scenes haven't been attached yet or the
// variant slot is empty. Also forwards the scene name so the renderer
// can apply scene-specific subtitle treatment (surface = black-outline
// caption with no slab; others = HBO quote card) and the per-beat
// camera-move animation when one is configured for this slot.
//
// QA-in-surface-mode: once a `<scene N/>` marker has flipped QA into
// surface-mode, subsequent re-applies (e.g. a late AttachSurfaceFrame
// streaming in slot N after applyQASurfaceAdvance parked on it) source
// the image from the surface bank rather than the dedicated QA still.
func (s *PuzzleStage) applyScene(name string, idx int) {
	s.mu.Lock()
	sc := s.sceneScenes
	qaSurface := s.qaSurfaceMode
	s.mu.Unlock()
	s.enc.SetPuzzleSceneName(name)
	if sc == nil {
		return
	}
	if name == scenes.SceneQA && qaSurface {
		if img := sc.ByNameIdxExact(scenes.SceneSurface, idx); img != nil {
			s.enc.SetSceneBackground(img)
			s.enc.SetSceneAnimation("")
		}
		return
	}
	img := sc.ByNameIdx(name, idx)
	if img == nil {
		return
	}
	s.enc.SetSceneBackground(img)
	s.enc.SetSceneAnimation(s.animationFor(name, idx))
}

// animationFor returns the planner-supplied animation kind for the
// given scene + variant index. Today only the surface phase has a
// per-beat plan; other phases (qa, reveal, conclusion) hold their
// still image (empty string → renderer treats as stall). Out-of-range
// indices fall back to stall as well.
func (s *PuzzleStage) animationFor(name string, idx int) string {
	if name != scenes.SceneSurface {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if idx < 0 || idx >= len(s.surfaceAnimations) {
		return ""
	}
	return s.surfaceAnimations[idx]
}

// AttachSurfaceAnimations records the planner's per-surface-frame
// camera-move list. Caller (cmd/debate-bot) hands this to the stage
// alongside the scene plan so each surface beat's image plays with
// its planned pan / zoom move when the host emits the matching
// `<scene N/>` marker. Empty / nil disables the feature; the
// renderer holds the still image instead. Safe to call before or
// after AttachScenes — the next applyScene / applySceneAdvance picks
// up the new list.
func (s *PuzzleStage) AttachSurfaceAnimations(anims []string) {
	if len(anims) == 0 {
		return
	}
	s.mu.Lock()
	s.surfaceAnimations = append(s.surfaceAnimations[:0], anims...)
	s.mu.Unlock()
}

// maybeStartSceneRotation kicks off a goroutine that swaps to the next
// variant of name every sceneRotationInterval, but only if the scene has
// more than one variant. Scenes with a single image (qa, reveal) skip
// rotation entirely. Surface AND conclusion also skip: both narration
// phases are driven by scene-switch markers in the host's transcript
// (see advanceScene) so the cuts land on paragraph beats rather than a
// wall clock. Caller is responsible for having called stopSceneRotation
// first if a different scene was previously active.
func (s *PuzzleStage) maybeStartSceneRotation(name string) {
	if name == scenes.SceneSurface || name == scenes.SceneConclusion {
		return
	}
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
	id := contentcreator.MsgChannelID(v)
	return id == "" || id == s.channelID
}

// handleTopic primes the encoder with the puzzle's framing. AffNames/NegNames
// come from buildTopicMsg already mapped (host on the left, players on the
// right); AffPosition carries the soup-surface (湯面) so viewers can read the
// scenario the whole round. We deliberately do NOT pass the truth (湯底) to
// any rendering surface — only the puzzle host's LLM prompt sees it.
func (s *PuzzleStage) handleTopic(m contentcreator.TopicMsg) {
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
func (s *PuzzleStage) handleTranscript(m contentcreator.TranscriptMsg) {
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
