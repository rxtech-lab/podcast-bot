// Package video runs a long-lived ffmpeg encoder that bakes the live debate
// transcript onto a video stream and muxes it with the TTS audio. Output is
// HLS (m3u8 + .ts segments) served by the HTTP server.
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
	cmd       *exec.Cmd
	videoIn   io.WriteCloser // raw rgba pipe:0 → ffmpeg
	audioIn   *os.File       // mp3 pipe:3 → ffmpeg (write side)
	audioPair *os.File       // read side; closed in parent after Start
	hlsDir    string

	rend *Renderer

	writeMu sync.Mutex
	closed  bool

	log  *slog.Logger
	done chan struct{}

	stateMu     sync.Mutex
	curSpeaker  string
	curRole     string
	curSide     string
	curTagText  string
	curBodyText string
}

// New starts the encoder. sessionDir is where HLS segments + the ffmpeg
// stderr log are written. Audio is attached separately via AttachAudio.
func New(ctx context.Context, sessionDir string, log *slog.Logger) (*Encoder, error) {
	hlsDir := filepath.Join(sessionDir, "hls")
	if err := os.MkdirAll(hlsDir, 0o755); err != nil {
		return nil, fmt.Errorf("hls dir: %w", err)
	}

	rend, err := newRenderer(videoWidth, videoHeight)
	if err != nil {
		return nil, fmt.Errorf("renderer: %w", err)
	}

	// Audio rides on an extra file descriptor (pipe:3 inside ffmpeg) so we can
	// keep stdin (pipe:0) reserved for raw video frames.
	audioR, audioW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("audio pipe: %w", err)
	}

	args := []string{
		"-loglevel", "warning",
		"-f", "rawvideo",
		"-pix_fmt", "rgba",
		"-s", fmt.Sprintf("%dx%d", videoWidth, videoHeight),
		"-r", fmt.Sprintf("%d", videoFPS),
		"-i", "pipe:0",
		"-f", "mp3",
		"-i", "pipe:3",
		"-c:v", "libx264",
		"-preset", "veryfast",
		"-tune", "zerolatency",
		"-pix_fmt", "yuv420p",
		"-g", fmt.Sprintf("%d", videoFPS*hlsSegmentSec),
		"-c:a", "aac",
		"-b:a", "96k",
		"-ar", "44100",
		"-f", "hls",
		"-hls_time", fmt.Sprintf("%d", hlsSegmentSec),
		"-hls_list_size", fmt.Sprintf("%d", hlsListSize),
		"-hls_flags", "delete_segments+append_list+independent_segments",
		"-hls_segment_type", "mpegts",
		"-hls_segment_filename", filepath.Join(hlsDir, "seg_%03d.ts"),
		filepath.Join(hlsDir, "stream.m3u8"),
	}

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.ExtraFiles = []*os.File{audioR}
	videoIn, err := cmd.StdinPipe()
	if err != nil {
		audioR.Close()
		audioW.Close()
		return nil, fmt.Errorf("ffmpeg stdin: %w", err)
	}
	cmd.Stdout = io.Discard

	stderrPath := filepath.Join(sessionDir, "ffmpeg-encoder.log")
	stderrFile, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		audioR.Close()
		audioW.Close()
		return nil, fmt.Errorf("open ffmpeg log: %w", err)
	}
	tail := newTailWriter(64 * 1024)
	cmd.Stderr = io.MultiWriter(stderrFile, tail)

	if err := cmd.Start(); err != nil {
		stderrFile.Close()
		audioR.Close()
		audioW.Close()
		return nil, fmt.Errorf("start ffmpeg encoder: %w", err)
	}
	// ffmpeg has dup'd the read side; parent doesn't need it open anymore.
	// Closing here means EOF will propagate when audioW is closed during shutdown.
	_ = audioR.Close()

	if log != nil {
		log.Info("video encoder started",
			"pid", cmd.Process.Pid,
			"hls_dir", hlsDir,
			"stderr_log", stderrPath,
		)
	}

	e := &Encoder{
		cmd:       cmd,
		videoIn:   videoIn,
		audioIn:   audioW,
		audioPair: audioR, // already closed; kept only for documentation
		hlsDir:    hlsDir,
		rend:      rend,
		log:       log,
		done:      make(chan struct{}),
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

// AttachAudio subscribes to live and pipes mp3 chunks into ffmpeg's audio fd.
// Runs until ctx is cancelled or the LiveStream / encoder closes.
func (e *Encoder) AttachAudio(ctx context.Context, live *audio.LiveStream) {
	ch, cancel := live.Subscribe(256)
	go func() {
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case <-e.done:
				return
			case chunk, ok := <-ch:
				if !ok {
					return
				}
				e.writeMu.Lock()
				if e.closed {
					e.writeMu.Unlock()
					return
				}
				_, err := e.audioIn.Write(chunk)
				e.writeMu.Unlock()
				if err != nil {
					if e.log != nil {
						e.log.Warn("encoder audio write", "err", err)
					}
					return
				}
			}
		}
	}()
}

// UpdateTag updates the speaker label rendered in the top pill. Kept for API
// compatibility with the previous drawtext-based encoder.
func (e *Encoder) UpdateTag(s string) error {
	e.stateMu.Lock()
	e.curTagText = s
	e.stateMu.Unlock()
	e.syncRenderer()
	return nil
}

// UpdateBody updates the body text rendered in the main panel.
func (e *Encoder) UpdateBody(s string) error {
	e.stateMu.Lock()
	e.curBodyText = s
	e.stateMu.Unlock()
	e.syncRenderer()
	return nil
}

// SetSpeaker is the richer state update used by Stage. role values match
// agent.Role string values ("host", "affirmative", etc).
func (e *Encoder) SetSpeaker(speaker, role, side string) {
	e.stateMu.Lock()
	e.curSpeaker = speaker
	e.curRole = role
	e.curSide = side
	e.stateMu.Unlock()
	e.syncRenderer()
}

func (e *Encoder) syncRenderer() {
	e.stateMu.Lock()
	speaker, role, side, body := e.curSpeaker, e.curRole, e.curSide, e.curBodyText
	e.stateMu.Unlock()
	e.rend.SetState(speaker, role, side, body)
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

// Close flushes audio + video, waits up to 2s for ffmpeg to exit, then SIGKILLs.
func (e *Encoder) Close() error {
	e.writeMu.Lock()
	if e.closed {
		e.writeMu.Unlock()
		return nil
	}
	e.closed = true
	_ = e.videoIn.Close()
	_ = e.audioIn.Close()
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
