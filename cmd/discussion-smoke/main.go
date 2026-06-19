// Command discussion-smoke renders a short discussion-type video against a
// real Encoder + bus + the full production stage set (debate + puzzle + series
// + discussion, all self-gating on TopicMsg.Type). It exists to visually
// confirm that "discussion" content renders with the DISCUSSION template
// (cinematic AI-style background + caption card) rather than falling through
// to the DEBATE two-panel layout.
//
// No LLM / TTS / image-gen network calls: the panel roster, transcript, and
// background palette are scripted procedurally so the smoke runs offline.
//
// Output: out/discussion-smoke/preview.mp4 (HLS segments left alongside).
//
// Usage:
//
//	go run ./cmd/discussion-smoke
//	go run ./cmd/discussion-smoke -out /tmp/disc -secs 12
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
)

func main() {
	out := flag.String("out", "out/discussion-smoke", "output directory")
	secs := flag.Int("secs", 12, "seconds of discussion to render")
	flag.Parse()

	if err := run(*out, *secs); err != nil {
		fmt.Fprintln(os.Stderr, "discussion-smoke failed:", err)
		os.Exit(1)
	}
}

func run(out string, secs int) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	enc, err := video.New(ctx, out, video.Resolution720p, log)
	if err != nil {
		return fmt.Errorf("encoder: %w", err)
	}

	bus := eventbus.New(log)

	// All four content-type stages run concurrently, exactly like
	// cmd/debate-bot: each self-gates on TopicMsg.Type, so only the
	// DiscussionStage should end up driving the encoder here. The empty
	// channel id makes every stage accept all events (single-channel smoke).
	const channelID = ""
	debateStage := video.NewDebateChannelStage(enc, channelID)
	puzzleStage := video.NewPuzzleChannelStage(enc, channelID)
	seriesStage := video.NewSeriesChannelStage(enc, channelID)
	discussionStage := video.NewDiscussionChannelStage(enc, channelID)
	go debateStage.Run(ctx, bus)
	go puzzleStage.Run(ctx, bus)
	go seriesStage.Run(ctx, bus)
	go discussionStage.Run(ctx, bus)

	// Give the Run goroutines a beat to subscribe before the first Publish —
	// the bus drops events for not-yet-registered subscribers.
	time.Sleep(50 * time.Millisecond)

	// Procedural background palette stands in for the AI-generated plates the
	// commander would normally produce. Attaching before the topic means the
	// first background paints the moment the stage activates.
	discussionStage.AttachPalette(proceduralPalette())

	// --- Discussion topic ------------------------------------------------
	bus.Publish(contentcreator.TopicMsg{
		ID:          "disc-smoke",
		Title:       "Vibe Coding 是炒作還是新常態？",
		Type:        config.ContentTypeDiscussion,
		Index:       0,
		Total:       1,
		AffNames:    []string{"Diego", "Priya", "Sam"},
		NegNames:    []string{"Mira"},
		AffPosition: "一場關於 AI 輔助開發前景的圓桌討論。",
	})
	bus.Publish(contentcreator.PhaseMsg{
		Phase: agent.PhaseFreeSpeech,
		Type:  config.ContentTypeDiscussion,
		Label: "討論",
	})

	lines := []struct {
		speaker string
		role    agent.Role
		text    string
		dur     time.Duration
	}{
		{"Mira", agent.RoleHost, "歡迎來到今天的圓桌——我們聊聊 vibe coding 到底是不是一門生意。", 4 * time.Second},
		{"Diego", agent.RoleDiscussant, "從商業模式看，真正能收費的是雲端執行與協作，而不是補全本身。", 5 * time.Second},
		{"Priya", agent.RoleDiscussant, "我擔心的是護城河：編排層很容易被大廠一個版本就吃掉。", 5 * time.Second},
		{"Sam", agent.RoleDiscussant, "但開發者的習慣一旦養成，遷移成本就成了最現實的壁壘。", 5 * time.Second},
	}

	perLine := time.Duration(secs) * time.Second / time.Duration(len(lines))
	if perLine < 2*time.Second {
		perLine = 2 * time.Second
	}
	for i, ln := range lines {
		bus.Publish(contentcreator.TranscriptMsg{
			Speaker:       ln.speaker,
			Role:          ln.role,
			Text:          ln.text,
			AudioDuration: ln.dur,
		})
		// Rotate to a different background partway so the smoke shows the
		// scene-swap path the commander drives in production.
		if i == len(lines)/2 {
			bus.Publish(contentcreator.SceneAdvanceMsg{Index: 1})
		}
		time.Sleep(perLine)
	}

	if err := enc.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "encoder close:", err)
	}
	bus.Close()

	// Transmux HLS → MP4 (stream-copy, no re-encode).
	mp4Path := filepath.Join(out, "preview.mp4")
	cmd := exec.Command("ffmpeg",
		"-y", "-loglevel", "warning",
		"-i", filepath.Join(enc.HLSDir(), "stream.m3u8"),
		"-c", "copy", "-movflags", "+faststart",
		mp4Path,
	)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg transmux: %w", err)
	}
	abs, _ := filepath.Abs(mp4Path)
	fmt.Println("preview mp4:", abs)
	fmt.Println("Expected: cinematic background + single caption card (discussion),")
	fmt.Println("NOT the two-panel affirmative/negative debate layout.")
	return nil
}

// proceduralPalette returns a few canvas-sized gradient plates so the
// discussion stage has backgrounds to paint without an image-gen round trip.
func proceduralPalette() []*image.RGBA {
	return []*image.RGBA{
		proceduralBg(color.RGBA{0x1a, 0x22, 0x3a, 0xff}, color.RGBA{0x05, 0x07, 0x10, 0xff}, 11),
		proceduralBg(color.RGBA{0x33, 0x18, 0x2c, 0xff}, color.RGBA{0x0a, 0x04, 0x08, 0xff}, 22),
		proceduralBg(color.RGBA{0x14, 0x2c, 0x24, 0xff}, color.RGBA{0x04, 0x0a, 0x08, 0xff}, 33),
	}
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
