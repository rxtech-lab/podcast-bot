// Package video runs a long-lived ffmpeg encoder that bakes the live debate
// transcript onto a video stream. Output is HLS (m3u8 + .ts segments) served
// by the HTTP server.
//
// The HLS stream carries video AND audio in one mux. The audio side is fed by
// an in-Go pump (encoderAudioPump) that consumes the realtime-paced LiveStream
// and pads with pre-generated silent MP3 frames whenever no TTS bytes are
// flowing — without continuous audio packets the HLS muxer would stall at the
// segment boundary. /api/audio/stream is kept around for fallback clients.
//
// Frames are rendered in Go using golang.org/x/image so we don't depend on
// ffmpeg's drawtext filter (which requires --enable-libfreetype, missing from
// many distro/brew default builds). ffmpeg only encodes + muxes.
package video

import (
	"context"
	"fmt"
	"image"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/sirily11/debate-bot/internal/audio"
)

const (
	// Internal compositing resolution. The Go renderer always paints into a
	// 1280×720 RGBA buffer; ffmpeg's scale filter resamples to the encoder's
	// chosen output size (see Resolution / outputDims).
	videoWidth    = 1280
	videoHeight   = 720
	videoFPS      = 30
	hlsSegmentSec = 2
	hlsListSize   = 6
)

// Resolution is the encoder's output resolution. The renderer composites at
// 1280×720 regardless; ffmpeg upscales to outputDims().
type Resolution string

const (
	Resolution720p  Resolution = "720p"
	Resolution1080p Resolution = "1080p"
	Resolution4K    Resolution = "4k"
)

// outputDims returns the (width, height) the encoder should emit for the
// requested resolution. Unknown / empty values fall back to 720p.
func outputDims(r Resolution) (int, int) {
	switch r {
	case Resolution1080p:
		return 1920, 1080
	case Resolution4K:
		return 3840, 2160
	default:
		return videoWidth, videoHeight
	}
}

// Encoder owns the ffmpeg process, the in-process frame compositor, and the
// HLS output directory.
type Encoder struct {
	cmd     *exec.Cmd
	videoIn io.WriteCloser // raw rgba pipe:0 → ffmpeg
	audioIn io.WriteCloser // mp3 pipe:3 → ffmpeg (fed by encoderAudioPump)
	hlsDir  string

	rend     *Renderer
	audioBuf []byte // pre-generated silent MP3 used as filler between TTS bursts
	pump     *encoderAudioPump

	writeMu sync.Mutex
	closed  bool

	log  *slog.Logger
	done chan struct{}

	stateMu         sync.Mutex
	curSpeaker      string
	curRole         string
	curSide         string
	curBodyText     string
	curBodyDuration time.Duration
}

// New starts the encoder. sessionDir is where HLS segments + the ffmpeg
// stderr log are written. res selects the output resolution; the renderer
// always composites at 1280×720 and ffmpeg's scale filter upsamples.
func New(ctx context.Context, sessionDir string, res Resolution, log *slog.Logger) (*Encoder, error) {
	outW, outH := outputDims(res)
	hlsDir := filepath.Join(sessionDir, "hls")
	if err := os.MkdirAll(hlsDir, 0o755); err != nil {
		return nil, fmt.Errorf("hls dir: %w", err)
	}

	rend, err := newRenderer(videoWidth, videoHeight)
	if err != nil {
		return nil, fmt.Errorf("renderer: %w", err)
	}

	silent, err := generateSilentMP3(silentBufSeconds)
	if err != nil {
		return nil, fmt.Errorf("silent mp3 buffer: %w", err)
	}

	codecArgs, codecName := videoCodecArgs()

	// Audio side-channel: ffmpeg reads MP3 from fd 3 (`pipe:3`) which the parent
	// writes to via `audioInW`. Using ExtraFiles lets us keep stdin reserved for
	// raw video while still feeding audio in-band to a single ffmpeg process.
	audioInR, audioInW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("audio pipe: %w", err)
	}

	args := []string{
		"-loglevel", "warning",
		"-thread_queue_size", "1024",
		"-f", "rawvideo",
		"-pix_fmt", "rgba",
		"-s", fmt.Sprintf("%dx%d", videoWidth, videoHeight),
		"-r", fmt.Sprintf("%d", videoFPS),
		"-i", "pipe:0",
		"-thread_queue_size", "1024",
		"-f", "mp3",
		"-i", "pipe:3",
	}
	args = append(args, codecArgs...)
	if outW != videoWidth || outH != videoHeight {
		// Upscale the 1280×720 composite to the requested output resolution.
		// Lanczos preserves text edges noticeably better than the default
		// bicubic at 1.5×/3× upscales.
		args = append(args, "-vf", fmt.Sprintf("scale=%d:%d:flags=lanczos", outW, outH))
	}
	args = append(args,
		"-pix_fmt", "yuv420p",
		"-g", fmt.Sprintf("%d", videoFPS*hlsSegmentSec),
		"-c:a", "aac",
		"-b:a", "64k",
		"-ar", "24000",
		"-ac", "1",
		"-map", "0:v:0",
		"-map", "1:a:0",
		"-f", "hls",
		"-hls_time", fmt.Sprintf("%d", hlsSegmentSec),
		"-hls_list_size", fmt.Sprintf("%d", hlsListSize),
		"-hls_flags", "delete_segments+append_list+independent_segments",
		"-hls_segment_type", "mpegts",
		"-hls_segment_filename", filepath.Join(hlsDir, "seg_%03d.ts"),
		filepath.Join(hlsDir, "stream.m3u8"),
	)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	videoIn, err := cmd.StdinPipe()
	if err != nil {
		_ = audioInR.Close()
		_ = audioInW.Close()
		return nil, fmt.Errorf("ffmpeg stdin: %w", err)
	}
	cmd.Stdout = io.Discard
	cmd.ExtraFiles = []*os.File{audioInR}

	stderrPath := filepath.Join(sessionDir, "ffmpeg-encoder.log")
	stderrFile, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		_ = audioInR.Close()
		_ = audioInW.Close()
		return nil, fmt.Errorf("open ffmpeg log: %w", err)
	}
	tail := newTailWriter(64 * 1024)
	cmd.Stderr = io.MultiWriter(stderrFile, tail)

	if err := cmd.Start(); err != nil {
		stderrFile.Close()
		_ = audioInR.Close()
		_ = audioInW.Close()
		return nil, fmt.Errorf("start ffmpeg encoder: %w", err)
	}
	// ffmpeg now owns the read end; close our copy so EOF propagates correctly
	// when we close `audioInW` on shutdown.
	_ = audioInR.Close()

	if log != nil {
		log.Info("video encoder started",
			"pid", cmd.Process.Pid,
			"codec", codecName,
			"hls_dir", hlsDir,
			"stderr_log", stderrPath,
			"silent_mp3_bytes", len(silent),
		)
	}

	e := &Encoder{
		cmd:      cmd,
		videoIn:  videoIn,
		audioIn:  audioInW,
		hlsDir:   hlsDir,
		rend:     rend,
		audioBuf: silent,
		log:      log,
		done:     make(chan struct{}),
	}
	e.pump = newEncoderAudioPump(audioInW, silent, log)
	go e.pump.run(ctx)

	go e.frameLoop(ctx)
	go func() {
		err := cmd.Wait()
		stderrFile.Close()
		tailStr := tail.String()
		if log != nil {
			log.Warn("video encoder exited",
				"err", err,
				"stderr_tail", tailStr,
				"stderr_log", stderrPath,
			)
		}
		if !e.closingByUs() {
			fmt.Fprintf(os.Stderr,
				"\n[video encoder exited unexpectedly] %v\nffmpeg stderr (last %d bytes from %s):\n%s\n",
				err, len(tailStr), stderrPath, tailStr)
		}
		close(e.done)
	}()

	return e, nil
}

// HLSDir returns the directory holding stream.m3u8 + segments.
func (e *Encoder) HLSDir() string { return e.hlsDir }

// AttachAudio subscribes the encoder's audio pump to the shared LiveStream so
// real TTS bytes get muxed into the HLS output. The pump pads with silent MP3
// frames whenever the LiveStream is idle, so ffmpeg's HLS muxer always has
// audio packets to interleave with video.
func (e *Encoder) AttachAudio(ctx context.Context, ls *audio.LiveStream) {
	if e.pump == nil || ls == nil {
		return
	}
	go e.pump.feedFrom(ctx, ls)
}

// SetTopic shows the topic title at the top of the video.
func (e *Encoder) SetTopic(s string) { e.rend.SetTopic(s) }

// SetPhase updates the phase status line under the topic title.
func (e *Encoder) SetPhase(s string) { e.rend.SetPhase(s) }

// SetClock updates the elapsed/total clock display at the bottom of the frame.
func (e *Encoder) SetClock(elapsed, total time.Duration) { e.rend.SetClock(elapsed, total) }

// SetSides loads the affirmative / negative speaker rosters into the side
// panels rendered on the left and right of the stage.
func (e *Encoder) SetSides(aff, neg []string) { e.rend.SetSides(aff, neg) }

// SetPositions sets each side's position statement (the stance they argue
// for), drawn as small footer text inside the side panels.
func (e *Encoder) SetPositions(aff, neg string) { e.rend.SetPositions(aff, neg) }

// SetPuzzleMode toggles the cinematic puzzle layout — minimal chrome over
// AI-generated scene backgrounds. PuzzleStage flips this on when a puzzle
// topic activates and off when it idles.
func (e *Encoder) SetPuzzleMode(b bool) { e.rend.SetPuzzleMode(b) }

// SetSceneBackground swaps the active scene image, crossfading from the
// previous one. Pass nil to clear (renderer falls back to its default bg
// plate). Used by PuzzleStage on TopicMsg + PhaseMsg as the puzzle moves
// surface → Q&A → reveal → conclusion.
func (e *Encoder) SetSceneBackground(img *image.RGBA) { e.rend.SetSceneBackground(img) }

// SetPuzzleSceneName records the active puzzle scene name (one of
// scenes.Scene*) so the renderer can apply scene-specific subtitle
// styling — today, the surface phase paints the caption directly on the
// scene with a black outline (no quote-card chrome), while QA/reveal/
// conclusion keep the slab-and-rule look. PuzzleStage calls this in
// lockstep with SetSceneBackground.
func (e *Encoder) SetPuzzleSceneName(name string) { e.rend.SetPuzzleSceneName(name) }

// SetSpeaker activates the centered subtitle box for the given speaker. role
// values match agent.Role string values ("host", "affirmative", etc).
// Calling with empty speaker hides the subtitle (idle state).
func (e *Encoder) SetSpeaker(speaker, role, side string) {
	e.stateMu.Lock()
	e.curSpeaker = speaker
	e.curRole = role
	e.curSide = side
	body := e.curBodyText
	dur := e.curBodyDuration
	e.stateMu.Unlock()
	e.rend.SetState(speaker, role, side, body, dur)
}

// SetBody updates the spoken text shown inside the subtitle box.
// audioDuration is the wall-clock length of the synthesized audio for s and
// drives time-based subtitle motion (scroll start). Pass 0 when unknown.
func (e *Encoder) SetBody(s string, audioDuration time.Duration) {
	e.stateMu.Lock()
	e.curBodyText = s
	e.curBodyDuration = audioDuration
	speaker, role, side := e.curSpeaker, e.curRole, e.curSide
	e.stateMu.Unlock()
	e.rend.SetState(speaker, role, side, s, audioDuration)
}

// userMsgTTL is how long a chat overlay stays on screen before vanishing.
const userMsgTTL = 5 * time.Second

// ShowUserMessage flashes a chat/viewer message on the video for a few
// seconds without disturbing the active speaker subtitle. username is the
// viewer's handle and is rendered ahead of the message in the ticker's
// accent colour; pass "" to suppress the prefix.
func (e *Encoder) ShowUserMessage(text, username string) {
	e.rend.ShowUserMessage(text, username, userMsgTTL)
}

// frameLoop pushes one rendered frame per video tick into ffmpeg's stdin.
// Any write error (typically caused by ffmpeg exiting) terminates the loop.
func (e *Encoder) frameLoop(ctx context.Context) {
	interval := time.Second / time.Duration(videoFPS)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.done:
			return
		case <-t.C:
			frame := e.rend.Frame()
			e.writeMu.Lock()
			if e.closed {
				e.writeMu.Unlock()
				return
			}
			_, err := e.videoIn.Write(frame)
			e.writeMu.Unlock()
			if err != nil {
				if e.log != nil && !e.closingByUs() {
					e.log.Warn("encoder video write", "err", err)
				}
				return
			}
		}
	}
}

// Close flushes video, waits up to 2s for ffmpeg to exit, then SIGKILLs.
func (e *Encoder) Close() error {
	e.writeMu.Lock()
	if e.closed {
		e.writeMu.Unlock()
		return nil
	}
	e.closed = true
	_ = e.videoIn.Close()
	e.writeMu.Unlock()

	if e.pump != nil {
		e.pump.close()
	}

	select {
	case <-e.done:
	case <-time.After(2 * time.Second):
		if e.cmd.Process != nil {
			_ = e.cmd.Process.Kill()
		}
	}
	return nil
}

func (e *Encoder) closingByUs() bool {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	return e.closed
}

// videoCodecArgs picks the H.264 encoder ffmpeg should use. Default on macOS
// is h264_videotoolbox (Apple's hardware encoder — runs on the media block,
// near-zero CPU); everywhere else, libx264. Override with
// DEBATE_BOT_VIDEO_CODEC=libx264|h264_videotoolbox if you want to force one.
//
// VideoToolbox doesn't accept libx264's -preset / -tune — we use -realtime 1
// for low latency and a fixed bitrate target instead. -allow_sw 1 lets ffmpeg
// fall back to software encoding inside videotoolbox if the GPU path fails
// (e.g. running under Rosetta or in a constrained sandbox).
func videoCodecArgs() (args []string, name string) {
	choice := os.Getenv("DEBATE_BOT_VIDEO_CODEC")
	if choice == "" {
		if runtime.GOOS == "darwin" {
			choice = "h264_videotoolbox"
		} else {
			choice = "libx264"
		}
	}
	switch choice {
	case "h264_videotoolbox":
		return []string{
			"-c:v", "h264_videotoolbox",
			"-realtime", "1",
			"-allow_sw", "1",
			"-b:v", "2M",
			"-profile:v", "high",
		}, "h264_videotoolbox"
	default:
		return []string{
			"-c:v", "libx264",
			"-preset", "veryfast",
			"-tune", "zerolatency",
		}, "libx264"
	}
}
