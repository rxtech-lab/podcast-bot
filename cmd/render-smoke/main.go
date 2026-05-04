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
		userName string
		elapsed  time.Duration
		total    time.Duration
		// stageElapsed simulates time since the most recent idle↔active
		// transition. <0 leaves the renderer's modeStart untouched (pre-
		// transition idle); 0 means transition just began (will look idle for
		// active states); >= stageTransitionDuration means fully settled.
		stageElapsed time.Duration
		// bodyElapsed backdates the subtitle's body-start clock so an
		// overflowing passage can be captured mid-scroll. 0 leaves it as the
		// just-set instant, which puts the renderer in the initial dwell
		// where the scroll offset is still 0.
		bodyElapsed time.Duration
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
			speaker: "Alice",
			role:    "affirmative",
			side:    "affirmative",
			body:    "AI 將在未來十年內取代大多數初級和中級程序員的工作。其能力提升曲線陡峭、邊際成本低、工具鏈正在快速成熟。",
			elapsed: 1*time.Minute + 12*time.Second,
			total:   30 * time.Minute,
		},
		{
			name:    "03-negative-zh-long",
			topic:   "AI 是否會取代程序員",
			phase:   "free-debate",
			speaker: "Linda",
			role:    "negative",
			side:    "negative",
			body:    "反方主張:AI 不會取代程序員,反而會放大他們的能力。寫代碼只是程序員工作的一小部分;設計、溝通、判斷、責任承擔仍需人類。即使工具更強大,責任邊界仍由人來界定。",
			elapsed: 14*time.Minute + 27*time.Second,
			total:   30 * time.Minute,
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
			phase:   "free-debate",
			speaker: "Carol",
			role:    "affirmative",
			side:    "affirmative",
			body:    "AI 將在未來十年內取代大多數初級和中級程序員的工作。",
			userMsg:  "請問正方,如果 AI 替代了所有初級程序員,新人從哪裡入行?",
			userName: "觀眾_42",
			elapsed:  3*time.Minute + 42*time.Second,
			total:    10 * time.Minute,
		},
		{
			name:     "07-user-overlay-idle",
			topic:    "AI 是否會取代程序員",
			phase:    "opening",
			userMsg:  "Hello panel — can you address the impact on entry-level hiring?",
			userName: "viewer_alpha",
		},
		// 08, 09, 10 — three samples along the idle→active transition curve so
		// we can eyeball the magic-move + crossfade.
		{
			name:         "08-transition-25",
			topic:        "AI 是否會取代程序員",
			phase:        "opening",
			speaker:      "Alice",
			role:         "affirmative",
			side:         "affirmative",
			body:         "AI 將在未來十年內取代大多數初級和中級程序員的工作。",
			stageElapsed: 150 * time.Millisecond,
		},
		{
			name:         "09-transition-50",
			topic:        "AI 是否會取代程序員",
			phase:        "opening",
			speaker:      "Alice",
			role:         "affirmative",
			side:         "affirmative",
			body:         "AI 將在未來十年內取代大多數初級和中級程序員的工作。",
			stageElapsed: 300 * time.Millisecond,
		},
		{
			name:         "10-transition-75",
			topic:        "AI 是否會取代程序員",
			phase:        "opening",
			speaker:      "Alice",
			role:         "affirmative",
			side:         "affirmative",
			body:         "AI 將在未來十年內取代大多數初級和中級程序員的工作。",
			stageElapsed: 450 * time.Millisecond,
		},
		// 11, 12 — overflowing subtitle. "Start" captures the initial dwell
		// (scroll offset still 0, opening lines visible). "Mid" is captured
		// well past the dwell so the card has scrolled forward enough to
		// reveal lines the legacy sliding-window code would have hidden.
		{
			name:    "11-long-subtitle-start",
			topic:   "AI 是否會取代程序員",
			phase:   "free-debate",
			speaker: "Linda",
			role:    "negative",
			side:    "negative",
			body:    "反方主張：AI 不會取代程序員，反而會放大他們的能力。寫代碼只是程序員工作的一小部分；設計、溝通、判斷、責任承擔仍需人類。即使工具更強大，責任邊界仍由人來界定。我們認為，真正的工程能力來自對問題本質的洞察，以及對複雜系統長期負責的能力，這不是統計性語言模型所能取代的。歷史上每一次工具升級——編譯器、IDE、雲端、開源——都讓程序員產出更高，而不是讓他們失業。",
			elapsed: 14*time.Minute + 27*time.Second,
			total:   30 * time.Minute,
		},
		{
			name:        "12-long-subtitle-mid",
			topic:       "AI 是否會取代程序員",
			phase:       "free-debate",
			speaker:     "Linda",
			role:        "negative",
			side:        "negative",
			body:        "反方主張：AI 不會取代程序員，反而會放大他們的能力。寫代碼只是程序員工作的一小部分；設計、溝通、判斷、責任承擔仍需人類。即使工具更強大，責任邊界仍由人來界定。我們認為，真正的工程能力來自對問題本質的洞察，以及對複雜系統長期負責的能力，這不是統計性語言模型所能取代的。歷史上每一次工具升級——編譯器、IDE、雲端、開源——都讓程序員產出更高，而不是讓他們失業。",
			elapsed:     14*time.Minute + 27*time.Second,
			total:       30 * time.Minute,
			bodyElapsed: 6 * time.Second,
		},
	}

	// Default smoke frames render the settled end-state of any transition
	// they trigger. Cases that explicitly set stageElapsed override this.
	const settledStage = 2 * time.Second

	for _, c := range cases {
		stage := c.stageElapsed
		if stage == 0 {
			stage = settledStage
		}
		path := fmt.Sprintf("%s/%s.png", *out, c.name)
		if err := writeFrame(path, c.topic, c.phase, c.speaker, c.role, c.side, c.body, c.userMsg, c.userName, c.elapsed, c.total, stage, c.bodyElapsed); err != nil {
			fmt.Fprintln(os.Stderr, c.name, err)
			os.Exit(1)
		}
		fmt.Println("wrote", path)
	}
}

func writeFrame(path, topic, phase, speaker, role, side, body, userMsg, userName string, elapsed, total, stageElapsed, bodyElapsed time.Duration) error {
	rend, err := video.NewRendererForTest(1280, 720)
	if err != nil {
		return err
	}
	rend.SetTopic(topic)
	rend.SetPhase(phase)
	rend.SetSides(
		[]string{"Alice", "Carol"},
		[]string{"Linda", "Bob"},
	)
	rend.SetState(speaker, role, side, body)
	if elapsed > 0 || total > 0 {
		rend.SetClock(elapsed, total)
	}
	if userMsg != "" {
		rend.ShowUserMessage(userMsg, userName, 30*time.Second)
		// Backdate the start so the smoke frame catches the ticker mid-scroll
		// — at t=0 the text would still be off the right edge.
		rend.AdvanceUserMessageForTest(5 * time.Second)
	}
	// Walk the renderer forward through the stage transition by the requested
	// amount so the frame captures either the settled state (default) or a
	// specific point along the easing curve.
	if stageElapsed > 0 {
		rend.AdvanceStageForTest(stageElapsed)
	}
	// Backdate the body's start time so an overflowing subtitle can be
	// captured partway through its vertical scroll.
	if bodyElapsed > 0 {
		rend.AdvanceBodyForTest(bodyElapsed)
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
