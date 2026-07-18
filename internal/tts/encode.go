package tts

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
)

// encodePCMToStereoMP3 wraps a mono s16le PCM reader (at inputRate Hz) with an
// ffmpeg subprocess emitting the pipeline's uniform output format: 48 kHz /
// 192 kbps STEREO CBR MP3. `-ac 2` duplicates the mono voice into identical
// L/R channels (dual-mono), which players render as a centered voice; the
// music mixer then adds true-stereo beds around it. `-write_xing 0` keeps the
// stream header-free so the byte→time contract (AudioBytesPerSec) stays exact.
//
// The transcode is streaming: the HTTP body is pumped into ffmpeg's stdin in
// the background while the caller reads MP3 bytes off stdout as they are
// produced, so live-stream latency is one subprocess spawn, not a full-clip
// buffer. The returned ReadCloser closes both pipes and waits for ffmpeg.
func encodePCMToStereoMP3(ctx context.Context, src io.ReadCloser, inputRate int) (io.ReadCloser, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-loglevel", "quiet",
		"-f", "s16le",
		"-ar", strconv.Itoa(inputRate),
		"-ac", "1",
		"-i", "pipe:0",
		"-c:a", "libmp3lame",
		"-b:a", "192k",
		"-ar", "48000",
		"-ac", "2",
		"-write_xing", "0",
		"-id3v2_version", "0",
		"-f", "mp3",
		"pipe:1",
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = src.Close()
		return nil, fmt.Errorf("tts ffmpeg stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = src.Close()
		_ = stdin.Close()
		return nil, fmt.Errorf("tts ffmpeg stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = src.Close()
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("tts ffmpeg start: %w", err)
	}
	// Pump HTTP body → ffmpeg stdin in the background so the caller can
	// stream bytes off stdout as they're produced.
	go func() {
		defer src.Close()
		defer stdin.Close()
		_, _ = io.Copy(stdin, src)
	}()
	return &transcodeReader{stdout: stdout, cmd: cmd}, nil
}

type transcodeReader struct {
	stdout io.ReadCloser
	cmd    *exec.Cmd
}

func (r *transcodeReader) Read(p []byte) (int, error) { return r.stdout.Read(p) }

func (r *transcodeReader) Close() error {
	err := r.stdout.Close()
	_ = r.cmd.Wait()
	return err
}
