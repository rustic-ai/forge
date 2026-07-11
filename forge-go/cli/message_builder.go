package cli

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/rustic-ai/forge/forge-go/protocol"
)

var (
	idGen     *protocol.GemstoneGenerator
	idGenErr  error
	idGenOnce sync.Once
)

func getIDGen() (*protocol.GemstoneGenerator, error) {
	idGenOnce.Do(func() {
		// Use machine ID 1 for CLI
		idGen, idGenErr = protocol.NewGemstoneGenerator(1)
	})
	return idGen, idGenErr
}

// BuildChatMessage creates a chatCompletionRequest message
func BuildChatMessage(userID, userName, text, topic string) (*protocol.Message, error) {
	gen, err := getIDGen()
	if err != nil {
		return nil, fmt.Errorf("failed to init ID generator: %w", err)
	}
	msgID, err := gen.Generate(protocol.PriorityNormal)
	if err != nil {
		return nil, fmt.Errorf("failed to generate message ID: %w", err)
	}

	// Create chat completion request payload
	payload := map[string]any{
		"messages": []any{
			map[string]any{
				"role": "user",
				"name": userID,
				"content": []any{
					map[string]any{
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

	convID, err := gen.Generate(protocol.PriorityNormal)
	if err != nil {
		return nil, fmt.Errorf("failed to generate conversation ID: %w", err)
	}
	convIDInt := convID.ToInt()

	msg := protocol.NewMessageFromGemstoneID(msgID)
	msg.Format = "rustic_ai.core.guild.agent_ext.depends.llm.models.ChatCompletionRequest" // Use full format to match routing rules
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
func BuildSystemMessage(topic string, payload map[string]any, format string) (*protocol.Message, error) {
	gen, err := getIDGen()
	if err != nil {
		return nil, fmt.Errorf("failed to init ID generator: %w", err)
	}
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
	gen, err := getIDGen()
	if err != nil {
		return nil, fmt.Errorf("failed to init ID generator: %w", err)
	}
	msgID, err := gen.Generate(protocol.PriorityNormal)
	if err != nil {
		return nil, fmt.Errorf("failed to generate message ID: %w", err)
	}

	payload := map[string]any{
		"dummy": 1,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	convID, err := gen.Generate(protocol.PriorityNormal)
	if err != nil {
		return nil, fmt.Errorf("failed to generate conversation ID: %w", err)
	}
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

// BuildWrappedChatMessage creates a wrapped chat message for UserProxyAgent
// This mimics how the gateway wraps user messages before sending them to user:{userID}
func BuildWrappedChatMessage(userID, userName, text string) (*protocol.Message, error) {
	gen, err := getIDGen()
	if err != nil {
		return nil, fmt.Errorf("failed to init ID generator: %w", err)
	}

	// Create the inner chat message
	innerMsg, err := BuildChatMessage(userID, userName, text, "default_topic")
	if err != nil {
		return nil, fmt.Errorf("failed to build inner message: %w", err)
	}

	// Generate a new ID for the wrapper
	wrapperID, err := gen.Generate(protocol.PriorityNormal)
	if err != nil {
		return nil, fmt.Errorf("failed to generate wrapper ID: %w", err)
	}

	// Marshal the inner message as the payload
	innerBytes, err := json.Marshal(innerMsg)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal inner message: %w", err)
	}

	// Create the wrapper message
	senderID := fmt.Sprintf("user_socket:%s", userID)
	wrapper := protocol.NewMessageFromGemstoneID(wrapperID)
	wrapper.Format = "rustic_ai.core.messaging.core.message.Message"
	wrapper.Sender = protocol.AgentTag{
		ID:   &senderID,
		Name: &userName,
	}
	wrapper.Topics = protocol.TopicsFromString(fmt.Sprintf("user:%s", userID))
	wrapper.Payload = json.RawMessage(innerBytes)
	wrapper.Thread = []uint64{wrapperID.ToInt()}

	return &wrapper, nil
}

// BuildUserProxyCreationRequest creates a UserAgentCreationRequest message
// This triggers the manager to create a UserProxyAgent for the user
func BuildUserProxyCreationRequest(userID, userName string) (*protocol.Message, error) {
	gen, err := getIDGen()
	if err != nil {
		return nil, fmt.Errorf("failed to init ID generator: %w", err)
	}
	msgID, err := gen.Generate(protocol.PriorityNormal)
	if err != nil {
		return nil, fmt.Errorf("failed to generate message ID: %w", err)
	}

	payload := map[string]any{
		"user_id":   userID,
		"user_name": userName,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	senderID := fmt.Sprintf("user_socket:%s", userID)
	msg := protocol.NewMessageFromGemstoneID(msgID)
	msg.Format = "rustic_ai.core.agents.system.models.UserAgentCreationRequest"
	msg.Sender = protocol.AgentTag{
		ID:   &senderID,
		Name: &userName,
	}
	msg.Topics = protocol.TopicsFromString("system_topic")
	msg.Payload = json.RawMessage(payloadBytes)

	return &msg, nil
}
