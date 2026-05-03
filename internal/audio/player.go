package audio

import (
	"fmt"
	"os/exec"
)

// VerifyTools checks ffplay and ffmpeg are on PATH. Call once at startup.
// ffmpeg is required by both LiveStream (pacer) and end-of-run ConcatToMP3.
// ffplay is required by the CLI run cmd to play the live audio stream.
func VerifyTools() error {
	for _, t := range []string{"ffplay", "ffmpeg"} {
		if _, err := exec.LookPath(t); err != nil {
			return fmt.Errorf("required binary %q not found on PATH", t)
		}
	}
	return nil
}
