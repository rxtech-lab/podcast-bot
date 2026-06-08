---
slug: code/internal/eventbus
title: Package internal/eventbus
description: Auto-generated go doc reference for the internal/eventbus package.
---

# Package `internal/eventbus`

_Generated with `go doc -all ./internal/eventbus`. Regenerate with `scripts/gen_go_docs.sh`._

```text
package eventbus // import "github.com/sirily11/debate-bot/internal/eventbus"

Package eventbus is a tiny in-memory pub/sub for debate orchestrator events.

One Bus is created per debate run. The orchestrator publishes typed events
(TranscriptMsg, TickMsg, PhaseMsg, etc.) and any number of subscribers (TUI SSE
bridge, HTTP /api/events handler, web clients) receive them.

Late subscribers do not see past events — history is fetched separately via
the transcript snapshot. Slow subscribers do not block the publisher: each
subscription has a buffered channel and overflow is dropped (logged).

TYPES

type Bus struct {
	// Has unexported fields.
}
    Bus fans out published values to every active subscriber.

func New(log *slog.Logger) *Bus
    New constructs an empty Bus. log is optional; nil disables drop logs.

func (b *Bus) Close()
    Close releases all subscriber channels. Subsequent Publish calls are no-ops.

func (b *Bus) Publish(v any)
    Publish broadcasts v to every current subscriber. Non-blocking per
    subscriber: if a subscriber's buffer is full the message is dropped for that
    one only.

func (b *Bus) Subscribe(bufSize int) (<-chan any, func())
    Subscribe registers a new subscriber and returns the receive channel and a
    cancel func. bufSize controls the per-subscriber buffer (use 64+ for chatty
    streams). Always call cancel when done to release the channel.
```
