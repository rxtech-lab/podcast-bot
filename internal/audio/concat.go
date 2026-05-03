package audio

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ConcatToMP3 writes a concat-demuxer list file and runs
//
//	ffmpeg -f concat -safe 0 -i <list> -c copy <outPath>
//
// All input files must share the same codec/rate/channels — guaranteed here
// because every turn uses the same Azure output format.
func ConcatToMP3(outDir, outPath string, files []string) error {
	if len(files) == 0 {
		return fmt.Errorf("no input files to concat")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	list := filepath.Join(outDir, "concat_list.txt")
	var b bytes.Buffer
	for _, p := range files {
		// concat-demuxer requires posix-style escaped single quotes.
		escaped := strings.ReplaceAll(p, "'", `'\''`)
		fmt.Fprintf(&b, "file '%s'\n", escaped)
	}
	if err := os.WriteFile(list, b.Bytes(), 0o644); err != nil {
		return err
	}
	cmd := exec.Command("ffmpeg", "-y",
		"-f", "concat", "-safe", "0",
		"-i", list,
		"-c", "copy",
		outPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg concat failed: %w (%s)", err, string(out))
	}
	return nil
}
