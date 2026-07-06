package mq

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	reconnectMaxAttempts = 10
	reconnectBaseDelay   = time.Second
	reconnectMaxDelay    = 30 * time.Second
)

// AMQPOptions tunes an AMQP client.
type AMQPOptions struct {
	// QueuePrefix is prepended to every physical queue name (E2E per-run
	// isolation). Prefixed queues are declared with an idle expiry so they
	// garbage-collect on a persistent broker.
	QueuePrefix string
	// Logf receives connection lifecycle and delivery diagnostics.
	// Defaults to log via fmt to stderr when nil.
	Logf func(format string, args ...any)
	// exitFn is called after reconnect attempts are exhausted; defaults to
	// os.Exit(1) so the orchestrator restarts the pod. Overridable in tests.
	exitFn func(code int)
}

// AMQPClient is the RabbitMQ-backed Client.
type AMQPClient struct {
	url  string
	opts AMQPOptions

	mu        sync.Mutex
	conn      *amqp.Connection
	pubCh     *amqp.Channel
	consumers []consumerReg
	closing   bool

	inflight sync.WaitGroup
}

type consumerReg struct {
	queue    string
	prefetch int
	handler  Handler
}

// NewAMQP dials the broker, asserts the full topology, and returns a client
// that auto-reconnects (and re-registers consumers) on connection loss.
func NewAMQP(url string, opts AMQPOptions) (*AMQPClient, error) {
	if opts.Logf == nil {
		opts.Logf = func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, format+"\n", args...)
		}
	}
	if opts.exitFn == nil {
		opts.exitFn = os.Exit
	}
	c := &AMQPClient{url: url, opts: opts}
	if err := c.connect(); err != nil {
		return nil, err
	}
	return c, nil
}

// physical maps a logical queue name to its broker name.
func (c *AMQPClient) physical(queue string) string {
	return c.opts.QueuePrefix + queue
}

func (c *AMQPClient) retryQueue(queue string) string {
	return c.physical(queue) + ".retry"
}

// connect dials, asserts topology, and installs the close watcher.
// Callers must not hold c.mu.
func (c *AMQPClient) connect() error {
	conn, err := amqp.Dial(c.url)
	if err != nil {
		return fmt.Errorf("mq: dial %s: %w", redactAMQPURL(c.url), err)
	}
	ch, err := conn.Channel()
	if err != nil {
		conn.Close()
		return fmt.Errorf("mq: open channel: %w", err)
	}
	if err := c.assertTopology(ch); err != nil {
		conn.Close()
		return fmt.Errorf("mq: assert topology: %w", err)
	}

	c.mu.Lock()
	c.conn = conn
	c.pubCh = ch
	c.mu.Unlock()

	closed := conn.NotifyClose(make(chan *amqp.Error, 1))
	go c.watchClose(closed)
	return nil
}

func (c *AMQPClient) watchClose(closed <-chan *amqp.Error) {
	amqpErr := <-closed
	c.mu.Lock()
	closing := c.closing
	c.conn = nil
	c.pubCh = nil
	c.mu.Unlock()
	if closing {
		return
	}
	if amqpErr != nil {
		c.opts.Logf("[mq] connection lost: %v", amqpErr)
	} else {
		c.opts.Logf("[mq] connection closed unexpectedly")
	}
	c.reconnect()
}

// reconnect re-dials with exponential backoff and re-registers all
// consumers. If every attempt fails the process exits so the orchestrator
// restarts the pod with a clean slate (linda-assistant pattern).
func (c *AMQPClient) reconnect() {
	for attempt := 1; attempt <= reconnectMaxAttempts; attempt++ {
		c.opts.Logf("[mq] reconnecting (attempt %d/%d)...", attempt, reconnectMaxAttempts)
		err := c.connect()
		if err == nil {
			err = c.resubscribeAll()
			if err == nil {
				c.opts.Logf("[mq] reconnected")
				return
			}
		}
		c.opts.Logf("[mq] reconnect attempt %d failed: %v", attempt, err)
		c.mu.Lock()
		closing := c.closing
		c.mu.Unlock()
		if closing {
			return
		}
		if attempt < reconnectMaxAttempts {
			delay := reconnectBaseDelay << uint(attempt-1)
			if delay > reconnectMaxDelay {
				delay = reconnectMaxDelay
			}
			time.Sleep(delay)
		}
	}
	c.opts.Logf("[mq] all reconnect attempts failed, exiting")
	c.opts.exitFn(1)
}

func (c *AMQPClient) resubscribeAll() error {
	c.mu.Lock()
	regs := make([]consumerReg, len(c.consumers))
	copy(regs, c.consumers)
	c.mu.Unlock()
	for _, reg := range regs {
		if err := c.startConsumer(reg); err != nil {
			return fmt.Errorf("re-subscribe %s: %w", reg.queue, err)
		}
	}
	return nil
}

// assertTopology idempotently declares the retry exchange plus each work
// queue and its TTL+DLX holding queue.
func (c *AMQPClient) assertTopology(ch *amqp.Channel) error {
	if err := ch.ExchangeDeclare(RetryExchange, "direct", true, false, false, false, nil); err != nil {
		return err
	}
	specs := []struct {
		queue           string
		consumerTimeout time.Duration
	}{
		{QueueGeneration, generationConsumerTimeout},
		{QueueDocs, 0},
		{QueuePlanning, 0},
	}
	for _, spec := range specs {
		workArgs := amqp.Table{}
		if spec.consumerTimeout > 0 {
			// Per-queue x-consumer-timeout is only valid on quorum queues
			// (RabbitMQ 4 rejects it on classic queues), so the heavy queue
			// is quorum — which also gives it replicated durability.
			workArgs["x-queue-type"] = "quorum"
			workArgs["x-consumer-timeout"] = spec.consumerTimeout.Milliseconds()
		}
		retryArgs := amqp.Table{
			"x-dead-letter-exchange":    "",
			"x-dead-letter-routing-key": c.physical(spec.queue),
		}
		if c.opts.QueuePrefix != "" {
			workArgs["x-expires"] = prefixedQueueExpiry.Milliseconds()
			retryArgs["x-expires"] = prefixedQueueExpiry.Milliseconds()
		}
		if _, err := ch.QueueDeclare(c.physical(spec.queue), true, false, false, false, workArgs); err != nil {
			return err
		}
		if _, err := ch.QueueDeclare(c.retryQueue(spec.queue), true, false, false, false, retryArgs); err != nil {
			return err
		}
		if err := ch.QueueBind(c.retryQueue(spec.queue), c.physical(spec.queue), RetryExchange, false, nil); err != nil {
			return err
		}
	}
	return nil
}

// Connected reports whether the broker connection is live (health checks).
func (c *AMQPClient) Connected(ctx context.Context) error {
	c.mu.Lock()
	ch := c.pubCh
	c.mu.Unlock()
	if ch == nil {
		return fmt.Errorf("mq: not connected")
	}
	if _, err := ch.QueueDeclarePassive(c.physical(QueueGeneration), true, false, false, false, nil); err != nil {
		return fmt.Errorf("mq: connectivity check: %w", err)
	}
	return nil
}

// Close stops consuming and releases the connection after waiting up to a
// grace period for in-flight handlers. The connection stays open while they
// drain so a handler interrupted by shutdown can still publish its retry.
func (c *AMQPClient) Close() error {
	c.mu.Lock()
	c.closing = true
	c.mu.Unlock()

	done := make(chan struct{})
	go func() {
		c.inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		c.opts.Logf("[mq] close: grace period expired with handlers still in flight")
	}

	c.mu.Lock()
	conn := c.conn
	c.conn = nil
	c.pubCh = nil
	c.mu.Unlock()
	if conn != nil {
		return conn.Close()
	}
	return nil
}

// redactAMQPURL hides credentials in connection errors.
func redactAMQPURL(url string) string {
	at := -1
	scheme := -1
	for i := 0; i+2 < len(url); i++ {
		if url[i] == ':' && url[i+1] == '/' && url[i+2] == '/' {
			scheme = i + 3
			break
		}
	}
	for i := len(url) - 1; i >= 0; i-- {
		if url[i] == '@' {
			at = i
			break
		}
	}
	if scheme >= 0 && at > scheme {
		return url[:scheme] + "***" + url[at:]
	}
	return url
}
