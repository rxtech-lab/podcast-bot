package mq

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testClientSuite exercises the Client semantics shared by the AMQP and
// inline implementations.
func testClientSuite(t *testing.T, newClient func(t *testing.T) Client) {
	t.Run("publish roundtrip", func(t *testing.T) {
		c := newClient(t)
		got := make(chan Task, 1)
		if err := c.RegisterHandler(QueueDocs, 1, func(ctx context.Context, task Task) error {
			got <- task
			return nil
		}); err != nil {
			t.Fatalf("RegisterHandler: %v", err)
		}
		task, err := NewTask(TaskSummary, "disc-1", map[string]string{"discussion_id": "disc-1"})
		if err != nil {
			t.Fatalf("NewTask: %v", err)
		}
		if err := c.Publish(context.Background(), QueueDocs, task); err != nil {
			t.Fatalf("Publish: %v", err)
		}
		select {
		case received := <-got:
			if received.Type != TaskSummary || received.Key != "disc-1" || received.Attempt != 1 {
				t.Fatalf("unexpected task: %+v", received)
			}
			var payload map[string]string
			if err := json.Unmarshal(received.Payload, &payload); err != nil || payload["discussion_id"] != "disc-1" {
				t.Fatalf("payload mismatch: %s (err=%v)", received.Payload, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for delivery")
		}
	})

	t.Run("retry re-enters after delay", func(t *testing.T) {
		c := newClient(t)
		got := make(chan Task, 1)
		if err := c.RegisterHandler(QueuePlanning, 1, func(ctx context.Context, task Task) error {
			got <- task
			return nil
		}); err != nil {
			t.Fatalf("RegisterHandler: %v", err)
		}
		task, _ := NewTask(TaskPlanningTurn, "run-1", nil)
		task.Attempt = 2
		start := time.Now()
		const delay = 300 * time.Millisecond
		if err := c.PublishRetry(context.Background(), QueuePlanning, task, delay); err != nil {
			t.Fatalf("PublishRetry: %v", err)
		}
		select {
		case received := <-got:
			if elapsed := time.Since(start); elapsed < delay {
				t.Fatalf("delivered after %s, before the %s backoff", elapsed, delay)
			}
			if received.Attempt != 2 {
				t.Fatalf("attempt = %d, want 2", received.Attempt)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("timed out waiting for retry delivery")
		}
	})

	t.Run("prefetch bounds concurrency", func(t *testing.T) {
		c := newClient(t)
		const prefetch = 2
		const total = 5
		var inFlight, peak atomic.Int32
		release := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(total)
		if err := c.RegisterHandler(QueueGeneration, prefetch, func(ctx context.Context, task Task) error {
			defer wg.Done()
			n := inFlight.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			<-release
			inFlight.Add(-1)
			return nil
		}); err != nil {
			t.Fatalf("RegisterHandler: %v", err)
		}
		for i := 0; i < total; i++ {
			task, _ := NewTask(TaskPodcastGenerate, "job", nil)
			if err := c.Publish(context.Background(), QueueGeneration, task); err != nil {
				t.Fatalf("Publish: %v", err)
			}
		}
		// Let deliveries reach the handler and saturate the prefetch window.
		deadline := time.Now().Add(10 * time.Second)
		for inFlight.Load() < prefetch && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if got := inFlight.Load(); got != prefetch {
			t.Fatalf("in-flight = %d, want %d", got, prefetch)
		}
		close(release)
		wg.Wait()
		if p := peak.Load(); p > prefetch {
			t.Fatalf("peak concurrency %d exceeded prefetch %d", p, prefetch)
		}
	})

	t.Run("handler error consumes exactly once", func(t *testing.T) {
		c := newClient(t)
		var calls atomic.Int32
		if err := c.RegisterHandler(QueueDocs, 1, func(ctx context.Context, task Task) error {
			calls.Add(1)
			return context.DeadlineExceeded
		}); err != nil {
			t.Fatalf("RegisterHandler: %v", err)
		}
		task, _ := NewTask(TaskMindmap, "disc-2", nil)
		if err := c.Publish(context.Background(), QueueDocs, task); err != nil {
			t.Fatalf("Publish: %v", err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for calls.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		// A broker would redeliver within this window if the message were
		// nacked/unacked; the always-ack contract forbids a second call.
		time.Sleep(500 * time.Millisecond)
		if got := calls.Load(); got != 1 {
			t.Fatalf("handler called %d times, want exactly 1", got)
		}
	})
}

func TestComputeRetryDelay(t *testing.T) {
	cases := []struct {
		nextAttempt int
		want        time.Duration
	}{
		{2, 60 * time.Second},
		{3, 120 * time.Second},
		{4, 240 * time.Second},
		{8, 600 * time.Second}, // capped
	}
	for _, tc := range cases {
		if got := ComputeRetryDelay(tc.nextAttempt); got != tc.want {
			t.Errorf("ComputeRetryDelay(%d) = %s, want %s", tc.nextAttempt, got, tc.want)
		}
	}
}

func TestPermanent(t *testing.T) {
	if IsPermanent(context.DeadlineExceeded) {
		t.Fatal("plain error reported permanent")
	}
	err := Permanent(context.DeadlineExceeded)
	if !IsPermanent(err) {
		t.Fatal("Permanent error not detected")
	}
	if Permanent(nil) != nil {
		t.Fatal("Permanent(nil) should be nil")
	}
}
