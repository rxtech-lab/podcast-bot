package mq

import (
	"context"
	"testing"
	"time"
)

func TestInlineClient(t *testing.T) {
	testClientSuite(t, func(t *testing.T) Client {
		c := NewInline()
		t.Cleanup(func() { c.Close() })
		return c
	})
}

func TestInlineBuffersBeforeRegistration(t *testing.T) {
	c := NewInline()
	defer c.Close()
	task, _ := NewTask(TaskSummary, "early", nil)
	if err := c.Publish(context.Background(), QueueDocs, task); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	got := make(chan Task, 1)
	if err := c.RegisterHandler(QueueDocs, 1, func(ctx context.Context, task Task) error {
		got <- task
		return nil
	}); err != nil {
		t.Fatalf("RegisterHandler: %v", err)
	}
	select {
	case received := <-got:
		if received.Key != "early" {
			t.Fatalf("unexpected task: %+v", received)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("buffered task never delivered")
	}
}
