package debate

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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
	TTS        *tts.Client
	OutDir     string
	Send       func(any) // tea.Program.Send wrapper, takes any tea.Msg
	Log        *slog.Logger
	Topic      string
	Language   string
	Transcript *Transcript
}

// Pipeline owns the goroutines for produce/play/memory stages.
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
	playedCh := make(chan *Turn)

	// Tick goroutine — sends elapsed/remaining to TUI every 1s.
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

	// Producer goroutine — single producer is enough because we need to keep
	// turn ordering deterministic; pipeline parallelism comes from producedCh
	// having buffer 1 and the player overlapping with the next produce.
	go func() {
		defer close(producedCh)
		for t := range turnCh {
			if err := p.produce(ctx, t); err != nil {
				p.d.Log.Warn("produce error", "turn", t.ID, "err", err)
				t.SetErr(err)
				close(t.TextOut)
				p.d.Send(ErrorMsg{Err: fmt.Errorf("turn %d produce: %w", t.ID, err)})
				continue
			}
			select {
			case producedCh <- t:
			case <-ctx.Done():
				return
			}
		}
	}()

	// Player goroutine.
	var files []string
	var filesMu sync.Mutex
	go func() {
		defer close(playedCh)
		for t := range producedCh {
			start := time.Now()
			if err := p.play(ctx, t); err != nil {
				p.d.Log.Warn("play error", "turn", t.ID, "err", err)
				t.SetErr(err)
			}
			p.d.Tracker.AddSpeaking(t.Speaker.Name(), time.Since(start))
			t.MarkPlayed()
			if t.AudioPath != "" {
				filesMu.Lock()
				files = append(files, t.AudioPath)
				filesMu.Unlock()
			}
			select {
			case playedCh <- t:
			case <-ctx.Done():
				return
			}
		}
	}()

	// Memory updater (consumer).
	for t := range playedCh {
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

// produce runs the LLM stream and pipes sentence-level TTS into a per-turn
// io.Pipe. The pipe reader is stashed on the turn for the player.
func (p *Pipeline) produce(ctx context.Context, t *Turn) error {
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
	mem, _ := t.Speaker.(memReader); _ = mem // accessor type; not all agents expose memory
	if mr, ok := t.Speaker.(interface{ MemoryRead() string }); ok {
		prompt.Memory = mr.MemoryRead()
	} else if br, ok := t.Speaker.(interface{ Memory() interface{ Read() (string, error) } }); ok {
		_ = br
	}

	stream, err := t.Speaker.Speak(ctx, prompt)
	if err != nil {
		return err
	}

	// Per-turn audio pipe: we write MP3 chunks for every sentence, the player
	// reads from the read end and tees to file + ffplay stdin.
	pr, pw := io.Pipe()
	t.AudioPath = filepath.Join(p.d.OutDir, fmt.Sprintf("turn_%03d.mp3", t.ID))
	t.audioReader = pr

	go func() {
		defer pw.Close()
		defer close(t.TextOut)
		splitter := &audio.SentenceSplitter{}
		for d := range stream.Deltas() {
			if d.Done {
				break
			}
			if d.TextChunk == "" {
				continue
			}
			for _, sent := range splitter.Push(d.TextChunk) {
				if err := p.synthSentence(ctx, t, sent, pw); err != nil {
					p.d.Log.Warn("tts error", "turn", t.ID, "err", err)
				}
			}
		}
		for _, sent := range splitter.Flush() {
			if err := p.synthSentence(ctx, t, sent, pw); err != nil {
				p.d.Log.Warn("tts error", "turn", t.ID, "err", err)
			}
		}
		if err := stream.Err(); err != nil {
			p.d.Log.Warn("llm stream error", "turn", t.ID, "speaker", t.Speaker.Name(), "err", err)
			t.SetErr(err)
			p.d.Send(ErrorMsg{Err: fmt.Errorf("turn %d %s: %w", t.ID, t.Speaker.Name(), err)})
		}
	}()

	return nil
}

// memReader is an unused marker type; kept to document intent.
type memReader interface{ MemoryRead() string }

func (p *Pipeline) synthSentence(ctx context.Context, t *Turn, sent string, pw io.Writer) error {
	if sent == "" {
		return nil
	}
	// Push transcript chunk to TUI as soon as we have it.
	select {
	case t.TextOut <- sent:
	default:
	}
	p.d.Send(TranscriptMsg{
		Speaker: t.Speaker.Name(), Role: t.Speaker.Role(),
		Side: t.Speaker.Side(), Text: sent,
	})
	body, err := p.d.TTS.SynthesizeStream(ctx, t.Speaker.Voice().ShortName, sent, p.d.Language)
	if err != nil {
		return err
	}
	defer body.Close()
	_, err = io.Copy(pw, body)
	return err
}

// play streams the per-turn audio reader to ffplay and the on-disk MP3 file.
// If the upstream reader yields zero bytes (e.g. the LLM call failed before
// any audio was produced), no file is written and t.AudioPath is cleared so
// the empty turn drops out of the final ffmpeg concat list.
func (p *Pipeline) play(ctx context.Context, t *Turn) error {
	if t.audioReader == nil || t.AudioPath == "" {
		return nil
	}
	n, err := audio.PlayStream(ctx, t.AudioPath, t.audioReader)
	if n == 0 {
		// Don't keep an empty-stream artefact, and don't include it in concat.
		_ = os.Remove(t.AudioPath)
		t.AudioPath = ""
	}
	return err
}

// updateMemories pushes the played turn into the transcript log AND into every
// other agent's memory (asynchronously triggers compression if large).
func (p *Pipeline) updateMemories(ctx context.Context, t *Turn) {
	full := p.d.Transcript.AppendFromTurn(t)
	for _, a := range p.d.Registry.All() {
		if a == t.Speaker {
			continue
		}
		_ = a.Listen(ctx, full)
	}
	// Final transcript event (completes the running line in the TUI).
	p.d.Send(TranscriptMsg{
		Speaker: t.Speaker.Name(), Role: t.Speaker.Role(),
		Side: t.Speaker.Side(), Text: "", Done: true,
	})
}

// Keep the llm package referenced even when no inline use exists.
var _ = llm.RoleUser
