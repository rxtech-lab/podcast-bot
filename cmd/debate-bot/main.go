package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	goqueue "github.com/michaelginalick/go-queue"
	"github.com/sirily11/debate-bot/internal/audio"
	"github.com/sirily11/debate-bot/internal/config"
	contentcreator "github.com/sirily11/debate-bot/internal/content_creator"
	"github.com/sirily11/debate-bot/internal/eventbus"
	"github.com/sirily11/debate-bot/internal/server"
	"github.com/sirily11/debate-bot/internal/storage"
	"github.com/sirily11/debate-bot/internal/util"
	"github.com/sirily11/debate-bot/internal/video"
	"github.com/sirily11/debate-bot/internal/videojob"
	"github.com/sirily11/debate-bot/internal/watcher"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "server", "run":
		// `run` is kept as an alias for `server` for backwards compatibility
		// with the previous TUI+server combo command.
		os.Exit(serverCmd(os.Args[2:]))
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `debate-bot — multi-agent live show podcast (TV-channel mode)

usage:
  debate-bot server --channel ./channels.json --content "./topics/*.md" \
                    [--mcp ./mcp.json] [--out ./out] [--addr :3000]

  --channel  path to channels.json — array of {id, number, title} channel
             definitions. Each topic.md frontmatter must declare a `+"`channel`"+`
             field whose value matches one of these ids.
  --content  path or glob to topic .md file(s) — repeatable; consecutive
             paths after one --content are also accepted. The directory each
             spec points at is automatically watched for new .md files at
             runtime (drop a new topic.md into the folder and it is loaded,
             validated against channels.json, appended to the matching
             channel's queue, and surfaced on the web UI without restart).

  Each topic.md must declare a `+"`type`"+` in its frontmatter — one of:
    - debate            multi-agent affirmative-vs-negative debate
    - situation-puzzle  海龜湯 / lateral-thinking puzzle (host knows the
                        hidden truth; players ask yes/no questions)
    - discussion        multi-agent panel discussion (discussants debate one
                        topic from different aspects; a silent commander
                        swaps background image + music on the fly)
  Unknown types abort startup with a clear error.

  Channels run in parallel as independent video + audio streams. Multiple
  topics assigned to the same channel are queued and play sequentially
  inside that channel. Every channel defined in channels.json pre-
  initialises its encoder so a freshly-dropped topic can start airing
  immediately, even if the channel started with no initial topics.

  --debate and --topic are deprecated aliases for --content (still work,
  print a warning). `+"`run`"+` is kept as an alias for `+"`server`"+` for backwards
  compatibility.

  --password gates the whole web UI + API behind a password (falls back to
  the APP_PASSWORD env var). When set, the browser must sign in before any
  /api/* route responds; unauthenticated requests get 401. Omit it (the
  default) to leave the server open.

env (loaded from .env if present):
  OPENAI_BASE_URL   OPENAI_API_KEY   HOST_MODEL
  COMPRESSION_BASE_URL   COMPRESSION_API_KEY   COMPRESSION_MODEL
  GEMINI_API_KEY                            (required — drives Lyria music
                                              and Gemini scene image gen)
  AZURE_SPEECH_KEY   AZURE_SPEECH_REGION   (required when tts_provider=azure)
  ELEVENLABS_API_KEY                        (required when tts_provider=eleven)
  OUT_DIR (optional, default ./out)`)
}

// loadedDebate is one resolved debate.md file with its parsed config.
type loadedDebate struct {
	id    string
	path  string
	title string
	topic *config.DebateTopic
}

// debateQueue is a FIFO of debates feeding one channel's runChannel goroutine.
// Push appends; Pop blocks until an item arrives, ctx cancels, or the queue is
// closed (and drained). Used to support both the original "fixed initial
// queue" mode (queue is closed after seeding) and the --watch mode (queue
// stays open and accepts new debates discovered at runtime).
type debateQueue struct {
	mu     sync.Mutex
	items  []loadedDebate
	notify chan struct{}
	closed bool
}

func newDebateQueue() *debateQueue {
	return &debateQueue{notify: make(chan struct{}, 1)}
}

// Push appends d. Returns false if the queue is already closed.
func (q *debateQueue) Push(d loadedDebate) bool {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return false
	}
	q.items = append(q.items, d)
	q.mu.Unlock()
	q.wake()
	return true
}

// Close marks the queue as sealed. Subsequent Pop calls return ok=false once
// the existing items are drained. No-op if already closed.
func (q *debateQueue) Close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.wake()
}

func (q *debateQueue) wake() {
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

// Remove drops the first queued debate matching id. Returns true if removed,
// false if the id wasn't found (already started, never queued, or already
// removed). Only affects pending items still sitting in the queue — a debate
// that has already been Pop'd is the runChannel goroutine's problem now.
func (q *debateQueue) Remove(id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, item := range q.items {
		if item.id == id {
			q.items = append(q.items[:i], q.items[i+1:]...)
			return true
		}
	}
	return false
}

// Pop returns the next item. ok=false on ctx cancellation, or when the queue
// is closed AND empty.
func (q *debateQueue) Pop(ctx context.Context) (loadedDebate, bool) {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			d := q.items[0]
			q.items = q.items[1:]
			q.mu.Unlock()
			return d, true
		}
		closed := q.closed
		q.mu.Unlock()
		if closed {
			return loadedDebate{}, false
		}
		select {
		case <-ctx.Done():
			return loadedDebate{}, false
		case <-q.notify:
		}
	}
}

// channelRuntime is the per-channel slice of audio/video infrastructure.
// live and enc are nil for off-air channels (no debates queued AND no watcher
// pre-initialised encoder).
type channelRuntime struct {
	def             config.Channel
	queue           *debateQueue
	live            *audio.LiveStream
	enc             *video.Encoder
	puzzleStage     *video.PuzzleStage     // retained so scene generators can call AttachScenes
	seriesStage     *video.SeriesStage     // retained for series episodes (preparation hooks reach in)
	discussionStage *video.DiscussionStage // retained so the palette generator can call AttachPalette

	// counterMu protects total + started, which feed the live TopicMsg.Total
	// and Index values. Both grow over the channel's lifetime: total
	// increments on every Push, started increments when runChannel actually
	// starts the next debate.
	counterMu sync.Mutex
	total     int
	started   int
}

// Server modes.
//
// modeStream is the default: the process airs every queued debate over
// per-channel HLS video + MP3 audio streams and the embedded SPA shows
// the TV-tuner UI.
//
// modeVideo skips channel/queue bootstrap entirely. The HTTP server
// instead exposes a job API: a browser uploads a script.md (and, for
// series, a zip of prior generations); the server runs one orchestrator,
// stitches the resulting HLS into a downloadable .mp4, and (for series)
// re-zips the persistent show archive so the next generation can chain.
//
// modeDashboard is the API backend for the standalone Next.js dashboard. It
// shares modeVideo's job pipeline (JSON script submit, live WS/SSE, S3 upload)
// but is a distinct mode value so GET /api/config reports "dashboard" and the
// embedded TV SPA is not served — the dashboard is the frontend. CORS +
// service-token auth (DASHBOARD_ORIGINS / DASHBOARD_SERVICE_TOKEN) are expected.
const (
	modeStream    = "stream"
	modeVideo     = "video"
	modeDashboard = "dashboard"
)

func videoDataRoot(env *config.Env, mode string) string {
	name := "video"
	if mode == modeDashboard {
		name = "dashboard"
	}
	return filepath.Join(env.PersistentRoot, name)
}

// ownerPodName returns this pod's identity for cross-pod routing, or "" when
// routing is disabled (which makes the server skip the job proxy entirely).
func ownerPodName(enabled bool, podName string) string {
	if !enabled {
		return ""
	}
	return podName
}

// peerHostResolver returns a function that maps an owner pod name to the
// host:port to dial for it, using the configured template (exactly one %s).
// Returns nil when routing is disabled or the template is malformed, which
// disables the proxy rather than dialing a bad address.
func peerHostResolver(enabled bool, template string) func(string) string {
	if !enabled || !strings.Contains(template, "%s") {
		return nil
	}
	return func(pod string) string {
		if pod == "" {
			return ""
		}
		return fmt.Sprintf(template, pod)
	}
}

// runtime owns every cross-channel resource: the shared event bus, server,
// session registry, and the per-channel encoders/livestreams.
//
// In modeVideo, channels / channelByID stay empty and the orchestrator
// is invoked per-job (see internal/content_creator/video_job.go).
type runtime struct {
	ctx    context.Context
	cancel context.CancelFunc
	log    *slog.Logger
	closer interface{ Close() error }

	mode        string
	env         *config.Env
	mcpCfg      *config.MCPConfig
	bus         *eventbus.Bus
	srv         *server.Server
	sessions    *server.SessionRegistry
	jobs        *server.JobRegistry
	discussions *server.DiscussionStore
	channels    []*channelRuntime
	channelByID map[string]*channelRuntime
	addr        string
	stopSig     chan os.Signal

	// loadedDebates tracks every absolute debate.md path that has already
	// been queued (initial --debate args + watcher discoveries). The stored
	// ref lets a delete event find which channel + debate id to drop. A
	// re-save of an already-loaded file is deduped via this map; a delete
	// removes the entry so re-creating the same filename re-queues.
	// Protected by loadedMu.
	loadedMu      sync.Mutex
	loadedDebates map[string]loadedRef
	// usedIDs tracks slug → count for id-collision suffixes ("ai" → "ai-2").
	// Shared across channels so two debates can never share an id.
	usedIDs map[string]int
}

// loadedRef is what we remember per queued debate path: the channel id +
// debate id needed to remove it from the queue / session registry when the
// underlying file is deleted from the watched directory.
type loadedRef struct {
	channelID string
	debateID  string
}

// resolveDebates expands a list of literal paths or globs into the set of
// debate files (deduped, sorted). Files are NOT yet loaded into topics here;
// the caller invokes loadDebate per path so the same code path runs for
// initial seed + watcher discoveries. A glob that matches nothing is a
// valid empty-match (the dir is still watched for files dropped later); a
// non-glob spec falls through as a literal path so config.LoadTopic can
// surface a clear "missing file" error to the user.
func resolveDebates(specs []string) ([]string, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	seen := map[string]bool{}
	var matches []string
	for _, spec := range specs {
		isGlob := strings.ContainsAny(spec, "*?[")
		ms, err := filepath.Glob(spec)
		if err != nil {
			return nil, fmt.Errorf("debate glob %q: %w", spec, err)
		}
		if len(ms) == 0 {
			if isGlob {
				continue
			}
			ms = []string{spec}
		}
		for _, m := range ms {
			abs, err := filepath.Abs(m)
			if err != nil {
				return nil, fmt.Errorf("debate abs path %q: %w", m, err)
			}
			if seen[abs] {
				continue
			}
			seen[abs] = true
			matches = append(matches, abs)
		}
	}
	sort.Strings(matches)
	return matches, nil
}

// watchDirsFromSpecs returns the unique absolute directories implied by a
// list of file paths or globs. The caller hands these to the fsnotify-backed
// watcher so any .md file dropped into the directory gets auto-loaded at
// runtime.
//
// Examples: "./topics/*.md" → "<cwd>/topics", "./debates/foo.md" →
// "<cwd>/debates", "topic.md" → "<cwd>".
//
// Future flags that take file/glob specs (e.g. a future --story for a
// different content type) should funnel their values through this helper
// too — the auto-watch behaviour then comes for free without any extra
// flag plumbing.
func watchDirsFromSpecs(specs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, spec := range specs {
		// Strip everything from the first glob metacharacter so
		// filepath.Dir lands on a real directory ("./topics/*.md" →
		// "./topics/").
		if idx := strings.IndexAny(spec, "*?["); idx >= 0 {
			spec = spec[:idx]
		}
		dir := filepath.Dir(spec)
		abs, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		if !seen[abs] {
			seen[abs] = true
			out = append(out, abs)
		}
	}
	return out
}

// loadDebate parses one debate.md and assigns it a globally-unique id by
// slugifying the filename and appending -2, -3, ... on collision. Caller must
// hold rt.loadedMu for the duration so the used-ids update is atomic.
//
// The caller is responsible for recording an entry in loadedDebates AFTER
// channel-id validation — that way a topic referencing an unknown channel
// doesn't leave a phantom entry behind, and a later delete event for the
// same path won't try to remove something that was never queued.
func (r *runtime) loadDebateLocked(path string) (loadedDebate, error) {
	t, err := config.LoadTopic(path)
	if err != nil {
		return loadedDebate{}, fmt.Errorf("load %s: %w", path, err)
	}
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	id := slugify(base)
	if id == "" {
		id = "debate"
	}
	r.usedIDs[id]++
	if r.usedIDs[id] > 1 {
		id = fmt.Sprintf("%s-%d", id, r.usedIDs[id])
	}
	return loadedDebate{id: id, path: path, title: t.Title, topic: t}, nil
}

// stringSlice satisfies flag.Value so --content / --watch can be supplied
// multiple times.
type stringSlice []string

func (s *stringSlice) String() string     { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error { *s = append(*s, v); return nil }

// deprecationWarning records which legacy flag (if any) the user passed so
// serverCmd can emit a single warning per run.
type deprecationWarning struct {
	debate bool // --debate was used
	topic  bool // --topic was used
}

// extractContentArgs hoists every --content (and the deprecated --debate /
// --topic aliases) occurrence out of args so the stdlib flag parser doesn't
// trip on the trailing values an unquoted shell glob produces.
func extractContentArgs(args []string) (specs []string, rest []string, deprecated deprecationWarning) {
	rest = make([]string, 0, len(args))
	collect := func(i int) ([]string, int) {
		var out []string
		i++
		for i < len(args) && !strings.HasPrefix(args[i], "-") {
			out = append(out, args[i])
			i++
		}
		return out, i - 1
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--content" || a == "-content":
			vs, j := collect(i)
			specs = append(specs, vs...)
			i = j
		case strings.HasPrefix(a, "--content="):
			specs = append(specs, strings.TrimPrefix(a, "--content="))
		case strings.HasPrefix(a, "-content="):
			specs = append(specs, strings.TrimPrefix(a, "-content="))
		case a == "--debate" || a == "-debate":
			deprecated.debate = true
			vs, j := collect(i)
			specs = append(specs, vs...)
			i = j
		case strings.HasPrefix(a, "--debate="):
			deprecated.debate = true
			specs = append(specs, strings.TrimPrefix(a, "--debate="))
		case strings.HasPrefix(a, "-debate="):
			deprecated.debate = true
			specs = append(specs, strings.TrimPrefix(a, "-debate="))
		case a == "--topic" || a == "-topic":
			deprecated.topic = true
			vs, j := collect(i)
			specs = append(specs, vs...)
			i = j
		case strings.HasPrefix(a, "--topic="):
			deprecated.topic = true
			specs = append(specs, strings.TrimPrefix(a, "--topic="))
		case strings.HasPrefix(a, "-topic="):
			deprecated.topic = true
			specs = append(specs, strings.TrimPrefix(a, "-topic="))
		default:
			rest = append(rest, a)
		}
	}
	return
}

var slugRe = regexp.MustCompile(`[^a-z0-9_-]+`)

// seriesEpisodeGap is the breathing pause held between two consecutive
// series episodes after orch.Run drains the audio. The stage is parked
// on the series fallback plate (no caption, no scene) for this window
// so back-to-back episodes don't read as a hard cut on a narrated
// drama. Cancellable via the runtime context.
const seriesEpisodeGap = 6 * time.Second

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugRe.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// bootstrap loads env, channels.json, every initial debate.md, validates the
// channel references, and stands up the per-channel infrastructure. Each
// channels.json entry becomes a channelRuntime with its encoder + livestream
// pre-initialised — auto-watch is always on, so a debate.md dropped into the
// watched directory at runtime can start airing on any channel without re-
// bootstrapping.
func bootstrap(channelsPath string, debateSpecs []string, mcpPath, outOverride, addr, password string) (*runtime, int) {
	if err := audio.VerifyTools(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return nil, 1
	}

	env, err := config.LoadEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, "env:", err)
		return nil, 1
	}
	if outOverride != "" {
		env.OutDir = outOverride
	}
	sessionStamp := time.Now().Format("2006-01-02_15-04-05")
	env.OutDir = filepath.Join(env.OutDir, "session-"+sessionStamp)
	if err := contentcreator.EnsureOutDir(env.OutDir); err != nil {
		fmt.Fprintln(os.Stderr, "out dir:", err)
		return nil, 1
	}
	fmt.Fprintln(os.Stdout, "session output:", env.OutDir)

	channelsCfg, err := config.LoadChannels(channelsPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "channels:", err)
		return nil, 1
	}

	debatePaths, err := resolveDebates(debateSpecs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "debate:", err)
		return nil, 1
	}

	mcpCfg, err := config.LoadMCPConfig(mcpPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mcp:", err)
		return nil, 1
	}

	log, closer, err := util.NewFileLogger(env.OutDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "logger init warning:", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	stopSig := make(chan os.Signal, 1)
	signal.Notify(stopSig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stopSig
		cancel()
	}()

	bus := eventbus.New(log)
	sessions := server.NewSessionRegistry()

	rt := &runtime{
		ctx: ctx, cancel: cancel, log: log, closer: closer,
		mode: modeStream,
		env:  env, mcpCfg: mcpCfg, bus: bus, sessions: sessions,
		addr: addr, stopSig: stopSig,
		channelByID:   map[string]*channelRuntime{},
		loadedDebates: map[string]loadedRef{},
		usedIDs:       map[string]int{},
	}

	// Resolve every initial debate path into a loaded topic. This happens
	// before per-channel infra is built so we know each channel's first-debate
	// resolution (encoder size).
	initial := make([]loadedDebate, 0, len(debatePaths))
	rt.loadedMu.Lock()
	for _, p := range debatePaths {
		d, err := rt.loadDebateLocked(p)
		if err != nil {
			rt.loadedMu.Unlock()
			cancel()
			fmt.Fprintln(os.Stderr, "debate:", err)
			return nil, 1
		}
		if channelsCfg.Find(d.topic.Channel) == nil {
			rt.loadedMu.Unlock()
			cancel()
			fmt.Fprintf(os.Stderr,
				"debate %s references unknown channel %q (channels.json defines: %s)\n",
				d.path, d.topic.Channel, strings.Join(channelIDs(channelsCfg), ", "))
			return nil, 1
		}
		rt.loadedDebates[d.path] = loadedRef{channelID: d.topic.Channel, debateID: d.id}
		initial = append(initial, d)
	}
	rt.loadedMu.Unlock()

	// Group initial debates by channel id, preserving sorted-by-filename
	// order within each group.
	byChannel := map[string][]loadedDebate{}
	for _, d := range initial {
		byChannel[d.topic.Channel] = append(byChannel[d.topic.Channel], d)
	}

	fmt.Fprintf(os.Stdout, "loaded %d channel(s) and %d initial debate(s):\n",
		len(channelsCfg.Channels), len(initial))
	for _, ch := range channelsCfg.Channels {
		queue := byChannel[ch.ID]
		if len(queue) == 0 {
			fmt.Fprintf(os.Stdout, "  ch %d [%s] %s — waiting (no initial debates)\n",
				ch.Number, ch.ID, ch.Title)
			log.Info("channel idle", "id", ch.ID, "number", ch.Number, "title", ch.Title)
			continue
		}
		fmt.Fprintf(os.Stdout, "  ch %d [%s] %s — %d debate(s)\n",
			ch.Number, ch.ID, ch.Title, len(queue))
		for i, d := range queue {
			fmt.Fprintf(os.Stdout, "    %d. %s — %s\n", i+1, d.id, d.title)
			log.Info("queued debate",
				"channel", ch.ID, "index", i+1, "of", len(queue),
				"id", d.id, "path", d.path, "title", d.title)
		}
	}

	// Build per-channel infrastructure. Every channel pre-initialises live +
	// encoder (auto-watch is always on, so any channel may receive a
	// dropped-in debate at runtime). The queue stays open across the whole
	// process lifetime; runChannel blocks on Pop until a debate arrives.
	for _, ch := range channelsCfg.Channels {
		queue := byChannel[ch.ID]
		cr := &channelRuntime{def: ch, queue: newDebateQueue()}

		channelOutDir := filepath.Join(env.OutDir, ch.ID)
		if err := contentcreator.EnsureOutDir(channelOutDir); err != nil {
			cancel()
			fmt.Fprintln(os.Stderr, "channel out dir:", err)
			return nil, 1
		}
		live, err := audio.NewLiveStream(ctx, log)
		if err != nil {
			cancel()
			fmt.Fprintln(os.Stderr, "livestream:", err)
			return nil, 1
		}
		cr.live = live

		// Pick the encoder resolution from the first queued debate; fall
		// back to 720p when no initial debates on this channel (a watcher-
		// added debate with a different resolution later will log the same
		// mismatch warning the multi-debate-on-one-channel path emits).
		res := video.Resolution(config.Resolution720p)
		if len(queue) > 0 {
			res = video.Resolution(queue[0].topic.Resolution)
			for _, d := range queue[1:] {
				if d.topic.Resolution != queue[0].topic.Resolution {
					log.Warn("debate resolution mismatch — using first debate's value",
						"channel", ch.ID,
						"using", queue[0].topic.Resolution,
						"ignored", d.topic.Resolution,
						"debate", d.id)
				}
			}
		}
		var hlsDir string
		enc, err := video.New(ctx, channelOutDir, res, log)
		if err != nil {
			log.Warn("video encoder disabled for channel", "id", ch.ID, "err", err)
			fmt.Fprintln(os.Stderr, "video disabled for", ch.ID, ":", err)
		} else {
			cr.enc = enc
			hlsDir = enc.HLSDir()
			enc.AttachAudio(ctx, live)
			// Each channel runs both stages concurrently — they self-gate on
			// TopicMsg.Type so only the one matching the active content drives
			// the encoder. Adding a third content type means dropping in a
			// third stage here without disturbing the existing two.
			debateStage := video.NewDebateChannelStage(enc, ch.ID)
			puzzleStage := video.NewPuzzleChannelStage(enc, ch.ID)
			cr.puzzleStage = puzzleStage
			seriesStage := video.NewSeriesChannelStage(enc, ch.ID)
			cr.seriesStage = seriesStage
			discussionStage := video.NewDiscussionChannelStage(enc, ch.ID)
			cr.discussionStage = discussionStage
			go debateStage.Run(ctx, bus)
			go puzzleStage.Run(ctx, bus)
			go seriesStage.Run(ctx, bus)
			go discussionStage.Run(ctx, bus)
		}

		rt.channels = append(rt.channels, cr)
		rt.channelByID[ch.ID] = cr
		sessions.RegisterChannel(ch.ID, ch.Number, ch.Title, hlsDir, cr.live)

		// Seed the per-channel queue (all pending). DBPath is set up-front so
		// the server can serve transcripts from disk even before that
		// debate's orchestrator boots; the file is created lazily, an absent
		// file is treated as "no transcript yet".
		seed := make([]server.Session, len(queue))
		for i, d := range queue {
			debateOut := filepath.Join(env.OutDir, ch.ID, d.id)
			seed[i] = server.Session{
				ID:     d.id,
				Title:  d.title,
				Status: server.StatusPending,
				DBPath: filepath.Join(debateOut, "session.db"),
			}
			cr.queue.Push(d)
		}
		cr.counterMu.Lock()
		cr.total = len(queue)
		cr.counterMu.Unlock()
		sessions.SeedChannelDebates(ch.ID, seed)
	}

	rt.srv = server.New(server.Deps{
		Mode:           modeStream,
		Bus:            bus,
		Sessions:       sessions,
		Log:            log,
		Password:       password,
		Env:            env,
		MCPCfg:         mcpCfg,
		AllowedOrigins: env.DashboardOrigins,
		ServiceToken:   env.DashboardServiceToken,
		AuthIssuer:     env.AuthIssuer,
	})

	return rt, 0
}

// bootstrapVideo is the modeVideo counterpart to bootstrap. It loads env,
// stamps a session OutDir, opens the file logger and event bus, and stands
// up the HTTP server with the JobRegistry mounted but no per-channel
// encoders or queues. Topics are uploaded at runtime via /api/jobs and
// each job spins its own short-lived encoder + livestream + stage.
//
// channels.json + topic .md preloading are intentionally skipped — in
// video mode the user-facing surface is browser uploads, not a watched
// directory.
func bootstrapVideo(mode, mcpPath, outOverride, addr string, maxConcurrency int, password string, forceAudio bool) (*runtime, int) {
	if err := audio.VerifyTools(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return nil, 1
	}
	if maxConcurrency < 1 {
		fmt.Fprintln(os.Stderr, "--max-concurrency must be >= 1")
		return nil, 2
	}

	env, err := config.LoadEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, "env:", err)
		return nil, 1
	}
	if outOverride != "" {
		env.OutDir = outOverride
	}
	sessionStamp := time.Now().Format("2006-01-02_15-04-05")
	env.OutDir = filepath.Join(env.OutDir, "session-"+sessionStamp)
	if err := contentcreator.EnsureOutDir(env.OutDir); err != nil {
		fmt.Fprintln(os.Stderr, "out dir:", err)
		return nil, 1
	}
	fmt.Fprintln(os.Stdout, "session output:", env.OutDir)

	mcpCfg, err := config.LoadMCPConfig(mcpPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mcp:", err)
		return nil, 1
	}

	log, closer, err := util.NewFileLogger(env.OutDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "logger init warning:", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	stopSig := make(chan os.Signal, 1)
	signal.Notify(stopSig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stopSig
		cancel()
	}()

	bus := eventbus.New(log)
	dataRoot := videoDataRoot(env, mode)
	jobEnv := *env
	jobEnv.OutDir = dataRoot
	if err := os.MkdirAll(dataRoot, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "data dir:", err)
		cancel()
		return nil, 1
	}
	jobs, err := server.NewJobRegistry(
		filepath.Join(dataRoot, "jobs.db"),
		env.TursoConnectionURL,
		env.TursoAuthToken,
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "jobs db:", err)
		cancel()
		return nil, 1
	}
	// Cross-pod routing is enabled only when this pod has both an identity and
	// a way to address peers; otherwise jobs stay unstamped (single-pod mode).
	routingEnabled := env.PodName != "" && env.PeerHostTemplate != ""
	if routingEnabled {
		jobs.SetPodName(env.PodName)
		fmt.Fprintln(os.Stdout, "Cross-pod job routing enabled · pod:", env.PodName)
	}
	discussions, err := server.NewDiscussionStore(
		filepath.Join(dataRoot, "native-discussions.db"),
		env.TursoConnectionURL,
		env.TursoAuthToken,
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "discussion db:", err)
		cancel()
		return nil, 1
	}
	queue, err := goqueue.NewQueue(maxConcurrency)
	if err != nil {
		fmt.Fprintln(os.Stderr, "job queue:", err)
		cancel()
		return nil, 1
	}

	rt := &runtime{
		ctx: ctx, cancel: cancel, log: log, closer: closer,
		mode: mode,
		env:  &jobEnv, mcpCfg: mcpCfg, bus: bus, jobs: jobs, discussions: discussions,
		addr: addr, stopSig: stopSig,
		channelByID:   map[string]*channelRuntime{},
		loadedDebates: map[string]loadedRef{},
		usedIDs:       map[string]int{},
	}

	uploadRoot := filepath.Join(dataRoot, "uploads")
	if err := os.MkdirAll(uploadRoot, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "upload dir:", err)
		cancel()
		return nil, 1
	}

	uploader, err := storage.New(ctx, storage.Config{
		Bucket:          env.S3Bucket,
		Region:          env.S3Region,
		Endpoint:        env.S3Endpoint,
		Prefix:          env.S3Prefix,
		DownloadBaseURL: env.S3DownloadBaseURL,
		AccessKeyID:     env.S3AccessKeyID,
		SecretAccessKey: env.S3SecretAccessKey,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "s3:", err)
		cancel()
		return nil, 1
	}
	if uploader.Enabled() {
		fmt.Fprintln(os.Stdout, "S3 upload enabled · bucket:", env.S3Bucket)
	}

	rt.srv = server.New(server.Deps{
		Mode:           mode,
		Bus:            bus,
		Jobs:           jobs,
		Discussions:    discussions,
		Progress:       server.NewDiscussionProgressStore(env.RedisURL, log),
		Log:            log,
		UploadRoot:     uploadRoot,
		Password:       password,
		Env:            &jobEnv,
		MCPCfg:         mcpCfg,
		AllowedOrigins: env.DashboardOrigins,
		ServiceToken:   env.DashboardServiceToken,
		AuthIssuer:     env.AuthIssuer,
		Uploader:       uploader,
		ForceAudio:     forceAudio,
		PodName:        ownerPodName(routingEnabled, env.PodName),
		PeerHostFor:    peerHostResolver(routingEnabled, env.PeerHostTemplate),
		// SubmitJob runs one upload through the orchestrator + stitch +
		// (for series) zip pipeline. Defined as a closure so it can
		// reach the env / bus / log without cycling the import graph.
		// Returns synchronously after validation; the heavy work runs
		// in a goroutine the runner spawns.
		SubmitJob: func(jobID string, req server.JobSubmission) error {
			// --audio forces every job to the audio-only feed, overriding
			// whatever the request asked for.
			if forceAudio {
				req.AudioOnly = true
			}
			return videojob.Submit(rt.ctx, videojob.Deps{
				Env:         &jobEnv,
				MCPCfg:      mcpCfg,
				Bus:         bus,
				Jobs:        jobs,
				Discussions: discussions,
				Queue:       queue,
				Log:         log,
				Uploader:    uploader,
			}, jobID, req)
		},
	})

	return rt, 0
}

func channelIDs(cfg *config.ChannelsConfig) []string {
	out := make([]string, len(cfg.Channels))
	for i, c := range cfg.Channels {
		out[i] = c.ID
	}
	return out
}

func (r *runtime) shutdown() {
	r.cancel()
	for _, c := range r.channels {
		if c.enc != nil {
			_ = c.enc.Close()
		}
		if c.live != nil {
			_ = c.live.CloseInput()
		}
	}
	if r.bus != nil {
		r.bus.Close()
	}
	if r.closer != nil {
		_ = r.closer.Close()
	}
	if r.discussions != nil {
		_ = r.discussions.Close()
	}
}

// channelSend wraps the shared bus.Publish so events emitted by an
// orchestrator are stamped with the channel id before they hit the bus.
func (r *runtime) channelSend(channelID string) func(any) {
	return func(v any) {
		r.bus.Publish(contentcreator.StampChannelID(v, channelID))
	}
}

// run starts every channel's queue concurrently. Returns when every channel's
// goroutine exits — either because the queue drained (no --watch) or ctx
// cancelled (--watch).
func (r *runtime) run() error {
	doneCh := make(chan struct{}, len(r.channels))
	active := 0
	for _, ch := range r.channels {
		// A channel can only run debates if it has a livestream. Off-air
		// channels (no infra) don't start a goroutine — the queue is also
		// closed for them so no work would be picked up anyway.
		if ch.live == nil {
			continue
		}
		active++
		go func(ch *channelRuntime) {
			defer func() { doneCh <- struct{}{} }()
			r.runChannel(ch)
		}(ch)
	}
	for i := 0; i < active; i++ {
		<-doneCh
		if r.ctx.Err() != nil {
			return r.ctx.Err()
		}
	}
	return nil
}

// runChannel drives one channel's queue. Pops debates one at a time, plays
// each through to completion, then loops. Exits when the queue is closed AND
// drained (no --watch) or ctx cancels.
func (r *runtime) runChannel(ch *channelRuntime) {
	send := r.channelSend(ch.def.ID)
	for {
		r.log.Info("channel waiting for next debate", "channel", ch.def.ID)
		d, ok := ch.queue.Pop(r.ctx)
		if !ok {
			r.log.Info("channel queue closed — exiting", "channel", ch.def.ID)
			return
		}
		r.log.Info("channel popped next debate",
			"channel", ch.def.ID, "id", d.id, "title", d.title, "type", d.topic.Type)

		ch.counterMu.Lock()
		i := ch.started
		ch.started++
		total := ch.total
		ch.counterMu.Unlock()

		debateEnv := *r.env
		debateEnv.OutDir = filepath.Join(r.env.OutDir, ch.def.ID, d.id)
		if err := contentcreator.EnsureOutDir(debateEnv.OutDir); err != nil {
			r.log.Error("debate out dir", "channel", ch.def.ID, "id", d.id, "err", err)
			r.sessions.SetDebateStatus(ch.def.ID, d.id, server.StatusError)
			continue
		}

		orch, err := contentcreator.New(&debateEnv, d.topic, r.mcpCfg, send, r.log, ch.live)
		if err != nil {
			r.log.Error("orchestrator build", "channel", ch.def.ID, "id", d.id, "err", err)
			r.sessions.SetDebateStatus(ch.def.ID, d.id, server.StatusError)
			continue
		}

		r.sessions.SetCurrentOrch(ch.def.ID, d.id, orch)
		r.sessions.SetDebateStatus(ch.def.ID, d.id, server.StatusRunning)

		r.log.Info("starting debate",
			"channel", ch.def.ID,
			"index", i+1, "of", total,
			"id", d.id, "title", d.title,
			"out_dir", debateEnv.OutDir)
		fmt.Fprintf(os.Stdout, "▶ ch %d [%s] starting debate %d/%d — %s\n",
			ch.def.Number, ch.def.ID, i+1, total, d.title)

		// For series content, flip the renderer into narration mode
		// synchronously BEFORE the topic msg lands on the bus. Without
		// this, the few frames between "send TopicMsg" and "SeriesStage
		// receives it" still render through the debate path and briefly
		// show the CNN-style "今日辯題 · TODAY'S TOPIC" idle card with
		// the episode title — visibly wrong for a narrated drama.
		if d.topic.Type == config.ContentTypeSeries && ch.seriesStage != nil {
			ch.seriesStage.Preactivate()
		}

		// Send TopicMsg FIRST so the puzzle stage activates immediately
		// with the title + "today's puzzle" idle decoration visible to
		// viewers — they get a clean "preparing scene…" screen instead of
		// staring at a stale frame from the previous topic.
		send(buildTopicMsg(d, i, total))

		if d.topic.Type == config.ContentTypeSituationPuzzle && ch.puzzleStage != nil {
			t0 := time.Now()
			r.log.Info("puzzle asset prep starting", "channel", ch.def.ID, "id", d.id)
			preparePuzzleAssets(r.ctx, r.log, &debateEnv, ch, d, orch)
			r.log.Info("puzzle asset prep done",
				"channel", ch.def.ID, "id", d.id,
				"elapsed", time.Since(t0).Round(time.Millisecond))
		}
		if d.topic.Type == config.ContentTypeDiscussion && ch.discussionStage != nil {
			t0 := time.Now()
			r.log.Info("discussion asset prep starting", "channel", ch.def.ID, "id", d.id)
			prepareDiscussionAssets(r.ctx, r.log, &debateEnv, ch, d, orch)
			r.log.Info("discussion asset prep done",
				"channel", ch.def.ID, "id", d.id,
				"elapsed", time.Since(t0).Round(time.Millisecond))
		}
		if d.topic.Type == config.ContentTypeSeries && ch.seriesStage != nil {
			t0 := time.Now()
			r.log.Info("series asset prep starting", "channel", ch.def.ID, "id", d.id)
			prepareSeriesAssets(r.ctx, r.log, &debateEnv, ch, d, orch)
			r.log.Info("series asset prep done",
				"channel", ch.def.ID, "id", d.id,
				"elapsed", time.Since(t0).Round(time.Millisecond))
		}

		r.log.Info("orchestrator run starting",
			"channel", ch.def.ID, "id", d.id, "type", d.topic.Type)
		runErr := orch.Run(r.ctx)
		r.log.Info("orchestrator run returned",
			"channel", ch.def.ID, "id", d.id, "type", d.topic.Type, "err", runErr)
		// Series episodes archive their per-run output (script, audio,
		// subtitles) into the persistent show directory so the next
		// episode's recap engine can read them. Best-effort — failure
		// here doesn't fail the run.
		if d.topic.Type == config.ContentTypeSeries {
			r.log.Info("series episode finished — preparing handoff",
				"channel", ch.def.ID, "id", d.id,
				"completed_index", i+1, "next_index", i+2, "of", total)
			t0 := time.Now()
			finishSeriesEpisode(r.log, &debateEnv, d)
			r.log.Info("series finish complete",
				"channel", ch.def.ID, "id", d.id,
				"elapsed", time.Since(t0).Round(time.Millisecond))
			// Inter-episode breathing room. orch.Run already drained
			// the audio (Pipeline.waitAudioDrained), but back-to-back
			// title cards read as a hard cut on a narrated drama.
			// Park the stage on the series fallback plate (no caption,
			// no scene image) and hold for a few seconds so the
			// audience reads "episode just ended" before the next
			// title slides in.
			if ch.seriesStage != nil {
				ch.seriesStage.PostEpisodeIdle()
				r.log.Info("series stage parked on intermission",
					"channel", ch.def.ID, "id", d.id)
			}
			r.log.Info("series inter-episode gap holding",
				"channel", ch.def.ID, "id", d.id, "duration", seriesEpisodeGap)
			select {
			case <-r.ctx.Done():
				r.log.Info("series inter-episode gap cancelled by ctx",
					"channel", ch.def.ID, "id", d.id)
				orch.Shutdown()
				return
			case <-time.After(seriesEpisodeGap):
			}
			r.log.Info("series inter-episode gap done",
				"channel", ch.def.ID, "id", d.id)
		}
		r.log.Info("orchestrator shutting down",
			"channel", ch.def.ID, "id", d.id)
		orch.Shutdown()
		r.log.Info("orchestrator shutdown complete",
			"channel", ch.def.ID, "id", d.id)
		r.sessions.SetCurrentOrch(ch.def.ID, "", nil)
		// Release the loadedDebates entry now that the debate has reached
		// a terminal state (Done or Error). onRemovedFile keeps the entry
		// alive for non-pending debates so a re-create-during-run is a
		// dedupe (no duplicate -2 entry); once the debate ends we let the
		// path be re-queued so the user can re-run via delete + add.
		r.loadedMu.Lock()
		delete(r.loadedDebates, d.path)
		r.loadedMu.Unlock()

		if runErr != nil {
			r.log.Error("debate finished with error",
				"channel", ch.def.ID, "id", d.id, "err", runErr)
			r.sessions.SetDebateStatus(ch.def.ID, d.id, server.StatusError)
			if r.ctx.Err() != nil {
				return
			}
			continue
		}
		r.sessions.SetDebateStatus(ch.def.ID, d.id, server.StatusDone)
		r.sessions.SetDebateOutputs(ch.def.ID, d.id,
			filepath.Join(debateEnv.OutDir, "transcript.txt"),
			filepath.Join(debateEnv.OutDir, "debate.mp3"))
		fmt.Fprintf(os.Stdout, "✓ ch %d [%s] finished debate %d/%d — %s\n",
			ch.def.Number, ch.def.ID, i+1, total, d.title)
		if d.topic.Type == config.ContentTypeSeries {
			r.log.Info("series channel ready for next episode",
				"channel", ch.def.ID, "completed_id", d.id,
				"completed_index", i+1, "next_index", i+2, "of", total)
		}
	}
}

// onWatchedFile is invoked by the directory watcher when a .md file has
// settled in a watched directory. We load the topic, validate its channel
// id, queue it, surface it on /api/topics, and broadcast TopicsChangedMsg
// so connected browsers refresh the channel list.
func (r *runtime) onWatchedFile(path string) {
	abs, err := filepath.Abs(path)
	if err != nil {
		r.log.Warn("watch: abs path", "path", path, "err", err)
		return
	}

	r.loadedMu.Lock()
	if _, dup := r.loadedDebates[abs]; dup {
		r.loadedMu.Unlock()
		return
	}
	d, err := r.loadDebateLocked(abs)
	if err != nil {
		r.loadedMu.Unlock()
		r.log.Warn("watch: load failed", "path", abs, "err", err)
		fmt.Fprintf(os.Stderr, "watch: failed to load %s: %v\n", abs, err)
		return
	}
	r.loadedMu.Unlock()

	cr := r.channelByID[d.topic.Channel]
	if cr == nil {
		r.log.Warn("watch: unknown channel — skipping",
			"path", abs, "channel", d.topic.Channel)
		fmt.Fprintf(os.Stderr,
			"watch: %s references unknown channel %q — skipping\n",
			abs, d.topic.Channel)
		return
	}
	if cr.live == nil {
		// Shouldn't happen: every configured channel pre-initialises infra
		// at bootstrap. Bail out loudly so a misconfig is noticed.
		r.log.Warn("watch: channel has no streaming infra — skipping",
			"path", abs, "channel", d.topic.Channel)
		fmt.Fprintf(os.Stderr,
			"watch: channel %q has no streaming infra — skipping %s\n",
			d.topic.Channel, abs)
		return
	}

	debateOut := filepath.Join(r.env.OutDir, cr.def.ID, d.id)
	sess := server.Session{
		ID:     d.id,
		Title:  d.title,
		Status: server.StatusPending,
		DBPath: filepath.Join(debateOut, "session.db"),
	}
	if !r.sessions.AppendChannelDebate(cr.def.ID, sess) {
		r.log.Warn("watch: session already registered — skipping",
			"channel", cr.def.ID, "id", d.id)
		return
	}
	if !cr.queue.Push(d) {
		r.log.Warn("watch: queue closed — skipping",
			"channel", cr.def.ID, "id", d.id)
		return
	}
	cr.counterMu.Lock()
	cr.total++
	cr.counterMu.Unlock()

	// Record the ref AFTER successful queueing so onRemovedFile knows what
	// to drop. Doing this before the registry append would orphan the entry
	// if the append failed (id collision).
	r.loadedMu.Lock()
	r.loadedDebates[abs] = loadedRef{channelID: cr.def.ID, debateID: d.id}
	r.loadedMu.Unlock()

	r.log.Info("watch: queued new debate",
		"channel", cr.def.ID, "id", d.id, "path", abs, "title", d.title)
	fmt.Fprintf(os.Stdout, "+ ch %d [%s] queued new debate — %s (%s)\n",
		cr.def.Number, cr.def.ID, d.title, d.id)

	// Tell every connected SSE client the channel list changed so the web
	// UI re-renders the queue without the viewer having to refresh.
	r.bus.Publish(contentcreator.TopicsChangedMsg{})
}

// onRemovedFile drops a pending debate when its underlying .md file
// disappears from a watched directory. Only Pending entries are removable —
// a Running debate keeps airing (yanking its metadata mid-stream would leave
// the UI inconsistent), and Done/Error stay as history. The map entry is
// always cleared so re-creating the same filename re-queues cleanly.
func (r *runtime) onRemovedFile(path string) {
	abs, err := filepath.Abs(path)
	if err != nil {
		r.log.Warn("watch: abs path on remove", "path", path, "err", err)
		return
	}

	r.loadedMu.Lock()
	ref, ok := r.loadedDebates[abs]
	if !ok {
		r.loadedMu.Unlock()
		// Surfaced at debug-only: the watcher fires OnRemove for every
		// .md path that disappears from a watched dir, but we only act on
		// paths that are actually queued. A miss here is normal (e.g.
		// the user deleted a file that was never registered, or the
		// matching debate already finished and runChannel released the
		// path) — log it so it's visible if the user is troubleshooting
		// "I deleted X but nothing happened".
		r.log.Debug("watch: remove for unknown path — ignoring", "path", abs)
		return
	}
	r.loadedMu.Unlock()

	cr := r.channelByID[ref.channelID]
	if cr == nil {
		r.log.Warn("watch: removed file references unknown channel",
			"path", abs, "channel", ref.channelID)
		return
	}

	status, removed := r.sessions.RemoveChannelDebate(ref.channelID, ref.debateID)
	if !removed {
		// The debate is already airing (Running) or finished (Done/Error).
		// Yanking the metadata mid-stream would leave the UI inconsistent,
		// and rewriting history is wrong — so leave the entry in place.
		// IMPORTANT: keep the loadedDebates entry too. If we cleared it
		// here, a re-add of the same filename (e.g. the user removes the
		// on-air file by accident and drops it back in) would dedup-miss
		// and queue a *duplicate* with a -2 id. Keeping the entry makes
		// the re-add a no-op until the running debate finishes and
		// runChannel releases the path itself.
		r.log.Info("watch: ignoring delete (debate not pending)",
			"channel", ref.channelID, "id", ref.debateID, "status", string(status))
		fmt.Fprintf(os.Stdout,
			"- ch %d [%s] file deleted but debate is %s — leaving in place (%s)\n",
			cr.def.Number, cr.def.ID, status, ref.debateID)
		return
	}
	// Removal succeeded — drop the loadedDebates entry so re-creating the
	// same filename queues a fresh debate (rather than getting deduped).
	r.loadedMu.Lock()
	delete(r.loadedDebates, abs)
	r.loadedMu.Unlock()
	cr.queue.Remove(ref.debateID)
	cr.counterMu.Lock()
	if cr.total > 0 {
		cr.total--
	}
	cr.counterMu.Unlock()

	r.log.Info("watch: removed pending debate",
		"channel", ref.channelID, "id", ref.debateID, "path", abs)
	fmt.Fprintf(os.Stdout, "- ch %d [%s] removed pending debate — %s\n",
		cr.def.Number, cr.def.ID, ref.debateID)

	r.bus.Publish(contentcreator.TopicsChangedMsg{})
}

func agentNames(specs []config.AgentSpec) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.Name
	}
	return out
}

// buildTopicMsg shapes the per-content-type TopicMsg the video stage consumes.
// Dispatches to the type-specific builder in puzzle.go / debate.go.
func buildTopicMsg(d loadedDebate, index, total int) contentcreator.TopicMsg {
	msg := contentcreator.TopicMsg{
		ID:    d.id,
		Title: d.title,
		Type:  d.topic.Type,
		Index: index,
		Total: total,
	}
	if d.topic.Type == config.ContentTypeSituationPuzzle {
		return buildPuzzleTopicMsg(d, msg)
	}
	if d.topic.Type == config.ContentTypeSeries {
		return buildSeriesTopicMsg(d, msg)
	}
	if d.topic.Type == config.ContentTypeDiscussion {
		return buildDiscussionTopicMsg(d, msg)
	}
	return buildDebateTopicMsg(d, msg)
}

// serverCmd: HTTP server hosting the web UI + per-channel HLS video + audio
// (modeStream) OR a job-driven /api/jobs upload-and-render flow that yields
// a downloadable .mp4 (modeVideo). Default is modeStream so existing
// invocations keep working unchanged.
func serverCmd(args []string) int {
	specs, rest, deprecated := extractContentArgs(args)
	if deprecated.debate {
		fmt.Fprintln(os.Stderr, "warning: --debate is deprecated, use --content")
	}
	if deprecated.topic {
		fmt.Fprintln(os.Stderr, "warning: --topic is deprecated, use --content")
	}
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	fs.Var((*stringSlice)(&specs), "content", "path or glob to topic .md file(s) — repeatable; consecutive paths after one --content are also accepted")
	mode := fs.String("mode", modeStream, "stream|video|dashboard — stream (default) airs HLS channels; video accepts uploaded scripts and renders downloadable mp4s; dashboard is the API backend for the Next.js dashboard (same job pipeline, no embedded SPA)")
	channelsPath := fs.String("channel", "./channels.json", "path to channels.json — array of {id, number, title} channel definitions")
	mcpPath := fs.String("mcp", "", "path to mcp.json (optional)")
	outDir := fs.String("out", "", "output directory (overrides OUT_DIR)")
	addr := fs.String("addr", ":3000", "HTTP listen address")
	maxConcurrency := fs.Int("max-concurrency", 2, "video mode: maximum number of video generations to run concurrently")
	password := fs.String("password", os.Getenv("APP_PASSWORD"), "if set, gate the web UI + API behind this password (falls back to APP_PASSWORD env)")
	audio := fs.Bool("audio", false, "video/dashboard mode: force every job to render as an audio-only feed (mp3 + subtitles, no images/video), regardless of the per-request audio_only flag")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if *maxConcurrency < 1 {
		fmt.Fprintln(os.Stderr, "--max-concurrency must be >= 1")
		return 2
	}
	switch *mode {
	case modeStream:
		if *maxConcurrency != 2 {
			fmt.Fprintln(os.Stderr, "warning: --max-concurrency is ignored unless --mode=video")
		}
		if *audio {
			fmt.Fprintln(os.Stderr, "warning: --audio is ignored unless --mode=video or --mode=dashboard")
		}
		return serverCmdStream(specs, *channelsPath, *mcpPath, *outDir, *addr, *password)
	case modeVideo, modeDashboard:
		if len(specs) > 0 {
			fmt.Fprintf(os.Stderr, "warning: --content is ignored in --mode=%s (scripts come from the API)\n", *mode)
		}
		return serverCmdVideo(*mode, *mcpPath, *outDir, *addr, *maxConcurrency, *password, *audio)
	default:
		fmt.Fprintf(os.Stderr, "unknown --mode %q (want stream|video|dashboard)\n", *mode)
		return 2
	}
}

// serverCmdStream runs the long-running TV-channel mode: per-channel
// encoders, fsnotify watching for new topic .md files, browser-side TV
// tuner UI.
func serverCmdStream(specs []string, channelsPath, mcpPath, outDir, addr, password string) int {
	if len(specs) == 0 {
		fmt.Fprintln(os.Stderr, "missing --content")
		return 2
	}

	rt, code := bootstrap(channelsPath, specs, mcpPath, outDir, addr, password)
	if code != 0 {
		return code
	}
	defer rt.shutdown()

	// Auto-watch the directories implied by --debate. As more spec-bearing
	// flags are added (e.g. a future --story for a different content type),
	// concatenate watchDirsFromSpecs(...) of each one here so anything
	// dropped into the same folder gets picked up automatically without
	// new flag plumbing. Validation is fail-fast: a typo'd folder yields a
	// clear startup error rather than a silently-disabled watcher.
	dirs := watchDirsFromSpecs(specs)
	validDirs := make([]string, 0, len(dirs))
	for _, abs := range dirs {
		info, err := os.Stat(abs)
		if err != nil {
			fmt.Fprintln(os.Stderr, "watch:", err)
			return 1
		}
		if !info.IsDir() {
			fmt.Fprintf(os.Stderr, "watch: %s is not a directory\n", abs)
			return 1
		}
		validDirs = append(validDirs, abs)
	}
	if len(validDirs) > 0 {
		w, err := watcher.New(validDirs, 250*time.Millisecond, rt.log, watcher.Callbacks{
			OnReady:  rt.onWatchedFile,
			OnRemove: rt.onRemovedFile,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "watch:", err)
			return 1
		}
		go func() {
			if err := w.Run(rt.ctx); err != nil && rt.ctx.Err() == nil {
				rt.log.Warn("watcher exited", "err", err)
			}
		}()
	}

	srvErrCh := make(chan error, 1)
	go func() {
		srvErrCh <- rt.srv.ListenAndServe(rt.ctx, rt.addr, func(a *net.TCPAddr) {
			fmt.Fprintln(os.Stdout, "server listening at http://"+a.String())
		})
	}()

	// Channel goroutines never exit voluntarily (the queue stays open across
	// the process lifetime so dropped-in debates can still be picked up
	// after the initial seed drains). Wait on signal / ctx cancellation
	// only, and let rt.run() unwind naturally during shutdown.
	go func() { _ = rt.run() }()
	<-rt.ctx.Done()
	rt.cancel()
	<-srvErrCh
	return 0
}

// serverCmdVideo runs the upload-and-render mode: no channels, no
// fsnotify watcher, no preloaded topics. The HTTP server stays up
// waiting for /api/jobs requests; each one runs end-to-end in
// internal/content_creator/video_job.go and writes its artefacts under
// <session>/jobs/<jobID>/.
func serverCmdVideo(mode, mcpPath, outDir, addr string, maxConcurrency int, password string, forceAudio bool) int {
	rt, code := bootstrapVideo(mode, mcpPath, outDir, addr, maxConcurrency, password, forceAudio)
	if code != 0 {
		return code
	}
	defer rt.shutdown()

	banner := "video mode — upload script.md via the web UI"
	if mode == modeDashboard {
		banner = "dashboard mode — API backend for the Next.js dashboard"
	}
	if forceAudio {
		banner += " · audio-only feed forced (--audio)"
	}
	srvErrCh := make(chan error, 1)
	go func() {
		srvErrCh <- rt.srv.ListenAndServe(rt.ctx, rt.addr, func(a *net.TCPAddr) {
			fmt.Fprintln(os.Stdout, "server listening at http://"+a.String()+
				"  ("+banner+")")
		})
	}()

	<-rt.ctx.Done()
	rt.cancel()
	<-srvErrCh
	return 0
}
