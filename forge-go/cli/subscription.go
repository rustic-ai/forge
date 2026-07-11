package cli

import (
	"context"
	"fmt"
	"sync"

	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/protocol"
)

// GuildSubscription wraps a messaging subscription for guild messages
type GuildSubscription struct {
	sub       messaging.Subscription
	msgChan   chan *protocol.Message
	errChan   chan error
	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once
}

// Subscribe creates a subscription to guild message topics
func (r *GuildRuntime) Subscribe(guildID, userID string, spec *protocol.GuildSpec) (*GuildSubscription, error) {
	// Subscribe to relevant topics
	topics := []string{
		"user_notifications:" + userID,       // Messages to user
		"user_system_notification:" + userID, // System notifications
		"user_message_broadcast",             // Broadcast messages from agents
		"user:" + userID,                     // Direct user messages
		"default_topic",                      // Guild default inbox
		"guild_status_topic",                 // Guild status
		"infra_events_topic",                 // Infrastructure events
	}

	// Add all additional_topics from agents in the guild
	for _, agent := range spec.Agents {
		topics = append(topics, agent.AdditionalTopics...)
	}

	backend, err := r.getMessagingBackend()
	if err != nil {
		return nil, fmt.Errorf("failed to get messaging backend: %w", err)
	}

	sub, err := backend.Subscribe(r.ctx, guildID, topics...)
	if err != nil {
		return nil, fmt.Errorf("failed to subscribe: %w", err)
	}

	ctx, cancel := context.WithCancel(r.ctx)
	guildSub := &GuildSubscription{
		sub:     sub,
		msgChan: make(chan *protocol.Message, 100),
		errChan: make(chan error, 10),
		ctx:     ctx,
		cancel:  cancel,
	}

	// Start message forwarding goroutine
	go guildSub.forward()

	return guildSub, nil
}

// Messages returns the channel for receiving messages
func (s *GuildSubscription) Messages() <-chan *protocol.Message {
	return s.msgChan
}

// Errors returns the channel for receiving errors
func (s *GuildSubscription) Errors() <-chan error {
	return s.errChan
}

// Close closes the subscription. It cancels the forwarding goroutine and closes
// the underlying subscription; msgChan/errChan are closed by forward() as it
// exits (it is their sole writer), so Close never closes them itself — doing so
// would race with an in-flight send and panic. Safe to call more than once.
func (s *GuildSubscription) Close() error {
	var err error
	s.closeOnce.Do(func() {
		s.cancel()
		err = s.sub.Close()
	})
	return err
}

// forward pumps messages and errors from the underlying subscription into the
// buffered channels until the context is cancelled or the source closes. It is
// the sole writer to msgChan/errChan and therefore the sole closer: closing them
// here (rather than in Close) guarantees no send races with a close. Every send
// is guarded by ctx.Done() so a stalled consumer cannot leak this goroutine.
func (s *GuildSubscription) forward() {
	defer close(s.msgChan)
	defer close(s.errChan)
	for {
		select {
		case <-s.ctx.Done():
			return
		case subMsg, ok := <-s.sub.Channel():
			if !ok {
				return
			}
			select {
			case s.msgChan <- subMsg.Message:
			case <-s.ctx.Done():
				return
			}
		case err, ok := <-s.sub.ErrChannel():
			if !ok {
				return
			}
			select {
			case s.errChan <- err:
			case <-s.ctx.Done():
				return
			}
		}
	}
}
