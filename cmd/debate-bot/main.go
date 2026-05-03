package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sirily11/debate-bot/internal/audio"
	"github.com/sirily11/debate-bot/internal/config"
	"github.com/sirily11/debate-bot/internal/debate"
	"github.com/sirily11/debate-bot/internal/tui"
	"github.com/sirily11/debate-bot/internal/util"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "run":
		os.Exit(runCmd(os.Args[2:]))
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
  debate-bot run --topic ./topic.md [--mcp ./mcp.json] [--out ./out]

env (loaded from .env if present):
  OPENAI_BASE_URL   OPENAI_API_KEY   HOST_MODEL
  COMPRESSION_BASE_URL   COMPRESSION_API_KEY   COMPRESSION_MODEL
  AZURE_SPEECH_KEY   AZURE_SPEECH_REGION
  OUT_DIR (optional, default ./out)`)
}

func runCmd(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	topicPath := fs.String("topic", "", "path to topic.md (required)")
	mcpPath := fs.String("mcp", "", "path to mcp.json (optional)")
	outDir := fs.String("out", "", "output directory (overrides OUT_DIR)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *topicPath == "" {
		fmt.Fprintln(os.Stderr, "missing --topic")
		return 2
	}

	if err := audio.VerifyTools(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	env, err := config.LoadEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, "env:", err)
		return 1
	}
	if *outDir != "" {
		env.OutDir = *outDir
	}
	if err := debate.EnsureOutDir(env.OutDir); err != nil {
		fmt.Fprintln(os.Stderr, "out dir:", err)
		return 1
	}

	topic, err := config.LoadTopic(*topicPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "topic:", err)
		return 1
	}
	mcpCfg, err := config.LoadMCPConfig(*mcpPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mcp:", err)
		return 1
	}

	log, closer, err := util.NewFileLogger(env.OutDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "logger init warning:", err)
	}
	if closer != nil {
		defer closer.Close()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)
		<-c
		cancel()
	}()

	userIn := make(chan string, 16)
	model := tui.NewModel(userIn)
	prog, wait := tui.Run(model)

	send := func(m any) {
		if msg, ok := m.(tea.Msg); ok {
			prog.Send(msg)
		} else {
			prog.Send(tea.Msg(m))
		}
	}

	orch, err := debate.New(env, topic, mcpCfg, send, log)
	if err != nil {
		fmt.Fprintln(os.Stderr, "orchestrator:", err)
		return 1
	}

	// Drain user input from TUI into orchestrator queue.
	go func() {
		for s := range userIn {
			orch.PushUserMessage(s)
		}
	}()

	// Run orchestrator concurrently with TUI.
	orchDone := make(chan error, 1)
	go func() {
		orchDone <- orch.Run(ctx)
	}()

	if err := wait(); err != nil {
		log.Error("tui error", "err", err)
	}
	cancel()
	orch.Shutdown()
	if err := <-orchDone; err != nil {
		log.Error("orchestrator finished with error", "err", err)
	}
	return 0
}
