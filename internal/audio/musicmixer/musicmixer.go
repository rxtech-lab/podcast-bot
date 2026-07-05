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
//	musicCmd:  -re -stream_loop -1 -i music.mp3   →  s16le PCM stdout
//	               (always producing at exactly 1× realtime)
//	ttsCmd:    -f mp3 -i pipe:0                   →  s16le PCM stdout
//	               (decodes whatever TTS bytes the producer Writes)
//	encCmd:    -f s16le -i pipe:0                 →  mp3 stdout → sink
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
	"syscall"
	"time"
)

// closeStageTimeout caps how long Close() waits at each of its three
// blocking handoffs (TTS reader EOF, mixLoop drain, encoder pump
// finish). A wedged subprocess or a stuck overlay would otherwise pin
// Close forever, blocking the channel runner from advancing to the
// next debate. On timeout Close force-kills every subprocess so the
// reader/mixer/pump goroutines unblock; we accept a small audible cut
// in trade for never freezing the channel.
const closeStageTimeout = 15 * time.Second

// postKillTimeout caps the wait for a goroutine / subprocess to exit
// AFTER killAll has SIGKILL'd everything. Once the kernel tears the
// pipes down the reader/mixer/pump goroutines should unblock within
// milliseconds; if they don't, the goroutine is wedged on something
// that isn't a pipe (e.g. a blocked write into a downstream sink that
// stopped consuming), and there's nothing more Close() can do about it.
// Bounding the wait lets Close() return so callers (the pipeline
// cleanup path) aren't pinned indefinitely. We log + leak in trade.
const postKillTimeout = 5 * time.Second

// musicVolume is the linear gain applied to the background bed
// before TTS is summed in. 0.10 ≈ −20 dBFS, quiet enough to sit
// under speech without competing with it.
const musicVolume = 0.10

// ttsVolume is the linear gain applied to TTS PCM before it's mixed
// in. 0.80 ≈ −1.9 dBFS, slightly attenuated from raw so the speaker
// sits at a comfortable volume above the music bed without clipping
// when peaks stack with the music. Tuned by ear.
const ttsVolume = 0.80

// overlayVolume is the linear gain applied to one-shot overlay clips
// dispatched via OverlapClip. Sits between musicVolume and ttsVolume —
// stingers should be clearly audible alongside narration without
// burying the speaker. Tunable per call via OverlapClip's volume arg
// (zero falls back to this default).
const overlayVolume = 0.35

// musicDuckFactor is how much the bed is attenuated while at least one
// overlay clip is in flight. 0.30 → bed drops to 30% of musicVolume,
// i.e. 0.10 × 0.30 = 0.03 absolute (~3% linear, ~−30 dBFS) so an
// overlapped Lyria stinger reads as the dominant texture and the
// original bed only ghosts underneath. Restored to musicVolume once
// all overlays drain.
const musicDuckFactor = 0.30

// musicDuckRampPerChunk caps how much the bed gain may move per mix
// iteration when ducking attacks / releases. 0.05 → reaches the duck
// floor in ~12 chunks (~600 ms at 50 ms/chunk) which reads as a smooth
// dip rather than a click. Same constant used for both attack and
// release; symmetric ramps avoid pumping artefacts.
const musicDuckRampPerChunk = 0.05

// musicCrossfadeChunks is how many mixLoop iterations the bed cross-
// fade takes on a ReplaceMusic call. chunkDuration × this constant
// = total fade window — kept around 1 s so the texture transition is
// clearly perceptible without dragging.
const musicCrossfadeChunks = 20 // 20 × 50 ms = 1 s

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

// ttsDeClickSamples applies a tiny gain envelope at TTS burst edges. The TTS
// path is fed by many independently encoded MP3 clips; even when the speech is
// clean, clip boundaries can decode to a discontinuity that sounds like a
// digital tick between lines. 8 ms is long enough to hide the discontinuity
// without making narration attacks feel faded.
const ttsDeClickSamples = pcmSampleRate * 8 / 1000

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

	// overlaysMu guards overlays — the slice of in-flight overlay
	// streams started by OverlapClip. The mix loop drains every entry
	// each iteration; a goroutine per clip pushes PCM into its channel
	// and closes the channel at EOF. The mix loop sweeps closed entries
	// out of the slice between iterations.
	overlaysMu sync.Mutex
	overlays   []*overlayStream

	// ttsScaleMu guards ttsScale — the per-turn TTS volume multiplier
	// applied on top of ttsVolume. Pipeline sets this to <1.0 during
	// turns where the speaker should sit lower under the bed (e.g.
	// puzzle surface narration), and restores to 1.0 between turns.
	// 0 / unset is treated as 1.0 by the mix loop.
	ttsScaleMu sync.Mutex
	ttsScale   float32

	// swapCh requests a music-bed cross-fade from ReplaceMusic. The
	// mix loop picks up the request at the next chunk boundary, spawns
	// a decoder for the new path, fades the old bed out / new bed in
	// over musicCrossfadeChunks iterations, then promotes the new
	// decoder to be the active musicCmd / musicOut. Single-buffered
	// so an in-flight swap blocks a second swap attempt — keeps the
	// fade-state machine simple.
	swapCh chan musicSwap

	pumpDone   chan struct{}
	mixDone    chan struct{}
	readerDone chan struct{}
	pumpErr    error
	mixErr     error

	// drainCh signals the mix loop to exit once every queued TTS PCM
	// chunk has been mixed into output. Close()'s old SIGINT-the-music-
	// then-wait-for-mixDone sequence dropped any TTS PCM still sitting
	// in ttsCh / residual at session end (audible as the previous
	// debate's last sentence cutting mid-word at sequential handoffs).
	// Close() closes drainCh AFTER readerDone so mixLoop knows TTS is
	// fully queued, and mixLoop polls drainCh + ttsCh==nil + len(residual)==0
	// at the top of each iteration to decide when it's safe to exit.
	drainCh chan struct{}

	// syncMu guards the TTS→output timeline map. The mix loop appends a
	// sync point at every idle→active TTS transition: TTS that arrives
	// while the loop is idle starts playing at the CURRENT output
	// position, so any bed-only gap the session accumulated (slow LLM,
	// TTS retries) shifts everything after it. Subtitle cue offsets are
	// exact on the TTS byte timeline; MapTTSToOutput converts them to
	// positions in the recorded output file.
	syncMu     sync.Mutex
	syncPoints []timelineSyncPoint
	ttsMixed   int64 // cumulative TTS PCM bytes mixed into output
	outMixed   int64 // cumulative output PCM bytes handed to the encoder

	closeOnce sync.Once
	closeErr  error
}

// timelineSyncPoint records that TTS-timeline position tts started
// playing at output-timeline position out. Between consecutive points
// TTS plays continuously, so interior positions map linearly.
type timelineSyncPoint struct {
	tts time.Duration
	out time.Duration
}

// pcmBytesToDuration converts a count of s16le PCM bytes at the
// pipeline format to play time.
func pcmBytesToDuration(n int64) time.Duration {
	return time.Duration(n) * time.Second /
		time.Duration(pcmSampleRate*pcmChannels*pcmSampleBytes)
}

// recordTTSBurstStart appends a sync point marking that the TTS stream
// (at cumulative position ttsMixed) resumed at the current output
// position. Called by the mix loop on every idle→active transition.
func (m *Mixer) recordTTSBurstStart() {
	m.syncMu.Lock()
	m.syncPoints = append(m.syncPoints, timelineSyncPoint{
		tts: pcmBytesToDuration(m.ttsMixed),
		out: pcmBytesToDuration(m.outMixed),
	})
	m.syncMu.Unlock()
}

// advanceTimeline accounts one mix iteration: ttsBytes of TTS PCM were
// mixed into an output chunk of outBytes.
func (m *Mixer) advanceTimeline(ttsBytes, outBytes int) {
	m.syncMu.Lock()
	m.ttsMixed += int64(ttsBytes)
	m.outMixed += int64(outBytes)
	m.syncMu.Unlock()
}

// MapTTSToOutput converts a position on the TTS input timeline (the
// cumulative duration of TTS audio written via Write, e.g. a subtitle
// cue offset derived from byte counts) to the corresponding position in
// the mixer's output stream — i.e. in the recorded audio file. Bed-only
// gaps that the session accumulated between TTS bursts are reflected by
// the sync points; positions between points map linearly, and positions
// past the newest point extrapolate from it (exact unless another gap
// follows, in which case a later call self-corrects). Identity before
// any TTS has been mixed.
func (m *Mixer) MapTTSToOutput(tts time.Duration) time.Duration {
	if m == nil {
		return tts
	}
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	pts := m.syncPoints
	if len(pts) == 0 {
		return tts
	}
	// Binary search: last point with pts[i].tts <= tts.
	lo, hi := 0, len(pts)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if pts[mid].tts <= tts {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	if pts[lo].tts > tts {
		// Position precedes the first burst — clamp onto it.
		return pts[0].out
	}
	return pts[lo].out + (tts - pts[lo].tts)
}

// overlayStream tracks one in-flight clip dispatched via OverlapClip.
// The decoder ffmpeg subprocess writes PCM into pcmCh; the reader
// goroutine closes pcmCh at EOF; the mix loop sweeps the closed entry
// out of Mixer.overlays between iterations.
type overlayStream struct {
	cmd    *exec.Cmd
	out    io.ReadCloser
	pcmCh  chan []byte
	volume float32
	done   chan struct{}
}

// musicSwap is one ReplaceMusic request. The new ffmpeg subprocess is
// already started by ReplaceMusic so the mix loop can read from out
// immediately on cross-fade entry.
type musicSwap struct {
	cmd *exec.Cmd
	out io.ReadCloser
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
		swapCh:     make(chan musicSwap, 1),
		ttsScale:   1,
		pumpDone:   make(chan struct{}),
		mixDone:    make(chan struct{}),
		readerDone: make(chan struct{}),
		drainCh:    make(chan struct{}),
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
// its remaining PCM drains through the mix loop; signals mixLoop to
// exit once that PCM has actually been mixed into output (the music
// decoder keeps running at realtime cadence during this drain so the
// listener hears every TTS sample); SIGINTs the music decoder; closes
// the encoder's stdin to flush its mp3 muxer trailer; and waits for
// the goroutines + processes to exit. Safe to call multiple times.
//
// The drain step replaced the original "SIGINT music immediately,
// then wait for mixDone" sequence — that path made mixLoop exit on
// musicOut EOF as soon as music was killed, which dropped whatever
// TTS PCM was still queued in ttsCh / residual. At sequential debate
// handoffs the dropped tail was the previous debate's last 1–3 s of
// audio, which presented as a sentence cutting mid-word.
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
		if !waitWithTimeout(m.readerDone, closeStageTimeout) {
			m.killAll()
			// Bounded post-kill wait. Mixer has no logger field, so
			// a wedge here returns silently and the goroutine leaks.
			_ = waitWithTimeout(m.readerDone, postKillTimeout)
		}

		// 2. Tell mixLoop it can exit once the queued TTS PCM has
		//    been fully mixed in. Music keeps running at realtime
		//    cadence during this window so the bed plays under the
		//    final TTS bytes; mixLoop's drainCh check exits the loop
		//    once ttsCh is closed-drained AND residual is empty.
		close(m.drainCh)
		if !waitWithTimeout(m.mixDone, closeStageTimeout) {
			m.killAll()
			_ = waitWithTimeout(m.mixDone, postKillTimeout)
		}

		// 3. Stop the music decoder now that mixLoop has exited.
		//    -re/-stream_loop -1 means it would otherwise run
		//    forever. The mix loop owns m.musicCmd / m.musicOut
		//    after a ReplaceMusic — read the pointer directly.
		if m.musicCmd != nil && m.musicCmd.Process != nil {
			_ = m.musicCmd.Process.Signal(syscall.SIGINT)
		}

		// 4. Close the encoder's stdin so it writes the mp3 muxer
		//    trailer; pump finishes copying the tail to the sink.
		if m.encIn != nil {
			_ = m.encIn.Close()
		}
		if !waitWithTimeout(m.pumpDone, closeStageTimeout) {
			m.killAll()
			_ = waitWithTimeout(m.pumpDone, postKillTimeout)
		}

		// 5. Reap subprocesses. Non-zero exits from SIGINT'd
		//    music ffmpeg are expected and not surfaced. Each
		//    Wait is bounded so a wedged subprocess (or a Wait
		//    racing with goroutines that haven't released their
		//    pipe handles) can't pin Close indefinitely. After
		//    timeout we SIGKILL and accept that the entry leaks.
		waitCmdWithTimeout(m.musicCmd, postKillTimeout)
		waitCmdWithTimeout(m.ttsCmd, postKillTimeout)
		waitCmdWithTimeout(m.encCmd, postKillTimeout)
		// 6. Reap any overlay clips still in flight at session end.
		m.overlaysMu.Lock()
		for _, ov := range m.overlays {
			if ov.cmd != nil && ov.cmd.Process != nil {
				_ = ov.cmd.Process.Signal(syscall.SIGINT)
				waitCmdWithTimeout(ov.cmd, postKillTimeout)
			}
		}
		m.overlays = nil
		m.overlaysMu.Unlock()

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

// waitWithTimeout blocks until ch is closed or d elapses. Returns true
// when ch closed in time, false on timeout. Callers escalate to
// force-kill on false.
func waitWithTimeout(ch <-chan struct{}, d time.Duration) bool {
	select {
	case <-ch:
		return true
	case <-time.After(d):
		return false
	}
}

// waitCmdWithTimeout reaps cmd with a wall-clock cap. exec.Cmd.Wait
// blocks until every goroutine attached to the process's stdout /
// stderr pipes has returned; if one of those is wedged (e.g. an
// io.Copy targeting a sink that stopped consuming), Wait can hang
// indefinitely even after the process has exited. SIGKILL on timeout
// + accept the goroutine leak so callers aren't pinned forever.
func waitCmdWithTimeout(cmd *exec.Cmd, d time.Duration) {
	if cmd == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case <-done:
		case <-time.After(d):
		}
	}
}

// killAll SIGKILLs every subprocess the mixer started. Used as the
// escalation step when one of Close's three blocking handoffs hits its
// timeout — the goroutines waiting on subprocess pipes unblock once
// the kernel tears the pipes down. Idempotent: kill on an already-
// exited process is a no-op error we ignore.
func (m *Mixer) killAll() {
	if m.ttsCmd != nil && m.ttsCmd.Process != nil {
		_ = m.ttsCmd.Process.Kill()
	}
	if m.musicCmd != nil && m.musicCmd.Process != nil {
		_ = m.musicCmd.Process.Kill()
	}
	if m.encCmd != nil && m.encCmd.Process != nil {
		_ = m.encCmd.Process.Kill()
	}
	m.overlaysMu.Lock()
	for _, ov := range m.overlays {
		if ov != nil && ov.cmd != nil && ov.cmd.Process != nil {
			_ = ov.cmd.Process.Kill()
		}
	}
	m.overlaysMu.Unlock()
}

// OverlapMusic is a free-standing wrapper around (*Mixer).OverlapClip with
// the default overlay volume. Mirrors ReplaceMusic so the dispatch surface
// reads symmetrically: overlapMusic(source) / replaceMusic(source). Kept as a
// thin shim so existing callers of (*Mixer).OverlapClip — and any operator
// that wants a non-default volume — keep working unchanged.
func OverlapMusic(m *Mixer, source string) error {
	if m == nil {
		return errors.New("musicmixer: nil mixer")
	}
	return m.OverlapClip(source, 0)
}

// ReplaceMusic is a free-standing companion to OverlapMusic. Cross-fades the
// active background bed over to source and keeps the new clip looped. Same
// behaviour as (*Mixer).ReplaceMusic — the package-level form just keeps the
// API symmetric with OverlapMusic at the dispatch sites.
func ReplaceMusic(m *Mixer, source string) error {
	if m == nil {
		return errors.New("musicmixer: nil mixer")
	}
	return m.ReplaceMusic(source)
}

// OverlapClip kicks off a one-shot overlay stream: an ffmpeg decoder
// reads path, emits PCM at the pipeline format, and the mix loop
// additively layers it on top of the music + TTS. The clip plays
// once (no looping) — short atmospheric stingers, single events. The
// clip's natural duration ends the overlay; reaped automatically.
//
// volume is the linear gain applied to the overlay's PCM; pass 0 to
// fall back to overlayVolume. Returns an error only on subprocess-
// start failure; a missing / unreadable file surfaces later as the
// overlay producing zero PCM (logged but not fatal). Safe to call
// concurrently — multiple overlays mix simultaneously.
func (m *Mixer) OverlapClip(path string, volume float32) error {
	if m == nil {
		return errors.New("musicmixer: nil mixer")
	}
	if path == "" {
		return errors.New("musicmixer: empty overlay path")
	}
	if volume <= 0 {
		volume = overlayVolume
	}
	cmd := exec.Command("ffmpeg",
		"-loglevel", "quiet",
		"-i", path,
		"-f", "s16le",
		"-ar", fmt.Sprintf("%d", pcmSampleRate),
		"-ac", fmt.Sprintf("%d", pcmChannels),
		"pipe:1",
	)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("overlay stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start overlay ffmpeg: %w", err)
	}
	ov := &overlayStream{
		cmd:    cmd,
		out:    out,
		pcmCh:  make(chan []byte, ttsBufferChunks),
		volume: volume,
		done:   make(chan struct{}),
	}
	m.overlaysMu.Lock()
	m.overlays = append(m.overlays, ov)
	m.overlaysMu.Unlock()
	go func() {
		defer close(ov.pcmCh)
		buf := make([]byte, chunkBytes)
		for {
			n, err := io.ReadFull(out, buf)
			if n > 0 {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				ov.pcmCh <- chunk
			}
			if err != nil {
				return
			}
		}
	}()
	return nil
}

// SetTTSVolumeScale multiplies the mixer's default ttsVolume by scale
// for subsequent mix iterations until reset. Use cases: per-turn
// volume contouring (e.g. puzzle surface narration sits lower under
// the music bed than ordinary Q&A turns). Pass 1 (or any value <= 0,
// which is normalised to 1) to restore the default. Safe to call from
// any goroutine — the mix loop reads the current value at each chunk.
func (m *Mixer) SetTTSVolumeScale(scale float32) {
	if m == nil {
		return
	}
	if scale <= 0 {
		scale = 1
	}
	m.ttsScaleMu.Lock()
	m.ttsScale = scale
	m.ttsScaleMu.Unlock()
}

// currentTTSScale reads the active TTS scale under the lock. Returns
// 1 when uninitialised so a zero-value scale doesn't silence the
// speaker.
func (m *Mixer) currentTTSScale() float32 {
	m.ttsScaleMu.Lock()
	defer m.ttsScaleMu.Unlock()
	if m.ttsScale <= 0 {
		return 1
	}
	return m.ttsScale
}

// ReplaceMusic cross-fades the active background bed over to a new
// looping clip at path. The new decoder uses the same -re /
// -stream_loop -1 contract as the session's original bed, so the
// replacement plays forever once the fade settles. The fade itself
// runs over musicCrossfadeChunks mix iterations (~1 s); during the
// window both beds play at proportional gains so there's no audible
// gap or flicker. A second ReplaceMusic call while a fade is still in
// flight blocks until the in-flight fade completes — keeps the
// state machine simple and avoids double-fade artifacts.
//
// Returns an error only on subprocess-start failure; mid-fade decoder
// failures are logged inside mixLoop and the old bed continues
// uninterrupted.
func (m *Mixer) ReplaceMusic(path string) error {
	if m == nil {
		return errors.New("musicmixer: nil mixer")
	}
	if path == "" {
		return errors.New("musicmixer: empty replacement music path")
	}
	cmd := exec.Command("ffmpeg",
		"-loglevel", "quiet",
		"-re",
		"-stream_loop", "-1",
		"-i", path,
		"-f", "s16le",
		"-ar", fmt.Sprintf("%d", pcmSampleRate),
		"-ac", fmt.Sprintf("%d", pcmChannels),
		"pipe:1",
	)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("replace stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start replace ffmpeg: %w", err)
	}
	// Block on the swap channel until the mix loop picks up an in-
	// flight fade — single-buffered, so a back-to-back ReplaceMusic
	// waits here rather than racing the fade state machine.
	m.swapCh <- musicSwap{cmd: cmd, out: out}
	return nil
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
			m.ttsCh <- chunk
		}
		if err != nil {
			return
		}
	}
}

// mixLoop is the heart of the mixer. It reads music PCM at the
// rate the music ffmpeg emits it (1× realtime, paced by -re),
// mixes any currently-buffered TTS PCM on top, plus any in-flight
// overlay streams and the cross-fade tail of a pending music swap,
// and writes the result to the encoder. Music alone flows through
// during gaps — no silence frames, no amix stalls.
func (m *Mixer) mixLoop() {
	defer close(m.mixDone)
	music := make([]byte, chunkBytes)
	var residual []byte // TTS PCM left over from a previous chunk

	// fadeState is non-zero while a ReplaceMusic cross-fade is in
	// progress. The new bed reads happen from `nextOut`; once
	// fadeRemaining hits zero the new decoder is promoted and the old
	// one is reaped.
	type fadeState struct {
		nextCmd *exec.Cmd
		nextOut io.ReadCloser
		// remaining counts down from musicCrossfadeChunks. The current
		// chunk's blend factor is (1 - remaining/musicCrossfadeChunks),
		// so the new bed ramps 0→1 across the window.
		remaining int
	}
	var fade *fadeState
	nextChunk := make([]byte, chunkBytes)
	mixBuf := make([]byte, chunkBytes)
	ttsActive := false

	// bedScale is the running duck multiplier applied to the music bed
	// gains. Starts at 1 (no duck) and ramps toward musicDuckFactor
	// while any overlay is in flight, then back to 1 once they all
	// drain. Smooth ramps (musicDuckRampPerChunk per iteration) keep
	// the attack/release inaudible.
	bedScale := float32(1)

	for {
		// Drain-and-exit check. Close() closes drainCh AFTER readerDone,
		// which guarantees ttsCh has been closed and every TTS PCM byte
		// is either sitting in ttsCh or already in residual. Once both
		// are empty, mixing more music chunks would just produce silence
		// past the end of the session, so exit cleanly here. Without
		// this check Close() used to SIGINT music immediately, which
		// dropped the unmixed tail of TTS audio (audible as a clipped
		// final sentence at sequential debate handoffs).
		select {
		case <-m.drainCh:
			if m.ttsCh == nil && len(residual) == 0 {
				return
			}
		default:
		}

		// Pick up a pending music-swap request before reading the next
		// chunk. We drain at most one — the channel is single-buffered
		// so a second swap during a fade waits for ReplaceMusic to
		// re-signal once this fade completes.
		if fade == nil {
			select {
			case swap := <-m.swapCh:
				fade = &fadeState{
					nextCmd:   swap.cmd,
					nextOut:   swap.out,
					remaining: musicCrossfadeChunks,
				}
			default:
			}
		}

		if _, err := io.ReadFull(m.musicOut, music); err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				m.mixErr = fmt.Errorf("read music: %w", err)
			}
			return
		}
		// Compute the linear cross-fade gains for this chunk. Without
		// an active fade, oldGain = musicVolume and newGain = 0 (no
		// next bed read). Mid-fade the two gains sum to musicVolume so
		// the perceived bed level stays constant through the swap.
		oldGain := float32(musicVolume)
		newGain := float32(0)
		if fade != nil {
			t := float32(musicCrossfadeChunks-fade.remaining+1) / float32(musicCrossfadeChunks)
			if t > 1 {
				t = 1
			}
			oldGain = float32(musicVolume) * (1 - t)
			newGain = float32(musicVolume) * t
			if _, err := io.ReadFull(fade.nextOut, nextChunk); err != nil {
				// New bed died unexpectedly mid-fade — abandon the swap
				// rather than killing the whole session. The old bed
				// keeps playing; ReplaceMusic can be retried.
				_ = fade.nextOut.Close()
				if fade.nextCmd != nil && fade.nextCmd.Process != nil {
					_ = fade.nextCmd.Process.Signal(syscall.SIGINT)
					_ = fade.nextCmd.Wait()
				}
				fade = nil
				oldGain = float32(musicVolume)
				newGain = 0
			}
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

		// Duck the bed while any overlay is in flight: ramp bedScale
		// toward musicDuckFactor when overlays exist, back to 1 when
		// they don't. Snapshot first so the same overlay slice drives
		// both the duck decision and the actual mix; otherwise the
		// duck would lag the overlay by one chunk.
		overlays := m.snapshotOverlays()
		duckTarget := float32(1)
		if len(overlays) > 0 {
			duckTarget = float32(musicDuckFactor)
		}
		switch {
		case bedScale > duckTarget:
			bedScale -= float32(musicDuckRampPerChunk)
			if bedScale < duckTarget {
				bedScale = duckTarget
			}
		case bedScale < duckTarget:
			bedScale += float32(musicDuckRampPerChunk)
			if bedScale > duckTarget {
				bedScale = duckTarget
			}
		}
		oldGain *= bedScale
		newGain *= bedScale

		// Build the chunk: bed (with cross-fade if active, ducked when
		// overlays are present) attenuated by oldGain + newGain, plus
		// TTS at ttsVolume, plus each in-flight overlay at its own
		// volume.
		composeChunk(mixBuf, music, nextChunk, fade != nil, oldGain, newGain)
		ttsFrame := residual
		if len(ttsFrame) > len(mixBuf) {
			ttsFrame = ttsFrame[:len(mixBuf)]
		}
		if len(ttsFrame) > 0 {
			fadeIn := !ttsActive
			if fadeIn {
				// TTS resumed after a bed-only gap: pin this TTS position
				// to the current output position for the subtitle timeline.
				m.recordTTSBurstStart()
			}
			fadeOut := len(residual) <= len(mixBuf) && len(m.ttsCh) == 0
			applyTTSDeClick(ttsFrame, fadeIn, fadeOut)
		}
		mixInto(mixBuf, ttsFrame, 1.0, ttsVolume*m.currentTTSScale())
		m.advanceTimeline(len(ttsFrame), len(mixBuf))
		if len(overlays) > 0 {
			for _, ov := range overlays {
				ovChunk := drainOverlayChunk(ov, len(mixBuf))
				if len(ovChunk) > 0 {
					mixInto(mixBuf, ovChunk, 1.0, ov.volume)
				}
			}
			m.sweepClosedOverlays()
		}
		if len(residual) >= len(mixBuf) {
			residual = residual[len(mixBuf):]
		} else {
			residual = nil
		}
		// "Active" must mean "TTS went into THIS output chunk (or leftover
		// PCM will seamlessly continue into the next one)". Sampling ttsCh
		// occupancy here raced with the reader goroutine: PCM arriving
		// between the drain above and this line marked the stream active
		// without having been mixed, so the next iteration's real burst
		// start skipped recordTTSBurstStart — and every subtitle cue after
		// that bed-only gap exported shifted by the gap's length.
		ttsActive = len(ttsFrame) > 0 || len(residual) > 0

		if fade != nil {
			fade.remaining--
			if fade.remaining <= 0 {
				// Promote the new bed: SIGINT the old, swap pointers,
				// reap. mixLoop's next iteration reads from the new
				// musicOut at full musicVolume.
				if m.musicCmd != nil && m.musicCmd.Process != nil {
					_ = m.musicCmd.Process.Signal(syscall.SIGINT)
				}
				_ = m.musicOut.Close()
				if m.musicCmd != nil {
					_ = m.musicCmd.Wait()
				}
				m.musicCmd = fade.nextCmd
				m.musicOut = fade.nextOut
				fade = nil
			}
		}

		if _, err := m.encIn.Write(mixBuf); err != nil {
			m.mixErr = fmt.Errorf("write enc: %w", err)
			return
		}
	}
}

// composeChunk writes the bed contribution into dst. Without a fade,
// dst = music * oldGain. Mid-fade, dst = music * oldGain + next * newGain.
// All buffers are s16le mono PCM at chunkBytes — caller guarantees lengths
// match.
func composeChunk(dst, music, next []byte, fading bool, oldGain, newGain float32) {
	for i := 0; i+1 < len(dst); i += 2 {
		m := int16(binary.LittleEndian.Uint16(music[i:]))
		mixed := int32(float32(m) * oldGain)
		if fading && i+1 < len(next) {
			n := int16(binary.LittleEndian.Uint16(next[i:]))
			mixed += int32(float32(n) * newGain)
		}
		if mixed > 32767 {
			mixed = 32767
		} else if mixed < -32768 {
			mixed = -32768
		}
		binary.LittleEndian.PutUint16(dst[i:], uint16(mixed))
	}
}

// snapshotOverlays returns a stable view of the active overlay slice
// for one mix-loop iteration. Holding overlaysMu only for the copy
// keeps OverlapClip / sweepClosedOverlays from blocking on the per-
// chunk drain.
func (m *Mixer) snapshotOverlays() []*overlayStream {
	m.overlaysMu.Lock()
	defer m.overlaysMu.Unlock()
	if len(m.overlays) == 0 {
		return nil
	}
	out := make([]*overlayStream, len(m.overlays))
	copy(out, m.overlays)
	return out
}

// drainOverlayChunk pulls up to n bytes of PCM out of one overlay
// stream's pcmCh. Concatenates back-to-back chunks until n is met or
// the channel is empty / closed. Closing the channel is the clip's
// EOF signal — caller relies on sweepClosedOverlays to drop the entry
// from m.overlays once the channel is closed AND the residual buffer
// is empty.
func drainOverlayChunk(ov *overlayStream, n int) []byte {
	if ov == nil || ov.pcmCh == nil {
		return nil
	}
	var buf []byte
	for len(buf) < n {
		select {
		case chunk, ok := <-ov.pcmCh:
			if !ok {
				ov.pcmCh = nil
				return buf
			}
			buf = append(buf, chunk...)
		default:
			return buf
		}
	}
	return buf
}

// sweepClosedOverlays drops overlay entries whose pcmCh has been nil'd
// out by drainOverlayChunk after observing EOF. Reaps the decoder
// subprocess synchronously so a long-running session doesn't leak
// zombie ffmpegs.
func (m *Mixer) sweepClosedOverlays() {
	m.overlaysMu.Lock()
	defer m.overlaysMu.Unlock()
	out := m.overlays[:0]
	for _, ov := range m.overlays {
		if ov.pcmCh != nil {
			out = append(out, ov)
			continue
		}
		if ov.cmd != nil {
			_ = ov.cmd.Wait()
		}
		close(ov.done)
	}
	m.overlays = out
}

// mixInto attenuates `music` by `musicVol` in place and additively
// mixes the leading `len(music)` bytes of `tts` (scaled by `ttsVol`)
// on top, clipping to int16 range. Both buffers are s16le-encoded
// mono PCM at the pipeline sample rate. Independent volume scales let
// the operator dial in the speaker-vs-bed balance without re-encoding
// the source files.
func mixInto(music, tts []byte, musicVol, ttsVol float32) {
	for i := 0; i+1 < len(music); i += 2 {
		m := int16(binary.LittleEndian.Uint16(music[i:]))
		mixed := int32(float32(m) * musicVol)
		if i+1 < len(tts) {
			t := int16(binary.LittleEndian.Uint16(tts[i:]))
			mixed += int32(float32(t) * ttsVol)
		}
		if mixed > 32767 {
			mixed = 32767
		} else if mixed < -32768 {
			mixed = -32768
		}
		binary.LittleEndian.PutUint16(music[i:], uint16(mixed))
	}
}

func applyTTSDeClick(pcm []byte, fadeIn, fadeOut bool) {
	if len(pcm) < pcmSampleBytes || (!fadeIn && !fadeOut) {
		return
	}
	samples := len(pcm) / pcmSampleBytes
	if samples == 0 {
		return
	}
	fadeSamples := ttsDeClickSamples
	if fadeSamples > samples {
		fadeSamples = samples
	}
	if fadeSamples <= 0 {
		return
	}
	for i := 0; i < fadeSamples; i++ {
		scale := float32(i+1) / float32(fadeSamples)
		if fadeIn {
			scalePCMSample(pcm, i, scale)
		}
		if fadeOut {
			idx := samples - 1 - i
			scalePCMSample(pcm, idx, scale)
		}
	}
}

func scalePCMSample(pcm []byte, sampleIndex int, scale float32) {
	i := sampleIndex * pcmSampleBytes
	if i < 0 || i+1 >= len(pcm) {
		return
	}
	v := int16(binary.LittleEndian.Uint16(pcm[i:]))
	scaled := int32(float32(v) * scale)
	if scaled > 32767 {
		scaled = 32767
	} else if scaled < -32768 {
		scaled = -32768
	}
	binary.LittleEndian.PutUint16(pcm[i:], uint16(int16(scaled)))
}

func (m *Mixer) pump(sink io.Writer) {
	defer close(m.pumpDone)
	if _, err := io.Copy(sink, m.encOut); err != nil {
		m.pumpErr = err
	}
}
