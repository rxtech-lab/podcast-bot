// Package video runs a long-lived ffmpeg encoder that bakes the live debate
// transcript onto a video stream. Output is HLS (m3u8 + .ts segments) served
// by the HTTP server.
//
// HLS here is video-only on purpose: muxing TTS audio into the same stream
// requires bridging multi-second gaps between turns, and ffmpeg's HLS muxer
// will not emit any segment until both inputs have a packet, so an idle audio
// FD blocks the entire stream. Audio is served separately at /api/audio/stream
// and the frontend renders an <audio> element alongside <video>; the ±2s drift
// between the two playback elements is acceptable since the transcript overlay
// is the only synced visual.
//
// Frames are rendered in Go using golang.org/x/image so we don't depend on
// ffmpeg's drawtext filter (which requires --enable-libfreetype, missing from
// many distro/brew default builds). ffmpeg only encodes + muxes.
package video

import (
	"context"
	"fmt"
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
	videoWidth    = 1280
	videoHeight   = 720
	videoFPS      = 15
	hlsSegmentSec = 2
	hlsListSize   = 6
)

// Encoder owns the ffmpeg process, the in-process frame compositor, and the
// HLS output directory.
type Encoder struct {
	cmd     *exec.Cmd
	videoIn io.WriteCloser // raw rgba pipe:0 → ffmpeg
	hlsDir  string

	rend *Renderer

	writeMu sync.Mutex
	closed  bool

	log  *slog.Logger
	done chan struct{}

	stateMu     sync.Mutex
	curSpeaker  string
	curRole     string
	curSide     string
	curBodyText string
}

// New starts the encoder. sessionDir is where HLS segments + the ffmpeg
// stderr log are written.
func New(ctx context.Context, sessionDir string, log *slog.Logger) (*Encoder, error) {
	hlsDir := filepath.Join(sessionDir, "hls")
	if err := os.MkdirAll(hlsDir, 0o755); err != nil {
		return nil, fmt.Errorf("hls dir: %w", err)
	}

	rend, err := newRenderer(videoWidth, videoHeight)
	if err != nil {
		return nil, fmt.Errorf("renderer: %w", err)
	}

	codecArgs, codecName := videoCodecArgs()

	args := []string{
		"-loglevel", "warning",
		"-f", "rawvideo",
		"-pix_fmt", "rgba",
		"-s", fmt.Sprintf("%dx%d", videoWidth, videoHeight),
		"-r", fmt.Sprintf("%d", videoFPS),
		"-i", "pipe:0",
		"-an",
	}
	args = append(args, codecArgs...)
	args = append(args,
		"-pix_fmt", "yuv420p",
		"-g", fmt.Sprintf("%d", videoFPS*hlsSegmentSec),
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
		return nil, fmt.Errorf("ffmpeg stdin: %w", err)
	}
	cmd.Stdout = io.Discard

	stderrPath := filepath.Join(sessionDir, "ffmpeg-encoder.log")
	stderrFile, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open ffmpeg log: %w", err)
	}
	tail := newTailWriter(64 * 1024)
	cmd.Stderr = io.MultiWriter(stderrFile, tail)

	if err := cmd.Start(); err != nil {
		stderrFile.Close()
		return nil, fmt.Errorf("start ffmpeg encoder: %w", err)
	}

	if log != nil {
		log.Info("video encoder started",
			"pid", cmd.Process.Pid,
			"codec", codecName,
			"hls_dir", hlsDir,
			"stderr_log", stderrPath,
		)
	}

	e := &Encoder{
		cmd:     cmd,
		videoIn: videoIn,
		hlsDir:  hlsDir,
		rend:    rend,
		log:     log,
		done:    make(chan struct{}),
	}

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

// AttachAudio is a no-op kept for callers that previously muxed TTS audio
// into the HLS stream. The encoder now produces video-only HLS; TTS audio is
// served separately at /api/audio/stream and the frontend plays both.
func (e *Encoder) AttachAudio(_ context.Context, _ *audio.LiveStream) {}

// SetTopic shows the topic title at the top of the video.
func (e *Encoder) SetTopic(s string) { e.rend.SetTopic(s) }

// SetPhase updates the phase status line under the topic title.
func (e *Encoder) SetPhase(s string) { e.rend.SetPhase(s) }

// SetSpeaker activates the centered subtitle box for the given speaker. role
// values match agent.Role string values ("host", "affirmative", etc).
// Calling with empty speaker hides the subtitle (idle state).
func (e *Encoder) SetSpeaker(speaker, role, side string) {
	e.stateMu.Lock()
	e.curSpeaker = speaker
	e.curRole = role
	e.curSide = side
	body := e.curBodyText
	e.stateMu.Unlock()
	e.rend.SetState(speaker, role, side, body)
}

// SetBody updates the spoken text shown inside the subtitle box.
func (e *Encoder) SetBody(s string) {
	e.stateMu.Lock()
	e.curBodyText = s
	speaker, role, side := e.curSpeaker, e.curRole, e.curSide
	e.stateMu.Unlock()
	e.rend.SetState(speaker, role, side, s)
}

// userMsgTTL is how long a chat overlay stays on screen before vanishing.
const userMsgTTL = 5 * time.Second

// ShowUserMessage flashes a chat/viewer message on the video for a few
// seconds without disturbing the active speaker subtitle.
func (e *Encoder) ShowUserMessage(text string) {
	e.rend.ShowUserMessage(text, userMsgTTL)
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
