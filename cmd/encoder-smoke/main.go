// Command encoder-smoke spins up the video.Encoder against a temp output dir,
// lets the audio pump emit silence for a few seconds (no LiveStream attached),
// then exits. Used to manually verify that the HLS muxer produces a manifest
// with both video and audio tracks before wiring the encoder into a full run.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/sirily11/debate-bot/internal/video"
)

func main() {
	out := flag.String("out", "out/encoder-smoke", "output directory")
	dur := flag.Duration("dur", 8*time.Second, "how long to run")
	flag.Parse()

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	abs, _ := filepath.Abs(*out)
	fmt.Println("session dir:", abs)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	enc, err := video.New(ctx, *out, video.Resolution720p, log)
	if err != nil {
		fmt.Fprintln(os.Stderr, "encoder:", err)
		os.Exit(1)
	}
	enc.SetTopic("encoder smoke test — silent audio + idle video")
	enc.SetPhase("idle")

	time.Sleep(*dur)
	if err := enc.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "close:", err)
	}
	fmt.Println("hls dir:", enc.HLSDir())
}
