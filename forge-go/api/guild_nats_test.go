package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rustic-ai/forge/forge-go/control"
	"github.com/rustic-ai/forge/forge-go/filesystem"
	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/supervisor"
	"github.com/rustic-ai/forge/forge-go/testutil/natstest"
)

func setupNATSTestServer(t *testing.T) (*Server, control.ControlPlane, supervisor.AgentStatusStore, store.Store, *http.ServeMux, func()) {
	t.Helper()

	s := natstest.StartServer(t)

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(func() { nc.Close() })

	msgBackend, err := messaging.NewNATSBackend(nc)
	require.NoError(t, err)
	t.Cleanup(func() { _ = msgBackend.Close() })

	controlPlane, err := control.NewNATSControlTransport(nc)
	require.NoError(t, err)

	statusStore, err := supervisor.NewNATSAgentStatusStore(nc)
	require.NoError(t, err)

	dbPath := filepath.Join(t.TempDir(), "test.db")
	dbStore, err := store.NewGormStore(store.DriverSQLite, dbPath)
	require.NoError(t, err)

	fsPath := filepath.Join(t.TempDir(), "files")
	resolver := filesystem.NewFileSystemResolver(fsPath)
	fileStore := filesystem.NewLocalFileStore(resolver)

	require.NoError(t, os.MkdirAll("conf", 0755))
	require.NoError(t, os.WriteFile("conf/agent-dependencies.yaml", []byte(fmt.Sprintf(`
filesystem:
  class_name: rustic_ai.core.guild.agent_ext.depends.filesystem.filesystem.FileSystemResolver
  properties:
    path_base: %s
    protocol: file
    storage_options:
      auto_mkdir: true
`, fsPath)), 0644))

	srv := NewServer(dbStore, statusStore, controlPlane, msgBackend, fileStore, ":0")

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/guilds", srv.HandleCreateGuild)
	mux.HandleFunc("GET /api/guilds/{id}", srv.HandleGetGuild)
	mux.HandleFunc("POST /api/guilds/{id}/relaunch", srv.HandleRelaunchGuild)

	cleanup := func() {
		_ = os.RemoveAll("conf")
	}

	return srv, controlPlane, statusStore, dbStore, mux, cleanup
}

func TestHandleCreateGuild_NATS(t *testing.T) {
	_, _, _, dbStore, mux, cleanup := setupNATSTestServer(t)
	defer cleanup()

	reqPayload := CreateGuildRequest{
		Spec: &protocol.GuildSpec{
			ID:          "test-guild-nats",
			Name:        "Test Guild NATS",
			Description: "A guild for NATS API testing",
			Agents: []protocol.AgentSpec{
				{
					ID:        "agent-1",
					Name:      "Agent 1",
					ClassName: "test.Agent",
				},
			},
		},
		OrganizationID: "org-nats-123",
	}

	body, err := json.Marshal(reqPayload)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/guilds", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusCreated, rr.Code)

	var resp map[string]interface{}
	err = json.Unmarshal(rr.Body.Bytes(), &resp)
	require.NoError(t, err)

	guildID := resp["id"].(string)
	assert.NotEmpty(t, guildID)

	model, err := dbStore.GetGuild(guildID)
	require.NoError(t, err)
	assert.Equal(t, guildID, model.ID)
	assert.Equal(t, store.GuildStatusRequested, model.Status)
}

func TestHandleRelaunchGuild_EnqueuesWhenManagerNotRunning_NATS(t *testing.T) {
	_, controlPlane, _, dbStore, mux, cleanup := setupNATSTestServer(t)
	defer cleanup()

	guildID := "relaunch-guild-nats-1"
	guildModel := &store.GuildModel{
		ID:             guildID,
		Name:           "Relaunch Guild NATS",
		Description:    "test",
		OrganizationID: "org-1",
		BackendConfig:  store.JSONB{},
		DependencyMap:  store.JSONB{},
		Status:         store.GuildStatusRequested,
	}
	require.NoError(t, dbStore.CreateGuild(guildModel))
	require.NoError(t, dbStore.CreateAgent(&store.AgentModel{
		ID:        guildID + "#a-0",
		GuildID:   &guildID,
		Name:      "Echo Agent",
		ClassName: "rustic_ai.core.agents.testutils.echo_agent.EchoAgent",
		Status:    store.AgentStatusPendingLaunch,
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/guilds/"+guildID+"/relaunch", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["is_relaunching"])

	// Verify the spawn request was pushed to the control plane
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	data, err := controlPlane.Pop(ctx, "forge:control:requests", 3*time.Second)
	require.NoError(t, err)
	require.NotEmpty(t, data)

	var wrapper struct {
		Command string                `json:"command"`
		Payload protocol.SpawnRequest `json:"payload"`
	}
	require.NoError(t, json.Unmarshal(data, &wrapper))
	assert.Equal(t, "spawn", wrapper.Command)
	assert.Equal(t, guildID, wrapper.Payload.GuildID)
	assert.Equal(t, guildID+"#manager_agent", wrapper.Payload.AgentSpec.ID)
}

func TestHandleRelaunchGuild_NoOpWhenManagerRunning_NATS(t *testing.T) {
	_, controlPlane, statusStore, dbStore, mux, cleanup := setupNATSTestServer(t)
	defer cleanup()

	guildID := "relaunch-guild-nats-2"
	guildModel := &store.GuildModel{
		ID:             guildID,
		Name:           "Relaunch Guild NATS",
		Description:    "test",
		OrganizationID: "org-1",
		BackendConfig:  store.JSONB{},
		DependencyMap:  store.JSONB{},
		Status:         store.GuildStatusRequested,
	}
	require.NoError(t, dbStore.CreateGuild(guildModel))

	// Write running status for the manager agent
	ctx := context.Background()
	managerID := guildID + "#manager_agent"
	require.NoError(t, statusStore.WriteStatus(ctx, guildID, managerID, &supervisor.AgentStatusJSON{
		State:     "running",
		Timestamp: time.Now(),
	}, 60*time.Second))

	req := httptest.NewRequest(http.MethodPost, "/api/guilds/"+guildID+"/relaunch", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, false, resp["is_relaunching"])

	// Verify no spawn request was pushed (Pop returns nil,nil on timeout)
	data, err := controlPlane.Pop(ctx, "forge:control:requests", 500*time.Millisecond)
	require.NoError(t, err)
	assert.Nil(t, data, "expected no message in the control queue")
}
