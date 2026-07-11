package command

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rustic-ai/forge/forge-go/cli"
	"github.com/rustic-ai/forge/forge-go/protocol"
)

func TestSelectUserMessageTopic(t *testing.T) {
	userProxy := "rustic_ai.core.agents.utils.user_proxy_agent.UserProxyAgent"

	dest := protocol.NewRoutingDestination()
	dest.Topics = protocol.TopicsFromString("dest_topic")

	cases := []struct {
		name        string
		spec        *protocol.GuildSpec
		wantTopic   string
		wantWrapped bool
	}{
		{
			name:        "user proxy uses wrapped messages",
			spec:        &protocol.GuildSpec{Routes: &protocol.RoutingSlip{Steps: []protocol.RoutingRule{{AgentType: &userProxy}}}},
			wantTopic:   "default_topic",
			wantWrapped: true,
		},
		{
			name:        "route destination topic",
			spec:        &protocol.GuildSpec{Routes: &protocol.RoutingSlip{Steps: []protocol.RoutingRule{{Destination: &dest}}}},
			wantTopic:   "dest_topic",
			wantWrapped: false,
		},
		{
			name:        "no routing falls back to first agent additional topic",
			spec:        &protocol.GuildSpec{Agents: []protocol.AgentSpec{{AdditionalTopics: []string{"agent_topic"}}}},
			wantTopic:   "agent_topic",
			wantWrapped: false,
		},
		{
			name:        "default when nothing configured",
			spec:        &protocol.GuildSpec{},
			wantTopic:   "default_topic",
			wantWrapped: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			topic, wrapped := selectUserMessageTopic(tc.spec)
			if topic != tc.wantTopic || wrapped != tc.wantWrapped {
				t.Errorf("got (%q, %v), want (%q, %v)", topic, wrapped, tc.wantTopic, tc.wantWrapped)
			}
		})
	}
}

func TestDetectPython(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\necho \"Python 3.13.0\"\n"
	py3 := filepath.Join(dir, "python3")
	if err := os.WriteFile(py3, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake python3: %v", err)
	}
	// Isolate PATH so only our fake python3 is discoverable (no pyenv/python).
	t.Setenv("PATH", dir)

	if got := detectPython(); got != py3 {
		t.Errorf("detectPython() = %q, want %q", got, py3)
	}
}

func TestDetectPython_NoneFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // empty dir on PATH -> nothing found
	if got := detectPython(); got != "" {
		t.Errorf("detectPython() = %q, want empty string", got)
	}
}

func TestDetectPython_PythonBranch(t *testing.T) {
	dir := t.TempDir()
	py := filepath.Join(dir, "python")
	if err := os.WriteFile(py, []byte("#!/bin/sh\necho \"Python 3.14.1\"\n"), 0o755); err != nil {
		t.Fatalf("write fake python: %v", err)
	}
	t.Setenv("PATH", dir)
	if got := detectPython(); got != py {
		t.Errorf("detectPython() = %q, want %q (python branch)", got, py)
	}
}

func TestDetectPython_Python3WrongVersionFallback(t *testing.T) {
	dir := t.TempDir()
	py3 := filepath.Join(dir, "python3")
	// An older python3 is still returned as a best-effort fallback.
	if err := os.WriteFile(py3, []byte("#!/bin/sh\necho \"Python 3.10.0\"\n"), 0o755); err != nil {
		t.Fatalf("write fake python3: %v", err)
	}
	t.Setenv("PATH", dir)
	if got := detectPython(); got != py3 {
		t.Errorf("detectPython() = %q, want %q (fallback)", got, py3)
	}
}

func TestRunGuildREPL_NoForgeRoot(t *testing.T) {
	// From a directory with no go.mod, findForgeRoot returns "" and runGuildREPL
	// should fail fast before touching any runtime.
	t.Chdir(t.TempDir())
	err := runGuildREPL(nil, []string{"missing.json"})
	if err == nil || !strings.Contains(err.Error(), "forge root") {
		t.Errorf("expected 'forge root' error, got %v", err)
	}
}

func TestShowAgentStatus(t *testing.T) {
	rt := &fakeRuntime{
		statuses: map[string]cli.AgentStatus{
			"a1": {AgentID: "a1", State: "running", PID: 100},
		},
		names: map[string]string{"a1": "EchoAgent"},
	}
	var buf bytes.Buffer
	if err := showAgentStatus(&buf, rt, "g1"); err != nil {
		t.Fatalf("showAgentStatus: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "EchoAgent") || !strings.Contains(out, "100") || !strings.Contains(out, "running") {
		t.Errorf("expected agent line with name/pid/state, got %q", out)
	}
}

func TestShowAgentStatus_Empty(t *testing.T) {
	var buf bytes.Buffer
	if err := showAgentStatus(&buf, &fakeRuntime{statuses: map[string]cli.AgentStatus{}}, "g1"); err != nil {
		t.Fatalf("showAgentStatus: %v", err)
	}
	if !strings.Contains(buf.String(), "No agents found") {
		t.Errorf("expected 'No agents found', got %q", buf.String())
	}
}

func TestShowAgentStatus_Error(t *testing.T) {
	rt := &fakeRuntime{statusErr: context.DeadlineExceeded}
	if err := showAgentStatus(&bytes.Buffer{}, rt, "g1"); err == nil {
		t.Error("expected error propagated from GetAgentStatuses")
	}
}

func TestSendChatMessage_Regular(t *testing.T) {
	rt := &fakeRuntime{}
	var buf bytes.Buffer
	if err := sendChatMessage(&buf, rt, "g1", "alice", "Alice", "hello", "some_topic", false); err != nil {
		t.Fatalf("sendChatMessage: %v", err)
	}
	if len(rt.published) != 1 {
		t.Fatalf("expected 1 published message, got %d", len(rt.published))
	}
	if rt.published[0].topic != "some_topic" {
		t.Errorf("published topic = %q, want some_topic", rt.published[0].topic)
	}
	if !strings.Contains(buf.String(), "Sending to topic: some_topic") {
		t.Errorf("expected send banner, got %q", buf.String())
	}
}

func TestSendChatMessage_Wrapped(t *testing.T) {
	rt := &fakeRuntime{}
	if err := sendChatMessage(&bytes.Buffer{}, rt, "g1", "alice", "Alice", "hi", "ignored", true); err != nil {
		t.Fatalf("sendChatMessage: %v", err)
	}
	if len(rt.published) != 1 || rt.published[0].topic != "user:alice" {
		t.Errorf("wrapped message should publish to user:alice, got %+v", rt.published)
	}
}

func TestSendChatMessage_PublishError(t *testing.T) {
	rt := &fakeRuntime{publishErr: context.DeadlineExceeded}
	if err := sendChatMessage(&bytes.Buffer{}, rt, "g1", "alice", "Alice", "hi", "t", false); err == nil {
		t.Error("expected publish error to propagate")
	}
}

func TestDisplayMessages_PrintsAndExitsOnClose(t *testing.T) {
	rt := &fakeRuntime{}
	src := newFakeSource()
	src.msgs <- msgWith("x", "default_topic", `{"text":"hello"}`)
	// Close only msgs so the loop deterministically prints the buffered message
	// and then returns on the closed channel (errs stays open+empty, never ready).
	close(src.msgs)

	var buf bytes.Buffer
	displayMessages(context.Background(), &buf, src, rt, "alice", false, false)

	if !strings.Contains(buf.String(), "hello") {
		t.Errorf("expected message content in output, got %q", buf.String())
	}
}

func TestDisplayMessages_ReportsErrors(t *testing.T) {
	src := newFakeSource()
	src.errs <- context.DeadlineExceeded
	// Close only errs; msgs stays open+empty so the loop deterministically
	// prints the error then returns on the closed errs channel.
	close(src.errs)

	var buf bytes.Buffer
	displayMessages(context.Background(), &buf, src, &fakeRuntime{}, "alice", false, false)

	if !strings.Contains(buf.String(), "Subscription error") {
		t.Errorf("expected subscription error in output, got %q", buf.String())
	}
}

func TestDisplayMessages_StopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Channels stay open; the cancelled context must make displayMessages return.
	done := make(chan struct{})
	go func() {
		displayMessages(ctx, &bytes.Buffer{}, newFakeSource(), &fakeRuntime{}, "alice", false, false)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("displayMessages did not return on context cancel")
	}
}
