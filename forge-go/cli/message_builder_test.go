package cli

import (
	"encoding/json"
	"testing"

	"github.com/rustic-ai/forge/forge-go/protocol"
)

func TestBuildChatMessage(t *testing.T) {
	msg, err := BuildChatMessage("alice", "Alice", "hello world", "default_topic")
	if err != nil {
		t.Fatalf("BuildChatMessage: %v", err)
	}

	if msg.Format != "rustic_ai.core.guild.agent_ext.depends.llm.models.ChatCompletionRequest" {
		t.Errorf("unexpected format: %s", msg.Format)
	}
	if msg.ID == 0 {
		t.Error("expected non-zero message ID")
	}
	if msg.ConversationID == nil {
		t.Error("expected ConversationID to be set")
	}
	if got := derefStr(msg.Sender.ID); got != "alice" {
		t.Errorf("sender ID = %q, want alice", got)
	}
	if got := derefStr(msg.Sender.Name); got != "Alice" {
		t.Errorf("sender name = %q, want Alice", got)
	}
	if topics := msg.Topics.ToSlice(); len(topics) != 1 || topics[0] != "default_topic" {
		t.Errorf("topics = %v, want [default_topic]", topics)
	}

	// Payload: messages[0] with role=user, name=alice, content[0].text=hello world.
	var payload map[string]any
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	messages, _ := payload["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}
	first, _ := messages[0].(map[string]any)
	if first["role"] != "user" {
		t.Errorf("role = %v, want user", first["role"])
	}
	if first["name"] != "alice" {
		t.Errorf("name = %v, want alice", first["name"])
	}
	content, _ := first["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected 1 content item, got %d", len(content))
	}
	item, _ := content[0].(map[string]any)
	if item["type"] != "text" || item["text"] != "hello world" {
		t.Errorf("content[0] = %v, want type=text text=hello world", item)
	}
}

func TestBuildSystemMessage_DefaultAndExplicitFormat(t *testing.T) {
	payload := map[string]any{"k": "v"}

	def, err := BuildSystemMessage("system_topic", payload, "")
	if err != nil {
		t.Fatalf("BuildSystemMessage: %v", err)
	}
	if def.Format != "system" {
		t.Errorf("default format = %q, want system", def.Format)
	}
	if topics := def.Topics.ToSlice(); len(topics) != 1 || topics[0] != "system_topic" {
		t.Errorf("topics = %v, want [system_topic]", topics)
	}

	explicit, err := BuildSystemMessage("t", payload, "custom.Format")
	if err != nil {
		t.Fatalf("BuildSystemMessage: %v", err)
	}
	if explicit.Format != "custom.Format" {
		t.Errorf("explicit format = %q, want custom.Format", explicit.Format)
	}
	var got map[string]any
	if err := json.Unmarshal(explicit.Payload, &got); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got["k"] != "v" {
		t.Errorf("payload = %v, want {k:v}", got)
	}
}

func TestBuildHealthCheckMessage(t *testing.T) {
	msg, err := BuildHealthCheckMessage("bob", "user:bob")
	if err != nil {
		t.Fatalf("BuildHealthCheckMessage: %v", err)
	}
	if msg.Format != "healthcheck" {
		t.Errorf("format = %q, want healthcheck", msg.Format)
	}
	if derefStr(msg.Sender.ID) != "bob" {
		t.Errorf("sender ID = %q, want bob", derefStr(msg.Sender.ID))
	}
	if msg.ConversationID == nil {
		t.Error("expected ConversationID to be set")
	}
	if topics := msg.Topics.ToSlice(); len(topics) != 1 || topics[0] != "user:bob" {
		t.Errorf("topics = %v, want [user:bob]", topics)
	}
}

func TestBuildWrappedChatMessage(t *testing.T) {
	msg, err := BuildWrappedChatMessage("alice", "Alice", "hi")
	if err != nil {
		t.Fatalf("BuildWrappedChatMessage: %v", err)
	}
	if msg.Format != "rustic_ai.core.messaging.core.message.Message" {
		t.Errorf("format = %q", msg.Format)
	}
	if got := derefStr(msg.Sender.ID); got != "user_socket:alice" {
		t.Errorf("sender ID = %q, want user_socket:alice", got)
	}
	if topics := msg.Topics.ToSlice(); len(topics) != 1 || topics[0] != "user:alice" {
		t.Errorf("topics = %v, want [user:alice]", topics)
	}
	if len(msg.Thread) != 1 {
		t.Errorf("thread len = %d, want 1", len(msg.Thread))
	}

	// The payload is the inner chat message.
	var inner protocol.Message
	if err := json.Unmarshal(msg.Payload, &inner); err != nil {
		t.Fatalf("unmarshal inner message: %v", err)
	}
	if inner.Format != "rustic_ai.core.guild.agent_ext.depends.llm.models.ChatCompletionRequest" {
		t.Errorf("inner format = %q", inner.Format)
	}
}

func TestBuildUserProxyCreationRequest(t *testing.T) {
	msg, err := BuildUserProxyCreationRequest("alice", "Alice")
	if err != nil {
		t.Fatalf("BuildUserProxyCreationRequest: %v", err)
	}
	if msg.Format != "rustic_ai.core.agents.system.models.UserAgentCreationRequest" {
		t.Errorf("format = %q", msg.Format)
	}
	if got := derefStr(msg.Sender.ID); got != "user_socket:alice" {
		t.Errorf("sender ID = %q, want user_socket:alice", got)
	}
	if topics := msg.Topics.ToSlice(); len(topics) != 1 || topics[0] != "system_topic" {
		t.Errorf("topics = %v, want [system_topic]", topics)
	}
	var payload map[string]any
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload["user_id"] != "alice" || payload["user_name"] != "Alice" {
		t.Errorf("payload = %v, want user_id=alice user_name=Alice", payload)
	}
}

func TestParseMessageFromJSON(t *testing.T) {
	orig, err := BuildChatMessage("alice", "Alice", "hi", "t")
	if err != nil {
		t.Fatalf("BuildChatMessage: %v", err)
	}
	raw, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	parsed, err := ParseMessageFromJSON(raw)
	if err != nil {
		t.Fatalf("ParseMessageFromJSON: %v", err)
	}
	if parsed.ID != orig.ID || parsed.Format != orig.Format {
		t.Errorf("round-trip mismatch: got id=%d format=%s", parsed.ID, parsed.Format)
	}

	if _, err := ParseMessageFromJSON([]byte("not json")); err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestMessageIDsAreUnique(t *testing.T) {
	a, err := BuildChatMessage("u", "U", "one", "t")
	if err != nil {
		t.Fatalf("BuildChatMessage: %v", err)
	}
	b, err := BuildChatMessage("u", "U", "two", "t")
	if err != nil {
		t.Fatalf("BuildChatMessage: %v", err)
	}
	if a.ID == b.ID {
		t.Errorf("expected unique message IDs, both = %d", a.ID)
	}
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
