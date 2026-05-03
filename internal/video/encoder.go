// Package video runs a long-lived ffmpeg encoder that bakes the live debate
// transcript onto a video stream and muxes it with the TTS audio. Output is
// HLS (m3u8 + .ts segments) served by the HTTP server.
package video

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sirily11/debate-bot/internal/audio"
)

const (
	videoWidth    = 1280
	videoHeight   = 720
	videoFPS      = 15 // text-only frames; low rate keeps CPU light
	hlsSegmentSec = 2
	hlsListSize   = 6
)

// Encoder owns the ffmpeg process, the live drawtext source files, and the
// HLS output directory.
type Encoder struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	hlsDir string

	tagPath  string
	bodyPath string

	writeMu sync.Mutex
	closed  bool

	log  *slog.Logger
	done chan struct{}
}

// New starts the encoder. sessionDir is where text source files and HLS
// segments are written. Audio is attached via AttachAudio.
func New(ctx context.Context, sessionDir string, log *slog.Logger) (*Encoder, error) {
	hlsDir := filepath.Join(sessionDir, "hls")
	if err := os.MkdirAll(hlsDir, 0o755); err != nil {
		return nil, fmt.Errorf("hls dir: %w", err)
	}

	tagPath := filepath.Join(sessionDir, "stage_tag.txt")
	bodyPath := filepath.Join(sessionDir, "stage_body.txt")
	// drawtext init fails on empty/missing textfile, so seed with a single
	// non-empty character.
	if err := os.WriteFile(tagPath, []byte(" "), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(bodyPath, []byte(" "), 0o644); err != nil {
		return nil, err
	}

	font, err := findFont()
	if err != nil {
		return nil, err
	}

	filter := buildFilter(font, tagPath, bodyPath)
	args := []string{
		"-loglevel", "warning",
		"-f", "lavfi", "-i", fmt.Sprintf("color=c=0x0e0e10:s=%dx%d:r=%d", videoWidth, videoHeight, videoFPS),
		"-f", "mp3", "-i", "pipe:0",
		"-filter_complex", filter,
		"-map", "[v]",
		"-map", "1:a",
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
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg stdin: %w", err)
	}
	// Inherit stderr only at warning level; ffmpeg logs go to the user's
	// terminal which is noisy. Discard for now; pipeline logs cover progress.
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start ffmpeg encoder: %w", err)
	}

	e := &Encoder{
		cmd:      cmd,
		stdin:    stdin,
		hlsDir:   hlsDir,
		tagPath:  tagPath,
		bodyPath: bodyPath,
		log:      log,
		done:     make(chan struct{}),
	}
	go func() {
		_ = cmd.Wait()
		close(e.done)
	}()
	return e, nil
}

// HLSDir returns the directory holding stream.m3u8 and segment files.
func (e *Encoder) HLSDir() string { return e.hlsDir }

// AttachAudio subscribes to live and pipes mp3 chunks into ffmpeg stdin.
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
				_, err := e.stdin.Write(chunk)
				e.writeMu.Unlock()
				if err != nil {
					if e.log != nil {
						e.log.Warn("encoder stdin write", "err", err)
					}
					return
				}
			}
		}
	}()
}

// UpdateTag writes the speaker label that appears in the top badge.
func (e *Encoder) UpdateTag(s string) error {
	if strings.TrimSpace(s) == "" {
		s = " "
	}
	return atomicWrite(e.tagPath, s)
}

// UpdateBody writes the live transcript text rendered in the main panel.
// Newlines are honoured; callers should pre-wrap to width.
func (e *Encoder) UpdateBody(s string) error {
	if s == "" {
		s = " "
	}
	return atomicWrite(e.bodyPath, s)
}

// Close flushes audio, waits up to 2s for ffmpeg to exit, then SIGKILLs.
func (e *Encoder) Close() error {
	e.writeMu.Lock()
	if e.closed {
		e.writeMu.Unlock()
		return nil
	}
	e.closed = true
	_ = e.stdin.Close()
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

// buildFilter assembles the drawtext filter chain. Two overlays:
//   - speaker tag in a colored box near the top
//   - body text below
//
// reload=1 makes drawtext re-read the textfile on every frame.
func buildFilter(font, tagPath, bodyPath string) string {
	esc := func(s string) string {
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `:`, `\:`)
		s = strings.ReplaceAll(s, `'`, `\'`)
		return s
	}
	font = esc(font)
	tag := esc(tagPath)
	body := esc(bodyPath)
	tagFilter := fmt.Sprintf(
		"drawtext=fontfile=%s:textfile=%s:reload=1:fontcolor=white:fontsize=40:x=80:y=70:box=1:boxcolor=0x9147ff@0.85:boxborderw=20",
		font, tag,
	)
	bodyFilter := fmt.Sprintf(
		"drawtext=fontfile=%s:textfile=%s:reload=1:fontcolor=white:fontsize=34:x=80:y=200:line_spacing=14",
		font, body,
	)
	return fmt.Sprintf("[0:v]%s,%s[v]", tagFilter, bodyFilter)
}

func atomicWrite(path, content string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
