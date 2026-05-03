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

  run     starts the TUI and an embedded HTTP server; the TUI consumes the
          server over loopback. Audio plays via local ffplay.
  server  starts only the HTTP server (no TUI, no local audio playback) so the
          web UI and remote clients can connect.

env (loaded from .env if present):
  OPENAI_BASE_URL   OPENAI_API_KEY   HOST_MODEL
  COMPRESSION_BASE_URL   COMPRESSION_API_KEY   COMPRESSION_MODEL
  AZURE_SPEECH_KEY   AZURE_SPEECH_REGION
  OUT_DIR (optional, default ./out)`)
}

// runtime bundles everything bootstrap() prepares for the two subcommands.
type runtime struct {
	ctx    context.Context
	cancel context.CancelFunc
	log    *slog.Logger
	closer interface{ Close() error }

	env     *config.Env
	bus     *eventbus.Bus
	live    *audio.LiveStream
	enc     *video.Encoder // may be nil if encoder failed to start
	orch    *debate.Orchestrator
	srv     *server.Server
	addr    string
	stopSig chan os.Signal
}

// bootstrap loads config, sets up the event bus, livestream, orchestrator and
// HTTP server. Shared by runCmd and serverCmd.
func bootstrap(topicPath, mcpPath, outOverride, addr string) (*runtime, int) {
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
	fmt.Fprintln(os.Stderr, "session output:", env.OutDir)

	topic, err := config.LoadTopic(topicPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "topic:", err)
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

	live, err := audio.NewLiveStream(ctx, log)
	if err != nil {
		cancel()
		fmt.Fprintln(os.Stderr, "livestream:", err)
		return nil, 1
	}

	bus := eventbus.New(log)

	orch, err := debate.New(env, topic, mcpCfg, bus.Publish, log, live)
	if err != nil {
		cancel()
		fmt.Fprintln(os.Stderr, "orchestrator:", err)
		return nil, 1
	}

	// Video encoder: optional. On failure (no font, ffmpeg missing the libx264
	// build, etc.) we log and continue without video — the chat-only UI still
	// works.
	var (
		enc      *video.Encoder
		hlsDir   string
	)
	enc, err = video.New(ctx, env.OutDir, log)
	if err != nil {
		log.Warn("video encoder disabled", "err", err)
		fmt.Fprintln(os.Stderr, "video disabled:", err)
	} else {
		hlsDir = enc.HLSDir()
		enc.AttachAudio(ctx, live)
		stage := video.NewStage(enc, topic.Title)
		go stage.Run(ctx, bus)
	}

	srv := server.New(server.Deps{
		Bus:         bus,
		LiveStream:  live,
		Transcript:  orch.Transcript,
		PushUser:    orch.PushUserMessage,
		Log:         log,
		VideoHLSDir: hlsDir,
	})

	rt := &runtime{
		ctx: ctx, cancel: cancel, log: log, closer: closer,
		env: env, bus: bus, live: live, enc: enc, orch: orch, srv: srv,
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

// runCmd: TUI + embedded server (loopback).
func runCmd(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	topicPath := fs.String("topic", "", "path to topic.md (required)")
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
	fmt.Fprintln(os.Stderr, "server listening at", baseURL)

	orchDone := make(chan error, 1)
	go func() {
		orchDone <- rt.orch.Run(rt.ctx)
	}()

	cliErr := runTUIClient(rt.ctx, rt.log, baseURL)
	rt.cancel()
	rt.orch.Shutdown()
	if err := <-orchDone; err != nil {
		rt.log.Error("orchestrator finished with error", "err", err)
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
	topicPath := fs.String("topic", "", "path to topic.md (required)")
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
			fmt.Fprintln(os.Stderr, "server listening at http://"+a.String())
		})
	}()

	orchDone := make(chan error, 1)
	go func() {
		orchDone <- rt.orch.Run(rt.ctx)
	}()

	select {
	case err := <-orchDone:
		if err != nil {
			rt.log.Error("orchestrator finished with error", "err", err)
		}
		// Give the server a moment to flush pending writes (final EndedMsg, last
		// audio bytes), then shut down.
		time.Sleep(500 * time.Millisecond)
	case <-rt.ctx.Done():
	}
	rt.cancel()
	rt.orch.Shutdown()
	<-srvErrCh
	return 0
}
