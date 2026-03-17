package scheduler

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMarkDispatched_NewEntry(t *testing.T) {
	pm := NewPlacementMap()
	attempts := pm.MarkDispatched("g1", "a1", "node1", []byte(`{"test":1}`))

	assert.Equal(t, 1, attempts)

	entry, ok := pm.Find("g1", "a1")
	require.True(t, ok)
	assert.Equal(t, SpawnDispatched, entry.State)
	assert.Equal(t, "node1", entry.NodeID)
	assert.Equal(t, 1, entry.Attempts)
	assert.False(t, entry.DispatchedAt.IsZero())
}

func TestMarkDispatched_IncrementAttempts(t *testing.T) {
	pm := NewPlacementMap()
	pm.MarkDispatched("g1", "a1", "node1", []byte(`{}`))
	attempts := pm.MarkDispatched("g1", "a1", "node2", []byte(`{"retry":true}`))

	assert.Equal(t, 2, attempts)

	entry, ok := pm.Find("g1", "a1")
	require.True(t, ok)
	assert.Equal(t, "node2", entry.NodeID)
	assert.Equal(t, 2, entry.Attempts)
}

func TestMarkDispatched_ResetFromFailed(t *testing.T) {
	pm := NewPlacementMap()
	pm.MarkDispatched("g1", "a1", "node1", []byte(`{}`))
	pm.MarkDispatched("g1", "a1", "node1", []byte(`{}`)) // attempts=2
	pm.MarkFailed("g1", "a1")

	attempts := pm.MarkDispatched("g1", "a1", "node2", []byte(`{}`))
	assert.Equal(t, 1, attempts, "attempts should reset from Failed state")
}

func TestMarkDispatched_PreservesAttemptsIfActive(t *testing.T) {
	pm := NewPlacementMap()
	pm.MarkDispatched("g1", "a1", "node1", []byte(`{}`))
	pm.MarkAcknowledged("g1", "a1")

	attempts := pm.MarkDispatched("g1", "a1", "node2", []byte(`{}`))
	assert.Equal(t, 2, attempts, "attempts should increment from Acknowledged state")
}

func TestIsActivelyTracked_ByState(t *testing.T) {
	pm := NewPlacementMap()

	assert.False(t, pm.IsActivelyTracked("g1", "missing"), "missing entry should not be tracked")

	pm.MarkDispatched("g1", "a1", "node1", nil)
	assert.True(t, pm.IsActivelyTracked("g1", "a1"), "Dispatched should be tracked")

	pm.MarkAcknowledged("g1", "a1")
	assert.True(t, pm.IsActivelyTracked("g1", "a1"), "Acknowledged should be tracked")

	pm.MarkRunning("g1", "a1")
	assert.True(t, pm.IsActivelyTracked("g1", "a1"), "Running should be tracked")

	pm.MarkFailed("g1", "a1")
	assert.False(t, pm.IsActivelyTracked("g1", "a1"), "Failed should not be tracked")
}

func TestMarkAcknowledged(t *testing.T) {
	pm := NewPlacementMap()
	pm.MarkDispatched("g1", "a1", "node1", nil)
	pm.MarkAcknowledged("g1", "a1")

	entry, ok := pm.Find("g1", "a1")
	require.True(t, ok)
	assert.Equal(t, SpawnAcknowledged, entry.State)
	assert.False(t, entry.AckedAt.IsZero())
}

func TestMarkRunning(t *testing.T) {
	pm := NewPlacementMap()
	pm.MarkDispatched("g1", "a1", "node1", nil)
	pm.MarkRunning("g1", "a1")

	entry, ok := pm.Find("g1", "a1")
	require.True(t, ok)
	assert.Equal(t, SpawnRunning, entry.State)
}

func TestMarkFailed(t *testing.T) {
	pm := NewPlacementMap()
	pm.MarkDispatched("g1", "a1", "node1", nil)
	pm.MarkFailed("g1", "a1")

	entry, ok := pm.Find("g1", "a1")
	require.True(t, ok)
	assert.Equal(t, SpawnFailed, entry.State)
}

func TestGetStaleDispatches(t *testing.T) {
	pm := NewPlacementMap()

	// Inject entries with old DispatchedAt via direct map manipulation
	pm.mu.Lock()
	pm.placements["g1:stale"] = AgentPlacement{
		GuildID:      "g1",
		AgentID:      "stale",
		NodeID:       "node1",
		State:        SpawnDispatched,
		DispatchedAt: time.Now().Add(-60 * time.Second),
		Attempts:     1,
	}
	pm.placements["g1:fresh"] = AgentPlacement{
		GuildID:      "g1",
		AgentID:      "fresh",
		NodeID:       "node1",
		State:        SpawnDispatched,
		DispatchedAt: time.Now(),
		Attempts:     1,
	}
	pm.placements["g1:acked"] = AgentPlacement{
		GuildID:      "g1",
		AgentID:      "acked",
		NodeID:       "node1",
		State:        SpawnAcknowledged,
		DispatchedAt: time.Now().Add(-60 * time.Second),
		AckedAt:      time.Now().Add(-60 * time.Second),
		Attempts:     1,
	}
	pm.mu.Unlock()

	stale := pm.GetStaleDispatches(30 * time.Second)
	require.Len(t, stale, 1)
	assert.Equal(t, "stale", stale[0].AgentID)
}

func TestGetStaleAcks(t *testing.T) {
	pm := NewPlacementMap()

	pm.mu.Lock()
	pm.placements["g1:stale-ack"] = AgentPlacement{
		GuildID: "g1",
		AgentID: "stale-ack",
		NodeID:  "node1",
		State:   SpawnAcknowledged,
		AckedAt: time.Now().Add(-120 * time.Second),
	}
	pm.placements["g1:fresh-ack"] = AgentPlacement{
		GuildID: "g1",
		AgentID: "fresh-ack",
		NodeID:  "node1",
		State:   SpawnAcknowledged,
		AckedAt: time.Now(),
	}
	pm.placements["g1:running"] = AgentPlacement{
		GuildID: "g1",
		AgentID: "running",
		NodeID:  "node1",
		State:   SpawnRunning,
	}
	pm.mu.Unlock()

	stale := pm.GetStaleAcks(60 * time.Second)
	require.Len(t, stale, 1)
	assert.Equal(t, "stale-ack", stale[0].AgentID)
}

func TestGetFailedOlderThan(t *testing.T) {
	pm := NewPlacementMap()

	pm.mu.Lock()
	pm.placements["g1:old-fail"] = AgentPlacement{
		GuildID:      "g1",
		AgentID:      "old-fail",
		State:        SpawnFailed,
		DispatchedAt: time.Now().Add(-10 * time.Minute),
	}
	pm.placements["g1:new-fail"] = AgentPlacement{
		GuildID:      "g1",
		AgentID:      "new-fail",
		State:        SpawnFailed,
		DispatchedAt: time.Now(),
	}
	pm.mu.Unlock()

	old := pm.GetFailedOlderThan(5 * time.Minute)
	require.Len(t, old, 1)
	assert.Equal(t, "old-fail", old[0].AgentID)
}

func TestPlace_BackwardCompat(t *testing.T) {
	pm := NewPlacementMap()
	pm.Place("g1", "a1", "node1", []byte(`{}`))

	entry, ok := pm.Find("g1", "a1")
	require.True(t, ok)
	assert.Equal(t, SpawnDispatched, entry.State)
	assert.False(t, entry.DispatchedAt.IsZero())
	assert.Equal(t, entry.PlacedAt, entry.DispatchedAt)
	assert.Equal(t, 1, entry.Attempts)
}

func TestConcurrentMarkDispatched(t *testing.T) {
	pm := NewPlacementMap()
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			guildID := "g1"
			agentID := "agent-" + string(rune('a'+idx%26))
			pm.MarkDispatched(guildID, agentID, "node1", nil)
		}(i)
	}

	wg.Wait()
	// No panic = success. Verify some entries exist.
	entry, ok := pm.Find("g1", "agent-a")
	assert.True(t, ok)
	assert.Equal(t, SpawnDispatched, entry.State)
}
