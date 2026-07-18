package audio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// inputBufferBytes is how many bytes of mp3 we let the producer race ahead of
// realtime playback before its writes block. TTS audio is 192 kbps stereo, so
// 360 KB ≈ 15 s of audio. The OS pipe alone is only ~16 KB on macOS, which
// is too thin to absorb the LLM time-to-first-token + TTS first-byte latency
// at turn boundaries; a 15 s headroom hides typical 1–2 s gaps without
// pushing the chat-input echo delay too far out.
const inputBufferBytes = 360 * 1024

// LiveStream is a single-writer, many-reader MP3 broadcaster.
//
// Internally it pipes the writer side through `ffmpeg -re` so that bytes are
// emitted to subscribers paced at realtime. The pipeline writes per-turn TTS
// MP3 bytes to LiveStream; HTTP clients and the local CLI ffplay subscribe.
//
// All TTS turns share the uniform 48kHz/192kbps stereo CBR MP3 format, which
// is byte-concat safe (same property ConcatToMP3 relies on). Late joiners
// resync at the next MP3 frame.
type LiveStream struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	log    *slog.Logger

	inBuf *bufferedPipe // writer-side buffer in front of ffmpeg's stdin

	// Byte counters used to align text overlays with audio playback. Producer
	// writes can race ~15s ahead of realtime via inBuf; text events keyed off
	// bytesWritten can be delayed by (bytesWritten-bytesPlayed)/audioBytesPerSec.
	bytesWritten atomic.Uint64
	bytesPlayed  atomic.Uint64

	// firstWriteAt records the wall-clock instant the producer first
	// wrote a non-empty byte chunk. The encoder's audio pump reaches
	// "first real audio" within milliseconds of this (just the ffmpeg
	// -re pacer's startup latency), so it doubles as the anchor the
	// stitched mp4's t=0 lines up with after the StartOffset trim.
	// Stays zero until the first non-empty Write.
	firstWriteMu sync.Mutex
	firstWriteAt time.Time

	mu     sync.RWMutex
	subs   map[uint64]*lsSub
	nextID atomic.Uint64
	closed atomic.Bool
	done   chan struct{}
}

// AudioBytesPerSec is the constant byte rate of the LiveStream output. Every
// TTS provider emits 48kHz/192kbps stereo CBR MP3 — 192 kbit/s = 24000
// bytes/s (MP3 bitrate is total, not per-channel) — and ffmpeg's `-c copy`
// preserves that rate.
const AudioBytesPerSec = 24000

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
	ls.inBuf = newBufferedPipe(stdin, inputBufferBytes)
	go ls.pump()
	return ls, nil
}

// Write forwards mp3 bytes to ffmpeg stdin via the in-process buffer. Safe
// for concurrent calls only if the caller serialises writes (the pipeline
// uses a single producer goroutine). Blocks once the buffer is full so the
// producer can never race more than ~inputBufferBytes ahead of playback.
func (l *LiveStream) Write(p []byte) (int, error) {
	if l.closed.Load() {
		return 0, io.ErrClosedPipe
	}
	n, err := l.inBuf.Write(p)
	if n > 0 {
		l.bytesWritten.Add(uint64(n))
		l.firstWriteMu.Lock()
		if l.firstWriteAt.IsZero() {
			l.firstWriteAt = time.Now()
		}
		l.firstWriteMu.Unlock()
	}
	return n, err
}

// FirstWriteAt returns the wall-clock instant the first non-empty Write
// arrived. Zero until any byte has been written. The pipeline uses this
// to anchor sidecar VTT timestamps to the same moment the encoder's
// pump observes "first real audio" — i.e. mp4 t=0 after StartOffset
// trim. Anchoring on the first sentence's synth-completion (the older
// approach) leaves the music-bed-only prefix unaccounted for and the
// first cue lands at 00:00 even though speech doesn't start for several
// seconds.
func (l *LiveStream) FirstWriteAt() time.Time {
	l.firstWriteMu.Lock()
	defer l.firstWriteMu.Unlock()
	return l.firstWriteAt
}

// BytesAhead returns how many bytes the producer is ahead of playback. Used
// by the pipeline to delay text-event publishing so subtitles align with the
// audio the listener actually hears.
func (l *LiveStream) BytesAhead() int64 {
	w := l.bytesWritten.Load()
	p := l.bytesPlayed.Load()
	if w <= p {
		return 0
	}
	return int64(w - p)
}

// CloseInput signals end-of-stream. The buffered pipe drains remaining bytes
// to ffmpeg's stdin, then closes it; the pump drains ffmpeg's output and
// closes subscriber channels. Call once when the orchestrator finishes.
func (l *LiveStream) CloseInput() error {
	if l.closed.Swap(true) {
		return nil
	}
	l.inBuf.Close()
	return nil
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

// bufferedPipe sits between the writer (debate pipeline) and the underlying
// ffmpeg stdin pipe. It owns a bounded byte buffer; Write blocks when the
// buffer is full, and a goroutine drains the buffer into the OS pipe (which
// itself is paced by ffmpeg's `-re` realtime input clock). The point is to
// give the producer enough headroom to fully synthesize the next turn while
// the previous turn's audio is still draining, hiding LLM/TTS startup
// latency that would otherwise be audible as silence between speakers.
type bufferedPipe struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    bytes.Buffer
	maxLen int
	closed bool
	target io.WriteCloser
	done   chan struct{}
}

func newBufferedPipe(target io.WriteCloser, maxLen int) *bufferedPipe {
	bp := &bufferedPipe{maxLen: maxLen, target: target, done: make(chan struct{})}
	bp.cond = sync.NewCond(&bp.mu)
	go bp.drain()
	return bp
}

// Write copies p into the bounded buffer. If the buffer is full, the call
// blocks until the drainer makes space or Close is invoked.
func (b *bufferedPipe) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	written := 0
	for len(p) > 0 {
		if b.closed {
			return written, io.ErrClosedPipe
		}
		avail := b.maxLen - b.buf.Len()
		if avail == 0 {
			b.cond.Wait()
			continue
		}
		n := min(len(p), avail)
		_, _ = b.buf.Write(p[:n])
		p = p[n:]
		written += n
		b.cond.Broadcast()
	}
	return written, nil
}

// Close marks the pipe closed; the drainer flushes any remaining bytes and
// then closes the underlying target. Returns immediately; use the done
// channel if you need to wait for the flush to finish.
func (b *bufferedPipe) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	b.cond.Broadcast()
	b.mu.Unlock()
}

func (b *bufferedPipe) drain() {
	defer close(b.done)
	defer func() { _ = b.target.Close() }()
	chunk := make([]byte, 4096)
	for {
		b.mu.Lock()
		for b.buf.Len() == 0 && !b.closed {
			b.cond.Wait()
		}
		if b.buf.Len() == 0 && b.closed {
			b.mu.Unlock()
			return
		}
		n, _ := b.buf.Read(chunk)
		b.cond.Broadcast()
		b.mu.Unlock()
		if n == 0 {
			continue
		}
		if _, err := b.target.Write(chunk[:n]); err != nil {
			b.mu.Lock()
			b.closed = true
			b.cond.Broadcast()
			b.mu.Unlock()
			return
		}
	}
}

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
			l.bytesPlayed.Add(uint64(n))
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
