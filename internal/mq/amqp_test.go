package mq

import (
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// Real-broker tests. Skipped unless RABBITMQ_TEST_URL is set (same pattern
// as the POSTGRES_TEST_DATABASE_URL-gated store tests). Locally:
//
//	brew services start rabbitmq
//	RABBITMQ_TEST_URL=amqp://guest:guest@127.0.0.1:5672/ go test ./internal/mq/
func amqpTestURL(t *testing.T) string {
	url := os.Getenv("RABBITMQ_TEST_URL")
	if url == "" {
		t.Skip("RABBITMQ_TEST_URL not set; skipping real-broker tests")
	}
	return url
}

var testRunSeq atomic.Int64

func newTestAMQP(t *testing.T) Client {
	url := amqpTestURL(t)
	prefix := fmt.Sprintf("test-%d-%d-", time.Now().UnixNano(), testRunSeq.Add(1))
	c, err := NewAMQP(url, AMQPOptions{
		QueuePrefix: prefix,
		Logf:        t.Logf,
		exitFn:      func(code int) { t.Errorf("unexpected exit(%d)", code) },
	})
	if err != nil {
		t.Fatalf("NewAMQP: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestAMQPClient(t *testing.T) {
	testClientSuite(t, newTestAMQP)
}

func TestAMQPTopologyIdempotent(t *testing.T) {
	url := amqpTestURL(t)
	prefix := fmt.Sprintf("test-idem-%d-", time.Now().UnixNano())
	for i := 0; i < 2; i++ {
		c, err := NewAMQP(url, AMQPOptions{QueuePrefix: prefix, Logf: t.Logf})
		if err != nil {
			t.Fatalf("NewAMQP round %d: %v", i+1, err)
		}
		if err := c.Connected(t.Context()); err != nil {
			t.Fatalf("Connected round %d: %v", i+1, err)
		}
		if err := c.Close(); err != nil {
			t.Fatalf("Close round %d: %v", i+1, err)
		}
	}
}
