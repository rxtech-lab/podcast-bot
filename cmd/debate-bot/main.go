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
	fmt.Fprintln(os.Stderr, `debate-bot — multi-agent debate podcast (web UI)

usage:
  debate-bot server --debate ./debate.md [--mcp ./mcp.json] [--out ./out] [--addr :3000]

  --debate accepts a single .md path OR a glob (e.g. "topics/*.md"). When the
  glob matches multiple files the debates are queued and run sequentially —
  the audio livestream and HLS video are reused across debates; the title
  swaps when each new debate starts.

  Adding "parallel: true" to a debate.md frontmatter switches the whole queue
  into TV-channel mode: every debate runs concurrently as its own channel,
  each with an independent video + audio stream. Switch channels in the web
  UI's left sidebar.

  --topic is a deprecated alias for --debate (still works, prints a warning).
  `+"`run`"+` is kept as an alias for `+"`server`"+` for backwards compatibility.

env (loaded from .env if present):
  OPENAI_BASE_URL   OPENAI_API_KEY   HOST_MODEL
  COMPRESSION_BASE_URL   COMPRESSION_API_KEY   COMPRESSION_MODEL
  AZURE_SPEECH_KEY   AZURE_SPEECH_REGION   (required when tts_provider=azure)
  ELEVENLABS_API_KEY                        (required when tts_provider=eleven)
  OUT_DIR (optional, default ./out)`)
}

// loadedTopic is one resolved entry in the multi-topic queue.
type loadedTopic struct {
	id    string
	path  string
	title string
	topic *config.DebateTopic
}

// channelRuntime is the per-channel slice of audio/video infrastructure used
// in parallel mode. In sequential mode there's exactly one of these, shared
// across the queue (set on runtime.live / runtime.enc).
type channelRuntime struct {
	id   string
	live *audio.LiveStream
	enc  *video.Encoder // may be nil if encoder failed to start
}

// runtime bundles the shared infrastructure that every queued debate reuses.
//
// In sequential mode `live` and `enc` hold the single shared streams and
// `channels` is empty. In parallel mode `live`/`enc` are nil and one entry per
// debate lives in `channels`.
type runtime struct {
	ctx    context.Context
	cancel context.CancelFunc
	log    *slog.Logger
	closer interface{ Close() error }

	env      *config.Env
	mcpCfg   *config.MCPConfig
	bus      *eventbus.Bus
	live     *audio.LiveStream
	enc      *video.Encoder // may be nil if encoder failed to start
	srv      *server.Server
	sessions *server.SessionRegistry
	topics   []loadedTopic
	channels []*channelRuntime // populated in parallel mode only
	parallel bool
	addr     string
	stopSig  chan os.Signal
}

// resolveTopics expands a list of literal paths or globs into the queue of
// topic files (deduped, sorted). Each resulting Topic gets a URL-safe id
// derived from its filename; collisions are suffixed with -2, -3, ...
//
// Each spec is glob-expanded; specs without metacharacters fall back to a
// literal lookup so users get a clear "file not found" from LoadTopic.
func resolveTopics(specs []string) ([]loadedTopic, error) {
	if len(specs) == 0 {
		return nil, fmt.Errorf("no topic paths provided")
	}
	seen := map[string]bool{}
	var matches []string
	for _, spec := range specs {
		ms, err := filepath.Glob(spec)
		if err != nil {
			return nil, fmt.Errorf("topic glob %q: %w", spec, err)
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
	out := make([]loadedTopic, 0, len(matches))
	for _, p := range matches {
		t, err := config.LoadTopic(p)
		if err != nil {
			return nil, fmt.Errorf("topic %s: %w", p, err)
		}
		base := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		id := slugify(base)
		if id == "" {
			id = "topic"
		}
		used[id]++
		if used[id] > 1 {
			id = fmt.Sprintf("%s-%d", id, used[id])
		}
		out = append(out, loadedTopic{id: id, path: p, title: t.Title, topic: t})
	}
	return out, nil
}

// stringSlice satisfies flag.Value so --topic can be supplied multiple times.
type stringSlice []string

func (s *stringSlice) String() string     { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error { *s = append(*s, v); return nil }

// extractDebateArgs hoists every --debate (and the deprecated --topic alias)
// occurrence out of args so the stdlib flag parser doesn't trip on the trailing
// values an unquoted shell glob produces. Without this, `--debate ./topics/*.md
// --mcp x.json` (which the shell expands into `--debate a.md b.md c.md --mcp
// x.json`) would see `b.md` as the first positional arg and silently stop, so
// --mcp would be ignored.
//
// All non-flag tokens that immediately follow a --debate/--topic occurrence are
// collected as debate paths until the next flag (anything starting with -).
// usedDeprecated is set when the legacy --topic spelling appeared, so the
// caller can print a one-line deprecation notice.
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
			i-- // outer loop will increment
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

// bootstrap loads config, sets up the event bus, livestream, video encoder
// and HTTP server. It does NOT build any orchestrators — those are built
// per-topic by runQueue.
func bootstrap(topicSpecs []string, mcpPath, outOverride, addr string) (*runtime, int) {
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

	topics, err := resolveTopics(topicSpecs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "topic:", err)
		return nil, 1
	}
	fmt.Fprintf(os.Stdout, "found %d topic(s) matching %v:\n", len(topics), topicSpecs)
	for i, t := range topics {
		fmt.Fprintf(os.Stdout, "  [%d/%d] %s  (%s)  — %s\n",
			i+1, len(topics), t.id, t.path, t.title)
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

	log.Info("topic queue resolved", "specs", topicSpecs, "count", len(topics))
	for i, t := range topics {
		log.Info("queued topic",
			"index", i+1,
			"of", len(topics),
			"id", t.id,
			"path", t.path,
			"title", t.title,
		)
	}

	ctx, cancel := context.WithCancel(context.Background())
	stopSig := make(chan os.Signal, 1)
	signal.Notify(stopSig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stopSig
		cancel()
	}()

	bus := eventbus.New(log)

	// Parallel mode is opt-in via `parallel: true` on any debate.md in the
	// queue — when set, every debate runs concurrently as its own channel,
	// each with an independent LiveStream + Encoder + HLS dir.
	parallel := false
	for _, t := range topics {
		if t.topic.Parallel {
			parallel = true
			break
		}
	}

	rt := &runtime{
		ctx: ctx, cancel: cancel, log: log, closer: closer,
		env: env, mcpCfg: mcpCfg, bus: bus,
		topics: topics, parallel: parallel,
		addr: addr, stopSig: stopSig,
	}

	// Seed the session registry with the queue and the queue mode.
	seed := make([]server.Session, len(topics))
	for i, t := range topics {
		seed[i] = server.Session{ID: t.id, Title: t.title, Status: server.StatusPending}
	}
	mode := server.ModeSequential
	if parallel {
		mode = server.ModeParallel
	}
	sessions := server.NewSessionRegistry(seed, mode)
	rt.sessions = sessions

	if parallel {
		// One LiveStream + Encoder + ChannelStage per debate. Each Stage
		// filters bus events by channel id, so concurrent topics never bleed
		// each other's transcript / phase / topic state into the wrong stream.
		for _, lt := range topics {
			res := video.Resolution(lt.topic.Resolution)
			channelOutDir := filepath.Join(env.OutDir, lt.id)
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
			cr := &channelRuntime{id: lt.id, live: live}
			enc, err := video.New(ctx, channelOutDir, res, log)
			if err != nil {
				log.Warn("video encoder disabled for channel", "id", lt.id, "err", err)
				fmt.Fprintln(os.Stderr, "video disabled for", lt.id, ":", err)
			} else {
				cr.enc = enc
				enc.AttachAudio(ctx, live)
				stage := video.NewChannelStage(enc, lt.id)
				go stage.Run(ctx, bus)
			}
			rt.channels = append(rt.channels, cr)

			hlsDir := ""
			if cr.enc != nil {
				hlsDir = cr.enc.HLSDir()
			}
			sessions.RegisterChannel(lt.id, server.ChannelResources{
				HLSDir:     hlsDir,
				LiveStream: live,
			})
		}
	} else {
		// Sequential mode (today): one shared LiveStream + Encoder + Stage
		// span the whole queue.
		live, err := audio.NewLiveStream(ctx, log)
		if err != nil {
			cancel()
			fmt.Fprintln(os.Stderr, "livestream:", err)
			return nil, 1
		}
		rt.live = live

		// All queued debates share the same encoder, so pick the resolution
		// from the first debate. Mixed resolutions aren't supported — flag any
		// divergence so it's obvious why later debates didn't change the size.
		res := video.Resolution(topics[0].topic.Resolution)
		for _, t := range topics[1:] {
			if t.topic.Resolution != topics[0].topic.Resolution {
				log.Warn("topic resolution mismatch — using first topic's value",
					"using", topics[0].topic.Resolution,
					"ignored", t.topic.Resolution,
					"topic", t.id)
			}
		}
		enc, err := video.New(ctx, env.OutDir, res, log)
		if err != nil {
			log.Warn("video encoder disabled", "err", err)
			fmt.Fprintln(os.Stderr, "video disabled:", err)
		} else {
			rt.enc = enc
			enc.AttachAudio(ctx, live)
			stage := video.NewStage(enc)
			go stage.Run(ctx, bus)
		}
	}

	deps := server.Deps{
		Bus:        bus,
		LiveStream: rt.live,
		Sessions:   sessions,
		Log:        log,
	}
	if rt.enc != nil {
		deps.VideoHLSDir = rt.enc.HLSDir()
	}
	rt.srv = server.New(deps)

	return rt, 0
}

func (r *runtime) shutdown() {
	r.cancel()
	if r.enc != nil {
		_ = r.enc.Close()
	}
	if r.live != nil {
		_ = r.live.CloseInput()
	}
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

// run dispatches to the right execution mode.
func (r *runtime) run() error {
	if r.parallel {
		return r.runParallel()
	}
	return r.runQueue()
}

// channelSend wraps the shared bus.Publish so events emitted by an orchestrator
// for a given channel are stamped with that channel id before they hit the bus.
// In sequential mode the channel id is the active topic's id (so the frontend
// can still associate transcript events with a queue entry).
func (r *runtime) channelSend(channelID string) func(any) {
	return func(v any) {
		r.bus.Publish(debate.StampChannelID(v, channelID))
	}
}

// runQueue plays each queued debate to completion in order. The shared bus,
// livestream, encoder and server stay up across debates; only the
// orchestrator (and its OutDir, agents, transcript) is rebuilt per debate.
func (r *runtime) runQueue() error {
	for i, lt := range r.topics {
		if r.ctx.Err() != nil {
			return r.ctx.Err()
		}

		topicEnv := *r.env
		topicEnv.OutDir = filepath.Join(r.env.OutDir, lt.id)
		if err := debate.EnsureOutDir(topicEnv.OutDir); err != nil {
			r.log.Error("topic out dir", "id", lt.id, "err", err)
			r.sessions.SetStatus(lt.id, server.StatusError)
			continue
		}

		send := r.channelSend(lt.id)
		orch, err := debate.New(&topicEnv, lt.topic, r.mcpCfg, send, r.log, r.live)
		if err != nil {
			r.log.Error("orchestrator build", "id", lt.id, "err", err)
			r.sessions.SetStatus(lt.id, server.StatusError)
			continue
		}

		r.sessions.SetStatus(lt.id, server.StatusRunning)
		r.sessions.SetCurrent(orch)

		r.log.Info("starting topic",
			"index", i+1,
			"of", len(r.topics),
			"id", lt.id,
			"title", lt.title,
			"out_dir", topicEnv.OutDir,
		)
		fmt.Fprintf(os.Stdout, "▶ starting debate %d/%d [%s] %s\n",
			i+1, len(r.topics), lt.id, lt.title)

		// Announce the new topic so the Stage updates the encoder title +
		// side panels and the web UI clears its transcript view.
		send(debate.TopicMsg{
			ID:       lt.id,
			Title:    lt.title,
			Index:    i,
			Total:    len(r.topics),
			AffNames: agentNames(lt.topic.Affirmative),
			NegNames: agentNames(lt.topic.Negative),
		})

		err = orch.Run(r.ctx)
		orch.Shutdown()
		r.sessions.SetCurrent(nil)
		if err != nil {
			r.log.Error("orchestrator finished with error", "id", lt.id, "err", err)
			r.sessions.SetStatus(lt.id, server.StatusError)
			if r.ctx.Err() != nil {
				return err
			}
			continue
		}
		r.sessions.SetStatus(lt.id, server.StatusDone)
		r.sessions.SetOutputs(lt.id,
			filepath.Join(topicEnv.OutDir, "transcript.txt"),
			filepath.Join(topicEnv.OutDir, "debate.mp3"),
		)
		fmt.Fprintf(os.Stdout, "✓ finished debate %d/%d [%s]\n",
			i+1, len(r.topics), lt.id)
	}
	return nil
}

// runParallel starts every queued debate concurrently, each on its own
// LiveStream + Encoder + ChannelStage. Returns when every orchestrator has
// finished (or the context is cancelled).
func (r *runtime) runParallel() error {
	if len(r.channels) != len(r.topics) {
		return fmt.Errorf("runtime not initialised for parallel mode: %d channels for %d debates",
			len(r.channels), len(r.topics))
	}

	type result struct {
		id  string
		err error
	}
	resultCh := make(chan result, len(r.topics))

	for i, lt := range r.topics {
		i, lt := i, lt
		ch := r.channels[i]
		topicEnv := *r.env
		topicEnv.OutDir = filepath.Join(r.env.OutDir, lt.id)
		if err := debate.EnsureOutDir(topicEnv.OutDir); err != nil {
			r.log.Error("channel out dir", "id", lt.id, "err", err)
			r.sessions.SetStatus(lt.id, server.StatusError)
			resultCh <- result{id: lt.id, err: err}
			continue
		}

		send := r.channelSend(lt.id)
		orch, err := debate.New(&topicEnv, lt.topic, r.mcpCfg, send, r.log, ch.live)
		if err != nil {
			r.log.Error("orchestrator build", "id", lt.id, "err", err)
			r.sessions.SetStatus(lt.id, server.StatusError)
			resultCh <- result{id: lt.id, err: err}
			continue
		}

		// Expose the orch on the existing channel registration so the server's
		// /api/transcript?channel=X and POST /api/messages?channel=X find it.
		r.sessions.RegisterChannel(lt.id, server.ChannelResources{
			Orch:       orch,
			HLSDir:     existingHLSDir(r.sessions.ChannelResources(lt.id)),
			LiveStream: ch.live,
		})
		r.sessions.SetStatus(lt.id, server.StatusRunning)

		r.log.Info("starting parallel debate",
			"index", i+1, "of", len(r.topics),
			"id", lt.id, "title", lt.title,
			"out_dir", topicEnv.OutDir)
		fmt.Fprintf(os.Stdout, "▶ starting channel [%s] %s\n", lt.id, lt.title)

		send(debate.TopicMsg{
			ID:       lt.id,
			Title:    lt.title,
			Index:    i,
			Total:    len(r.topics),
			AffNames: agentNames(lt.topic.Affirmative),
			NegNames: agentNames(lt.topic.Negative),
		})

		go func() {
			runErr := orch.Run(r.ctx)
			orch.Shutdown()
			if runErr != nil {
				r.log.Error("channel finished with error", "id", lt.id, "err", runErr)
				r.sessions.SetStatus(lt.id, server.StatusError)
			} else {
				r.sessions.SetStatus(lt.id, server.StatusDone)
				r.sessions.SetOutputs(lt.id,
					filepath.Join(topicEnv.OutDir, "transcript.txt"),
					filepath.Join(topicEnv.OutDir, "debate.mp3"),
				)
				fmt.Fprintf(os.Stdout, "✓ finished channel [%s]\n", lt.id)
			}
			resultCh <- result{id: lt.id, err: runErr}
		}()
	}

	var firstErr error
	for range r.topics {
		res := <-resultCh
		if res.err != nil && firstErr == nil && r.ctx.Err() == nil {
			firstErr = res.err
		}
	}
	return firstErr
}

// existingHLSDir is a small helper so RegisterChannel calls can preserve the
// HLSDir installed at bootstrap time when only updating the orchestrator.
func existingHLSDir(prev *server.ChannelResources) string {
	if prev == nil {
		return ""
	}
	return prev.HLSDir
}

func agentNames(specs []config.AgentSpec) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.Name
	}
	return out
}

// serverCmd: HTTP server hosting the web UI + HLS video + audio stream.
func serverCmd(args []string) int {
	specs, rest, deprecated := extractDebateArgs(args)
	if deprecated {
		fmt.Fprintln(os.Stderr, "warning: --topic is deprecated, use --debate")
	}
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	fs.Var((*stringSlice)(&specs), "debate", "path or glob to debate .md file(s) — repeatable; consecutive paths after one --debate are also accepted")
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

	rt, code := bootstrap(specs, *mcpPath, *outDir, *addr)
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
			rt.log.Error("topic queue error", "err", err)
		}
		// Give the server a moment to flush pending writes (final EndedMsg, last
		// audio bytes), then shut down.
		time.Sleep(500 * time.Millisecond)
	case <-rt.ctx.Done():
	}
	rt.cancel()
	<-srvErrCh
	return 0
}
