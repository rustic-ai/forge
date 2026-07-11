package command

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/rustic-ai/forge/forge-go/cli"
	"github.com/rustic-ai/forge/forge-go/protocol"
)

// captureStdout redirects os.Stdout for the duration of fn and returns whatever
// was written. inspectGuild/validateGuild print via fmt.Print* to os.Stdout
// directly, so this is the only way to assert their output. Tests using it must
// not run in parallel (os.Stdout is process-global).
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	fn()
	_ = w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}

// publishedMessage records a PublishMessage call for assertions.
type publishedMessage struct {
	namespace string
	topic     string
	msg       *protocol.Message
}

// fakeRuntime is a test double for the guildRuntime interface.
type fakeRuntime struct {
	statuses   map[string]cli.AgentStatus
	statusErr  error
	names      map[string]string
	published  []publishedMessage
	publishErr error
}

func (f *fakeRuntime) GetAgentStatuses(string) (map[string]cli.AgentStatus, error) {
	return f.statuses, f.statusErr
}

func (f *fakeRuntime) GetAgentName(agentID string) string {
	if n, ok := f.names[agentID]; ok {
		return n
	}
	return agentID
}

func (f *fakeRuntime) PublishMessage(namespace, topic string, msg *protocol.Message) error {
	f.published = append(f.published, publishedMessage{namespace, topic, msg})
	return f.publishErr
}

// fakeSource is a test double for the messageSource interface.
type fakeSource struct {
	msgs chan *protocol.Message
	errs chan error
}

func newFakeSource() *fakeSource {
	return &fakeSource{
		msgs: make(chan *protocol.Message, 8),
		errs: make(chan error, 8),
	}
}

func (f *fakeSource) Messages() <-chan *protocol.Message { return f.msgs }
func (f *fakeSource) Errors() <-chan error               { return f.errs }

// writeSpecFile writes content to a file with the given name under a temp dir.
func writeSpecFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write spec file: %v", err)
	}
	return path
}

// minimal valid guild specs reused across command tests.
const (
	validGuildJSON = `{"name":"Echo","description":"d","agents":[{"id":"a1","name":"EchoAgent","class_name":"pkg.Echo"}]}`
	blueprintJSON  = `{"name":"BP","spec":{"name":"Inner","description":"d","agents":[{"id":"a1","name":"EchoAgent","class_name":"pkg.Echo"}]}}`
)
