package mq

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

func (c *AMQPClient) publishChannel() (*amqp.Channel, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pubCh == nil {
		return nil, fmt.Errorf("mq: not connected")
	}
	return c.pubCh, nil
}

// Publish enqueues a task onto a work queue (persistent delivery).
func (c *AMQPClient) Publish(ctx context.Context, queue string, t Task) error {
	body, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("mq: marshal task: %w", err)
	}
	ch, err := c.publishChannel()
	if err != nil {
		return err
	}
	err = ch.PublishWithContext(ctx, "", c.physical(queue), false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         body,
	})
	if err != nil {
		return fmt.Errorf("mq: publish %s to %s: %w", t.Type, queue, err)
	}
	c.opts.Logf("[mq] published %s key=%s attempt=%d queue=%s", t.Type, t.Key, t.Attempt, queue)
	return nil
}

// PublishRetry places a task in the queue's holding queue with a per-message
// TTL; on expiry RabbitMQ dead-letters it back into the work queue, which is
// what implements the backoff delay.
func (c *AMQPClient) PublishRetry(ctx context.Context, queue string, t Task, delay time.Duration) error {
	body, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("mq: marshal task: %w", err)
	}
	ch, err := c.publishChannel()
	if err != nil {
		return err
	}
	err = ch.PublishWithContext(ctx, RetryExchange, c.physical(queue), false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Expiration:   strconv.FormatInt(delay.Milliseconds(), 10),
		Body:         body,
	})
	if err != nil {
		return fmt.Errorf("mq: publish retry %s to %s: %w", t.Type, queue, err)
	}
	c.opts.Logf("[mq] scheduled retry %s key=%s attempt=%d queue=%s delay=%s", t.Type, t.Key, t.Attempt, queue, delay)
	return nil
}
