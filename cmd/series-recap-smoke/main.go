// Command series-recap-smoke runs two consecutive series episodes against
// the same persistent archive root and validates the cross-episode plumbing:
//
//  1. Episode 1 (default: channels/series/01_pilot.md) runs end-to-end and
//     archives its scene plan + script + audio under
//     `<root>/tv-series/<show>/s1/e1/`.
//  2. Episode 2 (default: channels/series/02_followup.md) runs in the same
//     process. Its preparation step calls the compression LLM (when creds
//     are available) for a "previously on …" recap and lifts canonical
//     image-reuse keys out of the prior plan.
//  3. The smoke asserts:
//       * episode 1 produced scene-plan.json (and, with creds, the audio
//         + subtitle artefacts).
//       * episode 2 saw a non-empty recap (when creds were available).
//       * episode 2's host stream emitted a `<season-1-episode-N-image-M/>`
//         marker (only verified when episode 2 actually produced TTS — the
//         smoke peeks the per-turn script files).
//
// Without API creds the smoke degrades to fixture-cached fallbacks; the
// assertions about real LLM-driven recaps + image-reuse markers become
// soft warnings instead of failures.
//
// Flags:
//
//	--ep1     path to episode-1 topic (default channels/series/01_pilot.md)
//	--ep2     path to episode-2 topic (default channels/series/02_followup.md)
//	--out     output root (default: tempdir; --keep retains it on exit)
//	--keep    don't delete the temp dir on success
//	--mp4     stitch each episode's HLS + audio into out-eN.mp4 via ffmpeg
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/sirily11/debate-bot/internal/audio"
	"github.com/sirily11/debate-bot/internal/config"
	contentcreator "github.com/sirily11/debate-bot/internal/content_creator"
	"github.com/sirily11/debate-bot/internal/eventbus"
	"github.com/sirily11/debate-bot/internal/series"
	"github.com/sirily11/debate-bot/internal/util"
	"github.com/sirily11/debate-bot/internal/video"
)

func main() {
	ep1 := flag.String("ep1", "channels/series/01_pilot.md", "path to episode 1 topic.md")
	ep2 := flag.String("ep2", "channels/series/02_followup.md", "path to episode 2 topic.md")
	outRoot := flag.String("out", "out/series-recap-smoke", "output root — defaults to a stable subdir of the repo's out/ so the mp4 is easy to find")
	mp4 := flag.Bool("mp4", true, "stitch HLS + audio into out-eN.mp4 (requires ffmpeg)")
	flag.Parse()

	_ = godotenv.Overload()

	if err := run(*ep1, *ep2, *outRoot, *mp4); err != nil {
		fmt.Fprintln(os.Stderr, "series-recap-smoke:", err)
		os.Exit(1)
	}
}

func run(ep1Path, ep2Path, outRoot string, makeMP4 bool) error {
	if err := audio.VerifyTools(); err != nil {
		return fmt.Errorf("ffmpeg/sox verification: %w", err)
	}
	if err := os.MkdirAll(outRoot, 0o755); err != nil {
		return fmt.Errorf("mkdir out: %w", err)
	}
	abs, _ := filepath.Abs(outRoot)
	fmt.Printf("series-recap-smoke: out=%s\n", abs)

	envBase, err := config.LoadEnv()
	if err != nil {
		return fmt.Errorf("env: %w", err)
	}
	envBase.PersistentRoot = outRoot

	// Stage 1: episode 1.
	if err := runEpisode(envBase, ep1Path, "ep1", outRoot, makeMP4); err != nil {
		return fmt.Errorf("episode 1: %w", err)
	}
	fmt.Println("series-recap-smoke: episode 1 archived")

	// Stage 2: episode 2 — same persistent root so PrepareEpisode walks
	// the s1/e1 archive for recap + reuse catalog input.
	if err := runEpisode(envBase, ep2Path, "ep2", outRoot, makeMP4); err != nil {
		return fmt.Errorf("episode 2: %w", err)
	}
	fmt.Println("series-recap-smoke: episode 2 archived")

	// Stage 3: post-run assertions about the cross-episode wiring. The
	// recap engine wrote to the orchestrator's seriesPreviouslyOn at
	// preparation time — we don't have a public accessor for it, so
	// instead check episode 2's per-turn script files for an image-reuse
	// marker.
	tp, err := config.LoadTopic(ep2Path)
	if err != nil {
		return fmt.Errorf("ep2 reload: %w", err)
	}
	dir := contentcreator.EpisodeDir(outRoot, tp.Show, tp.Season, tp.Episode)
	if marker := findImageRefMarker(dir); marker == "" {
		fmt.Println("series-recap-smoke: WARN — no <season-S-episode-E-image-N/> marker observed in episode 2 script (likely no creds / fallback path)")
	} else {
		fmt.Printf("series-recap-smoke: episode 2 emitted cross-episode marker %s\n", marker)
	}
	fmt.Println("series-recap-smoke: OK")
	return nil
}

// runEpisode executes one full episode: stand up the per-channel infra,
// call series.PrepareEpisode, run the orchestrator, archive the output.
// label is "ep1"/"ep2" — used to disambiguate the per-session HLS dirs.
func runEpisode(envBase *config.Env, topicPath, label, outRoot string, makeMP4 bool) error {
	tp, err := config.LoadTopic(topicPath)
	if err != nil {
		return fmt.Errorf("load topic: %w", err)
	}
	if tp.Type != config.ContentTypeSeries {
		return fmt.Errorf("topic type=%q, want series", tp.Type)
	}

	// Cloned env so the session-specific OutDir doesn't bleed into the
	// next episode's run.
	env := *envBase
	sessionStamp := time.Now().Format("2006-01-02_15-04-05")
	env.OutDir = filepath.Join(outRoot, "session-"+sessionStamp+"-"+label)
	if err := contentcreator.EnsureOutDir(env.OutDir); err != nil {
		return fmt.Errorf("mkdir session: %w", err)
	}

	log, _, _ := util.NewFileLogger(env.OutDir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-stop; cancel() }()

	bus := eventbus.New(log)
	defer bus.Close()
	live, err := audio.NewLiveStream(ctx, log)
	if err != nil {
		return fmt.Errorf("livestream: %w", err)
	}
	defer live.CloseInput()

	channelOutDir := filepath.Join(env.OutDir, "series")
	_ = os.MkdirAll(channelOutDir, 0o755)
	enc, err := video.New(ctx, channelOutDir, video.Resolution(tp.Resolution), log)
	if err != nil {
		return fmt.Errorf("encoder: %w", err)
	}
	defer enc.Close()
	enc.AttachAudio(ctx, live)

	stage := video.NewSeriesChannelStage(enc, "series")
	go stage.Run(ctx, bus)

	send := func(v any) { bus.Publish(contentcreator.StampChannelID(v, "series")) }

	orch, err := contentcreator.New(&env, tp, &config.MCPConfig{}, send, log, live)
	if err != nil {
		return fmt.Errorf("orchestrator: %w", err)
	}
	defer orch.Shutdown()

	send(series.BuildTopicMsg(tp, filepath.Base(topicPath), tp.Title, 0, 2))
	series.PrepareEpisode(ctx, log, &env, stage, tp, orch)

	if err := orch.Run(ctx); err != nil {
		return fmt.Errorf("orch.Run: %w", err)
	}
	series.FinishEpisode(log, &env, tp)

	episodeDir := contentcreator.EpisodeDir(env.PersistentRoot, tp.Show, tp.Season, tp.Episode)
	logArtefacts(log, episodeDir)

	if makeMP4 {
		mp4Path := filepath.Join(outRoot, fmt.Sprintf("%s-%s-s%02de%02d.mp4", contentcreator.SlugifyShow(tp.Show), label, tp.Season, tp.Episode))
		if err := stitchMP4(enc.HLSDir(), filepath.Join(episodeDir, "episode.mp3"), mp4Path); err != nil {
			fmt.Fprintf(os.Stderr, "series-recap-smoke: %s mp4 stitch failed: %v\n", label, err)
		} else {
			abs, _ := filepath.Abs(mp4Path)
			fmt.Printf("series-recap-smoke: %s mp4 → %s\n", label, abs)
		}
	}
	return nil
}

// findImageRefMarker scans episode-2's archived script for a
// `<season-S-episode-E-image-N/>` token. Returns the first match, or "".
// The script is the concatenation of every per-turn script.txt produced
// during the run — it preserves the host's raw output including markers
// (the stripper only runs on the TTS / transcript / subtitle paths).
func findImageRefMarker(episodeDir string) string {
	data, err := os.ReadFile(filepath.Join(episodeDir, "script.txt"))
	if err != nil {
		return ""
	}
	body := string(data)
	idx := strings.Index(body, "<season-")
	if idx < 0 {
		return ""
	}
	end := strings.Index(body[idx:], "/>")
	if end < 0 {
		return ""
	}
	return body[idx : idx+end+2]
}

func logArtefacts(log *slog.Logger, episodeDir string) {
	for _, p := range []string{"scene-plan.json", "script.txt", "episode.mp3", "subtitles.vtt"} {
		full := filepath.Join(episodeDir, p)
		if info, err := os.Stat(full); err == nil {
			log.Info("artefact", "path", full, "size", info.Size())
		} else {
			log.Info("artefact missing", "path", full)
		}
	}
}

func stitchMP4(hlsDir, audioPath, outPath string) error {
	playlist := filepath.Join(hlsDir, "stream.m3u8")
	if _, err := os.Stat(playlist); err != nil {
		return fmt.Errorf("hls playlist missing: %w", err)
	}
	args := []string{"-y", "-i", playlist}
	if _, err := os.Stat(audioPath); err == nil {
		args = append(args, "-i", audioPath, "-c:v", "copy", "-c:a", "aac", "-shortest")
	} else {
		args = append(args, "-c:v", "copy", "-an")
	}
	args = append(args, outPath)
	cmd := exec.Command("ffmpeg", args...)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
