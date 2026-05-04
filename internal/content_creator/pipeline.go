package contentcreator

import (
	"context"
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
	"github.com/sirily11/debate-bot/internal/llm"
	"github.com/sirily11/debate-bot/internal/tts"
)

// Deps are everything the pipeline needs to run.
type Deps struct {
	Planner    Planner
	Tracker    *Tracker
	Registry   *agent.Registry
	TTS        tts.Provider
	OutDir     string
	Send       func(any) // event-bus publish wrapper
	Log        *slog.Logger
	Topic      string
	Language   string
	// ContentType is the topic.Type discriminator (config.ContentType*).
	// Stamped onto PhaseMsg so the frontend can label phases without
	// hardcoding the per-format mapping.
	ContentType string
	Transcript  *Transcript
	LiveStream  *audio.LiveStream // shared mp3 broadcaster (paced by ffmpeg -re)

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
}

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

// surfaceSubtitleExtraDelay is an additional offset applied ONLY to the
// puzzle host's surface-narration subtitle dispatch. Surface turns are
// long, slow, and music-bedded — the subtitle was landing slightly ahead
// of the spoken audio on listener-side playback, breaking the late-night
// storyteller feel. Scene markers are NOT delayed (they remain tied to
// the audio start), only the on-screen caption shifts.
const surfaceSubtitleExtraDelay = 1500 * time.Millisecond

// Pipeline owns the goroutines for produce/memory stages.
type Pipeline struct {
	d Deps
	// sessionMixer wraps LiveStream with a long-lived ffmpeg amix process
	// that keeps a looped background music bed underneath every TTS turn.
	// nil means no music is configured; produce() falls through to writing
	// directly into LiveStream.
	sessionMixer *musicmixer.Mixer

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
}

// NewPipeline creates a Pipeline.
func NewPipeline(d Deps) *Pipeline { return &Pipeline{d: d} }

// Run boots all stages and blocks until the planner stops emitting turns
// AND every stage drains. Returns the produced audio file paths in order.
func (p *Pipeline) Run(ctx context.Context) ([]string, error) {
	turnCh := make(chan *Turn, 2)
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
			p.d.Log.Info("session music mixer attached", "music", path)
			defer func() {
				if cerr := p.sessionMixer.Close(); cerr != nil {
					p.d.Log.Warn("session music mixer close", "err", cerr)
				}
			}()
		}
	}

	// Tick goroutine — publishes elapsed/remaining once a second.
	tickCtx, tickCancel := context.WithCancel(ctx)
	go p.tickLoop(tickCtx)
	defer tickCancel()

	// Planner goroutine.
	go func() {
		defer close(turnCh)
		for {
			t, ok := p.d.Planner.Next(ctx)
			if !ok {
				return
			}
			select {
			case turnCh <- t:
			case <-ctx.Done():
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
			if t.Phase != lastPhase {
				p.d.Send(PhaseMsg{
					Phase: t.Phase,
					Type:  p.d.ContentType,
					Label: PhaseLabel(p.d.ContentType, t.Phase),
				})
				lastPhase = t.Phase
			}
			start := time.Now()
			if err := p.produce(ctx, t); err != nil {
				p.d.Log.Warn("produce error", "turn", t.ID, "err", err)
				t.SetErr(err)
				p.d.Send(ErrorMsg{Err: fmt.Errorf("turn %d produce: %w", t.ID, err)})
			}
			p.d.Tracker.AddSpeaking(t.Speaker.Name(), time.Since(start))
			t.MarkPlayed()
			if t.AudioPath != "" {
				filesMu.Lock()
				files = append(files, t.AudioPath)
				filesMu.Unlock()
			}
			select {
			case producedCh <- t:
			case <-ctx.Done():
				return
			}
		}
	}()

	// Memory updater (consumer).
	for t := range producedCh {
		p.updateMemories(ctx, t)
	}

	filesMu.Lock()
	defer filesMu.Unlock()
	return append([]string(nil), files...), nil
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
		Phase:         t.Phase,
		SegmentNo:     t.ID,
		SecondsBudget: int(t.Budget / time.Second),
		Recent:        p.d.Transcript.RecentN(40),
		TopicTitle:    p.d.Topic,
		TopicLanguage: p.d.Language,
		Instructions:  t.Directive,
		Side:          t.Speaker.Side(),
	}
	if mr, ok := t.Speaker.(interface{ MemoryRead() string }); ok {
		prompt.Memory = mr.MemoryRead()
	}

	stream, err := t.Speaker.Speak(ctx, prompt)
	if err != nil {
		return err
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

	// MinChars=6 coalesces sub-6-rune sentences with the next one so the
	// puzzle host's "是。" / "不是。" / "與此無關。" answer prefix doesn't get
	// its own ~0.5s audio clip + flickering subtitle. The clarifying
	// clause that always follows pulls the combined text well past the
	// threshold. Long debate prose is unaffected — its sentences are
	// already long enough to emit on their own.
	splitter := &audio.SentenceSplitter{MinChars: 6}
	defer close(t.TextOut)
	for d := range stream.Deltas() {
		if d.Done {
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
			n, err := p.synthSentence(ctx, t, sent, sink)
			if err != nil {
				p.d.Log.Warn("tts error", "turn", t.ID, "err", err)
			}
			if n > 0 {
				wroteAny = true
			}
		}
	}
	for _, sent := range splitter.Flush() {
		n, err := p.synthSentence(ctx, t, sent, sink)
		if err != nil {
			p.d.Log.Warn("tts error", "turn", t.ID, "err", err)
		}
		if n > 0 {
			wroteAny = true
		}
	}
	if err := stream.Err(); err != nil {
		p.d.Log.Warn("llm stream error", "turn", t.ID, "speaker", t.Speaker.Name(), "err", err)
		t.SetErr(err)
		p.d.Send(ErrorMsg{Err: fmt.Errorf("turn %d %s: %w", t.ID, t.Speaker.Name(), err)})
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
	// Drop indices that would point past the planner's frame count for
	// this phase so a stray "<scene 99/>" doesn't pin the rotation on
	// the last frame for the rest of the turn. Unnumbered legacy markers
	// (-1) are kept; the renderer treats them as "advance one".
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
	if cleaned == "" {
		// Marker-only sentence (rare — usually only the surface narration's
		// final paragraph break). Both buckets fire immediately; there's
		// no audio gap to defer trailing advances against.
		for _, idx := range leadAdvances {
			p.d.Send(SceneAdvanceMsg{Index: idx})
		}
		for _, idx := range trailAdvances {
			p.d.Send(SceneAdvanceMsg{Index: idx})
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
	t.AppendText(sent)
	// Push transcript chunk to per-turn channel synchronously (used to build
	// the persisted transcript line in updateMemories — order matters there).
	select {
	case t.TextOut <- sent:
	default:
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

	body, err := p.d.TTS.SynthesizeStream(ctx, t.Speaker.Voice().ShortName, sent, p.d.Language)
	if err != nil {
		return 0, err
	}
	defer body.Close()
	n, err := io.Copy(sink, body)
	if err != nil {
		return n, err
	}

	audioDuration := time.Duration(float64(n) /
		float64(audio.AudioBytesPerSec) * float64(time.Second))
	p.playheadMu.Lock()
	p.nextPlayAt = p.nextPlayAt.Add(audioDuration)
	p.playheadMu.Unlock()

	msg := TranscriptMsg{
		Speaker: t.Speaker.Name(), Role: t.Speaker.Role(),
		Side: t.Speaker.Side(), Text: sent,
		AudioDuration: audioDuration,
	}
	send := p.d.Send
	// Subtitle dispatch is offset on surface turns only — see
	// surfaceSubtitleExtraDelay. Scene markers (leading + trailing)
	// stay locked to the audio timeline so the picture doesn't drift
	// even when the caption is held.
	subtitleAt := targetSend
	if strings.HasPrefix(t.Directive, "surface") {
		subtitleAt = subtitleAt.Add(surfaceSubtitleExtraDelay)
	}
	fireSubtitle := func() { send(msg) }
	fireLeadingScenes := func() {
		for _, idx := range leadAdvances {
			send(SceneAdvanceMsg{Index: idx})
		}
	}
	fireTrailing := func() {
		for _, idx := range trailAdvances {
			send(SceneAdvanceMsg{Index: idx})
		}
	}
	if remaining := time.Until(subtitleAt); remaining <= 50*time.Millisecond {
		fireSubtitle()
	} else {
		time.AfterFunc(remaining, fireSubtitle)
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
	t.sceneAdvances += len(leadAdvances) + len(trailAdvances)
	return n, nil
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
	full := p.d.Transcript.AppendFromTurn(t)
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
	// Final transcript event (completes the running line in the TUI / web).
	p.d.Send(TranscriptMsg{
		Speaker: t.Speaker.Name(), Role: t.Speaker.Role(),
		Side: t.Speaker.Side(), Text: "", Done: true,
	})
}

// Keep the llm package referenced even when no inline use exists.
var _ = llm.RoleUser
