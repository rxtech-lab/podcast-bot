package audio

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// PlayStream tees streaming MP3 bytes from src into ffplay's stdin AND a file
// at mp3Path. Blocks until ffplay exits or ctx is cancelled (then the process
// is killed and the partial file remains).
//
// Returns the number of bytes copied. When src yields zero bytes (e.g. the
// upstream LLM call failed before any audio could be produced), no file is
// created and ffplay is not spawned — the caller can detect this via the
// returned count and skip the turn.
//
// ffplay must be on PATH. Verify with VerifyTools at startup.
func PlayStream(ctx context.Context, mp3Path string, src io.Reader) (int64, error) {
	// Peek to detect an empty upstream stream BEFORE we create a file or spawn
	// ffplay. Avoids littering the out dir with 0-byte mp3s and avoids spawning
	// ffplay for nothing (which on macOS sometimes pops a transient OS sound).
	br := bufio.NewReader(src)
	if _, err := br.Peek(1); err != nil {
		if errors.Is(err, io.EOF) {
			return 0, nil
		}
		return 0, fmt.Errorf("peek audio: %w", err)
	}

	f, err := os.Create(mp3Path)
	if err != nil {
		return 0, fmt.Errorf("create audio file: %w", err)
	}
	defer f.Close()

	cmd := exec.CommandContext(ctx, "ffplay",
		"-nodisp", "-autoexit", "-loglevel", "quiet", "-i", "-")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return 0, fmt.Errorf("ffplay stdin: %w", err)
	}
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start ffplay: %w", err)
	}

	tee := io.MultiWriter(f, stdin)
	n, copyErr := io.Copy(tee, br)
	stdin.Close()
	waitErr := cmd.Wait()
	if copyErr != nil && !errors.Is(copyErr, io.EOF) {
		return n, fmt.Errorf("stream tee: %w", copyErr)
	}
	if waitErr != nil {
		// ffplay exit with a non-zero status when killed mid-play; treat as non-fatal
		// when the context is cancelled.
		if ctx.Err() != nil {
			return n, ctx.Err()
		}
		return n, fmt.Errorf("ffplay: %w", waitErr)
	}
	return n, nil
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
