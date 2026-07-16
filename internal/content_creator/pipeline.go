package contentcreator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/audio"
	"github.com/sirily11/debate-bot/internal/audio/musicmixer"
	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/llm"
	"github.com/sirily11/debate-bot/internal/tts"
)

// Deps are everything the pipeline needs to run.
type Deps struct {
	Planner  Planner
	Tracker  *Tracker
	Registry *agent.Registry
	TTS      tts.Provider
	OutDir   string
	Send     func(any) // event-bus publish wrapper
	Log      *slog.Logger
	Topic    string
	Language string
	// Background and SourceDocuments ground discussion agents in the plan's
	// background paragraphs and the user's uploaded reference documents.
	// Set only for the discussion content type (other formats use Background
	// for their own purposes); both flow onto every SpeakPrompt and render
	// as conditional prompt sections — empty means byte-identical prompts.
	Background      string
	SourceDocuments string
	// ContentType is the topic.Type discriminator (config.ContentType*).
	// Stamped onto PhaseMsg so the frontend can label phases without
	// hardcoding the per-format mapping.
	ContentType string
	// AudioOnly marks an audio-only feed whose audio.mp3 is recorded straight
	// from the LiveStream at t=0 with no stitch StartOffset trim, so vttBias
	// (a trim-compensation offset) must not be applied to its sidecar cues.
	AudioOnly  bool
	Transcript *Transcript
	LiveStream *audio.LiveStream // shared mp3 broadcaster (paced by ffmpeg -re)

	// MusicPaths maps planner directive prefix → mp3 file path for turns
	// that should play with a Lyria-generated background bed mixed under
	// the host's TTS. Today the situation-puzzle planner uses keys
	// "surface" and "reveal"; other content types leave this nil.
	// pipeline.produce looks the key up by t.Directive (matching either
	// the bare directive or its prefix before any ":") and routes that
	// turn's TTS through musicmixer.New. Empty/missing key → dry TTS.
	MusicPaths map[string]string

	// SurfaceFrames is the visual director's surface-frame count for the
	// current puzzle. The pipeline caps SceneAdvanceMsg events emitted
	// from the surface narration at SurfaceFrames-1 so excess markers
	// from the host LLM don't wrap the rotation back to frame 0 mid-show.
	// 0 disables the cap (no plan available, accept whatever the host
	// emits).
	SurfaceFrames int
	// ConclusionFrames is the same idea for the conclusion phase. The
	// conclusion now reads as a longer reflective epilogue with scene
	// markers driving the image rotation; the pipeline caps marker count
	// at ConclusionFrames-1.
	ConclusionFrames int
	// NarrationFrames is the visual director's per-episode narration-frame
	// count for the series content type. Mirrors SurfaceFrames. The pipeline
	// caps SceneAdvanceMsg events emitted from a `narrate` directive at
	// NarrationFrames-1 so excess markers from the host LLM don't wrap the
	// rotation back to frame 0 mid-episode. 0 disables the cap.
	NarrationFrames int

	// HasSeriesPreviouslyOn means this series episode includes the optional
	// opening recap turn. The stitched mp4 lands soft subtitles slightly early
	// on those episodes, so the VTT sidecar gets a small extra delay.
	HasSeriesPreviouslyOn bool

	// SoundPaths is the planner's per-cue clip list — index N is the
	// on-disk mp3 path the mixer plays when the host emits
	// "<sound-overlapped-N/>" or "<sound-replace-N/>". Nil / empty
	// disables the feature (host's prompt omits the sound section so
	// no markers appear in the stream). Paths that don't exist are
	// dropped at dispatch time with a warning rather than failing the
	// turn.
	SoundPaths []string

	// AudioBookImageURLs maps scene-beat index → illustration URL for an
	// audiobook run. When the host fires `<scene N/>`, the pipeline emits an
	// image-only TranscriptMsg carrying AudioBookImageURLs[N] so the chat
	// transcript shows the picture at that point in the narration. Nil/empty
	// for non-audiobook runs; an empty entry means "no image for this beat".
	AudioBookImageURLs []string
	// AudioBookImageCaptions maps scene-beat index → the planner's short
	// description for that illustration. It is carried as the transcript text
	// for image rows so clients can caption the active artwork without another
	// fetch.
	AudioBookImageCaptions []string

	// RecordAudioBookImageOffset, when non-nil, is called each time an
	// audiobook scene image is emitted, with the beat index and its position
	// on the produced audio timeline in seconds (VTT cue space). The
	// orchestrator collects these so the video post-pass can hold each
	// illustration for exactly the span it was on screen during the live run.
	RecordAudioBookImageOffset func(beat int, seconds float64)
}

// surfaceTTSScale is the multiplier applied to the mixer's default
// TTS gain during a puzzle host's "surface" turn. 0.6 → speaker drops
// to 60% so the music bed and any planner-generated sound cues sit
// more forward in the mix while still keeping the narration
// intelligible. Other turns (Q&A, reveal, conclusion, debate format)
// run at full TTS volume.
const surfaceTTSScale = 0.6

// subtitleClientLatency compensates for buffering that happens after the
// LiveStream's stdout — primarily the browser MediaSource source buffer
// (~1.5–2s on Chromium for low-bitrate MP3) and any OS audio buffering.
// The renderer's TranscriptMsg dispatch is delayed by bytesAhead/rate +
// subtitleClientLatency so the subtitle change lands when the listener
// actually starts hearing the new sentence.
//
// Bumped 1500ms → 2300ms → 3100ms. The latest bump was driven by the
// puzzle Q&A and conclusion sections: their short turns mean BytesAhead
// drops near zero between turns, so the bytesAhead/rate term contributes
// almost nothing and clientLatency alone has to cover the full
// browser/OS buffering chain. With turns < 3s the constant becomes the
// dominant offset and 2300ms wasn't enough.
const subtitleClientLatency = 3100 * time.Millisecond

// postProducerGrace is the breathing window held between the LLM
// closing its stream ("producer drained") and the mixer teardown.
// The mixer + LiveStream keep several seconds of decoded TTS PCM /
// mp3 buffered behind the listener; closing immediately would cut
// the last sentence mid-word. A flat sleep is simpler and more
// reliable than the previous mixer-Close-then-poll-BytesAhead path,
// which has shown pathological hangs that pin the channel runner.
// A var (not const) so integration tests can shrink the tail wait.
var postProducerGrace = 20 * time.Second

const vttBaseBias = 1 * time.Second
const vttPreviouslyOnBias = 1 * time.Second

// vttDiscussionBias is an extra sidecar-subtitle delay applied only to
// discussion content. The cue offset is anchored to LiveStream's first
// write plus vttBaseBias, a gap tuned for debate/series where the
// encoder begins right as the producer starts writing. Discussion runs
// its background-asset prep before the orchestrator, so first-real-audio
// lands later relative to the encoder start than the base bias assumes —
// the sidecar .vtt ended up ~1.5s ahead of the stitched mp4's audio.
// This nudge realigns it. Only affects the recorded .vtt sidecar; live
// subtitle dispatch uses targetSend directly and is unchanged.
const vttDiscussionBias = 1500 * time.Millisecond

// cleanupHardCap caps the total wall time spent in the post-producer
// cleanup tail (grace sleep + sessionMixer.Close + waitAudioDrained).
// Each step has its own internal timeout, but those have been observed
// to compound or fail to fire in pathological mixer/encoder states,
// pinning the channel runner forever. Once this cap is reached the
// pipeline returns regardless — at worst the listener loses ~0.5s of
// tail audio at the handoff, which is strictly better than the
// channel hanging and never starting the next debate.
const cleanupHardCap = 90 * time.Second

// Pipeline owns the goroutines for produce/memory stages.
type Pipeline struct {
	d Deps
	// sessionMixer wraps LiveStream with a long-lived ffmpeg amix process
	// that keeps a looped background music bed underneath every TTS turn.
	// nil means no music is configured; produce() falls through to writing
	// directly into LiveStream.
	sessionMixer *musicmixer.Mixer

	// vtt accumulates one WebVTT cue per synthesised sentence so the run
	// emits a sidecar subtitles.vtt next to debate.mp3. Allows clients to
	// toggle captions off in their player while the burn-in subtitle the
	// renderer paints stays unchanged. Always non-nil; WriteTo no-ops on
	// an empty cue list (no caption file is written for an audio-only
	// run with zero TTS output).
	vtt *vttWriter

	// openingImageEmitted marks that the audiobook's beat-0 illustration was
	// already surfaced at the start of the first narration turn. Only touched
	// from produce(), which runs turns serially.
	openingImageEmitted bool
	// audioBookMaxSceneIndex is the largest explicit `<scene N/>` accepted
	// across audiobook turns. Completion is honored only after this reaches
	// the final planned illustration beat.
	audioBookMaxSceneIndex int

	// nextPlayAt is the wall-clock moment the next-to-be-synthesized
	// sentence's first audio byte is expected to reach the listener.
	// Advanced by each sentence's audio duration after synth so
	// back-to-back sentences within a turn schedule serially even when
	// the bytes are still buffered inside the music mixer (the mixer's
	// internal ffmpeg pipeline holds many seconds of audio that
	// LiveStream.BytesAhead can't see, so naïvely deriving targetSend
	// from BytesAhead alone makes every sentence fire at roughly
	// now+clientLatency and the subtitle jumps speakers before the
	// previous speaker's audio drains). Resynced at each call against
	// LiveStream.BytesAhead so any silence-pad the pump inserted during
	// inter-turn idle still counts toward when the next chunk plays.
	playheadMu sync.Mutex
	nextPlayAt time.Time

	// ttsTimelinePos is the cumulative duration of synthesized TTS audio
	// written so far, derived purely from byte counts (bytes ÷
	// audio.AudioBytesPerSec) — the exact position the NEXT sentence
	// starts at on the TTS timeline. Audio-only subtitle cues are stamped
	// with this and translated to the recorded file's timeline via
	// recordedPos. Guarded by playheadMu alongside nextPlayAt.
	ttsTimelinePos time.Duration

	stopMu      sync.Mutex
	stopProduce context.CancelFunc
}

// NewPipeline creates a Pipeline.
func NewPipeline(d Deps) *Pipeline { return &Pipeline{d: d, vtt: newVTTWriter()} }

// ForceStop stops planning and any in-flight turn generation. Cleanup and
// artifact finalization continue under the parent job context.
func (p *Pipeline) ForceStop() {
	if p == nil {
		return
	}
	p.stopMu.Lock()
	stop := p.stopProduce
	p.stopMu.Unlock()
	if stop != nil {
		stop()
	}
}

func (p *Pipeline) plannerDone() bool {
	if p == nil || p.d.Planner == nil {
		return false
	}
	done, ok := p.d.Planner.(interface{ Done() bool })
	return ok && done.Done()
}

func (p *Pipeline) validateAudioBookCompletionRequest(t *Turn) {
	if p == nil || t == nil || p.d.ContentType != config.ContentTypeAudioBook {
		return
	}
	if t.maxSceneIndex > p.audioBookMaxSceneIndex {
		p.audioBookMaxSceneIndex = t.maxSceneIndex
	}
	validator, ok := p.d.Planner.(interface {
		ValidateEndAfterTurn(maxSceneIndex, requiredFinalSceneIndex int) (requested, accepted bool)
	})
	if !ok {
		return
	}
	required := p.audioBookRequiredFinalSceneIndex()
	requested, accepted := validator.ValidateEndAfterTurn(p.audioBookMaxSceneIndex, required)
	if !requested || p.d.Log == nil {
		return
	}
	if accepted {
		p.d.Log.Info("audiobook end accepted",
			"turn", t.ID,
			"max_scene", p.audioBookMaxSceneIndex,
			"required_scene", required)
		return
	}
	p.d.Log.Warn("audiobook end rejected before final scene",
		"turn", t.ID,
		"max_scene", p.audioBookMaxSceneIndex,
		"required_scene", required)
}

func (p *Pipeline) audioBookRequiredFinalSceneIndex() int {
	if p == nil {
		return 0
	}
	frames := len(p.d.AudioBookImageURLs)
	if frames == 0 {
		frames = p.d.NarrationFrames
	}
	if frames <= 1 {
		return 0
	}
	return frames - 1
}

// SubtitleCues returns the timed WebVTT cues accumulated so far.
func (p *Pipeline) SubtitleCues() []SubtitleCue {
	if p == nil || p.vtt == nil {
		return nil
	}
	return p.vtt.Cues()
}

func (p *Pipeline) vttBias() time.Duration {
	// Audio-only feeds record audio.mp3 straight from the LiveStream at t=0
	// with no stitch StartOffset trim. vttBias exists only to realign the
	// sidecar .vtt against that front-trimmed mp4, so it would push captions
	// late (~2.5s for discussion) against the untrimmed recording. Skip it.
	if p != nil && p.d.AudioOnly {
		return 0
	}
	bias := vttBaseBias
	if p != nil && p.d.HasSeriesPreviouslyOn {
		bias += vttPreviouslyOnBias
	}
	if p != nil && p.d.ContentType == config.ContentTypeDiscussion {
		bias += vttDiscussionBias
	}
	return bias
}

// recordedPos maps a position on the TTS byte timeline (cumulative
// duration of synthesized audio, exact via byte counts) to the position
// in the recorded audio.mp3. Dry audio-only recordings ARE the raw TTS
// byte stream, so the mapping is identity. Mixer-backed recordings run
// at realtime with the music bed filling any production stall, so every
// bed-only gap shifts all narration after it — the mixer tracks exactly
// where each TTS burst landed in its output and MapTTSToOutput replays
// that. Non-audio-only (stitched) runs never call this.
func (p *Pipeline) recordedPos(tts time.Duration) time.Duration {
	if p != nil && p.d.AudioOnly && p.sessionMixer != nil {
		return p.sessionMixer.MapTTSToOutput(tts)
	}
	return tts
}

// Run boots all stages and blocks until the planner stops emitting turns
// AND every stage drains. Returns the produced audio file paths in order.
func (p *Pipeline) Run(ctx context.Context) ([]string, error) {
	produceCtx, stopProduce := context.WithCancel(ctx)
	p.stopMu.Lock()
	p.stopProduce = stopProduce
	p.stopMu.Unlock()
	defer func() {
		stopProduce()
		p.stopMu.Lock()
		p.stopProduce = nil
		p.stopMu.Unlock()
	}()

	turnBuffer := 2
	if p.d.ContentType == config.ContentTypeDiscussion {
		// Keep a shallow look-ahead for smoothness while limiting audience
		// question latency. A deeper queue can leave multiple ordinary
		// discussant turns ahead of the host's address-user turn.
		turnBuffer = 1
	}
	turnCh := make(chan *Turn, turnBuffer)
	producedCh := make(chan *Turn, 1)

	// Session-wide music mixer. If a music file is configured, all turns
	// route their TTS through this single mixer so the bed plays
	// continuously across the whole run instead of restarting per turn.
	if path := p.sessionMusicPath(); path != "" {
		m, err := musicmixer.NewSession(path, p.d.LiveStream)
		if err != nil {
			p.d.Log.Warn("session music mixer disabled — falling back to dry TTS",
				"music", path, "err", err)
		} else {
			p.sessionMixer = m
			if p.d.AudioOnly {
				// Audio-only cues are stamped on the TTS byte timeline;
				// export them on the recorded file's timeline via the
				// mixer's TTS→output sync map (captures bed-only gaps).
				p.vtt.SetTimelineMapper(m.MapTTSToOutput)
			}
			p.d.Log.Info("session music mixer attached", "music", path)
			defer func() {
				if cerr := p.sessionMixer.Close(); cerr != nil {
					p.d.Log.Warn("session music mixer close", "err", cerr)
				}
			}()
		}
	}

	// Tick goroutine — publishes elapsed/remaining once a second.
	tickCtx, tickCancel := context.WithCancel(produceCtx)
	go p.tickLoop(tickCtx)
	defer tickCancel()

	// Planner goroutine.
	go func() {
		defer close(turnCh)
		for {
			t, ok := p.d.Planner.Next(produceCtx)
			if !ok {
				return
			}
			select {
			case turnCh <- t:
			case <-produceCtx.Done():
				return
			}
		}
	}()

	// Producer goroutine — single producer keeps turn ordering deterministic
	// while writing into the shared LiveStream which paces playback at realtime.
	var files []string
	var filesMu sync.Mutex
	// Track phase transitions so subscribers see PhaseMsg as the planner moves
	// from opening → free-debate → closing → verdict → conclusion → ended.
	// (Without this the UI is stuck on whatever phase Setup announced.)
	lastPhase := agent.PhaseSetup
	go func() {
		defer close(producedCh)
		for t := range turnCh {
			if p.plannerDone() {
				stopProduce()
				return
			}
			if t.Phase != lastPhase {
				p.dispatchPhaseMsg(PhaseMsg{
					Phase: t.Phase,
					Type:  p.d.ContentType,
					Label: PhaseLabel(p.d.ContentType, t.Phase),
				})
				lastPhase = t.Phase
			}
			// Per-turn status milestones: gives the SPA log a clear
			// narrative of which speaker / directive is being
			// generated and how long the resulting audio took. Two
			// events per turn keep the stream readable; the
			// orchestrator's TranscriptMsg events still stream the
			// actual text inline.
			directive := strings.TrimSpace(t.Directive)
			if len(directive) > 60 {
				directive = directive[:57] + "…"
			}
			startStatus := fmt.Sprintf("turn %d · %s · narrating",
				t.ID, t.Speaker.Name())
			if directive != "" {
				startStatus += " (" + directive + ")"
			}
			p.d.Send(StatusMsg{Text: startStatus})
			start := time.Now()
			if err := p.produce(produceCtx, t); err != nil {
				p.d.Log.Warn("produce error", "turn", t.ID, "err", err)
				t.SetErr(err)
				p.d.Send(ErrorMsg{Err: fmt.Errorf("turn %d produce: %w", t.ID, err)})
			} else {
				p.d.Send(StatusMsg{Text: fmt.Sprintf(
					"turn %d audio ready · %s · %s",
					t.ID, t.Speaker.Name(),
					time.Since(start).Round(time.Second))})
			}
			p.reviewAudioBookLoop(produceCtx, t)
			p.d.Tracker.AddSpeaking(t.Speaker.Name(), time.Since(start))
			p.validateAudioBookCompletionRequest(t)
			t.MarkPlayed()
			if t.AudioPath != "" {
				filesMu.Lock()
				files = append(files, t.AudioPath)
				filesMu.Unlock()
			}
			select {
			case producedCh <- t:
			case <-produceCtx.Done():
				return
			}
			if p.plannerDone() {
				stopProduce()
				return
			}
		}
	}()

	// Memory updater (consumer).
	for t := range producedCh {
		p.updateMemories(ctx, t)
	}

	// Once the LLM closes its stream we hold for a fixed grace window
	// before tearing the mixer down. The mixer's TTS pipeline still has
	// several seconds of decoded PCM buffered (encoder pipe + LiveStream's
	// realtime-paced output buffer); closing immediately would cut the
	// final sentence mid-word at the listener side. A simple sleep is
	// preferable to the previous "close, then poll BytesAhead" approach
	// because the mixer's drain path has shown spurious hangs that pin
	// the channel runner indefinitely. Bounded by ctx so a hard shutdown
	// (Ctrl-C) doesn't have to wait the full window out.
	p.d.Log.Info("pipeline producer drained — holding for tail playback",
		"turns", len(files), "grace", postProducerGrace, "hard_cap", cleanupHardCap)
	cleanupCtx, cancelCleanup := context.WithTimeout(ctx, cleanupHardCap)
	defer cancelCleanup()
	select {
	case <-cleanupCtx.Done():
	case <-time.After(postProducerGrace):
	}
	if p.sessionMixer != nil {
		t0 := time.Now()
		p.d.Log.Info("pipeline closing session mixer")
		// Run the close on a side goroutine so cleanupCtx can pull
		// the rip-cord if the mixer wedges (observed: the pump
		// goroutine blocked writing into LiveStream after the
		// listener stopped consuming, and the unbounded post-kill
		// waits inside Mixer.Close hung the pipeline forever — see
		// 2026-05-06 stuck-job investigation). Hitting the cap
		// leaks the mixer goroutine + ffmpeg subprocesses for the
		// rest of this process's life; that's strictly better than
		// pinning the channel runner / video job indefinitely.
		closeDone := make(chan error, 1)
		go func() { closeDone <- p.sessionMixer.Close() }()
		select {
		case cerr := <-closeDone:
			if cerr != nil {
				p.d.Log.Warn("session music mixer close (drain)", "err", cerr)
			}
			p.d.Log.Info("pipeline session mixer closed",
				"elapsed", time.Since(t0).Round(time.Millisecond))
		case <-cleanupCtx.Done():
			p.d.Log.Warn("session music mixer close hit hard cap — abandoning",
				"elapsed", time.Since(t0).Round(time.Millisecond),
				"cap", cleanupHardCap)
		}
	}
	if cleanupCtx.Err() != nil {
		p.d.Log.Warn("pipeline cleanup hard cap exceeded — skipping audio drain",
			"cap", cleanupHardCap)
	} else {
		t0 := time.Now()
		p.waitAudioDrained(cleanupCtx)
		p.d.Log.Info("pipeline audio drained",
			"elapsed", time.Since(t0).Round(time.Millisecond))
	}

	// Sidecar WebVTT next to the run's audio archive. WriteTo no-ops on
	// an empty cue list, so a run that produced no TTS output (degenerate
	// planner / setup failure) doesn't leave a malformed file behind.
	if vttPath := filepath.Join(p.d.OutDir, "subtitles.vtt"); p.vtt != nil {
		if err := p.vtt.WriteTo(vttPath); err != nil {
			p.d.Log.Warn("subtitles.vtt write failed", "path", vttPath, "err", err)
		}
	}

	filesMu.Lock()
	defer filesMu.Unlock()
	return append([]string(nil), files...), nil
}

// waitAudioDrained polls LiveStream.BytesAhead until the producer-vs-playback
// delta is small enough that the listener has heard substantially all of the
// produced audio. Bounded by audioDrainTimeout so a hung output pipeline can't
// pin the channel runner — at the timeout we return regardless and accept a
// small audible cut at the handoff (better than freezing the channel).
//
// audioDrainEpsilon (~0.5s of mp3 bytes) is the threshold rather than 0
// because LiveStream's bytesPlayed counter advances in 4 KB pump reads; the
// last fraction of a chunk may show a non-zero BytesAhead even when the
// listener has effectively heard everything.
func (p *Pipeline) waitAudioDrained(ctx context.Context) {
	if p.d.LiveStream == nil {
		return
	}
	const (
		audioDrainTimeout = 30 * time.Second
		audioDrainEpsilon = audio.AudioBytesPerSec / 2 // ~0.5s of mp3
		pollInterval      = 100 * time.Millisecond
	)
	deadline := time.Now().Add(audioDrainTimeout)
	for {
		ahead := p.d.LiveStream.BytesAhead()
		if ahead <= int64(audioDrainEpsilon) {
			return
		}
		if time.Now().After(deadline) {
			p.d.Log.Warn("audio drain timed out — proceeding with handoff",
				"bytes_ahead", ahead,
				"approx_seconds", float64(ahead)/float64(audio.AudioBytesPerSec))
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pollInterval):
		}
	}
}

// dispatchPhaseMsg defers a phase change so it lands on the listener's
// timeline instead of the producer's. Without this, the visual phase chip
// + scene background flip the instant the planner emits the first turn of
// the new phase — but the previous phase's audio is still draining out of
// the LiveStream / music-mixer / browser source buffer (often 10-15s
// worth on long surface narrations), so the audience sees the QA scene
// while still hearing the tail of the surface story. We use the same
// playhead nextPlayAt that synthSentence maintains: that value is the
// listener-side wall-clock time when the *next* sentence will be heard,
// which is also where the new phase's audio will start. AfterFunc-fire
// the PhaseMsg at that moment so picture and sound flip together.
func (p *Pipeline) dispatchPhaseMsg(msg PhaseMsg) {
	p.playheadMu.Lock()
	target := p.nextPlayAt
	p.playheadMu.Unlock()
	send := p.d.Send
	remaining := time.Until(target)
	if remaining <= 50*time.Millisecond {
		send(msg)
		return
	}
	time.AfterFunc(remaining, func() { send(msg) })
}

func (p *Pipeline) tickLoop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.d.Send(TickMsg{
				Elapsed:   p.d.Tracker.Elapsed(),
				Remaining: p.d.Tracker.Remaining(),
			})
		}
	}
}

// sessionMusicPath picks the file to use as session-wide background
// music. Prefers an explicit "session" key, falls back to "surface"
// (calmer ambient bed, the safer default to play under Q&A and
// conclusion turns too), then "reveal", then any first available
// path. Files that don't exist on disk are skipped so a partial
// musicgen failure degrades gracefully to dry TTS.
func (p *Pipeline) sessionMusicPath() string {
	if len(p.d.MusicPaths) == 0 {
		return ""
	}
	for _, k := range []string{"session", "surface", "reveal"} {
		if path := p.d.MusicPaths[k]; path != "" {
			if _, err := os.Stat(path); err == nil {
				return path
			}
		}
	}
	for _, path := range p.d.MusicPaths {
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

// produce runs the LLM stream sentence-by-sentence and synthesizes each
// sentence to MP3, writing every chunk to the shared LiveStream broadcaster
// AND a per-turn file (so the end-of-run ConcatToMP3 keeps working).
func (p *Pipeline) produce(ctx context.Context, t *Turn) error {
	// When the host is about to address a user question, unhide the pending
	// user line so subsequent agent prompts (built later in this pipeline)
	// can see and respond to it. Until this point the line stays hidden so
	// already-buffered candidate turns don't preempt the host.
	if strings.HasPrefix(t.Directive, "address-user:") {
		p.d.Transcript.AcknowledgeUserLines()
	}

	// An audiobook's host starts at `<scene 1/>` — beat 0 is the opening
	// frame and never gets its own marker, so surface it in the transcript
	// (and record its offset) at the start of the first narration turn.
	if p.d.ContentType == config.ContentTypeAudioBook &&
		strings.HasPrefix(t.Directive, "narrate") && !p.openingImageEmitted {
		p.openingImageEmitted = true
		p.emitSceneImage(t, 0, p.audioOffsetNow())
	}

	// Inline the predecessor's actual rendered text into directives whose
	// payload is not known at planner time (today: the puzzle host's
	// "answer:" / "evaluate-solution:" turns reference the player's just-
	// asked question, but the planner runs ahead of the producer so a
	// transcript-based lookup at planner time misses). The planner emits the
	// directive with an empty payload and a PrevTurn pointer; by the time
	// produce() runs for this turn, the predecessor's produce() has finished
	// and PrevTurn.FullText() is final.
	if t.PrevTurn != nil && directiveWantsPrevText(t.Directive) {
		if prev := strings.TrimSpace(t.PrevTurn.FullText()); prev != "" {
			t.Directive += prev
		}
	}

	// Bumped 20 → 40 so puzzle players see deeper Q&A history and stop
	// re-asking questions a teammate already covered. 40 lines comfortably
	// fits a full puzzle round (surface + ~15 Q&A pairs + a couple of
	// audience interjections) without blowing the prompt budget.
	prompt := agent.SpeakPrompt{
		Phase:           t.Phase,
		SegmentNo:       t.ID,
		SecondsBudget:   int(t.Budget / time.Second),
		Recent:          p.d.Transcript.RecentN(40),
		TopicTitle:      p.d.Topic,
		TopicLanguage:   p.d.Language,
		Background:      p.d.Background,
		SourceDocuments: p.d.SourceDocuments,
		Instructions:    t.Directive,
		Side:            t.Speaker.Side(),
		ToolResult:      t.RecordToolResult,
	}
	if mr, ok := t.Speaker.(interface{ MemoryRead() string }); ok {
		prompt.Memory = mr.MemoryRead()
	}

	stream, err := t.Speaker.Speak(ctx, prompt)
	if err != nil {
		return err
	}

	// Per-turn TTS volume contour. The puzzle host's surface narration
	// is the long, music-driven monologue at the start of a 海龜湯
	// round; dropping the speaker to 60% during it lets the bed sit
	// more prominently while still keeping the voice intelligible.
	// Restored at turn end so subsequent Q&A / reveal / conclusion
	// turns play at full speaker volume. No-op when the session
	// mixer isn't attached (dry TTS path).
	if p.sessionMixer != nil {
		if t.Directive == "surface" {
			p.sessionMixer.SetTTSVolumeScale(surfaceTTSScale)
			defer p.sessionMixer.SetTTSVolumeScale(1)
		}
	}

	turnPath := filepath.Join(p.d.OutDir, fmt.Sprintf("turn_%03d.mp3", t.ID))
	t.AudioPath = turnPath

	turnFile, err := os.Create(turnPath)
	if err != nil {
		return fmt.Errorf("create turn file: %w", err)
	}
	defer turnFile.Close()

	// Per-turn raw-script file: captures the host's UNCLEANED stream
	// including `<scene N/>` markers, paragraph breaks, and any other
	// stage cues that get stripped before TTS / transcript / memory
	// see them. Lives next to turn_NNN.mp3 so a single folder gives a
	// full picture of the turn for post-mortem debugging — especially
	// useful for diagnosing scene-marker placement vs. audio drift.
	scriptPath := filepath.Join(p.d.OutDir, fmt.Sprintf("turn_%03d.script.txt", t.ID))
	scriptFile, err := os.Create(scriptPath)
	if err != nil {
		return fmt.Errorf("create turn script file: %w", err)
	}
	defer scriptFile.Close()
	if _, err := fmt.Fprintf(scriptFile, "# turn %d  speaker=%s  role=%s  directive=%s\n",
		t.ID, t.Speaker.Name(), t.Speaker.Role(), t.Directive); err != nil {
		p.d.Log.Warn("script header write", "turn", t.ID, "err", err)
	}

	// Audio sink: tee to the shared livestream (paced via ffmpeg -re) and
	// the per-turn file. Writes are serialized within this goroutine, so a
	// plain MultiWriter is safe.
	//
	// When a session-wide music mixer is configured, the LiveStream side is
	// routed through it so a looped Lyria-generated bed plays continuously
	// underneath every turn's TTS. The per-turn file always receives the
	// dry TTS so end-of-run ConcatToMP3 produces an unmixed archive. The
	// mixer keeps the music flowing through inter-turn gaps via an in-graph
	// lavfi silence input — no Go-side bracketing is needed.
	liveSink := io.Writer(p.d.LiveStream)
	if p.sessionMixer != nil {
		liveSink = p.sessionMixer
	}
	sink := io.MultiWriter(turnFile, liveSink)
	wroteAny := false
	closedAfterPlannerDone := false

	// MinChars=6 coalesces sub-6-rune sentences with the next one so the
	// puzzle host's "是。" / "不是。" / "與此無關。" answer prefix doesn't get
	// its own ~0.5s audio clip + flickering subtitle. The clarifying
	// clause that always follows pulls the combined text well past the
	// threshold. Long debate prose is unaffected — its sentences are
	// already long enough to emit on their own.
	splitter := &audio.SentenceSplitter{MinChars: 6}
	t.curCharIdx = -1
	defer close(t.TextOut)
	// The audiobook narrator can fall into an endless sign-off loop inside a
	// single turn (see fillerWatchdog); the post-turn boundary judge never
	// sees it, so watch the spoken sentences here and cut the turn as soon
	// as the loop is unmistakable.
	var watchdog *fillerWatchdog
	if p.d.ContentType == config.ContentTypeAudioBook && strings.HasPrefix(t.Directive, "narrate") {
		watchdog = &fillerWatchdog{}
	}
	fillerStop := false
	for d := range stream.Deltas() {
		if d.Done {
			break
		}
		if p.plannerDone() {
			stream.Close()
			closedAfterPlannerDone = true
			break
		}
		if d.TextChunk == "" {
			continue
		}
		// Mirror raw LLM bytes (markers and all) into the per-turn
		// script file before splitting / cleaning. ChunkBoundaries
		// are not preserved as newlines — the file is a verbatim
		// reconstruction of what the model wrote.
		if _, err := scriptFile.WriteString(d.TextChunk); err != nil {
			p.d.Log.Warn("script chunk write", "turn", t.ID, "err", err)
		}
		for _, sent := range splitter.Push(d.TextChunk) {
			if watchdog.Observe(sent) {
				p.d.Log.Warn("audiobook filler loop detected — stopping narration",
					"turn", t.ID,
					"sentence_preview", truncatePreview(sent, 120))
				p.forceAudioBookDone()
				stream.Close()
				closedAfterPlannerDone = true
				fillerStop = true
				break
			}
			n, err := p.synthSentence(ctx, t, sent, sink)
			if err != nil {
				p.d.Log.Warn("tts error", "turn", t.ID, "err", err)
			}
			if n > 0 {
				wroteAny = true
			}
		}
		if fillerStop {
			break
		}
	}
	if !fillerStop {
		for _, sent := range splitter.Flush() {
			n, err := p.synthSentence(ctx, t, sent, sink)
			if err != nil {
				p.d.Log.Warn("tts error", "turn", t.ID, "err", err)
			}
			if n > 0 {
				wroteAny = true
			}
		}
	}
	if err := stream.Err(); err != nil {
		if !(closedAfterPlannerDone && errors.Is(err, context.Canceled)) {
			p.d.Log.Warn("llm stream error", "turn", t.ID, "speaker", t.Speaker.Name(), "err", err)
			t.SetErr(err)
			p.d.Send(ErrorMsg{Err: fmt.Errorf("turn %d %s: %w", t.ID, t.Speaker.Name(), err)})
		}
	}

	if !wroteAny {
		// Don't keep an empty-stream artefact, and don't include it in concat.
		_ = turnFile.Close()
		_ = os.Remove(turnPath)
		_ = scriptFile.Close()
		_ = os.Remove(scriptPath)
		t.AudioPath = ""
	}

	// Once a turn whose directive answered the audience has finished
	// streaming, retire the user line so the audience-steering block in
	// runStream doesn't keep nagging every subsequent agent to acknowledge
	// the same question. Covers both the puzzle host (address-user answers
	// directly) and the debate flow (host paraphrases, candidate answers).
	if wroteAny && (strings.HasPrefix(t.Directive, "address-user:") ||
		strings.HasPrefix(t.Directive, "answer-user:")) {
		p.d.Transcript.MarkUserLinesAddressed()
	}

	return nil
}

// forceAudioBookDone marks the audiobook planner finished from inside a turn
// (used by the filler watchdog, which detects a degenerate narration loop
// mid-stream and must stop the planner from asking for another loop).
func (p *Pipeline) forceAudioBookDone() {
	if f, ok := p.d.Planner.(interface{ ForceDoneAtChapterBoundary() }); ok {
		f.ForceDoneAtChapterBoundary()
	}
}

func (p *Pipeline) reviewAudioBookLoop(ctx context.Context, t *Turn) bool {
	if p == nil || t == nil || p.d.ContentType != config.ContentTypeAudioBook || !strings.HasPrefix(t.Directive, "narrate") {
		return false
	}
	guard, ok := p.d.Planner.(interface {
		ReviewAudioBookLoop(context.Context, string) bool
	})
	if !ok {
		return false
	}
	stop := guard.ReviewAudioBookLoop(ctx, t.FullText())
	if stop && p.d.Log != nil {
		reason := ""
		if reporter, ok := p.d.Planner.(interface{ ChapterBoundaryReason() string }); ok {
			reason = reporter.ChapterBoundaryReason()
		}
		p.d.Log.Warn("audiobook boundary judge stopped narration",
			"turn", t.ID,
			"reason", reason,
			"turn_preview", truncatePreview(t.FullText(), 140))
	}
	return stop
}

// emitSceneImage sends an image-only TranscriptMsg for the illustration
// anchored to scene-beat idx, so an audiobook's chat transcript shows the
// generated picture at the moment the host advances to that beat. offset is
// the image's position on the produced audio timeline (VTT cue space);
// negative values clamp to zero. No-op when there's no image for idx
// (non-audiobook runs, out-of-range, or empty URL).
func (p *Pipeline) emitSceneImage(t *Turn, idx int, offset time.Duration) {
	if idx < 0 || idx >= len(p.d.AudioBookImageURLs) {
		return
	}
	url := p.d.AudioBookImageURLs[idx]
	if url == "" {
		return
	}
	if offset < 0 {
		offset = 0
	}
	offsetMS := offset.Milliseconds()
	if p.d.RecordAudioBookImageOffset != nil {
		p.d.RecordAudioBookImageOffset(idx, offset.Seconds())
	}
	caption := ""
	if idx < len(p.d.AudioBookImageCaptions) {
		caption = strings.TrimSpace(p.d.AudioBookImageCaptions[idx])
	}
	p.d.Transcript.AppendLine(agent.TranscriptLine{
		Speaker:       t.Speaker.Name(),
		Role:          t.Speaker.Role(),
		Side:          t.Speaker.Side(),
		Text:          caption,
		ImageURL:      url,
		At:            time.Now(),
		AudioOffsetMS: offsetMS,
	})
	p.d.Send(TranscriptMsg{
		Speaker:       t.Speaker.Name(),
		Role:          t.Speaker.Role(),
		Text:          caption,
		ImageURL:      url,
		Done:          true,
		AudioOffsetMS: offsetMS,
	})
}

// audioOffsetNow returns the running playhead's position on the recorded
// audio timeline (the same offset space as the exported VTT cues) — used
// for events that fire between sentences, where no per-sentence cueStart
// is available. Zero when the stream hasn't started writing yet.
func (p *Pipeline) audioOffsetNow() time.Duration {
	if p.d.AudioOnly {
		p.playheadMu.Lock()
		tts := p.ttsTimelinePos
		p.playheadMu.Unlock()
		return p.recordedPos(tts)
	}
	if p.d.LiveStream == nil {
		return 0
	}
	firstWrite := p.d.LiveStream.FirstWriteAt()
	if firstWrite.IsZero() {
		return 0
	}
	p.playheadMu.Lock()
	playAt := p.nextPlayAt
	p.playheadMu.Unlock()
	if playAt.IsZero() {
		return 0
	}
	off := playAt.Sub(firstWrite) - subtitleClientLatency + p.vttBias()
	if off < 0 {
		off = 0
	}
	return off
}

// transcriptSegment is one contiguous run of a sentence spoken by a single
// voice (the narrator or a named guest), resolved from the parsed <char-N>
// spans.
type transcriptSegment struct {
	speaker string
	role    agent.Role
	side    string
	text    string
}

// resolveTranscriptSegments maps the parsed character spans of one sentence to
// per-speaker transcript segments. Narrator spans (idx < 0) use the turn's own
// speaker; a span pointing at a valid cast member uses that guest's display
// name. Adjacent spans by the same speaker are merged, and whitespace-only
// spans attach to the running segment so spacing is preserved. This is what
// lets a dialogue turn render as distinct speaker bubbles instead of one
// narrator line quoting everyone.
func (p *Pipeline) resolveTranscriptSegments(t *Turn, spans []charSpan) []transcriptSegment {
	var cast []agent.SeriesCharacter
	if host, ok := t.Speaker.(*agent.SeriesHost); ok {
		cast = host.Characters()
	}
	narrator := t.Speaker.Name()
	narratorRole := t.Speaker.Role()
	narratorSide := t.Speaker.Side()
	out := make([]transcriptSegment, 0, len(spans))
	for _, s := range spans {
		speaker, role, side := narrator, narratorRole, narratorSide
		if s.idx >= 0 && s.idx < len(cast) && strings.TrimSpace(cast[s.idx].Name) != "" {
			speaker, role, side = strings.TrimSpace(cast[s.idx].Name), narratorRole, ""
		}
		if strings.TrimSpace(s.text) == "" {
			if n := len(out); n > 0 {
				out[n-1].text += s.text
			}
			continue
		}
		if n := len(out); n > 0 && out[n-1].speaker == speaker {
			out[n-1].text += s.text
			continue
		}
		out = append(out, transcriptSegment{speaker: speaker, role: role, side: side, text: s.text})
	}
	filtered := out[:0]
	for _, s := range out {
		if s.text = strings.TrimSpace(s.text); s.text != "" {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

func (p *Pipeline) synthSentence(ctx context.Context, t *Turn, sent string, sink io.Writer) (int64, error) {
	if sent == "" {
		return 0, nil
	}
	// Strip any scene-switch markers the speaker emitted in this sentence.
	// stripSceneMarkers separates leading markers (fire with this
	// sentence's TranscriptMsg) from trailing markers (fire AFTER this
	// sentence's audio finishes — the image only advances once the
	// previous beat's last words have been heard). Without that split,
	// "[paragraph N last sentence] <scene/>" cuts the image to frame N+1
	// while paragraph N's audio is still playing — the "image one ahead
	// of audio" bug.
	cleaned, leadAdvances, trailAdvances := stripSceneMarkers(sent)
	// Strip sound-cue markers next, against the already-cleaned string so
	// scene + sound cues can coexist in one sentence. Same leading /
	// trailing dispatch semantics — the cue lands on either the audio-start
	// or audio-end moment of this sentence.
	var leadSounds, trailSounds []SoundMarker
	cleaned, leadSounds, trailSounds = stripSoundMarkers(cleaned)
	// Strip cross-episode image-reference markers (series content type only
	// emits them; the regex no-ops on streams without any). Same lead /
	// trail semantics as scene + sound markers — leading swaps the renderer
	// to the prior-episode imagery on audio-start; trailing fires after
	// audio-end.
	var leadImageRefs, trailImageRefs []string
	cleaned, leadImageRefs, trailImageRefs = stripImageRefMarkers(cleaned)
	// Parse `<char-N>...</char-N>` voice spans (series content type
	// only — the regex no-ops on streams without any). Unlike scene /
	// sound / image-ref markers, character markers DON'T fire stage
	// events; they only carry per-segment voice info that the synth
	// path uses to build a multi-voice SSML envelope below. The
	// returned cleanText (markers removed, inner text retained) is
	// what the transcript / subtitle / TTS see.
	charClean, charSpans, hadCharMarkers, endCharIdx := splitCharacterSpansFrom(cleaned, t.curCharIdx)
	t.curCharIdx = endCharIdx
	if hadCharMarkers {
		cleaned = charClean
	}
	naturalChunks := []naturalChunk{{spans: charSpans}}
	if isSeriesNaturalTurn(t) {
		var hadPause, hadBreath bool
		cleaned, naturalChunks, hadPause, hadBreath = splitNaturalSpeech(charSpans)
		if hadPause || hadBreath {
			p.d.Log.Info("series natural speech marker",
				"turn", t.ID,
				"pause", hadPause,
				"breath", hadBreath,
				"sentence_preview", truncatePreview(cleaned, 60))
		}
	}
	// Drop indices that would point past the planner's frame count for
	// this phase so a stray "<scene 99/>" doesn't pin the rotation on
	// the last frame for the rest of the turn. Unnumbered legacy markers
	// (-1) are kept; the renderer treats them as "advance one".
	leadAdvances, trailAdvances = p.normalizeSceneMarkerTiming(t, leadAdvances, trailAdvances)
	leadAdvances, trailAdvances = p.clampLeadTrail(t, leadAdvances, trailAdvances)
	if len(leadAdvances) > 0 || len(trailAdvances) > 0 {
		p.d.Log.Info("scene marker",
			"turn", t.ID,
			"directive", strings.SplitN(t.Directive, ":", 2)[0],
			"leading", len(leadAdvances),
			"trailing", len(trailAdvances),
			"already_emitted", t.sceneAdvances,
			"sentence_preview", truncatePreview(cleaned, 60))
	}
	if len(leadSounds) > 0 || len(trailSounds) > 0 {
		p.d.Log.Info("sound marker",
			"turn", t.ID,
			"directive", strings.SplitN(t.Directive, ":", 2)[0],
			"leading", len(leadSounds),
			"trailing", len(trailSounds),
			"sentence_preview", truncatePreview(cleaned, 60))
	}
	if len(leadImageRefs) > 0 || len(trailImageRefs) > 0 {
		p.d.Log.Info("image-ref marker",
			"turn", t.ID,
			"directive", strings.SplitN(t.Directive, ":", 2)[0],
			"leading", len(leadImageRefs),
			"trailing", len(trailImageRefs),
			"sentence_preview", truncatePreview(cleaned, 60))
	}
	if cleaned == "" && !naturalChunksHaveAudio(naturalChunks) {
		// Marker-only sentence (rare — usually only the surface narration's
		// final paragraph break). Both buckets fire immediately; there's
		// no audio gap to defer trailing advances against. The audio offset
		// is the running playhead — no per-sentence cueStart exists here.
		t.RecordSceneMarkers(leadAdvances, trailAdvances)
		markerOffset := p.audioOffsetNow()
		for _, idx := range leadAdvances {
			p.d.Send(SceneAdvanceMsg{Index: idx})
			p.emitSceneImage(t, idx, markerOffset)
		}
		for _, idx := range trailAdvances {
			p.d.Send(SceneAdvanceMsg{Index: idx})
			p.emitSceneImage(t, idx, markerOffset)
		}
		for _, m := range leadSounds {
			p.d.Send(SoundCueMsg{Index: m.Index, Mode: m.Mode})
			p.dispatchSoundCue(m)
		}
		for _, m := range trailSounds {
			p.d.Send(SoundCueMsg{Index: m.Index, Mode: m.Mode})
			p.dispatchSoundCue(m)
		}
		for _, k := range leadImageRefs {
			p.d.Send(ImageRefMsg{Key: k})
		}
		for _, k := range trailImageRefs {
			p.d.Send(ImageRefMsg{Key: k})
		}
		t.sceneAdvances += len(leadAdvances) + len(trailAdvances)
		return 0, nil
	}
	sent = cleaned
	// Capture the sentence on the turn itself so a downstream turn whose
	// directive references this one (Turn.PrevTurn) can read the verbatim
	// rendered text via FullText() once produce() returns. Distinct from the
	// TextOut channel, which AppendFromTurn drains into the transcript and
	// has a bounded buffer that drops sentences when full.
	if sent != "" {
		t.AppendText(sent)
		// Push transcript chunk to per-turn channel synchronously (used to build
		// the persisted transcript line in updateMemories — order matters there).
		select {
		case t.TextOut <- sent:
		default:
		}
	}

	// Schedule subtitle dispatch for when this sentence's first audio
	// byte will reach the listener. p.nextPlayAt is the running playhead
	// (advanced after synth by each sentence's audioDuration); resync
	// here against LiveStream.BytesAhead so any silence-pad the pump
	// inserted between turns still counts. We always pick the LATER of
	// the two so the playhead never goes backwards. subtitleClientLatency
	// covers downstream buffering (browser MediaSource buffer, OS audio
	// buffer) that BytesAhead can't see past stdout.
	p.playheadMu.Lock()
	bytesAhead := p.d.LiveStream.BytesAhead()
	nowSync := time.Now().Add(
		time.Duration(float64(bytesAhead)/float64(audio.AudioBytesPerSec)*float64(time.Second)) +
			subtitleClientLatency)
	if nowSync.After(p.nextPlayAt) {
		p.nextPlayAt = nowSync
	}
	targetSend := p.nextPlayAt
	p.playheadMu.Unlock()

	n, err := p.synthNaturalChunks(ctx, t, sent, naturalChunks, hadCharMarkers, sink)
	if err != nil {
		return n, err
	}
	t.RecordSceneMarkers(leadAdvances, trailAdvances)

	audioDuration := time.Duration(float64(n) /
		float64(audio.AudioBytesPerSec) * float64(time.Second))
	p.playheadMu.Lock()
	p.nextPlayAt = p.nextPlayAt.Add(audioDuration)
	ttsStart := p.ttsTimelinePos
	p.ttsTimelinePos += audioDuration
	p.playheadMu.Unlock()

	// Sidecar WebVTT cue for this sentence.
	//
	// Audio-only runs: the cue is stamped with the sentence's exact
	// position on the TTS byte timeline (cumulative synthesized audio
	// before it). For dry recordings that IS the file position; for
	// mixer-backed recordings the vttWriter's timeline mapper (the
	// mixer's TTS→output sync map, see recordedPos) translates it at
	// export time, including any bed-only gaps the session accumulated.
	// Wall clocks are never involved, so slow LLM/TTS production cannot
	// skew the cue timeline.
	//
	// Stitched-video runs: the cue offset is the listener-clock playhead
	// (targetSend) minus LiveStream's first write — the same anchor the
	// encoder pump treats as first-real-audio and stitch.StartOffset
	// trims the mp4 to — minus the player-side buffering the listener
	// clock includes, plus the empirically tuned vttBias.
	var cueStart time.Duration
	if p.d.AudioOnly {
		cueStart = ttsStart
	} else if firstWrite := p.d.LiveStream.FirstWriteAt(); !firstWrite.IsZero() {
		cueStart = targetSend.Sub(firstWrite) - subtitleClientLatency + p.vttBias()
	}
	if sent != "" {
		p.vtt.Append(sent, cueStart, audioDuration)
	}

	// Split the sentence into per-speaker segments so a dialogue turn shows each
	// guest's words as their own attributed bubble instead of one narrator line
	// that merely quotes them. A sentence with no <char-N> markers yields a
	// single narrator segment — identical to the previous single-message path.
	// Segments are persisted as they stream (per-speaker rows, in order), and
	// the live transcript messages carry the resolved speaker name.
	transcriptSpans := charSpans
	if isSeriesNaturalTurn(t) {
		transcriptSpans = transcriptSpansFromNaturalChunks(naturalChunks)
	}
	segs := p.resolveTranscriptSegments(t, transcriptSpans)
	segMsgs := make([]TranscriptMsg, 0, len(segs))
	if len(segs) > 0 {
		totalRunes := 0
		for _, s := range segs {
			totalRunes += len([]rune(s.text))
		}
		emittedAt := time.Now()
		for _, s := range segs {
			// Apportion the sentence's measured audio duration across segments by
			// text length so caption scrolling stays roughly aligned per speaker.
			segDur := audioDuration
			if totalRunes > 0 && len(segs) > 1 {
				segDur = time.Duration(float64(audioDuration) * float64(len([]rune(s.text))) / float64(totalRunes))
			}
			segMsgs = append(segMsgs, TranscriptMsg{
				Speaker: s.speaker, Role: s.role, Side: s.side, Text: s.text,
				AudioDuration: segDur,
			})
			t.SetLastSpeaker(s.speaker, s.role, s.side)
			p.d.Transcript.AppendAgentSegment(t.ID, agent.TranscriptLine{
				Speaker: s.speaker, Role: s.role, Side: s.side, Text: s.text, At: emittedAt,
			})
		}
	}
	send := p.d.Send
	subtitleAt := targetSend
	fireSubtitle := func() {
		for _, m := range segMsgs {
			send(m)
		}
	}
	// Scene-image offsets are recorded on the recorded-file timeline.
	// recordedPos is evaluated inside the closures — they fire at listener
	// time, several seconds after the sentence was mixed, so the mixer's
	// TTS→output sync point for this sentence exists by then.
	fireLeadingScenes := func() {
		for _, idx := range leadAdvances {
			send(SceneAdvanceMsg{Index: idx})
			// Leading markers swap the image at this sentence's audio start.
			p.emitSceneImage(t, idx, p.recordedPos(cueStart))
		}
	}
	fireTrailing := func() {
		for _, idx := range trailAdvances {
			send(SceneAdvanceMsg{Index: idx})
			// Trailing markers advance after this sentence's audio finishes.
			p.emitSceneImage(t, idx, p.recordedPos(cueStart+audioDuration))
		}
	}
	if len(segMsgs) > 0 {
		if remaining := time.Until(subtitleAt); remaining <= 50*time.Millisecond {
			fireSubtitle()
		} else {
			time.AfterFunc(remaining, fireSubtitle)
		}
	}
	if len(leadAdvances) > 0 {
		if remaining := time.Until(targetSend); remaining <= 50*time.Millisecond {
			fireLeadingScenes()
		} else {
			time.AfterFunc(remaining, fireLeadingScenes)
		}
	}
	if len(trailAdvances) > 0 {
		// targetSend is the sentence's audio-start moment; add
		// audioDuration to land at audio-end on the listener's timeline.
		trailAt := targetSend.Add(audioDuration)
		if remaining := time.Until(trailAt); remaining <= 50*time.Millisecond {
			fireTrailing()
		} else {
			time.AfterFunc(remaining, fireTrailing)
		}
	}
	if len(leadSounds) > 0 {
		ms := append([]SoundMarker(nil), leadSounds...)
		fire := func() {
			for _, m := range ms {
				send(SoundCueMsg{Index: m.Index, Mode: m.Mode})
				p.dispatchSoundCue(m)
			}
		}
		if remaining := time.Until(targetSend); remaining <= 50*time.Millisecond {
			fire()
		} else {
			time.AfterFunc(remaining, fire)
		}
	}
	if len(trailSounds) > 0 {
		ms := append([]SoundMarker(nil), trailSounds...)
		trailAt := targetSend.Add(audioDuration)
		fire := func() {
			for _, m := range ms {
				send(SoundCueMsg{Index: m.Index, Mode: m.Mode})
				p.dispatchSoundCue(m)
			}
		}
		if remaining := time.Until(trailAt); remaining <= 50*time.Millisecond {
			fire()
		} else {
			time.AfterFunc(remaining, fire)
		}
	}
	if len(leadImageRefs) > 0 {
		ks := append([]string(nil), leadImageRefs...)
		fire := func() {
			for _, k := range ks {
				send(ImageRefMsg{Key: k})
			}
		}
		if remaining := time.Until(targetSend); remaining <= 50*time.Millisecond {
			fire()
		} else {
			time.AfterFunc(remaining, fire)
		}
	}
	if len(trailImageRefs) > 0 {
		ks := append([]string(nil), trailImageRefs...)
		trailAt := targetSend.Add(audioDuration)
		fire := func() {
			for _, k := range ks {
				send(ImageRefMsg{Key: k})
			}
		}
		if remaining := time.Until(trailAt); remaining <= 50*time.Millisecond {
			fire()
		} else {
			time.AfterFunc(remaining, fire)
		}
	}
	t.sceneAdvances += len(leadAdvances) + len(trailAdvances)
	return n, nil
}

func (p *Pipeline) normalizeSceneMarkerTiming(t *Turn, lead, trail []int) ([]int, []int) {
	if p == nil || t == nil || p.d.ContentType != config.ContentTypeAudioBook {
		return lead, trail
	}
	if !strings.HasPrefix(t.Directive, "narrate") || len(lead) > 0 || len(trail) == 0 {
		return lead, trail
	}
	return trail, nil
}

// dispatchSoundCue resolves marker → on-disk clip path and asks the
// session mixer to play it. No-ops when the mixer isn't attached
// (session music gen failed) or when the index points outside the
// configured SoundPaths slice; both surface as a warning so a missing
// clip doesn't take the whole turn down.
func (p *Pipeline) dispatchSoundCue(m SoundMarker) {
	if p.sessionMixer == nil {
		return
	}
	if m.Index < 0 || m.Index >= len(p.d.SoundPaths) {
		p.d.Log.Warn("sound cue index out of range",
			"index", m.Index,
			"have", len(p.d.SoundPaths),
			"mode", string(m.Mode))
		return
	}
	path := p.d.SoundPaths[m.Index]
	if path == "" {
		p.d.Log.Warn("sound cue path empty", "index", m.Index, "mode", string(m.Mode))
		return
	}
	switch m.Mode {
	case SoundCueOverlap:
		if err := musicmixer.OverlapMusic(p.sessionMixer, path); err != nil {
			p.d.Log.Warn("sound overlap failed",
				"index", m.Index, "path", path, "err", err)
		}
	case SoundCueReplace:
		if err := musicmixer.ReplaceMusic(p.sessionMixer, path); err != nil {
			p.d.Log.Warn("sound replace failed",
				"index", m.Index, "path", path, "err", err)
		}
	default:
		p.d.Log.Warn("sound cue unknown mode",
			"index", m.Index, "mode", string(m.Mode))
	}
}

// truncatePreview clips s to n runes for log lines so a long sentence
// doesn't blow out the log entry. Adds an ellipsis on truncation.
func truncatePreview(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// clampLeadTrail filters out scene-marker indices that point past the
// current phase's frame count so a stray "<scene 99/>" against a 14-
// frame plan doesn't pin the renderer on the last frame. Unnumbered
// legacy markers (markerIdxNoNumber) pass through unchanged — the
// renderer interprets them as "advance one". Returns the filtered
// leading and trailing slices in document order.
func (p *Pipeline) clampLeadTrail(t *Turn, lead, trail []int) ([]int, []int) {
	count := p.phaseFrameCount(t)
	return clampMarkerIndices(lead, count), clampMarkerIndices(trail, count)
}

// clampMarkerIndices drops any explicit index >= count and keeps -1
// (legacy unnumbered markers) intact. count <= 0 disables the cap.
func clampMarkerIndices(in []int, count int) []int {
	if len(in) == 0 || count <= 0 {
		return in
	}
	out := in[:0]
	for _, idx := range in {
		if idx == markerIdxNoNumber || idx < count {
			out = append(out, idx)
		}
	}
	return out
}

// phaseFrameCount returns how many planned frames the current turn's
// directive has. 0 means "no plan for this phase" — clamp is disabled.
func (p *Pipeline) phaseFrameCount(t *Turn) int {
	switch {
	case strings.HasPrefix(t.Directive, "surface"):
		return p.d.SurfaceFrames
	case strings.HasPrefix(t.Directive, "conclusion"):
		return p.d.ConclusionFrames
	case strings.HasPrefix(t.Directive, "narrate"),
		strings.HasPrefix(t.Directive, "previously"):
		return p.d.NarrationFrames
	}
	// Other directives (answer, evaluate-solution, …) don't cut scenes;
	// 0 disables the per-marker clamp so any future marker-emitting
	// directive isn't silently muted.
	return 0
}

// updateMemories pushes the played turn into the transcript log AND into every
// other agent's memory (asynchronously triggers compression if large).
//
// Special case: the host also records its own turns into its own memory via
// ListenSelf. This lets the host see its prior intros / handoffs /
// address-user lines on subsequent turns and stop recycling identical
// phrasing. Other agents keep the original behaviour (their own past turns
// only show up via the recent-transcript window in the prompt body).
func (p *Pipeline) updateMemories(ctx context.Context, t *Turn) {
	p.maybeJudgeTurn(ctx, t)
	// The turn's spoken text was already persisted per-speaker as it streamed
	// (AppendAgentSegment). Close the open line and stamp the now-final
	// turn-level sources / judgement onto it — no consolidated line is appended
	// here, which would double the transcript.
	p.d.Transcript.CloseTurn(t.ID, t.Sources(), t.JudgementComment())
	// Agent memory still sees one consolidated entry per turn (markers stripped),
	// built from the accumulated full text rather than a persisted line.
	full := agent.TranscriptLine{
		Speaker:          t.Speaker.Name(),
		Role:             t.Speaker.Role(),
		Side:             t.Speaker.Side(),
		Text:             t.FullText(),
		At:               time.Now(),
		Sources:          t.Sources(),
		JudgementComment: t.JudgementComment(),
	}
	for _, a := range p.d.Registry.All() {
		if a == t.Speaker {
			if t.Speaker.Role() == agent.RoleHost {
				if ls, ok := a.(interface {
					ListenSelf(context.Context, agent.TranscriptLine) error
				}); ok {
					_ = ls.ListenSelf(ctx, full)
				}
			}
			continue
		}
		_ = a.Listen(ctx, full)
	}
	// Final transcript event completes the last-emitted speaker's running line
	// (a dialogue turn may end on a guest, not the narrator).
	lastName, lastRole, lastSide := t.LastSpeaker()
	p.d.Send(TranscriptMsg{
		Speaker: lastName, Role: lastRole, Side: lastSide, Text: "", Done: true,
		Sources: t.Sources(), JudgementComment: t.JudgementComment(),
	})
}

func (p *Pipeline) maybeJudgeTurn(ctx context.Context, t *Turn) {
	if p == nil || p.d.ContentType != config.ContentTypeDiscussion {
		return
	}
	if t == nil || t.Speaker == nil || t.Speaker.Role() != agent.RoleDiscussant {
		return
	}
	j, ok := p.d.Registry.Judgement.(*agent.Judgement)
	if !ok || j == nil {
		return
	}
	text := strings.TrimSpace(t.FullText())
	if text == "" {
		return
	}
	line := agent.TranscriptLine{
		Speaker: t.Speaker.Name(),
		Role:    t.Speaker.Role(),
		Side:    t.Speaker.Side(),
		Text:    text,
		Sources: t.Sources(),
	}
	j.EmitActivity(string(ActivitySearching), "judgement")
	judgeCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	res, err := j.Analyze(judgeCtx, p.d.Topic, line, p.d.Transcript.RecentN(40))
	j.EmitActivity(string(ActivityIdle), "")
	if err != nil {
		p.d.Log.Warn("judgement analyze failed", "turn", t.ID, "err", err)
		if comment := fallbackJudgementComment(line); comment != "" {
			t.SetJudgementComment(comment)
		}
		return
	}
	for _, src := range res.Sources {
		t.addSource(src)
	}
	line.Sources = t.Sources()
	if res.ShouldComment {
		t.SetJudgementComment(res.Comment)
	} else if comment := fallbackJudgementComment(line); comment != "" {
		t.SetJudgementComment(comment)
	}
}

func fallbackJudgementComment(line agent.TranscriptLine) string {
	if strings.TrimSpace(line.Text) == "" || len(line.Sources) > 0 {
		return ""
	}
	text := strings.ToLower(line.Text)
	markers := []string{
		"fastest", "best", "must", "only way", "guarantee", "proves",
		"current", "now", "already", "standard", "data", "percent", "%",
		"最快", "最佳", "必须", "唯一", "保证", "证明", "目前", "现在",
		"已经", "标准", "数据", "量化", "结果", "机会",
	}
	hits := 0
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			hits++
		}
	}
	if hits < 2 {
		return ""
	}
	if containsCJK(line.Text) {
		return "这点需要更强的证据支撑，先不要把它当成定论。"
	}
	return "That point needs stronger evidence before treating it as settled."
}

func containsCJK(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}

// Keep the llm package referenced even when no inline use exists.
var _ = llm.RoleUser
