// Package eventbus is a tiny in-memory pub/sub for debate orchestrator events.
//
// One Bus is created per debate run. The orchestrator publishes typed events
// (TranscriptMsg, TickMsg, PhaseMsg, etc.) and any number of subscribers
// (TUI SSE bridge, HTTP /api/events handler, web clients) receive them.
//
// Late subscribers do not see past events — history is fetched separately via
// the transcript snapshot. Slow subscribers do not block the publisher: each
// subscription has a buffered channel and overflow is dropped (logged).
package eventbus

import (
	"log/slog"
	"sync"
	"sync/atomic"
)

// Bus fans out published values to every active subscriber.
type Bus struct {
	mu     sync.RWMutex
	subs   map[uint64]*subscriber
	nextID atomic.Uint64
	log    *slog.Logger
	closed bool
}

type subscriber struct {
	ch      chan any
	dropped atomic.Uint64
}

// New constructs an empty Bus. log is optional; nil disables drop logs.
func New(log *slog.Logger) *Bus {
	return &Bus{
		subs: map[uint64]*subscriber{},
		log:  log,
	}
}

// Publish broadcasts v to every current subscriber. Non-blocking per subscriber:
// if a subscriber's buffer is full the message is dropped for that one only.
func (b *Bus) Publish(v any) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return
	}
	for id, s := range b.subs {
		select {
		case s.ch <- v:
		default:
			n := s.dropped.Add(1)
			if b.log != nil && (n == 1 || n%50 == 0) {
				b.log.Warn("eventbus: subscriber dropping events", "sub", id, "dropped", n)
			}
		}
	}
}

// Subscribe registers a new subscriber and returns the receive channel and a
// cancel func. bufSize controls the per-subscriber buffer (use 64+ for chatty
// streams). Always call cancel when done to release the channel.
func (b *Bus) Subscribe(bufSize int) (<-chan any, func()) {
	if bufSize <= 0 {
		bufSize = 64
	}
	s := &subscriber{ch: make(chan any, bufSize)}
	id := b.nextID.Add(1)
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		close(s.ch)
		return s.ch, func() {}
	}
	b.subs[id] = s
	b.mu.Unlock()

	cancel := func() {
		b.mu.Lock()
		if cur, ok := b.subs[id]; ok && cur == s {
			delete(b.subs, id)
			close(s.ch)
		}
		b.mu.Unlock()
	}
	return s.ch, cancel
}

// Close releases all subscriber channels. Subsequent Publish calls are no-ops.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for _, s := range b.subs {
		close(s.ch)
	}
	b.subs = map[uint64]*subscriber{}
}
