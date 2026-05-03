package audio

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// PlayStream tees streaming MP3 bytes from src into ffplay's stdin AND a file
// at mp3Path. Blocks until ffplay exits or ctx is cancelled (then the process
// is killed and the partial file remains).
//
// ffplay must be on PATH. Verify with VerifyTools at startup.
func PlayStream(ctx context.Context, mp3Path string, src io.Reader) error {
	f, err := os.Create(mp3Path)
	if err != nil {
		return fmt.Errorf("create audio file: %w", err)
	}
	defer f.Close()

	cmd := exec.CommandContext(ctx, "ffplay",
		"-nodisp", "-autoexit", "-loglevel", "quiet", "-i", "-")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("ffplay stdin: %w", err)
	}
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffplay: %w", err)
	}

	tee := io.MultiWriter(f, stdin)
	_, copyErr := io.Copy(tee, src)
	stdin.Close()
	waitErr := cmd.Wait()
	if copyErr != nil && copyErr != io.EOF {
		return fmt.Errorf("stream tee: %w", copyErr)
	}
	if waitErr != nil {
		// ffplay exit with a non-zero status when killed mid-play; treat as non-fatal
		// when the context is cancelled.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("ffplay: %w", waitErr)
	}
	return nil
}

// VerifyTools checks ffplay and ffmpeg are on PATH. Call once at startup.
func VerifyTools() error {
	for _, t := range []string{"ffplay", "ffmpeg"} {
		if _, err := exec.LookPath(t); err != nil {
			return fmt.Errorf("required binary %q not found on PATH", t)
		}
	}
	return nil
}
