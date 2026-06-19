package video

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/sirily11/debate-bot/internal/config"
	contentcreator "github.com/sirily11/debate-bot/internal/content_creator"
	"github.com/sirily11/debate-bot/internal/eventbus"
	"github.com/sirily11/debate-bot/internal/video/scenes"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// readPuzzleState snapshots the renderer fields that decide which template
// Frame() dispatches to: puzzleMode gates puzzle/series vs debate, and
// puzzleSceneName selects the discussion (qa) caption treatment.
func readPuzzleState(r *Renderer) (mode bool, sceneName, idleLabel string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.puzzleMode, r.puzzleSceneName, r.puzzleIdleLabel
}

// waitPuzzleMode polls the renderer until puzzleMode == want or the deadline
// passes. The stage processes bus events on its own goroutine, so the flip is
// asynchronous — we can't read it synchronously right after Publish.
func waitPuzzleMode(r *Renderer, want bool, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if mode, _, _ := readPuzzleState(r); mode == want {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	mode, _, _ := readPuzzleState(r)
	return mode == want
}

// publishUntil republishes msg until check() holds or the deadline passes.
// The bus drops events for subscribers that haven't registered yet (Run calls
// bus.Subscribe on its own goroutine), and the stages' handlers are idempotent,
// so re-publishing is the deterministic way to close the subscribe/publish race
// in a test. Production never hits this race — the orchestrator subscribes long
// before the first TopicMsg is sent.
func publishUntil(bus *eventbus.Bus, msg any, check func() bool, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		bus.Publish(msg)
		time.Sleep(5 * time.Millisecond)
		if check() {
			return true
		}
	}
	return check()
}

// TestDiscussionStageActivateSetsPuzzleMode is the direct unit check: calling
// activate() must flip the renderer into discussion render mode (puzzleMode +
// qa scene + the discussion idle pill). If this regresses, Frame() falls
// through to frameDebate and discussion content renders with the debate layout.
func TestDiscussionStageActivateSetsPuzzleMode(t *testing.T) {
	r := renderForTest(t)
	enc := &Encoder{rend: r}
	stage := NewDiscussionStage(enc)

	stage.activate()

	mode, sceneName, idle := readPuzzleState(r)
	if !mode {
		t.Fatalf("puzzleMode = false after activate; Frame() would dispatch to frameDebate (debate template)")
	}
	if sceneName != scenes.SceneQA {
		t.Fatalf("puzzleSceneName = %q, want %q (qa caption treatment)", sceneName, scenes.SceneQA)
	}
	if idle != "討論  ·  DISCUSSION" {
		t.Fatalf("idle label = %q, want the discussion pill", idle)
	}
}

// TestDiscussionStageStreamingPath reproduces the cmd/debate-bot streaming
// wiring end to end: a real bus, the debate + discussion channel stages both
// subscribed to the same channel-bound Encoder, and a discussion TopicMsg
// stamped with the channel id (exactly what runtime.channelSend publishes).
// The debate stage must go idle and the discussion stage must drive the
// renderer into discussion mode — i.e. the video must NOT use the debate
// template.
func TestDiscussionStageStreamingPath(t *testing.T) {
	r := renderForTest(t)
	enc := &Encoder{rend: r}

	log := discardLogger()
	bus := eventbus.New(log)
	defer bus.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const channelID = "discussions"
	// Mirror main.go: every content-type stage runs concurrently on the same
	// channel-bound encoder and self-gates on TopicMsg.Type.
	debateStage := NewDebateChannelStage(enc, channelID)
	discussionStage := NewDiscussionChannelStage(enc, channelID)
	go debateStage.Run(ctx, bus)
	go discussionStage.Run(ctx, bus)

	// Stamp the channel id the way runtime.channelSend does before publishing.
	topic := contentcreator.StampChannelID(contentcreator.TopicMsg{
		ID:          "disc-1",
		Title:       "AI 與創意的邊界",
		Type:        config.ContentTypeDiscussion,
		AffNames:    []string{"Alice", "Bob"},
		NegNames:    []string{"Host"},
		AffPosition: "一場關於人工智慧與人類創意的圓桌討論。",
	}, channelID)

	ok := publishUntil(bus, topic, func() bool {
		mode, _, _ := readPuzzleState(r)
		return mode
	}, time.Second)
	if !ok {
		mode, sceneName, idle := readPuzzleState(r)
		t.Fatalf("discussion TopicMsg did not enable discussion render mode: "+
			"puzzleMode=%v sceneName=%q idle=%q (Frame() would use the debate template)",
			mode, sceneName, idle)
	}
	if _, sceneName, _ := readPuzzleState(r); sceneName != scenes.SceneQA {
		t.Fatalf("puzzleSceneName = %q, want %q", sceneName, scenes.SceneQA)
	}
}

// TestDiscussionStageHandoffToDebate is the handoff control: after a discussion
// topic puts the renderer in discussion mode, a following debate topic on the
// same channel must put it back in debate mode. The DiscussionStage owns that
// reset (its idle() calls SetPuzzleMode(false)) — without it a debate that
// follows a discussion would inherit the discussion layout.
func TestDiscussionStageHandoffToDebate(t *testing.T) {
	r := renderForTest(t)
	enc := &Encoder{rend: r}

	bus := eventbus.New(discardLogger())
	defer bus.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const channelID = "tech"
	debateStage := NewDebateChannelStage(enc, channelID)
	discussionStage := NewDiscussionChannelStage(enc, channelID)
	go debateStage.Run(ctx, bus)
	go discussionStage.Run(ctx, bus)

	disc := contentcreator.StampChannelID(contentcreator.TopicMsg{
		ID: "disc-1", Title: "圓桌", Type: config.ContentTypeDiscussion,
	}, channelID)
	if !publishUntil(bus, disc, func() bool { m, _, _ := readPuzzleState(r); return m }, time.Second) {
		t.Fatalf("discussion topic never enabled puzzle mode (subscribe race not closed)")
	}

	// Now hand off to a debate topic — the discussion stage must idle and reset.
	deb := contentcreator.StampChannelID(contentcreator.TopicMsg{
		ID: "deb-1", Title: "遠端工作是不是未來的常態", Type: config.ContentTypeDebate,
	}, channelID)
	bus.Publish(deb)
	if !waitPuzzleMode(r, false, 500*time.Millisecond) {
		mode, sceneName, _ := readPuzzleState(r)
		t.Fatalf("debate topic after discussion did not reset puzzle mode: puzzleMode=%v sceneName=%q", mode, sceneName)
	}
}
