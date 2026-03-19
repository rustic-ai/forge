package store_test

import (
	"testing"

	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStore_UpdateGuildRouteStatus(t *testing.T) {
	db := setupTestDB(t)
	defer func() { _ = db.Close() }()

	guild := &store.GuildModel{ID: "g-route", Name: "G", OrganizationID: "org"}
	require.NoError(t, db.CreateGuild(guild))

	route := &store.GuildRoutes{GuildID: pointerStr("g-route"), Status: store.RouteStatusActive}
	require.NoError(t, db.CreateGuildRoute(route))
	require.NoError(t, db.UpdateGuildRouteStatus("g-route", route.ID, store.RouteStatusDeleted))

	fetched, err := db.GetGuild("g-route")
	require.NoError(t, err)
	require.Len(t, fetched.Routes, 1)
	assert.Equal(t, store.RouteStatusDeleted, fetched.Routes[0].Status)

	err = db.UpdateGuildRouteStatus("g-route", "missing", store.RouteStatusDeleted)
	assert.Equal(t, store.ErrNotFound, err)
}

func TestStore_ProcessHeartbeatStatus(t *testing.T) {
	db := setupTestDB(t)
	defer func() { _ = db.Close() }()

	guildID := "g-heartbeat"
	require.NoError(t, db.CreateGuild(&store.GuildModel{ID: guildID, Name: "HB", OrganizationID: "org"}))
	require.NoError(t, db.CreateAgent(&store.AgentModel{
		ID:        "a-1",
		GuildID:   &guildID,
		Name:      "A1",
		ClassName: "test.Agent",
		Status:    store.AgentStatusPendingLaunch,
	}))

	agentStatus, found, err := db.ProcessHeartbeatStatus(
		guildID,
		"a-1",
		store.AgentStatusRunning,
		store.GuildStatusRunning,
	)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, store.AgentStatusRunning, agentStatus)

	agentModel, err := db.GetAgent(guildID, "a-1")
	require.NoError(t, err)
	assert.Equal(t, store.AgentStatusRunning, agentModel.Status)

	guildModel, err := db.GetGuild(guildID)
	require.NoError(t, err)
	assert.Equal(t, store.GuildStatusRunning, guildModel.Status)

	_, missingFound, err := db.ProcessHeartbeatStatus(
		guildID,
		"missing-agent",
		store.AgentStatusRunning,
		store.GuildStatusWarning,
	)
	require.NoError(t, err)
	assert.False(t, missingFound)

	guildModel, err = db.GetGuild(guildID)
	require.NoError(t, err)
	assert.Equal(t, store.GuildStatusWarning, guildModel.Status)

	_, _, err = db.ProcessHeartbeatStatus("missing-guild", "a-1", store.AgentStatusRunning, store.GuildStatusRunning)
	assert.Equal(t, store.ErrNotFound, err)
}
