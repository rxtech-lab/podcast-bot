package mq

import (
	"context"
	"encoding/json"
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
)

// RegisterHandler starts consuming queue on a dedicated channel with the
// given prefetch (the per-pod concurrency bound for that queue). The
// registration survives reconnects: the close watcher re-runs it against the
// fresh connection.
//
// Deliveries are ALWAYS acked — including handler errors and undecodable
// payloads — because retry orchestration lives above this layer (attempt
// counter in the payload + TTL/DLX holding queue). Leaving a message unacked
// would make the broker redeliver immediately, bypassing the backoff.
func (c *AMQPClient) RegisterHandler(queue string, prefetch int, h Handler) error {
	reg := consumerReg{queue: queue, prefetch: prefetch, handler: h}
	c.mu.Lock()
	c.consumers = append(c.consumers, reg)
	c.mu.Unlock()
	return c.startConsumer(reg)
}

func (c *AMQPClient) startConsumer(reg consumerReg) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("mq: not connected")
	}
	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("mq: consumer channel for %s: %w", reg.queue, err)
	}
	if err := ch.Qos(reg.prefetch, 0, false); err != nil {
		ch.Close()
		return fmt.Errorf("mq: qos %s: %w", reg.queue, err)
	}
	deliveries, err := ch.Consume(c.physical(reg.queue), "", false, false, false, false, nil)
	if err != nil {
		ch.Close()
		return fmt.Errorf("mq: consume %s: %w", reg.queue, err)
	}
	go c.consumeLoop(ch, reg, deliveries)
	return nil
}

func (c *AMQPClient) consumeLoop(ch *amqp.Channel, reg consumerReg, deliveries <-chan amqp.Delivery) {
	// The deliveries channel closes when the channel/connection dies; the
	// connection close watcher owns reconnection and re-registration, so
	// this loop just drains and exits.
	for d := range deliveries {
		d := d
		c.inflight.Add(1)
		go func() {
			defer c.inflight.Done()
			defer func() {
				if r := recover(); r != nil {
					c.opts.Logf("[mq] handler panic on %s: %v", reg.queue, r)
				}
				if err := d.Ack(false); err != nil {
					c.opts.Logf("[mq] ack failed on %s: %v", reg.queue, err)
				}
			}()
			var t Task
			if err := json.Unmarshal(d.Body, &t); err != nil {
				c.opts.Logf("[mq] dropping undecodable message on %s: %v", reg.queue, err)
				return
			}
			if err := reg.handler(context.Background(), t); err != nil {
				// The dispatch layer has already routed this to retry or
				// terminal handling; surfacing it here is diagnostics only.
				c.opts.Logf("[mq] handler error on %s (%s key=%s attempt=%d): %v", reg.queue, t.Type, t.Key, t.Attempt, err)
			}
		}()
	}
}
