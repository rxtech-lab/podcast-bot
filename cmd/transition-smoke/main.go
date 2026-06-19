// Command transition-smoke reproduces the sequential-mode topic transition
// flow against a real Encoder + bus + DebateStage + PuzzleStage so we can
// eyeball whether two back-to-back topics hand off cleanly. No LLM / TTS /
// scene-gen network calls — speakers, transcripts, scenes, and timing are
// scripted procedurally so the smoke runs offline.
//
// Output: out/transition-smoke/<mode>/preview.mp4 (also leaves the HLS
// segments behind in the same folder).
//
// Modes:
//
//	puzzle-puzzle   two 海龜湯 puzzles back-to-back
//	puzzle-debate   puzzle followed by a debate
//	debate-puzzle   debate followed by a puzzle
//	debate-debate   two debates back-to-back
//	series-series   two narrated TV-series episodes back-to-back (mirrors
//	                runChannel's Preactivate → TopicMsg → PostEpisodeIdle →
//	                inter-episode gap → next-episode handoff)
//	series-debate   series followed by a debate
//	debate-series   debate followed by a series
//	series-puzzle   series followed by a puzzle
//	puzzle-series   puzzle followed by a series
package main

import (
	"context"
	"flag"
	"fmt"
	"image"
	"image/color"
	"log/slog"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
	contentcreator "github.com/sirily11/debate-bot/internal/content_creator"
	"github.com/sirily11/debate-bot/internal/eventbus"
	"github.com/sirily11/debate-bot/internal/video"
	"github.com/sirily11/debate-bot/internal/video/scenes"
)

func main() {
	mode := flag.String("mode", "all", "transition mode: puzzle-puzzle | puzzle-debate | debate-puzzle | debate-debate | series-series | series-debate | debate-series | series-puzzle | puzzle-series | all")
	out := flag.String("out", "out/transition-smoke", "output directory root")
	flag.Parse()

	modes := []string{
		"puzzle-puzzle", "puzzle-debate", "debate-puzzle", "debate-debate",
		"series-series", "series-debate", "debate-series",
		"series-puzzle", "puzzle-series",
	}
	if *mode != "all" {
		modes = []string{*mode}
	}

	for _, m := range modes {
		fmt.Println("=== mode:", m, "===")
		dir := filepath.Join(*out, m)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := runScenario(dir, m); err != nil {
			fmt.Fprintf(os.Stderr, "%s failed: %v\n", m, err)
			os.Exit(1)
		}
	}
}

// scenario describes one of the two topics in a transition. kind is "debate",
// "puzzle", or "series". show / season / episode are only meaningful for
// series scenarios — the other kinds leave them zero.
type scenario struct {
	kind    string
	id      string
	title   string
	show    string
	season  int
	episode int
}

func runScenario(out, mode string) error {
	first, second := parseModes(mode)
	if first.kind == "" {
		return fmt.Errorf("unknown mode %q", mode)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	enc, err := video.New(ctx, out, video.Resolution720p, log)
	if err != nil {
		return fmt.Errorf("encoder: %w", err)
	}

	bus := eventbus.New(log)

	// All three stages running concurrently mirrors production (cmd/debate-bot:
	// each channel runs every stage and they self-gate on TopicMsg.Type).
	// SeriesStage with empty channelID accepts every event (no per-channel
	// gating), the same behaviour we want for a single-channel smoke run.
	debateStage := video.NewDebateStage(enc)
	puzzleStage := video.NewPuzzleStage(enc)
	seriesStage := video.NewSeriesChannelStage(enc, "")
	go debateStage.Run(ctx, bus)
	go puzzleStage.Run(ctx, bus)
	go seriesStage.Run(ctx, bus)

	// Procedural scene plates so the puzzle stage has something to paint
	// without a network round trip. Each scenario gets its own palette so
	// the transition between two puzzles is visually obvious.
	scA := proceduralScenes(0)
	scB := proceduralScenes(1)
	// Procedural series narration frames (different palette per scenario
	// so the series→series swap is visually obvious).
	seriesFramesA := proceduralSeriesFrames(0)
	seriesFramesB := proceduralSeriesFrames(1)

	// --- Topic 1 ----------------------------------------------------------
	playTopic(bus, puzzleStage, seriesStage, first, scA, seriesFramesA, 0)
	// Hold the topic on screen long enough that the encoder records a
	// stretch of "first topic" before we cut to the second one. 10 s
	// captures the surface narration beat plus a couple of transcript
	// changes.
	time.Sleep(10 * time.Second)

	// Series episodes route through the runChannel handoff: stage parks
	// on PostEpisodeIdle, then we hold for the inter-episode gap before
	// Preactivating the next episode. This is the exact sequence the bug
	// report flagged ("never moves to next ep") so the smoke must exercise
	// it end-to-end.
	if first.kind == "series" {
		seriesStage.PostEpisodeIdle()
		time.Sleep(3 * time.Second) // shortened gap so the smoke stays under a minute
	}
	if second.kind == "series" {
		seriesStage.Preactivate()
	}

	// --- Topic 2 — the transition under test -----------------------------
	playTopic(bus, puzzleStage, seriesStage, second, scB, seriesFramesB, 1)
	time.Sleep(8 * time.Second)

	if err := enc.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "encoder close:", err)
	}
	bus.Close()

	// Transmux HLS → MP4 (stream-copy, no re-encode).
	mp4Path := filepath.Join(out, "preview.mp4")
	manifest := filepath.Join(enc.HLSDir(), "stream.m3u8")
	cmd := exec.Command("ffmpeg",
		"-y",
		"-loglevel", "warning",
		"-i", manifest,
		"-c", "copy",
		"-movflags", "+faststart",
		mp4Path,
	)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg transmux: %w", err)
	}
	abs, _ := filepath.Abs(mp4Path)
	fmt.Println("preview mp4:", abs)
	return nil
}

func parseModes(mode string) (a, b scenario) {
	puzzle1 := scenario{kind: "puzzle", id: "p1", title: "燈塔"}
	puzzle2 := scenario{kind: "puzzle", id: "p2", title: "归途"}
	debate1 := scenario{kind: "debate", id: "d1", title: "AI 是否會取代程序員"}
	debate2 := scenario{kind: "debate", id: "d2", title: "遠端工作是不是未來的常態"}
	series1 := scenario{kind: "series", id: "s1", title: "夜行記 — 第一夜",
		show: "夜行記", season: 1, episode: 1}
	series2 := scenario{kind: "series", id: "s2", title: "夜行記 — 第二夜",
		show: "夜行記", season: 1, episode: 2}
	switch mode {
	case "puzzle-puzzle":
		return puzzle1, puzzle2
	case "puzzle-debate":
		return puzzle1, debate1
	case "debate-puzzle":
		return debate1, puzzle1
	case "debate-debate":
		return debate1, debate2
	case "series-series":
		return series1, series2
	case "series-debate":
		return series1, debate1
	case "debate-series":
		return debate1, series1
	case "series-puzzle":
		return series1, puzzle1
	case "puzzle-series":
		return puzzle1, series1
	}
	return scenario{}, scenario{}
}

// playTopic publishes the events one topic generates over its first ~10 s
// of life: TopicMsg, PhaseMsg, a short transcript, and (for puzzles/series) a
// scene-attach + initial scene advance. Mirrors what cmd/debate-bot does
// when it admits a topic.
func playTopic(bus *eventbus.Bus, ps *video.PuzzleStage, ss *video.SeriesStage,
	s scenario, sc *scenes.PuzzleScenes, sf []*image.RGBA, idx int,
) {
	switch s.kind {
	case "series":
		// Attach the narration frames BEFORE TopicMsg so the stage's
		// handleTopic doesn't blank the bg between topic-arrival and the
		// first scene-attach. This mirrors prepareSeriesAssets's streaming
		// path, where AttachNarrationFrame fires as each PNG finishes
		// before the planner starts the host turn.
		for variant, img := range sf {
			ss.AttachNarrationFrame(variant, img)
		}
		bus.Publish(contentcreator.TopicMsg{
			ID:          s.id,
			Title:       s.title,
			Type:        config.ContentTypeSeries,
			Index:       idx,
			Total:       2,
			AffNames:    []string{"Linda"},
			NegNames:    nil,
			AffPosition: "在那座永遠半濕半冷的小鎮，林夜每晚走完整座小鎮，像在送信，也像在等回音。",
			NegPosition: "",
			Show:        s.show,
			Season:      s.season,
			Episode:     s.episode,
		})
		bus.Publish(contentcreator.PhaseMsg{
			Phase: agent.PhaseFreeSpeech,
			Type:  config.ContentTypeSeries,
			Label: "本集",
		})
		time.Sleep(500 * time.Millisecond)
		bus.Publish(contentcreator.TranscriptMsg{
			Speaker:       "Linda",
			Role:          agent.RoleSeriesHost,
			Text:          firstSentence(s.title),
			AudioDuration: 4 * time.Second,
		})
		// Advance to a second narration beat partway through so the
		// renderer crossfades scenes during the smoke — confirms the
		// per-frame bg path stays smooth (and validates the perf fix
		// for series_bg.png).
		time.Sleep(2 * time.Second)
		bus.Publish(contentcreator.SceneAdvanceMsg{Index: 1})
		time.Sleep(1500 * time.Millisecond)
		bus.Publish(contentcreator.TranscriptMsg{
			Speaker:       "Linda",
			Role:          agent.RoleSeriesHost,
			Text:          secondSentence(s.title),
			AudioDuration: 5 * time.Second,
		})
	case "puzzle":
		bus.Publish(contentcreator.TopicMsg{
			ID:          s.id,
			Title:       s.title,
			Type:        config.ContentTypeSituationPuzzle,
			Index:       idx,
			Total:       2,
			AffNames:    []string{"出題者"},
			NegNames:    []string{"Alice", "Bob", "Carol"},
			AffPosition: "一名男子走進一家海邊的高級餐廳，點了一碗海龜湯……",
			NegPosition: "",
		})
		ps.AttachScenes(sc)
		bus.Publish(contentcreator.PhaseMsg{
			Phase: agent.PhaseOpening,
			Type:  config.ContentTypeSituationPuzzle,
			Label: "出題",
		})
		// Two transcript chunks ~3s apart so the subtitle visibly changes
		// during the topic's airtime.
		time.Sleep(500 * time.Millisecond)
		bus.Publish(contentcreator.TranscriptMsg{
			Speaker:       "出題者",
			Role:          agent.RolePuzzleHost,
			Text:          firstSentence(s.title),
			AudioDuration: 4 * time.Second,
		})
		time.Sleep(3 * time.Second)
		bus.Publish(contentcreator.TranscriptMsg{
			Speaker:       "出題者",
			Role:          agent.RolePuzzleHost,
			Text:          secondSentence(s.title),
			AudioDuration: 5 * time.Second,
		})
	case "debate":
		bus.Publish(contentcreator.TopicMsg{
			ID:          s.id,
			Title:       s.title,
			Type:        config.ContentTypeDebate,
			Index:       idx,
			Total:       2,
			AffNames:    []string{"Linda", "Bob"},
			NegNames:    []string{"Alice", "Carol"},
			AffPosition: "正方主張：AI 將在十年內取代多數初階程序員。",
			NegPosition: "反方主張：AI 不會取代程序員，反而會放大他們的能力。",
		})
		bus.Publish(contentcreator.PhaseMsg{
			Phase: agent.PhaseOpening,
			Type:  config.ContentTypeDebate,
			Label: "立論",
		})
		time.Sleep(500 * time.Millisecond)
		bus.Publish(contentcreator.TranscriptMsg{
			Speaker:       "Linda",
			Role:          agent.RoleAffirmative,
			Side:          "affirmative",
			Text:          firstSentence(s.title),
			AudioDuration: 4 * time.Second,
		})
		time.Sleep(3 * time.Second)
		bus.Publish(contentcreator.TranscriptMsg{
			Speaker:       "Alice",
			Role:          agent.RoleNegative,
			Side:          "negative",
			Text:          secondSentence(s.title),
			AudioDuration: 5 * time.Second,
		})
	}
}

// proceduralSeriesFrames returns four canvas-sized procedural narration
// PNGs the smoke can hand to SeriesStage.AttachNarrationFrame. Each set
// (seed) gets a different palette so a series→series swap is visually
// obvious. Four frames is enough that one SceneAdvance during playback
// produces a visible crossfade.
func proceduralSeriesFrames(set int) []*image.RGBA {
	tilt := uint8(set * 0x40)
	frames := make([]*image.RGBA, 4)
	for i := range frames {
		frames[i] = proceduralBg(
			color.RGBA{0x1a + tilt + uint8(i*0x06), 0x12 + uint8(i*0x05), 0x28 - uint8(i*0x04), 0xff},
			color.RGBA{0x04, 0x05, 0x0a, 0xff},
			int64(500+set*100+i),
		)
	}
	return frames
}

// firstSentence / secondSentence are scripted lines so the smoke video has
// some readable subtitle text without pulling in an LLM.
func firstSentence(title string) string {
	switch title {
	case "燈塔":
		return "三十五年來，那座除役的燈塔，每晚八點仍會準時亮起。"
	case "归途":
		return "願你在每一段黑路上，都能遇見一盞沒有遲到的燈。"
	case "AI 是否會取代程序員":
		return "AI 將在未來十年內取代大多數初級和中級程序員的工作。"
	case "遠端工作是不是未來的常態":
		return "遠端工作把雇主的監管成本外包給了員工自己。"
	case "夜行記 — 第一夜":
		return "霧川的霧，從不從天上來……是從海裡，一寸一寸爬上來的。"
	case "夜行記 — 第二夜":
		return "那一晚的信箱裡，多了一封沒有寄件人的信——只寫著「給夜，從你母親」。"
	}
	return title
}

func secondSentence(title string) string {
	switch title {
	case "燈塔":
		return "守燈人從不下山，村民也從不過問。"
	case "归途":
		return "回家的人不需要證明自己回得去。"
	case "AI 是否會取代程序員":
		return "反方認為，工程責任仍須由人類承擔。"
	case "遠端工作是不是未來的常態":
		return "正方主張：辦公室的時代已經結束。"
	case "夜行記 — 第一夜":
		return "他總是在夜裡上路……在燈火最少的時候，沿著熟悉的街道，一站一站走下去。"
	case "夜行記 — 第二夜":
		return "鎮上的人都說她跳了海，可是林夜不相信。"
	}
	return ""
}

// proceduralScenes builds a full PuzzleScenes off a deterministic seed so
// the same scenario always produces visually-consistent placeholder bgs.
// Each "set" (seed) gets a different palette so a puzzle→puzzle smoke
// can verify the renderer actually swaps to the new background.
func proceduralScenes(set int) *scenes.PuzzleScenes {
	out := &scenes.PuzzleScenes{}
	// Tilt the palette per-set so set 0 / set 1 are obviously different.
	tilt := uint8(set * 0x40)
	for i := 0; i < scenes.SurfaceVariantCount; i++ {
		out.Surface = append(out.Surface, proceduralBg(
			color.RGBA{0x12 + tilt + uint8(i*0x05), 0x1c + uint8(i*0x04), 0x36 - uint8(i*0x04), 0xff},
			color.RGBA{0x05, 0x08, 0x14, 0xff},
			int64(100+set*100+i),
		))
	}
	out.QA = proceduralBg(
		color.RGBA{0x1a + tilt, 0x22, 0x2c, 0xff},
		color.RGBA{0x06, 0x0a, 0x10, 0xff},
		2+int64(set*10))
	out.Reveal = proceduralBg(
		color.RGBA{0x3a + tilt, 0x10, 0x12, 0xff},
		color.RGBA{0x08, 0x02, 0x05, 0xff},
		3+int64(set*10))
	for i := 0; i < scenes.ConclusionVariantCount; i++ {
		out.Conclusion = append(out.Conclusion, proceduralBg(
			color.RGBA{0x2c + tilt + uint8(i*0x06), 0x24 - uint8(i*0x03), 0x18 + uint8(i*0x05), 0xff},
			color.RGBA{0x0c, 0x09, 0x05, 0xff},
			int64(200+set*100+i),
		))
	}
	return out
}

func proceduralBg(top, bot color.RGBA, seed int64) *image.RGBA {
	const w, h = 1280, 720
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewSource(seed))
	for y := 0; y < h; y++ {
		ty := float64(y) / float64(h-1)
		base := color.RGBA{
			uint8(float64(top.R)*(1-ty) + float64(bot.R)*ty),
			uint8(float64(top.G)*(1-ty) + float64(bot.G)*ty),
			uint8(float64(top.B)*(1-ty) + float64(bot.B)*ty),
			0xff,
		}
		for x := 0; x < w; x++ {
			n := int(r.Int31n(24)) - 12
			i := img.PixOffset(x, y)
			img.Pix[i] = clip(int(base.R) + n)
			img.Pix[i+1] = clip(int(base.G) + n)
			img.Pix[i+2] = clip(int(base.B) + n)
			img.Pix[i+3] = 0xff
		}
	}
	return img
}

func clip(v int) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}
