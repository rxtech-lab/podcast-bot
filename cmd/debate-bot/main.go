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
	"syscall"
	"time"

	"github.com/sirily11/debate-bot/internal/audio"
	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/debate"
	"github.com/sirily11/debate-bot/internal/eventbus"
	"github.com/sirily11/debate-bot/internal/server"
	"github.com/sirily11/debate-bot/internal/util"
	"github.com/sirily11/debate-bot/internal/video"
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
	fmt.Fprintln(os.Stderr, `debate-bot — multi-agent debate podcast (TV-channel mode)

usage:
  debate-bot server --channel ./channels.json --debate "./topics/*.md" \
                    [--mcp ./mcp.json] [--out ./out] [--addr :3000]

  --channel  path to channels.json — array of {id, number, title} channel
             definitions. Each debate.md frontmatter must declare a `+"`channel`"+`
             field whose value matches one of these ids.
  --debate   path or glob to debate .md file(s) — repeatable; consecutive
             paths after one --debate are also accepted.

  Channels run in parallel as independent video + audio streams. Multiple
  debates assigned to the same channel are queued and play sequentially
  inside that channel. A channel defined in channels.json with no debates
  is listed but renders as "off air" in the web UI.

  --topic is a deprecated alias for --debate (still works, prints a warning).
  `+"`run`"+` is kept as an alias for `+"`server`"+` for backwards compatibility.

env (loaded from .env if present):
  OPENAI_BASE_URL   OPENAI_API_KEY   HOST_MODEL
  COMPRESSION_BASE_URL   COMPRESSION_API_KEY   COMPRESSION_MODEL
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

// channelRuntime is the per-channel slice of audio/video infrastructure.
// live and enc are nil for off-air channels (no debates assigned).
type channelRuntime struct {
	def     config.Channel
	debates []loadedDebate
	live    *audio.LiveStream
	enc     *video.Encoder
}

// runtime owns every cross-channel resource: the shared event bus, server,
// session registry, and the per-channel encoders/livestreams.
type runtime struct {
	ctx    context.Context
	cancel context.CancelFunc
	log    *slog.Logger
	closer interface{ Close() error }

	env      *config.Env
	mcpCfg   *config.MCPConfig
	bus      *eventbus.Bus
	srv      *server.Server
	sessions *server.SessionRegistry
	channels []*channelRuntime
	addr     string
	stopSig  chan os.Signal
}

// resolveDebates expands a list of literal paths or globs into the queue of
// debate files (deduped, sorted). Each resulting debate gets a URL-safe id
// derived from its filename; collisions are suffixed with -2, -3, ...
func resolveDebates(specs []string) ([]loadedDebate, error) {
	if len(specs) == 0 {
		return nil, fmt.Errorf("no debate paths provided")
	}
	seen := map[string]bool{}
	var matches []string
	for _, spec := range specs {
		ms, err := filepath.Glob(spec)
		if err != nil {
			return nil, fmt.Errorf("debate glob %q: %w", spec, err)
		}
		if len(ms) == 0 {
			ms = []string{spec}
		}
		for _, m := range ms {
			if seen[m] {
				continue
			}
			seen[m] = true
			matches = append(matches, m)
		}
	}
	sort.Strings(matches)

	used := map[string]int{}
	out := make([]loadedDebate, 0, len(matches))
	for _, p := range matches {
		t, err := config.LoadTopic(p)
		if err != nil {
			return nil, fmt.Errorf("debate %s: %w", p, err)
		}
		base := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		id := slugify(base)
		if id == "" {
			id = "debate"
		}
		used[id]++
		if used[id] > 1 {
			id = fmt.Sprintf("%s-%d", id, used[id])
		}
		out = append(out, loadedDebate{id: id, path: p, title: t.Title, topic: t})
	}
	return out, nil
}

// stringSlice satisfies flag.Value so --debate can be supplied multiple times.
type stringSlice []string

func (s *stringSlice) String() string     { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error { *s = append(*s, v); return nil }

// extractDebateArgs hoists every --debate (and the deprecated --topic alias)
// occurrence out of args so the stdlib flag parser doesn't trip on the trailing
// values an unquoted shell glob produces.
func extractDebateArgs(args []string) (debates []string, rest []string, usedDeprecated bool) {
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--debate" || a == "-debate":
			i++
			for i < len(args) && !strings.HasPrefix(args[i], "-") {
				debates = append(debates, args[i])
				i++
			}
			i--
		case strings.HasPrefix(a, "--debate="):
			debates = append(debates, strings.TrimPrefix(a, "--debate="))
		case strings.HasPrefix(a, "-debate="):
			debates = append(debates, strings.TrimPrefix(a, "-debate="))
		case a == "--topic" || a == "-topic":
			usedDeprecated = true
			i++
			for i < len(args) && !strings.HasPrefix(args[i], "-") {
				debates = append(debates, args[i])
				i++
			}
			i--
		case strings.HasPrefix(a, "--topic="):
			usedDeprecated = true
			debates = append(debates, strings.TrimPrefix(a, "--topic="))
		case strings.HasPrefix(a, "-topic="):
			usedDeprecated = true
			debates = append(debates, strings.TrimPrefix(a, "-topic="))
		default:
			rest = append(rest, a)
		}
	}
	return
}

var slugRe = regexp.MustCompile(`[^a-z0-9_-]+`)

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugRe.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// bootstrap loads env, channels.json, every debate.md, validates the channel
// references, and stands up the per-channel infrastructure. Each
// channel.json entry becomes a channelRuntime; channels with no debates
// assigned get nil live/enc and are surfaced as "off air" in the UI.
func bootstrap(channelsPath string, debateSpecs []string, mcpPath, outOverride, addr string) (*runtime, int) {
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
	if err := debate.EnsureOutDir(env.OutDir); err != nil {
		fmt.Fprintln(os.Stderr, "out dir:", err)
		return nil, 1
	}
	fmt.Fprintln(os.Stdout, "session output:", env.OutDir)

	channelsCfg, err := config.LoadChannels(channelsPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "channels:", err)
		return nil, 1
	}

	debates, err := resolveDebates(debateSpecs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "debate:", err)
		return nil, 1
	}

	// Validate each debate's channel id against channels.json.
	for _, d := range debates {
		if channelsCfg.Find(d.topic.Channel) == nil {
			fmt.Fprintf(os.Stderr,
				"debate %s references unknown channel %q (channels.json defines: %s)\n",
				d.path, d.topic.Channel, strings.Join(channelIDs(channelsCfg), ", "))
			return nil, 1
		}
	}

	// Group debates by channel id, preserving sorted-by-filename order within
	// each group.
	byChannel := map[string][]loadedDebate{}
	for _, d := range debates {
		byChannel[d.topic.Channel] = append(byChannel[d.topic.Channel], d)
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

	fmt.Fprintf(os.Stdout, "loaded %d channel(s) and %d debate(s):\n",
		len(channelsCfg.Channels), len(debates))
	for _, ch := range channelsCfg.Channels {
		queue := byChannel[ch.ID]
		if len(queue) == 0 {
			fmt.Fprintf(os.Stdout, "  ch %d [%s] %s — off air (no debates)\n",
				ch.Number, ch.ID, ch.Title)
			log.Info("channel off air", "id", ch.ID, "number", ch.Number, "title", ch.Title)
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
		env: env, mcpCfg: mcpCfg, bus: bus, sessions: sessions,
		addr: addr, stopSig: stopSig,
	}

	// Build per-channel infrastructure. Channels without debates skip the
	// LiveStream + Encoder so we don't burn an ffmpeg process for a stream no
	// one will watch — they're still registered with the server (off-air) so
	// the UI lists them.
	for _, ch := range channelsCfg.Channels {
		queue := byChannel[ch.ID]
		cr := &channelRuntime{def: ch, debates: queue}

		var hlsDir string
		if len(queue) > 0 {
			channelOutDir := filepath.Join(env.OutDir, ch.ID)
			if err := debate.EnsureOutDir(channelOutDir); err != nil {
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

			// All debates on a channel share its encoder; pick the resolution
			// from the first debate and warn on mismatches so it's obvious why
			// later debates didn't change the size.
			res := video.Resolution(queue[0].topic.Resolution)
			for _, d := range queue[1:] {
				if d.topic.Resolution != queue[0].topic.Resolution {
					log.Warn("debate resolution mismatch — using first debate's value",
						"channel", ch.ID,
						"using", queue[0].topic.Resolution,
						"ignored", d.topic.Resolution,
						"debate", d.id)
				}
			}
			enc, err := video.New(ctx, channelOutDir, res, log)
			if err != nil {
				log.Warn("video encoder disabled for channel", "id", ch.ID, "err", err)
				fmt.Fprintln(os.Stderr, "video disabled for", ch.ID, ":", err)
			} else {
				cr.enc = enc
				hlsDir = enc.HLSDir()
				enc.AttachAudio(ctx, live)
				stage := video.NewChannelStage(enc, ch.ID)
				go stage.Run(ctx, bus)
			}
		}

		rt.channels = append(rt.channels, cr)
		sessions.RegisterChannel(ch.ID, ch.Number, ch.Title, hlsDir, cr.live)

		// Seed the per-channel queue (all pending). Status flips to running
		// when each debate's orchestrator starts inside runChannels. DBPath
		// is set up-front so the server can serve transcripts from disk
		// even before that debate's orchestrator boots (the file is created
		// lazily; an absent file is treated as "no transcript yet").
		seed := make([]server.Session, len(queue))
		for i, d := range queue {
			debateOut := filepath.Join(env.OutDir, ch.ID, d.id)
			seed[i] = server.Session{
				ID:     d.id,
				Title:  d.title,
				Status: server.StatusPending,
				DBPath: filepath.Join(debateOut, "session.db"),
			}
		}
		sessions.SeedChannelDebates(ch.ID, seed)
	}

	rt.srv = server.New(server.Deps{
		Bus:      bus,
		Sessions: sessions,
		Log:      log,
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
}

// channelSend wraps the shared bus.Publish so events emitted by an
// orchestrator are stamped with the channel id before they hit the bus.
func (r *runtime) channelSend(channelID string) func(any) {
	return func(v any) {
		r.bus.Publish(debate.StampChannelID(v, channelID))
	}
}

// run starts every channel's queue concurrently. Returns when every channel
// has finished its queue (or the context is cancelled).
func (r *runtime) run() error {
	doneCh := make(chan struct{}, len(r.channels))
	active := 0
	for _, ch := range r.channels {
		if len(ch.debates) == 0 || ch.live == nil {
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

// runChannel plays this channel's queue of debates sequentially. All channels
// are driven by independent goroutines so they progress in parallel.
func (r *runtime) runChannel(ch *channelRuntime) {
	send := r.channelSend(ch.def.ID)
	total := len(ch.debates)
	for i, d := range ch.debates {
		if r.ctx.Err() != nil {
			return
		}

		debateEnv := *r.env
		debateEnv.OutDir = filepath.Join(r.env.OutDir, ch.def.ID, d.id)
		if err := debate.EnsureOutDir(debateEnv.OutDir); err != nil {
			r.log.Error("debate out dir", "channel", ch.def.ID, "id", d.id, "err", err)
			r.sessions.SetDebateStatus(ch.def.ID, d.id, server.StatusError)
			continue
		}

		orch, err := debate.New(&debateEnv, d.topic, r.mcpCfg, send, r.log, ch.live)
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

		send(debate.TopicMsg{
			ID:       d.id,
			Title:    d.title,
			Index:    i,
			Total:    total,
			AffNames: agentNames(d.topic.Affirmative),
			NegNames: agentNames(d.topic.Negative),
		})

		runErr := orch.Run(r.ctx)
		orch.Shutdown()
		r.sessions.SetCurrentOrch(ch.def.ID, "", nil)

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
	}
}

func agentNames(specs []config.AgentSpec) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.Name
	}
	return out
}

// serverCmd: HTTP server hosting the web UI + per-channel HLS video + audio.
func serverCmd(args []string) int {
	specs, rest, deprecated := extractDebateArgs(args)
	if deprecated {
		fmt.Fprintln(os.Stderr, "warning: --topic is deprecated, use --debate")
	}
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	fs.Var((*stringSlice)(&specs), "debate", "path or glob to debate .md file(s) — repeatable; consecutive paths after one --debate are also accepted")
	channelsPath := fs.String("channel", "./channels.json", "path to channels.json — array of {id, number, title} channel definitions")
	mcpPath := fs.String("mcp", "", "path to mcp.json (optional)")
	outDir := fs.String("out", "", "output directory (overrides OUT_DIR)")
	addr := fs.String("addr", ":3000", "HTTP listen address")
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	if len(specs) == 0 {
		fmt.Fprintln(os.Stderr, "missing --debate")
		return 2
	}

	rt, code := bootstrap(*channelsPath, specs, *mcpPath, *outDir, *addr)
	if code != 0 {
		return code
	}
	defer rt.shutdown()

	srvErrCh := make(chan error, 1)
	go func() {
		srvErrCh <- rt.srv.ListenAndServe(rt.ctx, rt.addr, func(a *net.TCPAddr) {
			fmt.Fprintln(os.Stdout, "server listening at http://"+a.String())
		})
	}()

	queueDone := make(chan error, 1)
	go func() {
		queueDone <- rt.run()
	}()

	select {
	case err := <-queueDone:
		if err != nil {
			rt.log.Error("channels run error", "err", err)
		}
		// Give the server a moment to flush pending writes (final EndedMsg,
		// last audio bytes), then shut down.
		time.Sleep(500 * time.Millisecond)
	case <-rt.ctx.Done():
	}
	rt.cancel()
	<-srvErrCh
	return 0
}
