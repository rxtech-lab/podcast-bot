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
	case "run":
		os.Exit(runCmd(os.Args[2:]))
	case "server":
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
	fmt.Fprintln(os.Stderr, `debate-bot — multi-agent debate podcast

usage:
  debate-bot run    --topic ./topic.md [--mcp ./mcp.json] [--out ./out] [--addr 127.0.0.1:0]
  debate-bot server --topic ./topic.md [--mcp ./mcp.json] [--out ./out] [--addr :3000]

  --topic accepts a single .md path OR a glob (e.g. "topics/*.md"). When the
  glob matches multiple files the topics are queued and run sequentially —
  the audio livestream and HLS video are reused across topics; the title
  swaps when each new topic starts.

  run     starts the TUI and an embedded HTTP server; the TUI consumes the
          server over loopback. Audio plays via local ffplay.
  server  starts only the HTTP server (no TUI, no local audio playback) so the
          web UI and remote clients can connect.

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
	topic *config.Topic
}

// runtime bundles the shared infrastructure that every queued topic reuses.
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
	addr     string
	stopSig  chan os.Signal
}

// resolveTopics expands a literal path or glob into one or more topic files
// (deduped, sorted). Each resulting Topic gets a URL-safe id derived from its
// filename; collisions are suffixed with -2, -3, ...
func resolveTopics(spec string) ([]loadedTopic, error) {
	matches, err := filepath.Glob(spec)
	if err != nil {
		return nil, fmt.Errorf("topic glob %q: %w", spec, err)
	}
	if len(matches) == 0 {
		// Fall back to a literal path so users get a clearer error from
		// LoadTopic when the file simply doesn't exist.
		matches = []string{spec}
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

var slugRe = regexp.MustCompile(`[^a-z0-9_-]+`)

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugRe.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

// bootstrap loads config, sets up the event bus, livestream, video encoder
// and HTTP server. It does NOT build any orchestrators — those are built
// per-topic by runQueue.
func bootstrap(topicSpec, mcpPath, outOverride, addr string) (*runtime, int) {
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

	topics, err := resolveTopics(topicSpec)
	if err != nil {
		fmt.Fprintln(os.Stderr, "topic:", err)
		return nil, 1
	}
	fmt.Fprintf(os.Stdout, "found %d topic(s) matching %q:\n", len(topics), topicSpec)
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

	log.Info("topic queue resolved", "spec", topicSpec, "count", len(topics))
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

	live, err := audio.NewLiveStream(ctx, log)
	if err != nil {
		cancel()
		fmt.Fprintln(os.Stderr, "livestream:", err)
		return nil, 1
	}

	bus := eventbus.New(log)

	// Video encoder + Stage live for the whole session and span topics. The
	// Stage subscribes to TopicMsg events to swap title and rosters between
	// runs.
	var (
		enc    *video.Encoder
		hlsDir string
	)
	enc, err = video.New(ctx, env.OutDir, log)
	if err != nil {
		log.Warn("video encoder disabled", "err", err)
		fmt.Fprintln(os.Stderr, "video disabled:", err)
	} else {
		hlsDir = enc.HLSDir()
		enc.AttachAudio(ctx, live)
		stage := video.NewStage(enc)
		go stage.Run(ctx, bus)
	}

	// Seed the session registry with the topic queue.
	seed := make([]server.Session, len(topics))
	for i, t := range topics {
		seed[i] = server.Session{ID: t.id, Title: t.title, Status: server.StatusPending}
	}
	sessions := server.NewSessionRegistry(seed)

	srv := server.New(server.Deps{
		Bus:         bus,
		LiveStream:  live,
		Sessions:    sessions,
		Log:         log,
		VideoHLSDir: hlsDir,
	})

	rt := &runtime{
		ctx: ctx, cancel: cancel, log: log, closer: closer,
		env: env, mcpCfg: mcpCfg, bus: bus, live: live, enc: enc, srv: srv,
		sessions: sessions, topics: topics,
		addr: addr, stopSig: stopSig,
	}
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
	if r.bus != nil {
		r.bus.Close()
	}
	if r.closer != nil {
		_ = r.closer.Close()
	}
}

// runQueue plays each queued topic to completion in order. The shared bus,
// livestream, encoder and server stay up across topics; only the
// orchestrator (and its OutDir, agents, transcript) is rebuilt per topic.
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

		orch, err := debate.New(&topicEnv, lt.topic, r.mcpCfg, r.bus.Publish, r.log, r.live)
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
		fmt.Fprintf(os.Stdout, "▶ starting topic %d/%d [%s] %s\n",
			i+1, len(r.topics), lt.id, lt.title)

		// Announce the new topic so the Stage updates the encoder title +
		// side panels and the web UI clears its transcript view.
		affNames := agentNames(lt.topic.Affirmative)
		negNames := agentNames(lt.topic.Negative)
		r.bus.Publish(debate.TopicMsg{
			ID:       lt.id,
			Title:    lt.title,
			Index:    i,
			Total:    len(r.topics),
			AffNames: affNames,
			NegNames: negNames,
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
		fmt.Fprintf(os.Stdout, "✓ finished topic %d/%d [%s]\n",
			i+1, len(r.topics), lt.id)
	}
	return nil
}

func agentNames(specs []config.AgentSpec) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.Name
	}
	return out
}

// runCmd: TUI + embedded server (loopback).
func runCmd(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	topicPath := fs.String("topic", "", "path or glob to topic .md file(s) (required)")
	mcpPath := fs.String("mcp", "", "path to mcp.json (optional)")
	outDir := fs.String("out", "", "output directory (overrides OUT_DIR)")
	addr := fs.String("addr", "127.0.0.1:0", "loopback HTTP listen address")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *topicPath == "" {
		fmt.Fprintln(os.Stderr, "missing --topic")
		return 2
	}

	rt, code := bootstrap(*topicPath, *mcpPath, *outDir, *addr)
	if code != 0 {
		return code
	}
	defer rt.shutdown()

	addrCh := make(chan *net.TCPAddr, 1)
	srvErrCh := make(chan error, 1)
	go func() {
		srvErrCh <- rt.srv.ListenAndServe(rt.ctx, rt.addr, func(a *net.TCPAddr) {
			addrCh <- a
		})
	}()

	bound := <-addrCh
	baseURL := fmt.Sprintf("http://%s", bound.String())
	fmt.Fprintln(os.Stdout, "server listening at", baseURL)

	queueDone := make(chan error, 1)
	go func() {
		queueDone <- rt.runQueue()
	}()

	cliErr := runTUIClient(rt.ctx, rt.log, baseURL)
	rt.cancel()
	if err := <-queueDone; err != nil {
		rt.log.Error("topic queue error", "err", err)
	}
	<-srvErrCh
	if cliErr != nil {
		rt.log.Error("tui error", "err", cliErr)
	}
	return 0
}

// serverCmd: headless HTTP server only (no TUI, no local audio playback).
func serverCmd(args []string) int {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	topicPath := fs.String("topic", "", "path or glob to topic .md file(s) (required)")
	mcpPath := fs.String("mcp", "", "path to mcp.json (optional)")
	outDir := fs.String("out", "", "output directory (overrides OUT_DIR)")
	addr := fs.String("addr", ":3000", "HTTP listen address")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *topicPath == "" {
		fmt.Fprintln(os.Stderr, "missing --topic")
		return 2
	}

	rt, code := bootstrap(*topicPath, *mcpPath, *outDir, *addr)
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
		queueDone <- rt.runQueue()
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
