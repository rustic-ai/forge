package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newManagerTestServer(t *testing.T) (*httptest.Server, store.Store) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "manager_api_test.db")
	dbStore, err := store.NewGormStore(store.DriverSQLite, dbPath)
	require.NoError(t, err)

	srv := NewServer(dbStore, nil, nil, nil, ":0")
	mux := http.NewServeMux()
	mux.HandleFunc("POST /manager/guilds/ensure", srv.HandleManagerEnsureGuild)
	mux.HandleFunc("GET /manager/guilds/{guild_id}/spec", srv.HandleManagerGetGuildSpec)
	mux.HandleFunc("PATCH /manager/guilds/{guild_id}/status", srv.HandleManagerUpdateGuildStatus)
	mux.HandleFunc("POST /manager/guilds/{guild_id}/agents/ensure", srv.HandleManagerEnsureAgent)
	mux.HandleFunc("PATCH /manager/guilds/{guild_id}/agents/{agent_id}/status", srv.HandleManagerUpdateAgentStatus)
	mux.HandleFunc("POST /manager/guilds/{guild_id}/routes", srv.HandleManagerAddRoutingRule)
	mux.HandleFunc("DELETE /manager/guilds/{guild_id}/routes/{rule_hashid}", srv.HandleManagerRemoveRoutingRule)
	mux.HandleFunc("POST /manager/guilds/{guild_id}/lifecycle/heartbeat", srv.HandleManagerProcessHeartbeat)

	ts := httptest.NewServer(mux)
	t.Cleanup(func() {
		ts.Close()
		_ = dbStore.Close()
	})
	return ts, dbStore
}

func jsonRequest(t *testing.T, method, url string, payload any, headers map[string]string) *http.Response {
	t.Helper()
	var body bytes.Buffer
	if payload != nil {
		err := json.NewEncoder(&body).Encode(payload)
		require.NoError(t, err)
	}
	req, err := http.NewRequest(method, url, &body)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func TestManagerEnsureGuildAndGetSpec(t *testing.T) {
	ts, _ := newManagerTestServer(t)

	spec := &protocol.GuildSpec{
		ID:          "g-manager-1",
		Name:        "Manager Guild",
		Description: "for manager API test",
		Agents: []protocol.AgentSpec{
			{ID: "g-manager-1#a-0", Name: "Echo", Description: "echo", ClassName: "test.Echo"},
		},
	}

	ensureReq := EnsureGuildRequest{GuildSpec: spec, OrganizationID: "org-1"}
	resp := jsonRequest(t, http.MethodPost, ts.URL+"/manager/guilds/ensure", ensureReq, nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var ensureResp EnsureGuildResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&ensureResp))
	assert.True(t, ensureResp.WasCreated)
	assert.Equal(t, store.GuildStatusPendingLaunch, ensureResp.Status)
	require.NotNil(t, ensureResp.GuildSpec)
	assert.Equal(t, "g-manager-1", ensureResp.GuildSpec.ID)

	getResp := jsonRequest(t, http.MethodGet, ts.URL+"/manager/guilds/g-manager-1/spec", nil, nil)
	defer getResp.Body.Close()
	require.Equal(t, http.StatusOK, getResp.StatusCode)

	var getPayload GuildSpecWithStatusResponse
	require.NoError(t, json.NewDecoder(getResp.Body).Decode(&getPayload))
	assert.Equal(t, store.GuildStatusPendingLaunch, getPayload.Status)
	require.NotNil(t, getPayload.GuildSpec)
	assert.Equal(t, "g-manager-1#a-0", getPayload.GuildSpec.Agents[0].ID)

	resp2 := jsonRequest(t, http.MethodPost, ts.URL+"/manager/guilds/ensure", ensureReq, nil)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	var ensureResp2 EnsureGuildResponse
	require.NoError(t, json.NewDecoder(resp2.Body).Decode(&ensureResp2))
	assert.False(t, ensureResp2.WasCreated)
	assert.Equal(t, store.GuildStatusStarting, ensureResp2.Status)
}

func TestManagerAgentRouteAndHeartbeatLifecycle(t *testing.T) {
	ts, _ := newManagerTestServer(t)

	spec := &protocol.GuildSpec{ID: "g-manager-2", Name: "G2", Description: "d"}
	ensureReq := EnsureGuildRequest{GuildSpec: spec, OrganizationID: "org-2"}
	ensureResp := jsonRequest(t, http.MethodPost, ts.URL+"/manager/guilds/ensure", ensureReq, nil)
	_ = ensureResp.Body.Close()
	require.Equal(t, http.StatusOK, ensureResp.StatusCode)

	agentSpec := protocol.AgentSpec{ID: "g-manager-2#a-1", Name: "A1", Description: "d", ClassName: "test.Agent"}
	agResp := jsonRequest(t, http.MethodPost, ts.URL+"/manager/guilds/g-manager-2/agents/ensure", agentSpec, nil)
	defer agResp.Body.Close()
	require.Equal(t, http.StatusCreated, agResp.StatusCode)

	updReq := UpdateAgentStatusRequest{Status: store.AgentStatusStarting}
	updResp := jsonRequest(
		t,
		http.MethodPatch,
		ts.URL+"/manager/guilds/g-manager-2/agents/g-manager-2%23a-1/status",
		updReq,
		nil,
	)
	defer updResp.Body.Close()
	require.Equal(t, http.StatusOK, updResp.StatusCode)
	var updPayload UpdateAgentStatusResponse
	require.NoError(t, json.NewDecoder(updResp.Body).Decode(&updPayload))
	assert.True(t, updPayload.Found)
	assert.Equal(t, store.AgentStatusStarting, updPayload.Status)

	routeReq := AddRouteRequest{RoutingRule: &protocol.RoutingRule{AgentType: strPtr("test.Agent")}}
	routeResp := jsonRequest(t, http.MethodPost, ts.URL+"/manager/guilds/g-manager-2/routes", routeReq, nil)
	defer routeResp.Body.Close()
	require.Equal(t, http.StatusCreated, routeResp.StatusCode)
	var routePayload AddRouteResponse
	require.NoError(t, json.NewDecoder(routeResp.Body).Decode(&routePayload))
	require.NotEmpty(t, routePayload.RuleHashID)

	rmResp := jsonRequest(
		t,
		http.MethodDelete,
		ts.URL+"/manager/guilds/g-manager-2/routes/"+routePayload.RuleHashID,
		nil,
		nil,
	)
	defer rmResp.Body.Close()
	require.Equal(t, http.StatusOK, rmResp.StatusCode)
	var rmPayload RemoveRouteResponse
	require.NoError(t, json.NewDecoder(rmResp.Body).Decode(&rmPayload))
	assert.True(t, rmPayload.Deleted)

	hbReq := HeartbeatStatusUpdateRequest{
		AgentID:     "g-manager-2#a-1",
		AgentStatus: store.AgentStatusRunning,
		GuildStatus: store.GuildStatusRunning,
	}
	hbResp := jsonRequest(t, http.MethodPost, ts.URL+"/manager/guilds/g-manager-2/lifecycle/heartbeat", hbReq, nil)
	defer hbResp.Body.Close()
	require.Equal(t, http.StatusOK, hbResp.StatusCode)
	var hbPayload HeartbeatStatusUpdateResponse
	require.NoError(t, json.NewDecoder(hbResp.Body).Decode(&hbPayload))
	assert.True(t, hbPayload.AgentFound)
	assert.Equal(t, store.AgentStatusRunning, hbPayload.AgentStatus)
	assert.Equal(t, store.GuildStatusRunning, hbPayload.GuildStatus)
}

func TestManagerEndpointAuthToken(t *testing.T) {
	ts, _ := newManagerTestServer(t)
	t.Setenv("FORGE_MANAGER_API_TOKEN", "secret-token")

	req := EnsureGuildRequest{
		GuildSpec:      &protocol.GuildSpec{ID: "g-auth", Name: "Auth", Description: "d"},
		OrganizationID: "org-auth",
	}

	unauth := jsonRequest(t, http.MethodPost, ts.URL+"/manager/guilds/ensure", req, nil)
	defer unauth.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, unauth.StatusCode)

	auth := jsonRequest(
		t,
		http.MethodPost,
		ts.URL+"/manager/guilds/ensure",
		req,
		map[string]string{"X-Forge-Manager-Token": "secret-token"},
	)
	defer auth.Body.Close()
	assert.Equal(t, http.StatusOK, auth.StatusCode)
}

func TestManagerEnsureGuild_CreatesWithResolvedFilesystemPathBase(t *testing.T) {
	ts, _ := newManagerTestServer(t)
	globalRoot := filepath.Join(t.TempDir(), "workspaces")
	t.Setenv("FORGE_FILESYSTEM_GLOBAL_ROOT", globalRoot)

	spec := &protocol.GuildSpec{
		ID:          "g-manager-fs",
		Name:        "Manager FS",
		Description: "persists resolved filesystem path",
		DependencyMap: map[string]protocol.DependencySpec{
			"filesystem": {
				ClassName: "rustic_ai.core.guild.agent_ext.depends.filesystem.FileSystemResolver",
				Properties: map[string]interface{}{
					"path_base": "uploads",
					"protocol":  "file",
				},
			},
		},
		Agents: []protocol.AgentSpec{
			{
				ID:          "g-manager-fs#a-0",
				Name:        "Worker",
				Description: "worker",
				ClassName:   "test.Agent",
				DependencyMap: map[string]protocol.DependencySpec{
					"filesystem": {
						ClassName: "rustic_ai.core.guild.agent_ext.depends.filesystem.FileSystemResolver",
						Properties: map[string]interface{}{
							"path_base": "private",
						},
					},
				},
			},
		},
	}

	ensureReq := EnsureGuildRequest{GuildSpec: spec, OrganizationID: "org-fs"}
	resp := jsonRequest(t, http.MethodPost, ts.URL+"/manager/guilds/ensure", ensureReq, nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var ensureResp EnsureGuildResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&ensureResp))
	require.NotNil(t, ensureResp.GuildSpec)

	fsDep, ok := ensureResp.GuildSpec.DependencyMap["filesystem"]
	require.True(t, ok)
	assert.Equal(t, filepath.Join(globalRoot, "uploads"), fsDep.Properties["path_base"])
	require.Len(t, ensureResp.GuildSpec.Agents, 1)
	agentDep, ok := ensureResp.GuildSpec.Agents[0].DependencyMap["filesystem"]
	require.True(t, ok)
	assert.Equal(t, filepath.Join(globalRoot, "private"), agentDep.Properties["path_base"])

	resp2 := jsonRequest(t, http.MethodPost, ts.URL+"/manager/guilds/ensure", ensureReq, nil)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode)

	var ensureResp2 EnsureGuildResponse
	require.NoError(t, json.NewDecoder(resp2.Body).Decode(&ensureResp2))
	require.NotNil(t, ensureResp2.GuildSpec)
	fsDep2, ok := ensureResp2.GuildSpec.DependencyMap["filesystem"]
	require.True(t, ok)
	assert.Equal(t, filepath.Join(globalRoot, "uploads"), fsDep2.Properties["path_base"])
	require.Len(t, ensureResp2.GuildSpec.Agents, 1)
	agentDep2, ok := ensureResp2.GuildSpec.Agents[0].DependencyMap["filesystem"]
	require.True(t, ok)
	assert.Equal(t, filepath.Join(globalRoot, "private"), agentDep2.Properties["path_base"])
}

func TestManagerEnsureGuild_RejectsFilesystemTraversal(t *testing.T) {
	ts, _ := newManagerTestServer(t)
	t.Setenv("FORGE_FILESYSTEM_GLOBAL_ROOT", filepath.Join(t.TempDir(), "workspaces"))

	spec := &protocol.GuildSpec{
		ID:          "g-manager-bad-fs",
		Name:        "Manager Bad FS",
		Description: "rejects traversal",
		DependencyMap: map[string]protocol.DependencySpec{
			"filesystem": {
				ClassName: "rustic_ai.core.guild.agent_ext.depends.filesystem.FileSystemResolver",
				Properties: map[string]interface{}{
					"path_base": "../escape",
					"protocol":  "file",
				},
			},
		},
	}

	ensureReq := EnsureGuildRequest{GuildSpec: spec, OrganizationID: "org-fs"}
	resp := jsonRequest(t, http.MethodPost, ts.URL+"/manager/guilds/ensure", ensureReq, nil)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusUnprocessableEntity, resp.StatusCode)
}

func TestManagerEnsureGuild_CreatesWithS3FilesystemGlobalRoot(t *testing.T) {
	ts, _ := newManagerTestServer(t)
	t.Setenv("FORGE_FILESYSTEM_GLOBAL_ROOT", "s3://forge-bucket/root")

	spec := &protocol.GuildSpec{
		ID:          "g-manager-s3",
		Name:        "Manager S3",
		Description: "persists resolved object store path",
		DependencyMap: map[string]protocol.DependencySpec{
			"filesystem": {
				ClassName: "rustic_ai.core.guild.agent_ext.depends.filesystem.FileSystemResolver",
				Properties: map[string]interface{}{
					"path_base": "uploads",
				},
			},
		},
		Agents: []protocol.AgentSpec{
			{
				ID:          "g-manager-s3#a-0",
				Name:        "Worker",
				Description: "worker",
				ClassName:   "test.Agent",
				DependencyMap: map[string]protocol.DependencySpec{
					"filesystem": {
						ClassName: "rustic_ai.core.guild.agent_ext.depends.filesystem.FileSystemResolver",
						Properties: map[string]interface{}{
							"path_base": "private",
						},
					},
				},
			},
		},
	}

	ensureReq := EnsureGuildRequest{GuildSpec: spec, OrganizationID: "org-s3"}
	resp := jsonRequest(t, http.MethodPost, ts.URL+"/manager/guilds/ensure", ensureReq, nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var ensureResp EnsureGuildResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&ensureResp))
	require.NotNil(t, ensureResp.GuildSpec)

	fsDep, ok := ensureResp.GuildSpec.DependencyMap["filesystem"]
	require.True(t, ok)
	assert.Equal(t, "s3", fsDep.Properties["protocol"])
	assert.Equal(t, "s3://forge-bucket/root/uploads", fsDep.Properties["path_base"])
	require.Len(t, ensureResp.GuildSpec.Agents, 1)
	agentDep, ok := ensureResp.GuildSpec.Agents[0].DependencyMap["filesystem"]
	require.True(t, ok)
	assert.Equal(t, "s3", agentDep.Properties["protocol"])
	assert.Equal(t, "s3://forge-bucket/root/private", agentDep.Properties["path_base"])
}

func TestManagerEnsureGuild_CreatesWithGCSFilesystemGlobalRoot(t *testing.T) {
	ts, _ := newManagerTestServer(t)
	t.Setenv("FORGE_FILESYSTEM_GLOBAL_ROOT", "gs://forge-bucket/root")

	spec := &protocol.GuildSpec{
		ID:          "g-manager-gcs",
		Name:        "Manager GCS",
		Description: "persists resolved gcs path",
		DependencyMap: map[string]protocol.DependencySpec{
			"filesystem": {
				ClassName: "rustic_ai.core.guild.agent_ext.depends.filesystem.FileSystemResolver",
				Properties: map[string]interface{}{
					"path_base": "uploads",
				},
			},
		},
		Agents: []protocol.AgentSpec{
			{
				ID:          "g-manager-gcs#a-0",
				Name:        "Worker",
				Description: "worker",
				ClassName:   "test.Agent",
				DependencyMap: map[string]protocol.DependencySpec{
					"filesystem": {
						ClassName: "rustic_ai.core.guild.agent_ext.depends.filesystem.FileSystemResolver",
						Properties: map[string]interface{}{
							"path_base": "private",
						},
					},
				},
			},
		},
	}

	ensureReq := EnsureGuildRequest{GuildSpec: spec, OrganizationID: "org-gcs"}
	resp := jsonRequest(t, http.MethodPost, ts.URL+"/manager/guilds/ensure", ensureReq, nil)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var ensureResp EnsureGuildResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&ensureResp))
	require.NotNil(t, ensureResp.GuildSpec)

	fsDep, ok := ensureResp.GuildSpec.DependencyMap["filesystem"]
	require.True(t, ok)
	assert.Equal(t, "gs", fsDep.Properties["protocol"])
	assert.Equal(t, "gs://forge-bucket/root/uploads", fsDep.Properties["path_base"])
	require.Len(t, ensureResp.GuildSpec.Agents, 1)
	agentDep, ok := ensureResp.GuildSpec.Agents[0].DependencyMap["filesystem"]
	require.True(t, ok)
	assert.Equal(t, "gs", agentDep.Properties["protocol"])
	assert.Equal(t, "gs://forge-bucket/root/private", agentDep.Properties["path_base"])
}

func strPtr(v string) *string { return &v }
