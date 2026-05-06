// Command render-smoke produces sample frames from the video renderer and
// writes them as PNGs so we can eyeball that the layout (topic title, phase,
// subtitle box) and CJK glyphs render correctly.
//
// Three modes:
//
//	--mode debate (default): the original CNN-style debate cases, output to
//	  out/render-smoke/.
//	--mode puzzle: cinematic situation-puzzle layout. If OPENAI_API_KEY /
//	  AI_GATEWAY_API_KEY is set, real Gemini-generated scene backgrounds are
//	  fetched (and disk-cached). Otherwise the smoke test falls back to a
//	  procedural noise bg so the layout is still reviewable. Output goes to
//	  out/puzzle-render-smoke/.
//	--mode puzzle-fade: emits an mp4 that demonstrates the cinematic name-
//	  plate fade-in / hold / fade-out so we can eyeball the smoothstep
//	  curve without having to wait the full 22 s hold in real time. Output
//	  goes to out/puzzle-fade-smoke/fade.mp4.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/joho/godotenv"
	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/video"
	"github.com/sirily11/debate-bot/internal/video/imagegen"
	"github.com/sirily11/debate-bot/internal/video/scenes"
)

func main() {
	mode := flag.String("mode", "debate", "render mode: debate | puzzle")
	out := flag.String("out", "", "output directory (default: out/render-smoke for debate, out/puzzle-render-smoke for puzzle)")
	flag.Parse()

	// Load .env if present so AI_GATEWAY_API_KEY / OPENAI_API_KEY (the
	// gateway key the rest of the bot already uses) work without the user
	// having to export them in the shell. Overload (not Load) so .env wins
	// over a stale shell value.
	_ = godotenv.Overload()

	switch *mode {
	case "puzzle":
		dir := *out
		if dir == "" {
			dir = "out/puzzle-render-smoke"
		}
		if err := runPuzzle(dir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "puzzle-fade":
		dir := *out
		if dir == "" {
			dir = "out/puzzle-fade-smoke"
		}
		if err := runPuzzleFade(dir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "debate", "":
		dir := *out
		if dir == "" {
			dir = "out/render-smoke"
		}
		if err := runDebate(dir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q (expected debate | puzzle | puzzle-fade)\n", *mode)
		os.Exit(1)
	}
}

// ---------- debate mode (existing) ----------

func runDebate(out string) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
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
		path := fmt.Sprintf("%s/%s.png", out, c.name)
		if err := writeFrame(path, c.topic, c.phase, c.speaker, c.role, c.side, c.body, c.userMsg, c.userName, c.elapsed, c.total, stage, c.bodyElapsed); err != nil {
			return fmt.Errorf("%s: %w", c.name, err)
		}
		fmt.Println("wrote", path)
	}
	return nil
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
	rend.SetPositions(
		"AI 是程序員的放大器：它讓人類更有產能，但設計、判斷、責任仍由工程師承擔。",
		"AI 將取代大量初階程序員：寫代碼可被自動化，行業必須面對結構性收縮。",
	)
	rend.SetState(speaker, role, side, body, 0)
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

// ---------- puzzle mode ----------

// puzzleTopicPath is the committed sample puzzle the smoke test renders
// against so the visual review reflects the real story flow.
const puzzleTopicPath = "channels/situation-puzzle/01_haigui_soup.md"

func runPuzzle(out string) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	bgsDir := filepath.Join(out, "bgs")
	if err := os.MkdirAll(bgsDir, 0o755); err != nil {
		return err
	}

	topic, err := config.LoadTopic(puzzleTopicPath)
	if err != nil {
		return fmt.Errorf("load %s: %w", puzzleTopicPath, err)
	}

	// Try to fetch real Gemini scene bgs. Falls back to procedural if no
	// API key is configured — we still want the layout to be reviewable
	// in CI / offline.
	puzzleScenes := buildPuzzleScenes(topic, bgsDir)

	cases := []struct {
		name        string
		phase       string
		scene       string
		speaker     string
		role        string
		body        string
		elapsed     time.Duration
		total       time.Duration
		bodyElapsed time.Duration
		// titleOver overrides topic.Title for this frame. Empty = use the
		// loaded topic title. Used to visualise how the idle card auto-
		// fits a multi-line title without forking the YAML fixture.
		titleOver string
	}{
		{
			name:  "01-idle-surface",
			phase: "出題",
			scene: scenes.SceneSurface,
		},
		{
			// "Scene still loading" state — what the audience sees during
			// the ~60s blocking gen at puzzle topic admission. No scene bg
			// is attached; the idle subtitle shows surface text under a
			// "TODAY'S PUZZLE" pill.
			name:  "01b-idle-loading",
			phase: "出題",
			scene: "", // intentionally empty so SetSceneBackground is skipped
		},
		{
			// Idle frame with a deliberately long title to verify the
			// card auto-grows to wrap the title across multiple lines
			// instead of clipping or staying at the single-line height.
			name:      "01c-idle-long-title",
			phase:     "出題",
			scene:     scenes.SceneSurface,
			titleOver: "深夜咖啡館的最後一位客人為什麼總是點同一杯沒人喝過的調酒",
		},
		{
			name:    "02-host-surface",
			phase:   "出題",
			scene:   scenes.SceneSurface,
			speaker: "出題者",
			role:    "puzzle-host",
			body:    "一名男子走進一家海邊的高級餐廳，點了一碗海龜湯。他喝了一口，呆坐片刻，放下湯匙，結帳離開，回家後便結束了自己的生命。為什麼？讓我們開始提問——記住，只能用是、不是、與此無關來回答。",
			elapsed: 30 * time.Second,
			total:   25 * time.Minute,
		},
		{
			name:    "03-player-qa",
			phase:   "問答",
			scene:   scenes.SceneQA,
			speaker: "Alice",
			role:    "player",
			body:    "他是不是在那家餐廳裡認出了什麼讓他震驚的東西？",
			elapsed: 4*time.Minute + 30*time.Second,
			total:   25 * time.Minute,
		},
		{
			name:    "04-host-answer",
			phase:   "問答",
			scene:   scenes.SceneQA,
			speaker: "出題者",
			role:    "puzzle-host",
			body:    "是。但更精確地說——他認出的不是物，而是味道。",
			elapsed: 4*time.Minute + 45*time.Second,
			total:   25 * time.Minute,
		},
		{
			name:        "05-host-reveal",
			phase:       "揭曉",
			scene:       scenes.SceneReveal,
			speaker:     "出題者",
			role:        "puzzle-host",
			body:        "這名男子曾在多年前隨船出海，船在風暴中失事。他與幾名同伴漂流在救生艇上多日，瀕臨餓死。一名同伴提議用釣到的海龜熬湯救命。獲救後男子再也沒吃過海龜湯——直到今天。今日他在餐廳第一次嚐到真正的海龜湯——味道與當年截然不同。他這才驚覺：當年的海龜湯，其實是同伴用犧牲者的肉熬製的，那些同伴為了讓他活下去，騙他說是海龜。",
			elapsed:     22 * time.Minute,
			total:       25 * time.Minute,
			bodyElapsed: 8 * time.Second,
		},
		{
			name:    "06-conclusion",
			phase:   "總結",
			scene:   scenes.SceneConclusion,
			speaker: "Alice",
			role:    "player",
			body:    "原來真相藏在味覺的差異裡——這個故事讓人久久無法平靜。",
			elapsed: 24*time.Minute + 30*time.Second,
			total:   25 * time.Minute,
		},
	}

	const settledStage = 2 * time.Second
	const settledScene = 2 * time.Second

	for _, c := range cases {
		path := filepath.Join(out, c.name+".png")
		title := topic.Title
		if c.titleOver != "" {
			title = c.titleOver
		}
		if err := writePuzzleFrame(path, topic, title, puzzleScenes,
			c.phase, c.scene, c.speaker, c.role, c.body,
			c.elapsed, c.total,
			settledStage, settledScene, c.bodyElapsed); err != nil {
			return fmt.Errorf("%s: %w", c.name, err)
		}
		fmt.Println("wrote", path)
	}
	return nil
}

func writePuzzleFrame(path string, topic *config.DebateTopic, title string, sc *scenes.PuzzleScenes,
	phase, scene, speaker, role, body string,
	elapsed, total, stageElapsed, sceneElapsed, bodyElapsed time.Duration,
) error {
	rend, err := video.NewRendererForTest(1280, 720)
	if err != nil {
		return err
	}
	rend.SetPuzzleMode(true)
	rend.SetTopic(title)
	rend.SetPhase(phase)
	// Surface text rides on the AffPosition slot (matches buildTopicMsg in
	// cmd/debate-bot/main.go). Renderer reads it for the idle subtitle.
	rend.SetPositions(topic.Surface, "")
	if img := sc.ByName(scene); img != nil {
		rend.SetSceneBackground(img)
	}
	rend.SetState(speaker, role, "", body, 0)
	if elapsed > 0 || total > 0 {
		rend.SetClock(elapsed, total)
	}
	if stageElapsed > 0 {
		rend.AdvanceStageForTest(stageElapsed)
	}
	if sceneElapsed > 0 {
		rend.AdvanceSceneForTest(sceneElapsed)
	}
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

// buildPuzzleScenes returns either real Gemini-generated bgs (when the API
// key is present and the call succeeds) or procedural bgs otherwise. Never
// returns nil.
func buildPuzzleScenes(topic *config.DebateTopic, cacheDir string) *scenes.PuzzleScenes {
	if client, err := imagegen.New(""); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		fmt.Println("→ generating puzzle scene bgs via Gemini (cached at", cacheDir+")")
		t0 := time.Now()
		sc, err := scenes.Generate(ctx, client, topic, cacheDir)
		if err != nil {
			fmt.Fprintln(os.Stderr, "scene gen partial:", err)
		} else {
			fmt.Println("  done in", time.Since(t0).Round(time.Millisecond))
		}
		fillMissing(sc)
		return sc
	}

	fmt.Println("→ no API key — using procedural bgs (set OPENAI_API_KEY to fetch Gemini scenes)")
	return proceduralScenes()
}

// fillMissing replaces nil scene fields with procedural placeholders so
// every smoke case always renders some bg. Surface/Conclusion are slices of
// variants so we fill any nil slot inside them.
func fillMissing(sc *scenes.PuzzleScenes) {
	proc := proceduralScenes()
	if len(sc.Surface) == 0 {
		sc.Surface = proc.Surface
	} else {
		for i, img := range sc.Surface {
			if img == nil {
				sc.Surface[i] = proc.Surface[i%len(proc.Surface)]
			}
		}
	}
	if sc.QA == nil {
		sc.QA = proc.QA
	}
	if sc.Reveal == nil {
		sc.Reveal = proc.Reveal
	}
	if len(sc.Conclusion) == 0 {
		sc.Conclusion = proc.Conclusion
	} else {
		for i, img := range sc.Conclusion {
			if img == nil {
				sc.Conclusion[i] = proc.Conclusion[i%len(proc.Conclusion)]
			}
		}
	}
}

// proceduralScenes synthesises distinguishable bg plates without any
// network. Each one is a different color palette + soft noise so the smoke
// test reviewer can still tell scenes apart. Surface/Conclusion get the
// same variant counts as the real generator so fillMissing can index into
// them by variant index.
func proceduralScenes() *scenes.PuzzleScenes {
	out := &scenes.PuzzleScenes{
		QA:     proceduralBg(color.RGBA{0x1a, 0x22, 0x2c, 0xff}, color.RGBA{0x06, 0x0a, 0x10, 0xff}, 2),
		Reveal: proceduralBg(color.RGBA{0x3a, 0x10, 0x12, 0xff}, color.RGBA{0x08, 0x02, 0x05, 0xff}, 3),
	}
	for i := 0; i < scenes.SurfaceVariantCount; i++ {
		// Step the seed and tilt the palette per variant so each procedural
		// surface frame looks distinct mid-rotation.
		out.Surface = append(out.Surface, proceduralBg(
			color.RGBA{uint8(0x12 + i*0x06), uint8(0x1c + i*0x04), uint8(0x36 - i*0x04), 0xff},
			color.RGBA{0x05, 0x08, 0x14, 0xff},
			int64(100+i),
		))
	}
	for i := 0; i < scenes.ConclusionVariantCount; i++ {
		out.Conclusion = append(out.Conclusion, proceduralBg(
			color.RGBA{uint8(0x2c + i*0x06), uint8(0x24 - i*0x03), uint8(0x18 + i*0x05), 0xff},
			color.RGBA{0x0c, 0x09, 0x05, 0xff},
			int64(200+i),
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
		// Vertical gradient.
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

// ---------- puzzle-fade mode ----------

// runPuzzleFade renders a short MP4 demonstrating the cinematic name-plate
// animation: pop-on fade-in (~300 ms) → 1 s steady hold → 22 s hold
// compressed to 0 by AdvanceSpeakerForTest → 1.2 s smoothstep fade-out
// → ~500 ms tail with the plate gone. Frames are piped raw RGBA into
// ffmpeg so we get a real mp4 to scrub through, not just a stack of
// PNGs. AdvanceSpeakerForTest jumps the renderer's internal clock per
// frame so the smoke runs in roughly real time (~3 s) regardless of the
// underlying 22 s hold the production renderer enforces.
func runPuzzleFade(out string) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}

	const (
		width  = 1280
		height = 720
		fps    = 30
	)

	// puzzle topic. Try the canonical sample first, fall back to whatever
	// situation-puzzle file happens to exist so the smoke test isn't tied
	// to a specific committed topic file.
	topicPath := puzzleTopicPath
	if _, err := os.Stat(topicPath); err != nil {
		matches, _ := filepath.Glob("channels/situation-puzzle/*.md")
		if len(matches) == 0 {
			return fmt.Errorf("no situation-puzzle topic files found")
		}
		topicPath = matches[0]
	}
	topic, err := config.LoadTopic(topicPath)
	if err != nil {
		return fmt.Errorf("load %s: %w", topicPath, err)
	}
	bgsDir := filepath.Join(out, "bgs")
	if err := os.MkdirAll(bgsDir, 0o755); err != nil {
		return err
	}
	puzzleScenes := buildPuzzleScenes(topic, bgsDir)

	rend, err := video.NewRendererForTest(width, height)
	if err != nil {
		return err
	}
	rend.SetPuzzleMode(true)
	rend.SetTopic(topic.Title)
	rend.SetPhase("出題")
	rend.SetPositions(topic.Surface, "")
	// SetPuzzleSceneName must come before SetState — framePuzzle reads it
	// to decide whether the lower-third runs through the cinematic
	// fade-in/hold/fade-out animation. Surface is the canonical
	// cinematic scene. Without this the plate just sits at full opacity.
	rend.SetPuzzleSceneName(scenes.SceneSurface)
	if img := puzzleScenes.ByName(scenes.SceneSurface); img != nil {
		rend.SetSceneBackground(img)
	}
	rend.SetState("出題者", "puzzle-host", "",
		"一名男子走進一家海邊的高級餐廳，點了一碗海龜湯。", 0)
	rend.SetClock(30*time.Second, 25*time.Minute)
	// Park the scene crossfade so we don't capture a transitional bg.
	rend.AdvanceSceneForTest(2 * time.Second)

	// Timeline (synthetic — backdated speakerStart drives the fade so we
	// don't have to wait the production 22 s hold in real time):
	//
	//   0 — 0.30 s   plate fades IN (live)
	//   0.30 s tail  ~0.7 s of steady hold so the eye reads the plate
	//   ~1.0 s       jump speakerStart so it's ε before fade-out start
	//   1.0 — 2.2 s  plate fades OUT (live, smoothstep)
	//   2.2 — 2.7 s  tail with the plate gone
	stage := []struct {
		name     string
		duration time.Duration
		// jumpAtStart, when non-zero, is added to the renderer's perceived
		// speaker age the instant we enter this stage (via
		// AdvanceSpeakerForTest). Lets us skip the 22 s production hold.
		jumpAtStart time.Duration
	}{
		{name: "fade-in", duration: 300 * time.Millisecond},
		{name: "hold", duration: 700 * time.Millisecond},
		// Jump speakerStart so the next frame sits just before the fade-out
		// trigger. We subtract the hold/fade-in we already rendered so the
		// effective elapsed is exactly surfaceFadeHoldDuration on entry.
		{name: "fade-out", duration: 1200 * time.Millisecond,
			jumpAtStart: 22*time.Second - (300+700)*time.Millisecond},
		{name: "post", duration: 500 * time.Millisecond},
	}

	mp4Path := filepath.Join(out, "fade.mp4")
	frameInterval := time.Second / time.Duration(fps)
	args := []string{
		"-y",
		"-loglevel", "error",
		"-f", "rawvideo",
		"-pix_fmt", "rgba",
		"-s", fmt.Sprintf("%dx%d", width, height),
		"-r", fmt.Sprintf("%d", fps),
		"-i", "pipe:0",
		"-c:v", "libx264",
		"-pix_fmt", "yuv420p",
		"-movflags", "+faststart",
		mp4Path,
	}
	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdout = os.Stdout
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("ffmpeg stdin: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ffmpeg start: %w", err)
	}

	frameCount := 0
	for _, st := range stage {
		if st.jumpAtStart > 0 {
			rend.AdvanceSpeakerForTest(st.jumpAtStart)
		}
		nFrames := int(st.duration / frameInterval)
		for i := 0; i < nFrames; i++ {
			pix := rend.Frame()
			if _, err := stdin.Write(pix); err != nil {
				_ = stdin.Close()
				_ = cmd.Wait()
				return fmt.Errorf("write frame %d (%s): %w", frameCount, st.name, err)
			}
			rend.AdvanceSpeakerForTest(frameInterval)
			frameCount++
		}
	}

	if err := stdin.Close(); err != nil {
		return fmt.Errorf("close ffmpeg stdin: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("ffmpeg wait: %w\n%s", err, stderr.String())
	}

	abs, _ := filepath.Abs(mp4Path)
	fmt.Printf("wrote %d frames → %s\n", frameCount, abs)
	return nil
}
