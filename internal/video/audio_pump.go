package video

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	"github.com/sirily11/debate-bot/internal/audio"
)

// silentBufSeconds is how many seconds of silent MP3 we pre-render at startup
// and rotate through whenever the producer is idle. Big enough to avoid
// hitting the same byte offset repeatedly within a single silence window
// (which can confuse some MP3 demuxers if the bitstream is too uniform), but
// small enough that allocation is cheap.
const silentBufSeconds = 30

// generateSilentMP3 produces a contiguous MPEG-1/2 Layer 3 byte stream of the
// requested duration in the same format as Azure TTS output
// (audio-24khz-48kbitrate-mono-mp3). The bytes are read from a one-shot
// ffmpeg subprocess fed by lavfi's anullsrc so they're guaranteed to be valid
// frame-aligned MP3.
func generateSilentMP3(seconds int) ([]byte, error) {
	cmd := exec.Command("ffmpeg",
		"-loglevel", "quiet",
		"-f", "lavfi",
		"-i", "anullsrc=channel_layout=mono:sample_rate=24000",
		"-t", fmt.Sprintf("%d", seconds),
		"-c:a", "libmp3lame",
		"-b:a", "48k",
		"-f", "mp3",
		"-",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg silent generator: %w", err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("silent mp3 generator produced no bytes")
	}
	return out, nil
}

// encoderAudioPump bridges the LiveStream MP3 broadcaster to the encoder's
// ffmpeg audio input. Real bytes go through verbatim; whenever no real bytes
// are available, the pump fills the output at exactly the LiveStream byte
// rate using rotating silent MP3 frames so the HLS muxer always sees audio
// packets and never stalls a segment.
type encoderAudioPump struct {
	out    io.WriteCloser
	silent []byte
	log    *slog.Logger

	mu     sync.Mutex
	cond   *sync.Cond
	buf    bytes.Buffer
	closed bool

	// onFirstRealAudio fires exactly once, the first time feedFrom
	// observes a non-empty chunk from the LiveStream. The encoder
	// uses this to stamp the wall-clock offset between encoder start
	// and first real audio for the stitch front-trim. nil disables.
	onFirstRealAudio func()
	firstSeen        bool
}

func newEncoderAudioPump(out io.WriteCloser, silent []byte, log *slog.Logger) *encoderAudioPump {
	p := &encoderAudioPump{out: out, silent: silent, log: log}
	p.cond = sync.NewCond(&p.mu)
	return p
}

// feedFrom subscribes to the LiveStream and dumps every chunk into the pump's
// internal buffer so run() can drain it at realtime pace. Returns when the
// LiveStream's subscriber channel closes, which happens when the pump is
// closed or the LiveStream's ffmpeg shuts down.
func (p *encoderAudioPump) feedFrom(ctx context.Context, ls *audio.LiveStream) {
	sub, cancel := ls.Subscribe(128)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case chunk, ok := <-sub:
			if !ok {
				return
			}
			p.mu.Lock()
			if p.closed {
				p.mu.Unlock()
				return
			}
			fireFirst := false
			if !p.firstSeen && len(chunk) > 0 {
				p.firstSeen = true
				fireFirst = p.onFirstRealAudio != nil
			}
			p.buf.Write(chunk)
			p.cond.Broadcast()
			p.mu.Unlock()
			if fireFirst {
				p.onFirstRealAudio()
			}
		}
	}
}

// run blocks until ctx is cancelled or the pump is closed, writing exactly
// `audio.AudioBytesPerSec` bytes per wall-clock second to the underlying
// writer (drawing from real bytes first, then silent filler).
func (p *encoderAudioPump) run(ctx context.Context) {
	const tickInterval = 100 * time.Millisecond
	start := time.Now()
	var written uint64
	silentOff := 0

	t := time.NewTicker(tickInterval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return
		}
		p.mu.Unlock()

		elapsed := time.Since(start).Seconds()
		expected := uint64(elapsed * float64(audio.AudioBytesPerSec))
		if expected <= written {
			continue
		}
		need := int(expected - written)
		// Cap one tick's write to avoid a huge backlog if the timer skipped.
		if need > 4*audio.AudioBytesPerSec {
			need = 4 * audio.AudioBytesPerSec
		}

		// First, drain any real audio currently buffered.
		var realChunk []byte
		p.mu.Lock()
		if p.buf.Len() > 0 {
			n := min(p.buf.Len(), need)
			realChunk = make([]byte, n)
			_, _ = p.buf.Read(realChunk)
		}
		p.mu.Unlock()

		if len(realChunk) > 0 {
			if _, err := p.out.Write(realChunk); err != nil {
				if p.log != nil {
					p.log.Warn("encoder audio pump real write", "err", err)
				}
				return
			}
			written += uint64(len(realChunk))
			need -= len(realChunk)
		}

		// Pad the rest with silent frames so the output matches wall-clock rate.
		for need > 0 {
			avail := len(p.silent) - silentOff
			if avail == 0 {
				silentOff = 0
				avail = len(p.silent)
			}
			n := min(avail, need)
			if _, err := p.out.Write(p.silent[silentOff : silentOff+n]); err != nil {
				if p.log != nil {
					p.log.Warn("encoder audio pump silence write", "err", err)
				}
				return
			}
			silentOff += n
			written += uint64(n)
			need -= n
		}
	}
}

// close releases the pump and shuts the audio pipe so ffmpeg sees EOF on its
// audio input and exits cleanly.
func (p *encoderAudioPump) close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.cond.Broadcast()
	p.mu.Unlock()
	_ = p.out.Close()
}
