// Package musicmixer streams a looping background music bed mixed
// underneath a pipeline of TTS mp3 bytes for the WHOLE debate
// session. The output mp3 stream matches the LiveStream's expected
// format (24 kHz mono 48 kbps mp3 — see audio.AudioBytesPerSec) so
// subscribers don't need to renegotiate their decoder partway
// through.
//
// Why this is not a single ffmpeg amix process: ffmpeg's amix
// consumes samples from every input in lockstep, so when the TTS
// pipe goes idle (between turns / during LLM latency) amix blocks
// waiting for the next packet and the music bed audibly freezes.
// `dropout_transition` only triggers on EOF, not on a stalled pipe,
// and adding a `lavfi anullsrc` as a third amix input doesn't help
// either — amix still demands packets from input-0 in step with
// the others. Writing silence frames into the same pipe as TTS
// works as long as you accept the audible byproduct (silence frames
// land between TTS frames in time order rather than mixed under
// them).
//
// Instead this package mixes at the PCM level inside the Go process
// so the music side has no dependency on the TTS side's liveness:
//
//   musicCmd:  -re -stream_loop -1 -i music.mp3   →  s16le PCM stdout
//                  (always producing at exactly 1× realtime)
//   ttsCmd:    -f mp3 -i pipe:0                   →  s16le PCM stdout
//                  (decodes whatever TTS bytes the producer Writes)
//   encCmd:    -f s16le -i pipe:0                 →  mp3 stdout → sink
//
// A mix goroutine reads music PCM at its realtime cadence and on
// every chunk drains whatever TTS PCM is currently buffered (non-
// blocking), additively mixes it onto the attenuated music, and
// writes the result to the encoder. When TTS is idle the mix loop
// just emits attenuated music — no silence frames, no stalls.
package musicmixer

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// musicVolume is the linear gain applied to the background bed
// before TTS is summed in. 0.10 ≈ −20 dBFS, quiet enough to sit
// under speech without competing with it.
const musicVolume = 0.10

// pcmSampleRate / pcmChannels / pcmSampleBytes describe the PCM
// format passed between the three ffmpeg processes and the Go
// mixer. The output mp3 is re-encoded to the same sample rate
// (audio.AudioBytesPerSec contract).
const (
	pcmSampleRate  = 24000
	pcmChannels    = 1
	pcmSampleBytes = 2 // s16le
)

// chunkDuration is how much PCM the mix loop processes per
// iteration. 50 ms is a small enough window that a turn's first TTS
// byte lands in audible output within ~one chunk, while large
// enough to amortise per-iteration overhead.
const chunkSamples = pcmSampleRate / 20 // 50 ms
const chunkBytes = chunkSamples * pcmChannels * pcmSampleBytes

// ttsBufferChunks bounds the in-Go TTS PCM backlog. TTS arrives
// faster than realtime, so the decoder produces PCM bursts; this
// channel queues them until the realtime-paced mix loop drains
// them. ~5 s of headroom matches LiveStream.inputBufferBytes.
const ttsBufferChunks = 100

// Mixer wraps the three ffmpeg processes plus the Go mix goroutine.
// The zero value is not usable; construct via NewSession.
type Mixer struct {
	musicCmd *exec.Cmd
	ttsCmd   *exec.Cmd
	encCmd   *exec.Cmd

	musicOut io.ReadCloser // music PCM (paced 1× realtime)
	ttsIn    io.WriteCloser
	ttsOut   io.ReadCloser // TTS PCM
	encIn    io.WriteCloser
	encOut   io.ReadCloser // mixed mp3

	// ttsCh delivers TTS PCM chunks from the ttsReader goroutine to
	// the mix loop. Buffered so a fast TTS burst doesn't block the
	// reader; bounded so a runaway producer can't accumulate
	// unbounded memory.
	ttsCh chan []byte

	// ttsPCMIngested / ttsPCMDrained count TTS PCM bytes queued by
	// ttsReader vs drained by mixLoop. Their difference is the audio
	// currently buffered between mixer.Write and the encoder — bytes
	// the producer has already pushed in but that haven't reached
	// LiveStream yet. BufferedAudio() exposes this so the pipeline's
	// subtitle playhead can account for the mixer-side lag that
	// LiveStream.BytesAhead can't see.
	ttsPCMIngested atomic.Uint64
	ttsPCMDrained  atomic.Uint64

	pumpDone   chan struct{}
	mixDone    chan struct{}
	readerDone chan struct{}
	pumpErr    error
	mixErr     error

	closeOnce sync.Once
	closeErr  error
}

// NewSession spawns the three ffmpeg processes and the goroutines
// that wire them together. Caller writes TTS mp3 bytes via Write
// during a turn; on session end calls Close, which closes the TTS
// stdin (signalling EOF to its decoder), waits for the mix loop to
// drain the in-flight buffers, and tears the music + encoder
// processes down.
func NewSession(musicPath string, sink io.Writer) (*Mixer, error) {
	if musicPath == "" {
		return nil, errors.New("musicmixer: empty musicPath")
	}
	if sink == nil {
		return nil, errors.New("musicmixer: nil sink")
	}

	// Music decoder: -re paces reads at realtime, -stream_loop -1
	// loops the file forever so the bed never runs dry. Output is
	// raw s16le PCM at the pipeline sample rate.
	musicCmd := exec.Command("ffmpeg",
		"-loglevel", "quiet",
		"-re",
		"-stream_loop", "-1",
		"-i", musicPath,
		"-f", "s16le",
		"-ar", fmt.Sprintf("%d", pcmSampleRate),
		"-ac", fmt.Sprintf("%d", pcmChannels),
		"pipe:1",
	)
	musicOut, err := musicCmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("music stdout: %w", err)
	}

	// TTS decoder: reads mp3 from stdin (the caller's Write feeds
	// this), emits PCM at the pipeline sample rate. Independent
	// from the music process so a stalled pipe here can't block
	// music's realtime output.
	ttsCmd := exec.Command("ffmpeg",
		"-loglevel", "quiet",
		"-f", "mp3",
		"-i", "pipe:0",
		"-f", "s16le",
		"-ar", fmt.Sprintf("%d", pcmSampleRate),
		"-ac", fmt.Sprintf("%d", pcmChannels),
		"pipe:1",
	)
	ttsIn, err := ttsCmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("tts stdin: %w", err)
	}
	ttsOut, err := ttsCmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("tts stdout: %w", err)
	}

	// Encoder: PCM → mp3 at the LiveStream-expected format.
	encCmd := exec.Command("ffmpeg",
		"-loglevel", "quiet",
		"-f", "s16le",
		"-ar", fmt.Sprintf("%d", pcmSampleRate),
		"-ac", fmt.Sprintf("%d", pcmChannels),
		"-i", "pipe:0",
		"-c:a", "libmp3lame",
		"-b:a", "48k",
		"-f", "mp3",
		"pipe:1",
	)
	encIn, err := encCmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("enc stdin: %w", err)
	}
	encOut, err := encCmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("enc stdout: %w", err)
	}

	if err := musicCmd.Start(); err != nil {
		return nil, fmt.Errorf("start music ffmpeg: %w", err)
	}
	if err := ttsCmd.Start(); err != nil {
		_ = musicCmd.Process.Kill()
		return nil, fmt.Errorf("start tts ffmpeg: %w", err)
	}
	if err := encCmd.Start(); err != nil {
		_ = musicCmd.Process.Kill()
		_ = ttsCmd.Process.Kill()
		return nil, fmt.Errorf("start enc ffmpeg: %w", err)
	}

	m := &Mixer{
		musicCmd:   musicCmd,
		ttsCmd:     ttsCmd,
		encCmd:     encCmd,
		musicOut:   musicOut,
		ttsIn:      ttsIn,
		ttsOut:     ttsOut,
		encIn:      encIn,
		encOut:     encOut,
		ttsCh:      make(chan []byte, ttsBufferChunks),
		pumpDone:   make(chan struct{}),
		mixDone:    make(chan struct{}),
		readerDone: make(chan struct{}),
	}
	go m.pump(sink)
	go m.ttsReader()
	go m.mixLoop()
	return m, nil
}

// Write forwards TTS mp3 bytes into the TTS decoder's stdin. The
// decoder's PCM output is consumed asynchronously by the mix loop,
// so a slow consumer eventually back-pressures the caller via the
// underlying pipe — same behaviour as the previous single-ffmpeg
// implementation.
func (m *Mixer) Write(p []byte) (int, error) {
	if m == nil || m.ttsIn == nil {
		return 0, io.ErrClosedPipe
	}
	return m.ttsIn.Write(p)
}

// Close finalises the session: signals EOF to the TTS decoder so
// its remaining PCM drains through the mix loop; closes the
// encoder's stdin to flush its mp3 muxer trailer; SIGINTs the music
// decoder (it would otherwise loop forever); and waits for the
// goroutines + processes to exit. Safe to call multiple times.
func (m *Mixer) Close() error {
	m.closeOnce.Do(func() {
		var firstErr error
		// 1. EOF the TTS decoder. Its PCM stdout drains; the
		//    ttsReader goroutine sees EOF and closes m.ttsCh.
		if m.ttsIn != nil {
			if cerr := m.ttsIn.Close(); cerr != nil {
				firstErr = fmt.Errorf("close tts stdin: %w", cerr)
			}
		}
		<-m.readerDone

		// 2. Stop the music decoder. -re/-stream_loop -1 means it
		//    would otherwise run forever; SIGINT makes it exit
		//    cleanly so the mix loop sees musicOut EOF and stops.
		if m.musicCmd != nil && m.musicCmd.Process != nil {
			_ = m.musicCmd.Process.Signal(syscall.SIGINT)
		}
		<-m.mixDone

		// 3. Close the encoder's stdin so it writes the mp3 muxer
		//    trailer; pump finishes copying the tail to the sink.
		if m.encIn != nil {
			_ = m.encIn.Close()
		}
		<-m.pumpDone

		// 4. Reap subprocesses. Non-zero exits from SIGINT'd
		//    music ffmpeg are expected and not surfaced.
		_ = m.musicCmd.Wait()
		_ = m.ttsCmd.Wait()
		_ = m.encCmd.Wait()

		if m.mixErr != nil && firstErr == nil {
			firstErr = m.mixErr
		}
		if m.pumpErr != nil && firstErr == nil {
			firstErr = m.pumpErr
		}
		m.closeErr = firstErr
	})
	return m.closeErr
}

// ttsReader copies TTS PCM out of the decoder into a buffered
// channel so the mix loop can drain it non-blockingly. Closes the
// channel on EOF so the mix loop can detect a finished session.
func (m *Mixer) ttsReader() {
	defer close(m.readerDone)
	defer close(m.ttsCh)
	buf := make([]byte, chunkBytes)
	for {
		n, err := io.ReadFull(m.ttsOut, buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			m.ttsPCMIngested.Add(uint64(n))
			m.ttsCh <- chunk
		}
		if err != nil {
			return
		}
	}
}

// pcmBytesPerSec is the byte rate of the s16le mono PCM that flows
// between ttsReader and mixLoop — used to convert the
// ingested-minus-drained byte deficit into an audio-time duration
// for BufferedAudio.
const pcmBytesPerSec = pcmSampleRate * pcmChannels * pcmSampleBytes

// BufferedAudio reports how much TTS audio is queued inside the
// mixer between Write() and the mix loop's drain pointer. The
// pipeline adds this to its subtitle playhead so a fast burst of
// TTS bytes (typical of the puzzle Q&A, where short turns synth
// faster than 1× realtime) doesn't make TranscriptMsgs fire ahead
// of the audio they describe — without this, the subtitle visibly
// ends before its audio does.
func (m *Mixer) BufferedAudio() time.Duration {
	if m == nil {
		return 0
	}
	ingested := m.ttsPCMIngested.Load()
	drained := m.ttsPCMDrained.Load()
	if ingested <= drained {
		return 0
	}
	return time.Duration(ingested-drained) * time.Second / pcmBytesPerSec
}

// mixLoop is the heart of the mixer. It reads music PCM at the
// rate the music ffmpeg emits it (1× realtime, paced by -re),
// mixes any currently-buffered TTS PCM on top, and writes the
// result to the encoder. Music alone flows through during gaps —
// no silence frames, no amix stalls.
func (m *Mixer) mixLoop() {
	defer close(m.mixDone)
	music := make([]byte, chunkBytes)
	var residual []byte // TTS PCM left over from a previous chunk

	for {
		if _, err := io.ReadFull(m.musicOut, music); err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				m.mixErr = fmt.Errorf("read music: %w", err)
			}
			return
		}

		// Drain whatever TTS PCM is queued without blocking, so
		// the music side never waits on TTS even briefly.
		for len(residual) < len(music) {
			select {
			case chunk, ok := <-m.ttsCh:
				if !ok {
					m.ttsCh = nil // drained + closed; stop selecting
					break
				}
				residual = append(residual, chunk...)
				continue
			default:
			}
			break
		}

		mixInto(music, residual, musicVolume)
		drained := min(len(music), len(residual))
		if drained > 0 {
			m.ttsPCMDrained.Add(uint64(drained))
		}
		if len(residual) >= len(music) {
			residual = residual[len(music):]
		} else {
			residual = nil
		}

		if _, err := m.encIn.Write(music); err != nil {
			m.mixErr = fmt.Errorf("write enc: %w", err)
			return
		}
	}
}

// mixInto attenuates `music` by `volume` in place and additively
// mixes the leading `len(music)` bytes of `tts` on top, clipping
// to int16 range. Both buffers are s16le-encoded mono PCM at the
// pipeline sample rate.
func mixInto(music, tts []byte, volume float32) {
	for i := 0; i+1 < len(music); i += 2 {
		m := int16(binary.LittleEndian.Uint16(music[i:]))
		mixed := int32(float32(m) * volume)
		if i+1 < len(tts) {
			t := int16(binary.LittleEndian.Uint16(tts[i:]))
			mixed += int32(t)
		}
		if mixed > 32767 {
			mixed = 32767
		} else if mixed < -32768 {
			mixed = -32768
		}
		binary.LittleEndian.PutUint16(music[i:], uint16(mixed))
	}
}

func (m *Mixer) pump(sink io.Writer) {
	defer close(m.pumpDone)
	if _, err := io.Copy(sink, m.encOut); err != nil {
		m.pumpErr = err
	}
}
