package cli

import (
	"context"
	"fmt"

	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/protocol"
)

// GuildSubscription wraps a messaging subscription for guild messages
type GuildSubscription struct {
	sub        messaging.Subscription
	msgChan    chan *protocol.Message
	errChan    chan error
	ctx        context.Context
	cancel     context.CancelFunc
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

// Close closes the subscription
func (s *GuildSubscription) Close() error {
	s.cancel()
	close(s.msgChan)
	close(s.errChan)
	return s.sub.Close()
}

func (s *GuildSubscription) forward() {
	for {
		select {
		case <-s.ctx.Done():
			return
		case subMsg, ok := <-s.sub.Channel():
			if !ok {
				return
			}
			s.msgChan <- subMsg.Message
		case err, ok := <-s.sub.ErrChannel():
			if !ok {
				return
			}
			s.errChan <- err
		}
	}
}
