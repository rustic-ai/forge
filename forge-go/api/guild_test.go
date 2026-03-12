package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rustic-ai/forge/forge-go/filesystem"
	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/protocol"
)

func setupTestServer(t *testing.T) (*Server, *miniredis.Miniredis, store.Store, *http.ServeMux, func()) {
	mr, err := miniredis.Run()
	require.NoError(t, err)

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	dbPath := filepath.Join(t.TempDir(), "test.db")
	dbStore, err := store.NewGormStore(store.DriverSQLite, dbPath)
	require.NoError(t, err)

	// Setup file store
	fsPath := filepath.Join(t.TempDir(), "files")
	resolver := filesystem.NewFileSystemResolver(fsPath)
	fileStore := filesystem.NewLocalFileStore(resolver)

	// Create a dummy config file for dependencies
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

	// Setup messaging client
	msgClient := messaging.NewClient(rdb)

	srv := NewServer(dbStore, rdb, msgClient, fileStore, ":0")

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/guilds", srv.HandleCreateGuild)
	mux.HandleFunc("GET /api/guilds/{id}", srv.HandleGetGuild)
	mux.HandleFunc("POST /api/guilds/{id}/relaunch", srv.HandleRelaunchGuild)
	mux.HandleFunc("POST /api/guilds/{id}/files/", srv.HandleFileUpload)
	mux.HandleFunc("GET /api/guilds/{id}/files/", srv.HandleFileList)
	mux.HandleFunc("GET /api/guilds/{id}/files/{filename}", srv.HandleFileDownload)
	mux.HandleFunc("DELETE /api/guilds/{id}/files/{filename}", srv.HandleFileDelete)
	mux.HandleFunc("POST /api/guilds/{id}/agents/{agent_id}/files/", srv.HandleAgentFileUpload)
	mux.HandleFunc("GET /api/guilds/{id}/agents/{agent_id}/files/", srv.HandleAgentFileList)
	mux.HandleFunc("GET /api/guilds/{id}/agents/{agent_id}/files/{filename}", srv.HandleAgentFileDownload)
	mux.HandleFunc("DELETE /api/guilds/{id}/agents/{agent_id}/files/{filename}", srv.HandleAgentFileDelete)

	cleanup := func() {
		rdb.Close()
		mr.Close()
		os.RemoveAll("conf")
	}

	return srv, mr, dbStore, mux, cleanup
}

func TestHandleCreateGuild(t *testing.T) {
	_, _, dbStore, mux, cleanup := setupTestServer(t)
	defer cleanup()

	reqPayload := CreateGuildRequest{
		Spec: &protocol.GuildSpec{
			ID:          "test-guild",
			Name:        "Test Guild",
			Description: "A guild for API testing",
			Agents: []protocol.AgentSpec{
				{
					ID:        "agent-1",
					Name:      "Agent 1",
					ClassName: "test.Agent",
				},
			},
		},
		OrganizationID: "org-123",
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

	// Verify in DB
	model, err := dbStore.GetGuild(guildID)
	require.NoError(t, err)
	assert.Equal(t, guildID, model.ID)
	assert.Equal(t, store.GuildStatusRequested, model.Status)
}

func TestHandleGetGuild(t *testing.T) {
	_, _, dbStore, mux, cleanup := setupTestServer(t)
	defer cleanup()

	// Seed DB
	spec := &protocol.GuildSpec{
		ID:          "test-get-guild",
		Name:        "Test Get Guild",
		Description: "A guild for testing GET",
		Agents: []protocol.AgentSpec{
			{ID: "agent-1"},
		},
		Properties: make(map[string]interface{}),
	}
	model := store.FromGuildSpec(spec, "org-456")
	model.ID = "test-get-guild"
	model.Status = store.GuildStatus("running")
	err := dbStore.CreateGuild(model)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/guilds/test-get-guild", nil)
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]interface{}
	err = json.Unmarshal(rr.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.Equal(t, "test-get-guild", resp["id"])
	assert.Equal(t, "Test Get Guild", resp["name"])
	assert.Equal(t, "running", resp["status"])
}

func TestHandleRelaunchGuild_EnqueuesWhenManagerNotRunning(t *testing.T) {
	_, mr, dbStore, mux, cleanup := setupTestServer(t)
	defer cleanup()

	guildID := "relaunch-guild-1"
	guildModel := &store.GuildModel{
		ID:             guildID,
		Name:           "Relaunch Guild",
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

	items, err := mr.List("forge:control:requests")
	require.NoError(t, err)
	require.Len(t, items, 1)

	var wrapper struct {
		Command string                `json:"command"`
		Payload protocol.SpawnRequest `json:"payload"`
	}
	require.NoError(t, json.Unmarshal([]byte(items[0]), &wrapper))
	assert.Equal(t, "spawn", wrapper.Command)
	assert.Equal(t, guildID, wrapper.Payload.GuildID)
	assert.Equal(t, guildID+"#manager_agent", wrapper.Payload.AgentSpec.ID)
}

func TestHandleRelaunchGuild_NoOpWhenManagerRunning(t *testing.T) {
	_, mr, dbStore, mux, cleanup := setupTestServer(t)
	defer cleanup()

	guildID := "relaunch-guild-2"
	guildModel := &store.GuildModel{
		ID:             guildID,
		Name:           "Relaunch Guild",
		Description:    "test",
		OrganizationID: "org-1",
		BackendConfig:  store.JSONB{},
		DependencyMap:  store.JSONB{},
		Status:         store.GuildStatusRequested,
	}
	require.NoError(t, dbStore.CreateGuild(guildModel))

	statusKey := "forge:agent:status:" + guildID + ":" + guildID + "#manager_agent"
	require.NoError(t, mr.Set(statusKey, `{"state":"running"}`))

	req := httptest.NewRequest(http.MethodPost, "/api/guilds/"+guildID+"/relaunch", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.Equal(t, false, resp["is_relaunching"])
	if !mr.Exists("forge:control:requests") {
		return
	}
	items, err := mr.List("forge:control:requests")
	require.NoError(t, err)
	assert.Len(t, items, 0)
}
