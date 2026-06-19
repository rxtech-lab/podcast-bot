// Command music-smoke verifies the music subsystem end-to-end before
// plumbing it into a full puzzle run. Two modes:
//
//   - default: hits Lyria 3 Pro and writes the resulting mp3 to disk.
//     Confirms the API request shape works.
//
//   - --session: exercises musicmixer.NewSession by piping a short
//     pre-generated TTS-style mp3 through it on top of the supplied
//     music bed, then writes the mixed output for offline listening.
//     Confirms the silence-filler keeps amix flowing between TTS
//     bursts and that volume balance is sane. Pass --music to point at
//     an existing bed (e.g. a previously cached Lyria clip).
//
// Reads GEMINI_API_KEY from the process env (or .env via godotenv) for
// the default mode; --session mode needs no API key.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/joho/godotenv"
	"github.com/sirily11/debate-bot/internal/audio/musicgen"
	"github.com/sirily11/debate-bot/internal/audio/musicmixer"
)

const defaultPrompt = `A quiet 30-second instrumental ambient bed for a
cinematic mystery scene. Soft sustained piano, low warm strings, occasional
distant bell. No vocals, no lyrics. Slow tempo, mastered quietly so it can
sit under a narrator's voice.`

func main() {
	out := flag.String("out", "out/music-smoke", "output directory")
	prompt := flag.String("prompt", defaultPrompt, "Lyria prompt (default mode)")
	timeout := flag.Duration("timeout", 3*time.Minute, "Lyria request timeout")
	session := flag.Bool("session", false, "test musicmixer.NewSession instead of generating")
	music := flag.String("music", "", "music bed path for --session mode")
	turns := flag.String("turns", "", "turn mp3 file or directory of turn_*.mp3 files (--session)")
	gap := flag.Duration("gap", 4*time.Second, "inter-turn pause used to verify continuous music (--session)")
	flag.Parse()

	_ = godotenv.Load()

	if err := os.MkdirAll(*out, 0o755); err != nil {
		die("mkdir: %v", err)
	}

	if *session {
		runSessionTest(*out, *music, *turns, *gap)
		return
	}
	runGenerateTest(*out, *prompt, *timeout)
}

func runGenerateTest(outDir, prompt string, timeout time.Duration) {
	client, err := musicgen.New("")
	if err != nil {
		die("musicgen client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	fmt.Println("requesting Lyria clip…")
	t0 := time.Now()
	mp3, err := client.Generate(ctx, musicgen.Request{Prompt: prompt})
	if err != nil {
		die("generate: %v", err)
	}
	elapsed := time.Since(t0).Round(time.Millisecond)

	stamp := time.Now().Format("20060102-150405")
	path := filepath.Join(outDir, "clip-"+stamp+".mp3")
	if err := os.WriteFile(path, mp3, 0o644); err != nil {
		die("write %s: %v", path, err)
	}
	abs, _ := filepath.Abs(path)
	fmt.Printf("ok — %d bytes in %s\nfile: %s\n", len(mp3), elapsed, abs)
}

// runSessionTest writes mixed output by replaying real TTS turn mp3
// files from a previous session through musicmixer.NewSession on top
// of the supplied music bed. Between turns it pauses the configured
// gap so the mixer's lavfi-silence input has to keep amix flowing —
// the resulting file should sound like continuous music with each
// turn of dialogue mixed on top.
//
// --turns can be a single mp3 (replayed N times) or a directory of
// turn_NNN.mp3 files; --gap is the inter-turn pause.
func runSessionTest(outDir, music, turnsArg string, gap time.Duration) {
	if music == "" {
		die("--session needs --music <path-to-bed.mp3>")
	}
	if _, err := os.Stat(music); err != nil {
		die("music: %v", err)
	}
	if turnsArg == "" {
		die("--session needs --turns <file-or-dir>")
	}

	turns, err := collectTurns(turnsArg)
	if err != nil {
		die("collect turns: %v", err)
	}
	if len(turns) == 0 {
		die("no turn mp3 files under %s", turnsArg)
	}
	fmt.Printf("turns: %d files from %s\n", len(turns), turnsArg)

	// Sink: write mixed output to a file for offline listening.
	stamp := time.Now().Format("20060102-150405")
	outPath := filepath.Join(outDir, "session-"+stamp+".mp3")
	outFile, err := os.Create(outPath)
	if err != nil {
		die("create out: %v", err)
	}
	defer outFile.Close()

	fmt.Println("starting session mixer…")
	mixer, err := musicmixer.NewSession(music, outFile)
	if err != nil {
		die("session mixer: %v", err)
	}

	for i, path := range turns {
		body, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read %s: %v\n", path, err)
			continue
		}
		if _, werr := mixer.Write(body); werr != nil {
			fmt.Fprintf(os.Stderr, "turn %d write: %v\n", i+1, werr)
			break
		}
		fmt.Printf("turn %d (%s, %d bytes) written\n",
			i+1, filepath.Base(path), len(body))
		// Inter-turn gap — the mixer's lavfi silence input should keep
		// amix producing music underneath this pause.
		if i < len(turns)-1 && gap > 0 {
			time.Sleep(gap)
		}
	}

	// One final gap so the closing trailer of the output captures a
	// stretch of music-only audio for listening.
	if gap > 0 {
		time.Sleep(gap)
	}

	fmt.Println("closing mixer…")
	if cerr := mixer.Close(); cerr != nil {
		die("mixer close: %v", cerr)
	}

	abs, _ := filepath.Abs(outPath)
	info, _ := os.Stat(outPath)
	size := int64(0)
	if info != nil {
		size = info.Size()
	}
	fmt.Printf("ok — %d bytes (%d turns, gap=%s)\nfile: %s\n",
		size, len(turns), gap, abs)
}

// collectTurns expands a --turns argument into an ordered list of
// mp3 paths. A directory is scanned for turn_*.mp3 files; a single
// file is returned as-is.
func collectTurns(arg string) ([]string, error) {
	info, err := os.Stat(arg)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{arg}, nil
	}
	matches, err := filepath.Glob(filepath.Join(arg, "turn_*.mp3"))
	if err != nil {
		return nil, err
	}
	return matches, nil
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "music-smoke: "+format+"\n", args...)
	os.Exit(1)
}
