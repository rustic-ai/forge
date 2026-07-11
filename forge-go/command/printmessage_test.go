package command

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rustic-ai/forge/forge-go/protocol"
)

func strptr(s string) *string { return &s }

func msgWith(format, topic, payload string) *protocol.Message {
	m := &protocol.Message{Format: format, Topics: protocol.TopicsFromString(topic)}
	if payload != "" {
		m.Payload = json.RawMessage(payload)
	}
	return m
}

func render(msg *protocol.Message, rt guildRuntime, userID string, verbose, showRouting bool) string {
	var buf bytes.Buffer
	printMessage(&buf, msg, rt, userID, verbose, showRouting)
	return buf.String()
}

func TestPrintMessage_SkipsNoisyFormats(t *testing.T) {
	rt := &fakeRuntime{}
	skip := []string{
		"healthReport",
		"heartbeat",
		"rustic_ai.core.guild.agent_ext.mixins.health.AgentsHealthReport",
		"rustic_ai.core.agents.system.models.GuildUpdatedAnnouncement",
		"rustic_ai.core.state.models.StateFetchResponse",
		"rustic_ai.forge.runtime.InfraEvent",
		"rustic_ai.core.agents.utils.user_proxy_agent.ParticipantList",
	}
	for _, format := range skip {
		out := render(msgWith(format, "default_topic", ""), rt, "alice", false, false)
		if out != "" {
			t.Errorf("format %q should be hidden, got output: %q", format, out)
		}
	}
}

func TestPrintMessage_SkipsOwnEchoesAndUnwrapNotifications(t *testing.T) {
	rt := &fakeRuntime{}

	// Echo of our own message on user:{userID}.
	if out := render(msgWith("x", "user:alice", ""), rt, "alice", false, false); out != "" {
		t.Errorf("user:alice echo should be hidden, got %q", out)
	}

	// user_notifications with <3 routing entries is an unwrap notification.
	m := msgWith("x", "user_notifications:alice", "")
	if out := render(m, rt, "alice", false, false); out != "" {
		t.Errorf("short user_notifications should be hidden, got %q", out)
	}
}

func TestPrintMessage_PayloadFormats(t *testing.T) {
	rt := &fakeRuntime{}
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{"chatCompletionRequest", `{"messages":[{"role":"user","content":[{"type":"text","text":"hello there"}]}]}`, "hello there"},
		{"chatCompletionResponse", `{"choices":[{"message":{"content":"agent reply"}}]}`, "agent reply"},
		{"textField", `{"text":"plain text"}`, "plain text"},
		{"contentField", `{"content":"raw content"}`, "raw content"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := render(msgWith("x", "default_topic", tc.payload), rt, "alice", false, false)
			if !strings.Contains(out, "💬") || !strings.Contains(out, tc.want) {
				t.Errorf("expected 💬 %q, got %q", tc.want, out)
			}
		})
	}
}

func TestPrintMessage_GenericPayloadTruncated(t *testing.T) {
	rt := &fakeRuntime{}
	long := strings.Repeat("a", 300)
	out := render(msgWith("x", "default_topic", `{"blob":"`+long+`"}`), rt, "alice", false, false)
	if !strings.Contains(out, "📄") || !strings.Contains(out, "...") {
		t.Errorf("expected truncated generic payload with 📄 and ..., got %q", out)
	}
}

func TestPrintMessage_SenderName(t *testing.T) {
	rt := &fakeRuntime{names: map[string]string{"a1": "Friendly Agent"}}

	// Explicit sender name wins.
	m := msgWith("x", "default_topic", `{"text":"hi"}`)
	m.Sender = protocol.AgentTag{Name: strptr("Agent Smith")}
	if out := render(m, rt, "alice", false, false); !strings.Contains(out, "From: Agent Smith") {
		t.Errorf("expected sender name, got %q", out)
	}

	// Name resolved from the runtime map by ID.
	m2 := msgWith("x", "default_topic", `{"text":"hi"}`)
	m2.Sender = protocol.AgentTag{ID: strptr("a1")}
	if out := render(m2, rt, "alice", false, false); !strings.Contains(out, "From: Friendly Agent") {
		t.Errorf("expected resolved name, got %q", out)
	}
}

func TestPrintMessage_VerboseShowsSkippedAndPayload(t *testing.T) {
	rt := &fakeRuntime{}
	// A normally-skipped format is shown in verbose mode, with a DEBUG line and
	// pretty-printed payload.
	out := render(msgWith("healthReport", "default_topic", `{"k":"v"}`), rt, "alice", true, false)
	if !strings.Contains(out, "[DEBUG]") {
		t.Errorf("verbose should print DEBUG header, got %q", out)
	}
	if !strings.Contains(out, "Payload:") {
		t.Errorf("verbose should pretty-print payload, got %q", out)
	}
}

func TestPrintMessage_RoutingHistory(t *testing.T) {
	rt := &fakeRuntime{}
	m := msgWith("x", "default_topic", `{"text":"hi"}`)
	m.MessageHistory = []protocol.ProcessEntry{
		{Agent: protocol.AgentTag{Name: strptr("RouterA")}, Processor: "route", ToTopics: []string{"t2"}},
	}
	out := render(m, rt, "alice", false, true)
	if !strings.Contains(out, "🔀 Routing History:") || !strings.Contains(out, "RouterA") || !strings.Contains(out, "route") {
		t.Errorf("expected routing history with RouterA/route, got %q", out)
	}
}
