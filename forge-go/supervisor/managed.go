package supervisor

import (
	"sync"
	"time"
)

type AgentState string

const (
	StateStarting   AgentState = "starting"
	StateRunning    AgentState = "running"
	StateStopped    AgentState = "stopped"
	StateRestarting AgentState = "restarting"
	StateFailed     AgentState = "failed"
)

// ManagedAgent tracks the lifecycle, PID, and restart metrics for a specific agent.
type ManagedAgent struct {
	mu sync.RWMutex

	GuildID      string
	ID           string
	State        AgentState
	PID          int
	LaunchPID    int
	RestartCount int
	LastError    error

	// Timestamps
	StartedAt  time.Time
	LastExitAt time.Time

	// Channels for coordinated shutdown
	stopCh chan struct{}
}

func NewManagedAgent(guildID, id string) *ManagedAgent {
	return &ManagedAgent{
		GuildID: guildID,
		ID:      id,
		State:   StateStarting,
		stopCh:  make(chan struct{}),
	}
}

func (m *ManagedAgent) SetState(state AgentState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.State = state
}

func (m *ManagedAgent) GetState() AgentState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.State
}

func (m *ManagedAgent) SetPID(pid int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.PID = pid
	if pid > 0 {
		// LaunchPID records the PID of the most recent launch and, unlike PID,
		// is not cleared when the process exits. It lets a spawn response report
		// the PID it launched even for a short-lived agent that has already
		// exited by the time the response is built.
		m.LaunchPID = pid
	}
	m.StartedAt = time.Now()
}

func (m *ManagedAgent) ClearPID() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.PID = 0
}

func (m *ManagedAgent) GetPID() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.PID
}

// GetLaunchPID returns the PID captured at the most recent launch. It remains
// valid after the process exits (until the next launch overwrites it).
func (m *ManagedAgent) GetLaunchPID() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.LaunchPID
}

func (m *ManagedAgent) RequestStop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	select {
	case <-m.stopCh:
		// already closed
	default:
		close(m.stopCh)
	}
}

func (m *ManagedAgent) IsStopRequested() bool {
	select {
	case <-m.stopCh:
		return true
	default:
		return false
	}
}
