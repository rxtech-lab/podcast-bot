package contentcreator

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/sirily11/debate-bot/internal/agent"
	"github.com/sirily11/debate-bot/internal/config"
)

// newTestCommander builds a minimal silent-director Commander. Its LLM/memory
// deps are nil — fine here because startDiscussionDirector only stores the
// agent and starts a loop that sleeps before its first (never-reached) tick.
func newTestCommander() *agent.Commander {
	base := agent.NewBase("Commander", agent.RoleCommander, nil, nil, nil, nil, nil)
	return agent.NewCommander(base, "Test Discussion", nil)
}

func newDirectorTestOrchestrator(disableImages bool) *Orchestrator {
	return &Orchestrator{
		Topic:         &config.DebateTopic{Type: config.ContentTypeDiscussion},
		Registry:      &agent.Registry{Commander: newTestCommander()},
		Send:          func(any) {},
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		disableImages: disableImages,
	}
}

// TestStartDiscussionDirector_AudioOnlySuppressesImagegen proves that when an
// audio-only feed sets disableImages, the discussion director is wired with a
// nil image client — so it can never call imagegen during Run — even when an
// API key IS available (the suppression flag, not a missing key, is the cause).
// maybeGenerate then short-circuits on img == nil, so no provider call happens.
func TestStartDiscussionDirector_AudioOnlySuppressesImagegen(t *testing.T) {
	t.Setenv("AI_GATEWAY_API_KEY", "test-key") // imagegen.New would otherwise succeed

	o := newDirectorTestOrchestrator(true)
	ctx, cancel := context.WithCancel(context.Background())
	o.startDiscussionDirector(ctx)
	cancel() // stop the Run goroutine before its first tick

	if o.discussionDirector == nil {
		t.Fatal("expected a discussion director to be started")
	}
	if o.discussionDirector.img != nil {
		t.Fatal("audio-only: director image client must be nil (no imagegen)")
	}
	// maybeGenerate must report "did not generate" with a nil image client,
	// confirming the image path is dead even if a cue asks for a background.
	if o.discussionDirector.maybeGenerate(ctx, "a calm background") {
		t.Fatal("audio-only: maybeGenerate must not start image generation")
	}
}

// TestStartDiscussionDirector_VideoWiresImagegen is the contrast case: with
// images enabled and a key present, the director gets a real image client.
// Together with the audio-only test this proves disableImages is what gates it.
func TestStartDiscussionDirector_VideoWiresImagegen(t *testing.T) {
	t.Setenv("AI_GATEWAY_API_KEY", "test-key")

	o := newDirectorTestOrchestrator(false)
	ctx, cancel := context.WithCancel(context.Background())
	o.startDiscussionDirector(ctx)
	cancel()

	if o.discussionDirector == nil {
		t.Fatal("expected a discussion director to be started")
	}
	if o.discussionDirector.img == nil {
		t.Fatal("video mode with a key: director image client should be non-nil")
	}
}
