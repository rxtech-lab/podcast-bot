package mq

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// InlineClient is the broker-free fallback used when RABBITMQ_URL is empty:
// the same Client semantics (unbounded queue, bounded concurrency per queue,
// delayed retry re-entry) implemented with goroutines. It preserves the
// zero-infra local dev experience the old in-memory goqueue pool provided —
// minus durability, which only a real broker gives.
type InlineClient struct {
	mu       sync.Mutex
	queues   map[string]*inlineQueue
	closed   bool
	inflight sync.WaitGroup
	timers   map[*time.Timer]struct{}
}

type inlineQueue struct {
	handler Handler
	slots   chan struct{} // semaphore sized to prefetch
	pending []Task        // tasks published before the handler registered
}

// NewInline returns an in-process Client.
func NewInline() *InlineClient {
	return &InlineClient{
		queues: make(map[string]*inlineQueue),
		timers: make(map[*time.Timer]struct{}),
	}
}

func (c *InlineClient) queue(name string) *inlineQueue {
	q, ok := c.queues[name]
	if !ok {
		q = &inlineQueue{}
		c.queues[name] = q
	}
	return q
}

// Publish dispatches the task to the queue's handler, waiting for a
// concurrency slot in a background goroutine (publish never blocks).
// Tasks published before RegisterHandler are buffered.
func (c *InlineClient) Publish(ctx context.Context, queueName string, t Task) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("mq: inline client closed")
	}
	q := c.queue(queueName)
	if q.handler == nil {
		q.pending = append(q.pending, t)
		return nil
	}
	c.dispatchLocked(q, t)
	return nil
}

// dispatchLocked launches the handler once a semaphore slot frees up.
// Caller holds c.mu.
func (c *InlineClient) dispatchLocked(q *inlineQueue, t Task) {
	c.inflight.Add(1)
	go func() {
		defer c.inflight.Done()
		q.slots <- struct{}{}
		defer func() { <-q.slots }()
		defer func() {
			if r := recover(); r != nil {
				// Match the AMQP consumer: a panicking handler is logged
				// and the message considered consumed.
				fmt.Printf("[mq] inline handler panic (%s key=%s): %v\n", t.Type, t.Key, r)
			}
		}()
		// Handler errors were already routed to retry/terminal by the
		// dispatch layer, mirroring the always-ack AMQP consumer.
		_ = q.handler(context.Background(), t)
	}()
}

// PublishRetry re-enqueues the task after delay.
func (c *InlineClient) PublishRetry(ctx context.Context, queueName string, t Task, delay time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("mq: inline client closed")
	}
	var timer *time.Timer
	timer = time.AfterFunc(delay, func() {
		c.mu.Lock()
		delete(c.timers, timer)
		c.mu.Unlock()
		_ = c.Publish(context.Background(), queueName, t)
	})
	c.timers[timer] = struct{}{}
	return nil
}

// RegisterHandler installs the queue's handler with a concurrency bound of
// prefetch and drains any tasks published before registration.
func (c *InlineClient) RegisterHandler(queueName string, prefetch int, h Handler) error {
	if prefetch < 1 {
		prefetch = 1
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	q := c.queue(queueName)
	if q.handler != nil {
		return fmt.Errorf("mq: handler already registered for %s", queueName)
	}
	q.handler = h
	q.slots = make(chan struct{}, prefetch)
	for _, t := range q.pending {
		c.dispatchLocked(q, t)
	}
	q.pending = nil
	return nil
}

// Connected always succeeds: there is no broker to lose.
func (c *InlineClient) Connected(ctx context.Context) error { return nil }

// Close stops accepting work, cancels scheduled retries, and waits briefly
// for in-flight handlers.
func (c *InlineClient) Close() error {
	c.mu.Lock()
	c.closed = true
	for timer := range c.timers {
		timer.Stop()
	}
	c.timers = map[*time.Timer]struct{}{}
	c.mu.Unlock()

	done := make(chan struct{})
	go func() {
		c.inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
	}
	return nil
}
