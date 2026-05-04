// Package musicmixer wraps a per-turn ffmpeg process that mixes a
// looping background music file underneath a stream of TTS mp3 bytes.
// The output mp3 stream matches the LiveStream's expected format
// (24 kHz mono 48 kbps mp3 — see audio.AudioBytesPerSec) so subscribers
// don't need to renegotiate their decoder when a music-mixed turn
// appears in the stream.
//
// Lifecycle: New() spawns ffmpeg, Write() forwards TTS bytes into
// stdin, Close() closes stdin and waits for ffmpeg to drain. Use one
// Mixer per turn — at the end of the turn, Close() flushes the tail of
// the mixed audio into the sink before the next dry-TTS turn begins
// writing directly to the LiveStream again.
package musicmixer

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
)

// musicVolume is the gain applied to the background bed before amix.
// 0.22 sits comfortably under spoken word at typical TTS levels — the
// host's voice stays clearly intelligible while the bed remains
// audible. Tunable later via topic frontmatter; constant for now.
const musicVolume = 0.22

// Mixer is the active ffmpeg subprocess for one music-mixed turn.
// The zero value is not usable; construct via New.
type Mixer struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser

	// pumpDone closes when the goroutine that copies ffmpeg's stdout
	// into the caller's sink has fully drained. Close() waits on it
	// so the tail of the mixed audio reaches the sink before the
	// caller starts writing dry TTS into the same sink.
	pumpDone chan struct{}
	pumpErr  error

	closeOnce sync.Once
	closeErr  error
}

// New spawns an ffmpeg process that:
//   - reads TTS mp3 from stdin (`-i pipe:0`),
//   - reads the background music file (`-i musicPath`),
//   - applies volume + infinite loop to the music, then mixes both via
//     amix with `duration=first` so the stream ends when the TTS ends,
//   - re-encodes to 24 kHz mono 48 kbps mp3 (matches LiveStream's
//     audio.AudioBytesPerSec contract) and writes to stdout.
//
// The stdout bytes are pumped into `sink` by a goroutine kicked off
// before New returns. Caller writes TTS via Write; on turn end calls
// Close which closes stdin, waits for ffmpeg + the pump, and returns
// the first error encountered.
func New(musicPath string, sink io.Writer) (*Mixer, error) {
	if musicPath == "" {
		return nil, errors.New("musicmixer: empty musicPath")
	}
	if sink == nil {
		return nil, errors.New("musicmixer: nil sink")
	}

	filter := fmt.Sprintf(
		"[1:a]volume=%.3f,aloop=loop=-1:size=2147483647[bg];"+
			"[0:a][bg]amix=inputs=2:duration=first:dropout_transition=0[a]",
		musicVolume,
	)

	cmd := exec.Command("ffmpeg",
		"-loglevel", "quiet",
		"-f", "mp3", "-i", "pipe:0",
		"-i", musicPath,
		"-filter_complex", filter,
		"-map", "[a]",
		"-c:a", "libmp3lame",
		"-b:a", "48k",
		"-ar", "24000",
		"-ac", "1",
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

	m := &Mixer{
		cmd:      cmd,
		stdin:    stdin,
		stdout:   stdout,
		pumpDone: make(chan struct{}),
	}
	go m.pump(sink)
	return m, nil
}

// Write forwards TTS bytes to ffmpeg's stdin. Returns the count
// reported by stdin's underlying pipe — the caller (pipeline.produce)
// already treats short writes as terminal.
func (m *Mixer) Write(p []byte) (int, error) {
	if m == nil || m.stdin == nil {
		return 0, io.ErrClosedPipe
	}
	return m.stdin.Write(p)
}

// Close finalises the turn: closes stdin, waits for ffmpeg to drain,
// waits for the stdout pump goroutine, returns the first error. Safe
// to call multiple times.
func (m *Mixer) Close() error {
	m.closeOnce.Do(func() {
		var err error
		if m.stdin != nil {
			if cerr := m.stdin.Close(); cerr != nil {
				err = fmt.Errorf("close stdin: %w", cerr)
			}
		}
		// Wait for the pump goroutine — without this, a fast caller
		// could start writing dry TTS to the sink before the tail of
		// the mixed audio has finished copying through.
		<-m.pumpDone
		if werr := m.cmd.Wait(); werr != nil && err == nil {
			err = fmt.Errorf("ffmpeg wait: %w", werr)
		}
		if m.pumpErr != nil && err == nil {
			err = m.pumpErr
		}
		m.closeErr = err
	})
	return m.closeErr
}

func (m *Mixer) pump(sink io.Writer) {
	defer close(m.pumpDone)
	// io.Copy swallows EOF — any non-nil error here is a real failure
	// (e.g. the sink rejected a write or ffmpeg crashed mid-stream).
	if _, err := io.Copy(sink, m.stdout); err != nil {
		m.pumpErr = err
	}
}
