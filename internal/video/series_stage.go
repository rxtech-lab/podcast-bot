package video

import (
	"context"
	"image"
	"strings"
	"sync"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/content_creator"
	"github.com/sirily11/debate-bot/internal/eventbus"
	"github.com/sirily11/debate-bot/internal/video/scenes"
)

// SeriesStage drives the encoder for content of type "series". Layout-wise
// it shares the Encoder + Renderer with the existing PuzzleStage (the
// chrome look — host-on-left, no debate sides — works the same way for a
// narrated TV-series episode), but the scene model is simpler: there is
// only one phase, "narration", with a single rotating beat list. The host
// also emits cross-episode `<season-S-episode-E-image-N/>` markers, which
// arrive as ImageRefMsg events and resolve through an in-memory map of
// pre-loaded prior-episode PNGs.
//
// Type gating mirrors PuzzleStage: only acts while the most recent
// TopicMsg.Type is `series`. Other content idles it. Two stages run per
// channel today (debate + puzzle); SeriesStage adds a third. Whichever
// matches the active topic drives the encoder.
type SeriesStage struct {
	enc       *Encoder
	channelID string

	mu         sync.Mutex
	active     bool
	curSpeaker string
	curRole    string
	body       strings.Builder

	// narration holds the per-beat freshly-generated PNGs (one entry per
	// planned narration beat). Streamed in via AttachNarrationFrame as
	// imagegen completes, and bulk-installed via AttachScenes from the
	// final scene generation result.
	narration []*image.RGBA
	curIdx    int

	// imageRefs is the cross-episode resolver map: canonical key
	// (s<S>e<E>i<N>) → in-memory PNG that the prior-episode archive
	// contains. Loaded once in cmd/debate-bot/series.go and handed to the
	// stage before the show starts. Empty / nil → ImageRefMsg events
	// become no-ops at this stage.
	imageRefs map[string]*image.RGBA

	// animations is the per-narration-beat camera-move list (parallel to
	// narration). Same protocol as PuzzleStage.surfaceAnimations.
	animations []string

	// episodeTitle caches TopicMsg.Title so handlePhase can build the
	// "本集 — {Title}" main-content section banner without re-reading
	// the bus. Reset together with the rest of per-episode state.
	episodeTitle string
}

// NewSeriesChannelStage creates a SeriesStage that filters bus events by
// channelID. Idle until a series TopicMsg arrives; switches off again on
// the next non-series topic.
func NewSeriesChannelStage(enc *Encoder, channelID string) *SeriesStage {
	return &SeriesStage{enc: enc, channelID: channelID}
}

// Run subscribes to bus and dispatches series events to the encoder.
// Returns when ctx is cancelled or the bus closes.
func (s *SeriesStage) Run(ctx context.Context, bus *eventbus.Bus) {
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
				if m.Type == config.ContentTypeSeries {
					s.activate()
					s.handleTopic(m)
				} else {
					s.idle(m.Type)
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
				s.handlePhase(m)
			case contentcreator.TickMsg:
				s.enc.SetClock(m.Elapsed, m.Elapsed+m.Remaining)
			case contentcreator.SceneAdvanceMsg:
				s.applyAdvance(m.Index)
			case contentcreator.ImageRefMsg:
				s.applyImageRef(m.Key)
			}
		}
	}
}

func (s *SeriesStage) activate() {
	s.mu.Lock()
	s.active = true
	s.mu.Unlock()
	// Reuse the puzzle layout: host alone, no debate sides, slab-style
	// subtitle treatment. The renderer's puzzle mode handles the visual
	// chrome we want for series too.
	s.enc.SetPuzzleMode(true)
	s.enc.SetPuzzleSceneName(scenes.SceneNarration)
}

// Preactivate flips the renderer into series narration mode
// synchronously — separate from the bus-driven activate() that fires
// when TopicMsg arrives. The channel runner calls it BEFORE sending the
// topic so frames rendered during the gap between TopicMsg dispatch
// and bus delivery don't briefly show the debate-style "TODAY'S TOPIC"
// idle card. Idempotent.
func (s *SeriesStage) Preactivate() {
	s.enc.SetPuzzleMode(true)
	s.enc.SetPuzzleSceneName(scenes.SceneNarration)
}

// PostEpisodeIdle parks the stage between two series episodes: caption /
// name plate cleared, scene image dropped, but puzzleMode + the
// "narration" scene name stay on so drawBackground keeps painting the
// series fallback plate (not the debate one). The channel runner calls
// this after orch.Run drains so the audience sees a clean intermission
// frame for the inter-episode pause window. Idempotent.
func (s *SeriesStage) PostEpisodeIdle() {
	s.mu.Lock()
	s.curSpeaker, s.curRole = "", ""
	s.body.Reset()
	s.curIdx = 0
	s.narration = nil
	s.animations = nil
	s.mu.Unlock()
	s.enc.SetSpeaker("", "", "")
	s.enc.SetBody("", 0)
	s.enc.SetSceneBackground(nil)
	s.enc.SetSceneAnimation("")
	s.enc.SetTopic("")
	s.enc.SetPhase("")
	s.enc.SetSeriesSectionLabel("", false)
}

func (s *SeriesStage) idle(nextType string) {
	s.mu.Lock()
	s.active = false
	s.curSpeaker, s.curRole = "", ""
	s.body.Reset()
	s.curIdx = 0
	s.mu.Unlock()
	// Puzzle content also rides the puzzleMode pipeline; only flip it off
	// when the next topic is a debate (the only mode that wants debate
	// chrome). Symmetric to PuzzleStage.idle's series carve-out — without
	// it the series→puzzle handoff would briefly drop into debate mode.
	if nextType != config.ContentTypeSituationPuzzle {
		s.enc.SetPuzzleMode(false)
	}
	s.enc.SetSceneBackground(nil)
	s.enc.SetSeriesLabel("", 0, 0, "")
	s.enc.SetSeriesSectionLabel("", false)
}

func (s *SeriesStage) isActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

func (s *SeriesStage) accepts(v any) bool {
	if s.channelID == "" {
		return true
	}
	id := contentcreator.MsgChannelID(v)
	return id == "" || id == s.channelID
}

func (s *SeriesStage) handleTopic(m contentcreator.TopicMsg) {
	s.enc.SetTopic(m.Title)
	s.enc.SetSides(m.AffNames, m.NegNames)
	s.enc.SetPositions(m.AffPosition, m.NegPosition)
	// Hand the show / season / episode + host name to the renderer so
	// it can paint the small top-left identification label that fades
	// out a few seconds in (the regular-TV-episode look). Host name is
	// the first AffNames entry, populated by buildSeriesTopicMsg.
	var hostName string
	if len(m.AffNames) > 0 {
		hostName = m.AffNames[0]
	}
	s.enc.SetSeriesLabel(m.Show, m.Season, m.Episode, hostName)
	s.mu.Lock()
	s.curSpeaker, s.curRole = "", ""
	s.body.Reset()
	// Reset per-episode state. The narration bank (and the cross-episode
	// resolver map) are cleared too — cmd/debate-bot/series.go re-installs
	// both via AttachNarrationFrame / AttachImageRefs after the new
	// episode's preparation finishes.
	s.narration = nil
	s.animations = nil
	s.imageRefs = nil
	s.curIdx = 0
	s.episodeTitle = strings.TrimSpace(m.Title)
	s.mu.Unlock()
	s.enc.SetSceneBackground(nil)
	// New episode → no recap/main banner is meaningful yet. handlePhase
	// re-installs the right one when the planner emits PhaseOpening or
	// PhaseFreeSpeech.
	s.enc.SetSeriesSectionLabel("", false)
}

func (s *SeriesStage) handlePhase(m contentcreator.PhaseMsg) {
	switch m.Phase {
	case agent.PhaseOpening:
		// Recap section banner — held until the next phase arrives.
		s.enc.SetSeriesSectionLabel("上集回顧", true)
	case agent.PhaseFreeSpeech:
		// The main narration prompt reserves frame 0 as the automatic
		// opener, so the host starts with <scene 1/> for the second
		// beat. When an optional previously-on recap runs first,
		// recap scene markers or image-reuse markers can leave a
		// different frame painted. Reset to narration[0] on the
		// listener-timed phase change so the main episode starts on
		// its intended first image.
		s.applyNarrationIndex(0)
		s.mu.Lock()
		title := s.episodeTitle
		s.mu.Unlock()
		label := "本集"
		if title != "" {
			label = "本集 — " + title
		}
		s.enc.SetSeriesSectionLabel(label, false)
	default:
		// Episode wrap-up etc.: drop the banner so it doesn't linger
		// into the post-episode idle frame.
		s.enc.SetSeriesSectionLabel("", false)
	}
}

// handleTranscript mirrors PuzzleStage.handleTranscript at the layout
// surface only — series episodes have a single speaker so there's no
// per-side colour code to look up. Done markers are dropped (same
// motivation as PuzzleStage: they can arrive ahead of the scheduled
// sentence TranscriptMsg and would otherwise blank the body for one
// frame on every speaker swap).
func (s *SeriesStage) handleTranscript(m contentcreator.TranscriptMsg) {
	if m.Done {
		return
	}
	s.mu.Lock()
	speakerKey := m.Speaker + "|" + string(m.Role)
	curKey := s.curSpeaker + "|" + s.curRole
	speakerChanged := speakerKey != curKey && m.Speaker != ""
	if speakerChanged {
		s.curSpeaker = m.Speaker
		s.curRole = string(m.Role)
		s.body.Reset()
		s.enc.SetSpeaker(m.Speaker, string(m.Role), "")
		if m.Text == "" {
			s.enc.SetBody("", 0)
		}
	}
	if m.Text != "" {
		s.body.Reset()
		s.body.WriteString(m.Text)
		s.enc.SetBody(s.body.String(), m.AudioDuration)
	}
	s.mu.Unlock()
}

// applyAdvance honours a SceneAdvanceMsg from the producer. idx >= 0 jumps
// to that absolute narration beat; idx < 0 advances by one. Out-of-range
// explicit indices clamp to the last available frame so a stray
// `<scene 99/>` against a 14-beat plan can't crash the renderer.
func (s *SeriesStage) applyAdvance(idx int) {
	if idx < 0 {
		s.mu.Lock()
		count := len(s.narration)
		if count == 0 {
			s.mu.Unlock()
			return
		}
		idx = (s.curIdx + 1) % count
		s.mu.Unlock()
	}
	s.applyNarrationIndex(idx)
}

func (s *SeriesStage) applyNarrationIndex(idx int) {
	s.mu.Lock()
	count := len(s.narration)
	if count == 0 {
		s.curIdx = 0
		s.mu.Unlock()
		return
	}
	if idx < 0 {
		idx = 0
	}
	if idx >= count {
		idx = count - 1
	}
	s.curIdx = idx
	img := s.narration[idx]
	anim := ""
	if idx >= 0 && idx < len(s.animations) {
		anim = s.animations[idx]
	}
	s.mu.Unlock()
	s.enc.SetPuzzleSceneName(scenes.SceneNarration)
	if img != nil {
		s.enc.SetSceneBackground(img)
		s.enc.SetSceneAnimation(anim)
	}
}

// applyImageRef resolves a cross-episode `<season-S-episode-E-image-N/>`
// marker through the in-memory resolver map and paints the matching
// archived frame. Unknown keys (catalog desync, missing PNG on disk)
// log silently — we leave whatever's currently painted in place rather
// than blanking the screen, which a renderer-side default would do.
func (s *SeriesStage) applyImageRef(key string) {
	s.mu.Lock()
	img := s.imageRefs[key]
	s.mu.Unlock()
	if img == nil {
		return
	}
	s.enc.SetPuzzleSceneName(scenes.SceneNarration)
	s.enc.SetSceneBackground(img)
	// Cross-episode reuse paints a still frame — the per-beat animation
	// list is keyed off the current episode's plan, not the prior, so the
	// safest default is no motion.
	s.enc.SetSceneAnimation("")
}

// AttachScenes additively installs every non-nil entry in sc.Narration on
// the stage's narration bank. Caller invokes this after a streaming gen
// pass completes; per-frame attaches via AttachNarrationFrame already may
// have populated some slots.
func (s *SeriesStage) AttachScenes(sc *scenes.PuzzleScenes) {
	if sc == nil {
		return
	}
	s.mu.Lock()
	if n := len(sc.Narration); n > 0 {
		if n > len(s.narration) {
			grown := make([]*image.RGBA, n)
			copy(grown, s.narration)
			s.narration = grown
		}
		for i, img := range sc.Narration {
			if img != nil {
				s.narration[i] = img
			}
		}
	}
	active := s.active
	curIdx := s.curIdx
	var apply *image.RGBA
	if active && curIdx >= 0 && curIdx < len(s.narration) {
		apply = s.narration[curIdx]
	}
	s.mu.Unlock()
	if apply != nil {
		s.enc.SetSceneBackground(apply)
	}
}

// AttachNarrationFrame installs a single narration variant produced by
// the streaming gen path. Mirrors PuzzleStage.AttachSurfaceFrame.
func (s *SeriesStage) AttachNarrationFrame(variant int, img *image.RGBA) {
	if img == nil || variant < 0 {
		return
	}
	s.mu.Lock()
	if variant >= len(s.narration) {
		grown := make([]*image.RGBA, variant+1)
		copy(grown, s.narration)
		s.narration = grown
	}
	s.narration[variant] = img
	active := s.active
	curIdx := s.curIdx
	s.mu.Unlock()
	if !active || curIdx != variant {
		return
	}
	// The renderer is currently parked on this exact slot (an earlier
	// `<scene N/>` marker landed before the image had finished
	// generating). Repaint now that the frame is available.
	s.enc.SetSceneBackground(img)
}

// AttachAnimations records the planner's per-beat camera-move list.
// Empty / nil disables motion (renderer holds the still image).
func (s *SeriesStage) AttachAnimations(anims []string) {
	if len(anims) == 0 {
		return
	}
	s.mu.Lock()
	s.animations = append(s.animations[:0], anims...)
	s.mu.Unlock()
}

// AttachImageRefs installs the cross-episode resolver map. Each entry
// maps a canonical image-ref key (s<S>e<E>i<N>, see
// contentcreator.ImageRefKey) to an in-memory *image.RGBA pre-loaded
// from the prior episode's archive. Empty / nil disables image-reuse
// painting for this episode.
func (s *SeriesStage) AttachImageRefs(refs map[string]*image.RGBA) {
	if len(refs) == 0 {
		return
	}
	s.mu.Lock()
	if s.imageRefs == nil {
		s.imageRefs = make(map[string]*image.RGBA, len(refs))
	}
	for k, v := range refs {
		if v != nil {
			s.imageRefs[k] = v
		}
	}
	s.mu.Unlock()
}
