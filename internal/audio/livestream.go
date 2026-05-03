package audio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"sync/atomic"
)

// LiveStream is a single-writer, many-reader MP3 broadcaster.
//
// Internally it pipes the writer side through `ffmpeg -re` so that bytes are
// emitted to subscribers paced at realtime. The pipeline writes per-turn TTS
// MP3 bytes to LiveStream; HTTP clients and the local CLI ffplay subscribe.
//
// All Azure TTS turns share the format audio-24khz-48kbitrate-mono-mp3, which
// is byte-concat safe (same property ConcatToMP3 relies on). Late joiners
// resync at the next MP3 frame.
type LiveStream struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	log    *slog.Logger

	mu     sync.RWMutex
	subs   map[uint64]*lsSub
	nextID atomic.Uint64
	closed atomic.Bool
	done   chan struct{}
}

type lsSub struct {
	ch      chan []byte
	dropped atomic.Uint64
}

// NewLiveStream starts the ffmpeg pacer subprocess and a pump goroutine that
// fans stdout to subscribers. ffmpeg must be on PATH (VerifyTools).
func NewLiveStream(ctx context.Context, log *slog.Logger) (*LiveStream, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-loglevel", "quiet",
		"-re",
		"-f", "mp3",
		"-i", "pipe:0",
		"-c", "copy",
		"-f", "mp3",
		"pipe:1",
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}
	ls := &LiveStream{
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdout,
		log:    log,
		subs:   map[uint64]*lsSub{},
		done:   make(chan struct{}),
	}
	go ls.pump()
	return ls, nil
}

// Write forwards mp3 bytes to ffmpeg stdin. Safe for concurrent calls only if
// the caller serialises writes (the pipeline uses a single producer goroutine).
func (l *LiveStream) Write(p []byte) (int, error) {
	if l.closed.Load() {
		return 0, io.ErrClosedPipe
	}
	return l.stdin.Write(p)
}

// CloseInput signals end-of-stream to ffmpeg. The pump drains remaining bytes
// then closes subscriber channels. Call once when the orchestrator finishes.
func (l *LiveStream) CloseInput() error {
	if l.closed.Swap(true) {
		return nil
	}
	return l.stdin.Close()
}

// Subscribe returns a chunk channel and a cancel func. bufChunks is the number
// of buffered chunks per subscriber (not bytes). 64 is fine for browsers.
func (l *LiveStream) Subscribe(bufChunks int) (<-chan []byte, func()) {
	if bufChunks <= 0 {
		bufChunks = 64
	}
	s := &lsSub{ch: make(chan []byte, bufChunks)}
	id := l.nextID.Add(1)
	l.mu.Lock()
	l.subs[id] = s
	l.mu.Unlock()

	cancel := func() {
		l.mu.Lock()
		if cur, ok := l.subs[id]; ok && cur == s {
			delete(l.subs, id)
			close(s.ch)
		}
		l.mu.Unlock()
	}
	return s.ch, cancel
}

// Done returns a channel closed when the pump exits (ffmpeg ended).
func (l *LiveStream) Done() <-chan struct{} { return l.done }

// pump reads ffmpeg stdout and fans bytes out to every subscriber. Per-call
// allocations are unavoidable (each sub gets its own slice) so subscribers
// can drain independently without aliasing.
func (l *LiveStream) pump() {
	defer close(l.done)
	defer func() {
		l.mu.Lock()
		for _, s := range l.subs {
			close(s.ch)
		}
		l.subs = map[uint64]*lsSub{}
		l.mu.Unlock()
	}()

	buf := make([]byte, 4096)
	for {
		n, err := l.stdout.Read(buf)
		if n > 0 {
			l.mu.RLock()
			for id, s := range l.subs {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				select {
				case s.ch <- chunk:
				default:
					d := s.dropped.Add(1)
					if l.log != nil && (d == 1 || d%200 == 0) {
						l.log.Warn("livestream: dropping audio chunks", "sub", id, "dropped", d)
					}
				}
			}
			l.mu.RUnlock()
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && l.log != nil {
				l.log.Warn("livestream: ffmpeg stdout read", "err", err)
			}
			_ = l.cmd.Wait()
			return
		}
	}
}
