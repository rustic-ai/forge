package cli

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/rustic-ai/forge/forge-go/protocol"
)

var (
	idGen     *protocol.GemstoneGenerator
	idGenOnce sync.Once
)

func getIDGen() *protocol.GemstoneGenerator {
	idGenOnce.Do(func() {
		// Use machine ID 1 for CLI
		gen, _ := protocol.NewGemstoneGenerator(1)
		idGen = gen
	})
	return idGen
}

// BuildChatMessage creates a chatCompletionRequest message
func BuildChatMessage(userID, userName, text, topic string) (*protocol.Message, error) {
	gen := getIDGen()
	msgID, err := gen.Generate(protocol.PriorityNormal)
	if err != nil {
		return nil, fmt.Errorf("failed to generate message ID: %w", err)
	}

	// Create chat completion request payload
	payload := map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{
				"role": "user",
				"name": userID,
				"content": []interface{}{
					map[string]interface{}{
						"type": "text",
						"text": text,
					},
				},
			},
		},
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	convID, _ := gen.Generate(protocol.PriorityNormal)
	convIDInt := convID.ToInt()

	msg := protocol.NewMessageFromGemstoneID(msgID)
	msg.Format = "chatCompletionRequest"
	msg.Sender = protocol.AgentTag{
		ID:   &userID,
		Name: &userName,
	}
	msg.Topics = protocol.TopicsFromString(topic)
	msg.Payload = json.RawMessage(payloadBytes)
	msg.ConversationID = &convIDInt

	return &msg, nil
}

// BuildSystemMessage creates a system message
func BuildSystemMessage(topic string, payload map[string]interface{}, format string) (*protocol.Message, error) {
	gen := getIDGen()
	msgID, err := gen.Generate(protocol.PriorityNormal)
	if err != nil {
		return nil, fmt.Errorf("failed to generate message ID: %w", err)
	}

	if format == "" {
		format = "system"
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	msg := protocol.NewMessageFromGemstoneID(msgID)
	msg.Format = format
	msg.Topics = protocol.TopicsFromString(topic)
	msg.Payload = json.RawMessage(payloadBytes)

	return &msg, nil
}

// ParseMessageFromJSON parses a message from JSON
func ParseMessageFromJSON(data []byte) (*protocol.Message, error) {
	var msg protocol.Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("failed to parse message: %w", err)
	}
	return &msg, nil
}

// BuildHealthCheckMessage creates a health check message
func BuildHealthCheckMessage(userID, topic string) (*protocol.Message, error) {
	gen := getIDGen()
	msgID, err := gen.Generate(protocol.PriorityNormal)
	if err != nil {
		return nil, fmt.Errorf("failed to generate message ID: %w", err)
	}

	payload := map[string]interface{}{
		"dummy": 1,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	convID, _ := gen.Generate(protocol.PriorityNormal)
	convIDInt := convID.ToInt()

	msg := protocol.NewMessageFromGemstoneID(msgID)
	msg.Format = "healthcheck"
	msg.Sender = protocol.AgentTag{
		ID: &userID,
	}
	msg.Topics = protocol.TopicsFromString(topic)
	msg.Payload = json.RawMessage(payloadBytes)
	msg.ConversationID = &convIDInt

	return &msg, nil
}
