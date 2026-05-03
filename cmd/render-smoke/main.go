// Command render-smoke produces sample frames from the video renderer and
// writes them as PNGs so we can eyeball that the layout (topic title, phase,
// subtitle box) and CJK glyphs render correctly.
package main

import (
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"
	"time"

	"github.com/sirily11/debate-bot/internal/video"
)

func main() {
	out := flag.String("out", "out/render-smoke", "output directory")
	flag.Parse()

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	cases := []struct {
		name     string
		topic    string
		phase    string
		speaker  string
		role     string
		side     string
		body     string
		userMsg  string
	}{
		{
			name:  "01-idle",
			topic: "AI 是否會取代程序員",
			phase: "opening",
		},
		{
			name:    "02-affirmative-zh",
			topic:   "AI 是否會取代程序員",
			phase:   "opening",
			speaker: "Linda",
			role:    "affirmative",
			side:    "affirmative",
			body:    "AI 將在未來十年內取代大多數初級和中級程序員的工作。其能力提升曲線陡峭、邊際成本低、工具鏈正在快速成熟。",
		},
		{
			name:    "03-negative-zh-long",
			topic:   "AI 是否會取代程序員",
			phase:   "free-speech",
			speaker: "Alice",
			role:    "negative",
			side:    "negative",
			body:    "反方主張:AI 不會取代程序員,反而會放大他們的能力。寫代碼只是程序員工作的一小部分;設計、溝通、判斷、責任承擔仍需人類。即使工具更強大,責任邊界仍由人來界定。",
		},
		{
			name:    "04-host-mixed",
			topic:   "AI 是否會取代程序員",
			phase:   "opening",
			speaker: "Host",
			role:    "host",
			body:    "歡迎來到今天的辯論,正方 Linda 請開始。Welcome everyone.",
		},
		{
			name:    "05-english-only",
			topic:   "Will AI replace programmers within ten years?",
			phase:   "closing",
			speaker: "Bob",
			role:    "affirmative",
			side:    "affirmative",
			body:    "The marginal cost of generated code is collapsing. Every productivity benchmark we've measured this year shows the gap closing.",
		},
		{
			name:    "06-user-overlay-zh",
			topic:   "AI 是否會取代程序員",
			phase:   "free-speech",
			speaker: "Linda",
			role:    "affirmative",
			side:    "affirmative",
			body:    "AI 將在未來十年內取代大多數初級和中級程序員的工作。",
			userMsg: "請問正方,如果 AI 替代了所有初級程序員,新人從哪裡入行?",
		},
		{
			name:    "07-user-overlay-idle",
			topic:   "AI 是否會取代程序員",
			phase:   "opening",
			userMsg: "Hello panel — can you address the impact on entry-level hiring?",
		},
	}

	for _, c := range cases {
		path := fmt.Sprintf("%s/%s.png", *out, c.name)
		if err := writeFrame(path, c.topic, c.phase, c.speaker, c.role, c.side, c.body, c.userMsg); err != nil {
			fmt.Fprintln(os.Stderr, c.name, err)
			os.Exit(1)
		}
		fmt.Println("wrote", path)
	}
}

func writeFrame(path, topic, phase, speaker, role, side, body, userMsg string) error {
	rend, err := video.NewRendererForTest(1280, 720)
	if err != nil {
		return err
	}
	rend.SetTopic(topic)
	rend.SetPhase(phase)
	rend.SetState(speaker, role, side, body)
	if userMsg != "" {
		rend.ShowUserMessage(userMsg, 30*time.Second)
	}

	pix := rend.Frame()
	img := &image.RGBA{
		Pix:    pix,
		Stride: 1280 * 4,
		Rect:   image.Rect(0, 0, 1280, 720),
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}
