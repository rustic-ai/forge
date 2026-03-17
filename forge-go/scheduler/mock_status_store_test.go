package scheduler

import (
	"context"
	"sync"
	"time"

	"github.com/rustic-ai/forge/forge-go/supervisor"
)

// mockStatusStore implements supervisor.AgentStatusStore for testing.
type mockStatusStore struct {
	mu       sync.RWMutex
	statuses map[string]*supervisor.AgentStatusJSON // key: "guildID:agentID"
}

func newMockStatusStore() *mockStatusStore {
	return &mockStatusStore{
		statuses: make(map[string]*supervisor.AgentStatusJSON),
	}
}

func (m *mockStatusStore) WriteStatus(_ context.Context, guildID, agentID string, status *supervisor.AgentStatusJSON, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statuses[guildID+":"+agentID] = status
	return nil
}

func (m *mockStatusStore) RefreshStatus(_ context.Context, _, _ string, _ time.Duration) error {
	return nil
}

func (m *mockStatusStore) GetStatus(_ context.Context, guildID, agentID string) (*supervisor.AgentStatusJSON, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := m.statuses[guildID+":"+agentID]
	return s, nil
}

func (m *mockStatusStore) DeleteStatus(_ context.Context, guildID, agentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.statuses, guildID+":"+agentID)
	return nil
}

// mockControlPusher implements protocol.ControlPusher and records pushed messages.
type mockControlPusher struct {
	mu       sync.Mutex
	messages []pushedMessage
}

type pushedMessage struct {
	QueueKey string
	Payload  []byte
}

func newMockControlPusher() *mockControlPusher {
	return &mockControlPusher{}
}

func (m *mockControlPusher) Push(_ context.Context, queueKey string, payload []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, pushedMessage{QueueKey: queueKey, Payload: payload})
	return nil
}

func (m *mockControlPusher) Messages() []pushedMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]pushedMessage, len(m.messages))
	copy(out, m.messages)
	return out
}
