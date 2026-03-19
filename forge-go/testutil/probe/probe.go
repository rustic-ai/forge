package probe

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// ProbeAgent is a test utility for interacting with Forge agents over Redis
type ProbeAgent struct {
	rdb *redis.Client
}

// NewProbeAgent creates a new ProbeAgent
func NewProbeAgent(rdb *redis.Client) *ProbeAgent {
	return &ProbeAgent{rdb: rdb}
}

// Publish stores and publishes a message exactly as the Python RedisMessagingBackend does
func (p *ProbeAgent) Publish(ctx context.Context, namespace, topic string, msg *Message) error {
	msgJSON, err := msg.ToJSON()
	if err != nil {
		return err
	}

	msgKey := fmt.Sprintf("msg:%s:%d", namespace, msg.ID)

	pipe := p.rdb.Pipeline()
	pipe.Set(ctx, msgKey, msgJSON, time.Hour)
	pipe.ZAdd(ctx, topic, redis.Z{
		Score:  msg.Timestamp,
		Member: msgJSON,
	})

	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("pipeline storage failed: %w", err)
	}

	err = p.rdb.Publish(ctx, topic, msgJSON).Err()
	if err != nil {
		return fmt.Errorf("PUBLISH failed: %w", err)
	}

	return nil
}

// Subscribe returns a channel that receives messages on the given topic
func (p *ProbeAgent) Subscribe(ctx context.Context, topic string) <-chan *Message {
	pubsub := p.rdb.Subscribe(ctx, topic)
	ch := make(chan *Message, 100)

	go func() {
		defer func() { _ = pubsub.Close() }()
		defer close(ch)
		for {
			select {
			case <-ctx.Done():
				return
			case redisMsg := <-pubsub.Channel():
				if redisMsg == nil {
					continue
				}
				m, err := MessageFromJSON(redisMsg.Payload)
				if err == nil {
					ch <- m
				}
			}
		}
	}()

	return ch
}

// WaitForMessage subscribes to a topic and waits for a single message or timeout.
func (p *ProbeAgent) WaitForMessage(ctx context.Context, topic string, timeout time.Duration) (*Message, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ch := p.Subscribe(ctx, topic)

	select {
	case msg := <-ch:
		if msg != nil {
			return msg, nil
		}
		return nil, fmt.Errorf("channel closed")
	case <-ctx.Done():
		return nil, fmt.Errorf("timeout waiting for message on topic %s", topic)
	}
}
