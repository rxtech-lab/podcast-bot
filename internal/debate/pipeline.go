package debate

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
	"github.com/sirily11/debate-bot/internal/llm"
	"github.com/sirily11/debate-bot/internal/tts"
)

// Deps are everything the pipeline needs to run.
type Deps struct {
	Planner    *Planner
	Tracker    *Tracker
	Registry   *agent.Registry
	TTS        tts.Provider
	OutDir     string
	Send       func(any) // event-bus publish wrapper
	Log        *slog.Logger
	Topic      string
	Language   string
	Transcript *Transcript
	LiveStream *audio.LiveStream // shared mp3 broadcaster (paced by ffmpeg -re)
}

// subtitleClientLatency compensates for buffering that happens after the
// LiveStream's stdout — primarily the browser MediaSource source buffer
// (~1.5s on Chromium for low-bitrate MP3) and any OS audio buffering. The
// renderer's TranscriptMsg dispatch is delayed by bytesAhead/rate +
// subtitleClientLatency so the subtitle change lands when the listener
// actually starts hearing the new sentence.
//
// Tune up if subtitles still beat the audio; tune down if subtitles lag.
const subtitleClientLatency = 1500 * time.Millisecond

// Pipeline owns the goroutines for produce/memory stages.
type Pipeline struct {
	d Deps
}

// NewPipeline creates a Pipeline.
func NewPipeline(d Deps) *Pipeline { return &Pipeline{d: d} }

// Run boots all stages and blocks until the planner stops emitting turns
// AND every stage drains. Returns the produced audio file paths in order.
func (p *Pipeline) Run(ctx context.Context) ([]string, error) {
	turnCh := make(chan *Turn, 2)
	producedCh := make(chan *Turn, 1)

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
				p.d.Send(PhaseMsg{Phase: t.Phase})
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
	prompt := agent.SpeakPrompt{
		Phase:         t.Phase,
		SegmentNo:     t.ID,
		SecondsBudget: int(t.Budget / time.Second),
		Recent:        p.d.Transcript.RecentN(20),
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

	// Audio sink: tee to the shared livestream (paced via ffmpeg -re) and the
	// per-turn file. Writes are serialized within this goroutine, so a plain
	// MultiWriter is safe.
	sink := io.MultiWriter(turnFile, p.d.LiveStream)
	wroteAny := false

	splitter := &audio.SentenceSplitter{}
	defer close(t.TextOut)
	for d := range stream.Deltas() {
		if d.Done {
			break
		}
		if d.TextChunk == "" {
			continue
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
		t.AudioPath = ""
	}

	return nil
}

func (p *Pipeline) synthSentence(ctx context.Context, t *Turn, sent string, sink io.Writer) (int64, error) {
	if sent == "" {
		return 0, nil
	}
	// Push transcript chunk to per-turn channel synchronously (used to build
	// the persisted transcript line in updateMemories — order matters there).
	select {
	case t.TextOut <- sent:
	default:
	}

	// Bus event drives the live UI subtitle. The producer races up to the
	// LiveStream's input buffer ahead of realtime playback; emitting the bus
	// event synchronously makes the subtitle race ahead of the audio. Delay
	// the publish by however far ahead this sentence's first audio byte will
	// land — bytesAhead at this moment is exactly the playback offset of the
	// *next* byte we're about to write at the ffmpeg-stdout boundary.
	//
	// We also add subtitleClientLatency to account for buffering downstream of
	// ffmpeg (browser MediaSource source buffer, ffplay decode pipeline, OS
	// audio buffer). Without it the subtitle still beats the audio by
	// roughly that amount because BytesAhead can't see past stdout.
	msg := TranscriptMsg{
		Speaker: t.Speaker.Name(), Role: t.Speaker.Role(),
		Side: t.Speaker.Side(), Text: sent,
	}
	bytesAhead := p.d.LiveStream.BytesAhead()
	delay := time.Duration(float64(bytesAhead)/float64(audio.AudioBytesPerSec)*float64(time.Second)) + subtitleClientLatency
	if delay <= 50*time.Millisecond {
		p.d.Send(msg)
	} else {
		time.AfterFunc(delay, func() { p.d.Send(msg) })
	}

	body, err := p.d.TTS.SynthesizeStream(ctx, t.Speaker.Voice().ShortName, sent, p.d.Language)
	if err != nil {
		return 0, err
	}
	defer body.Close()
	return io.Copy(sink, body)
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
