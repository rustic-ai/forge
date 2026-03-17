package scheduler

import (
	"sync"
	"time"
)

// SpawnState tracks the lifecycle of a dispatched agent spawn.
type SpawnState string

const (
	SpawnDispatched   SpawnState = "dispatched"
	SpawnAcknowledged SpawnState = "acknowledged"
	SpawnRunning      SpawnState = "running"
	SpawnFailed       SpawnState = "failed"
)

type AgentPlacement struct {
	GuildID      string
	AgentID      string
	NodeID       string
	State        SpawnState
	DispatchedAt time.Time
	AckedAt      time.Time
	Attempts     int
	PlacedAt     time.Time // kept for backward compat
	Payload      []byte
}

type PlacementMap struct {
	mu         sync.RWMutex
	placements map[string]AgentPlacement // map[guildID:agentID]AgentPlacement
}

func NewPlacementMap() *PlacementMap {
	return &PlacementMap{
		placements: make(map[string]AgentPlacement),
	}
}

// Place adds or replaces a placement entry. Sets State=Dispatched and DispatchedAt for backward compat.
func (p *PlacementMap) Place(guildID, agentID, nodeID string, payload []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	key := guildID + ":" + agentID
	p.placements[key] = AgentPlacement{
		GuildID:      guildID,
		AgentID:      agentID,
		NodeID:       nodeID,
		State:        SpawnDispatched,
		DispatchedAt: now,
		Attempts:     1,
		PlacedAt:     now,
		Payload:      payload,
	}
}

// MarkDispatched upserts an entry with State=Dispatched, increments Attempts, and returns the new count.
// If the existing entry is in Failed state, attempts reset to 1.
func (p *PlacementMap) MarkDispatched(guildID, agentID, nodeID string, payload []byte) int {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	key := guildID + ":" + agentID
	existing, exists := p.placements[key]

	attempts := 1
	if exists && existing.State != SpawnFailed {
		attempts = existing.Attempts + 1
	}

	p.placements[key] = AgentPlacement{
		GuildID:      guildID,
		AgentID:      agentID,
		NodeID:       nodeID,
		State:        SpawnDispatched,
		DispatchedAt: now,
		Attempts:     attempts,
		PlacedAt:     now,
		Payload:      payload,
	}
	return attempts
}

// MarkAcknowledged transitions an entry to Acknowledged state.
func (p *PlacementMap) MarkAcknowledged(guildID, agentID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	key := guildID + ":" + agentID
	if entry, ok := p.placements[key]; ok {
		entry.State = SpawnAcknowledged
		entry.AckedAt = time.Now()
		p.placements[key] = entry
	}
}

// MarkRunning transitions an entry to Running state.
func (p *PlacementMap) MarkRunning(guildID, agentID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	key := guildID + ":" + agentID
	if entry, ok := p.placements[key]; ok {
		entry.State = SpawnRunning
		p.placements[key] = entry
	}
}

// MarkFailed transitions an entry to Failed state.
func (p *PlacementMap) MarkFailed(guildID, agentID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	key := guildID + ":" + agentID
	if entry, ok := p.placements[key]; ok {
		entry.State = SpawnFailed
		p.placements[key] = entry
	}
}

// IsActivelyTracked returns true if the entry exists and is in Dispatched, Acknowledged, or Running state.
func (p *PlacementMap) IsActivelyTracked(guildID, agentID string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	key := guildID + ":" + agentID
	entry, ok := p.placements[key]
	if !ok {
		return false
	}
	return entry.State == SpawnDispatched || entry.State == SpawnAcknowledged || entry.State == SpawnRunning
}

// GetStaleDispatches returns entries in Dispatched state where DispatchedAt is older than timeout.
func (p *PlacementMap) GetStaleDispatches(timeout time.Duration) []AgentPlacement {
	p.mu.RLock()
	defer p.mu.RUnlock()

	now := time.Now()
	var result []AgentPlacement
	for _, entry := range p.placements {
		if entry.State == SpawnDispatched && now.Sub(entry.DispatchedAt) > timeout {
			result = append(result, entry)
		}
	}
	return result
}

// GetStaleAcks returns entries in Acknowledged state where AckedAt is older than timeout.
func (p *PlacementMap) GetStaleAcks(timeout time.Duration) []AgentPlacement {
	p.mu.RLock()
	defer p.mu.RUnlock()

	now := time.Now()
	var result []AgentPlacement
	for _, entry := range p.placements {
		if entry.State == SpawnAcknowledged && now.Sub(entry.AckedAt) > timeout {
			result = append(result, entry)
		}
	}
	return result
}

// GetFailedOlderThan returns entries in Failed state where DispatchedAt is older than age.
func (p *PlacementMap) GetFailedOlderThan(age time.Duration) []AgentPlacement {
	p.mu.RLock()
	defer p.mu.RUnlock()

	now := time.Now()
	var result []AgentPlacement
	for _, entry := range p.placements {
		if entry.State == SpawnFailed && now.Sub(entry.DispatchedAt) > age {
			result = append(result, entry)
		}
	}
	return result
}

func (p *PlacementMap) Remove(guildID, agentID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	key := guildID + ":" + agentID
	delete(p.placements, key)
}

func (p *PlacementMap) Find(guildID, agentID string) (AgentPlacement, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	key := guildID + ":" + agentID
	placement, ok := p.placements[key]
	return placement, ok
}

func (p *PlacementMap) AgentsOnNode(nodeID string) []AgentPlacement {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var result []AgentPlacement
	for _, placement := range p.placements {
		if placement.NodeID == nodeID {
			result = append(result, placement)
		}
	}
	return result
}

// Global placement map for the server
var GlobalPlacementMap = NewPlacementMap()
var GlobalScheduler = NewScheduler(GlobalNodeRegistry)
