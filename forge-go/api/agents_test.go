package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAgentRegistry(t *testing.T) {
	// Setup generic server without full redis/db initialization just for routing tests
	server := NewServer(nil, nil, nil, nil, nil, ":9999")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/registry/agents", server.HandleListAgents)
	mux.HandleFunc("GET /api/registry/agents/{class_name}", server.HandleGetAgentByClassName)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	t.Run("ListAgents", func(t *testing.T) {
		res, err := http.Get(ts.URL + "/api/registry/agents")
		assert.NoError(t, err)
		assert.Equal(t, http.StatusOK, res.StatusCode)

		var agents map[string]AgentRegistryEntry
		err = json.NewDecoder(res.Body).Decode(&agents)
		_ = res.Body.Close()

		assert.NoError(t, err)
		assert.GreaterOrEqual(t, len(agents), 3)
		assert.Contains(t, agents, "rustic_ai.core.agents.support.support_agent.SupportAgent")
		assert.Contains(t, agents, "rustic_ai.core.agents.testutils.echo_agent.EchoAgent")
		assert.Equal(t, "SupportAgent", agents["rustic_ai.core.agents.support.support_agent.SupportAgent"].AgentName)
	})

	t.Run("GetAgentByClassName_Success", func(t *testing.T) {
		res, err := http.Get(ts.URL + "/api/registry/agents/rustic_ai.forge.agents.system.guild_manager_agent.GuildManagerAgent")
		assert.NoError(t, err)
		assert.Equal(t, http.StatusOK, res.StatusCode)

		var agent AgentRegistryEntry
		err = json.NewDecoder(res.Body).Decode(&agent)
		_ = res.Body.Close()

		assert.NoError(t, err)
		assert.Equal(t, "GuildManagerAgent", agent.AgentName)
	})

	t.Run("GetAgentByClassName_NotFound", func(t *testing.T) {
		res, err := http.Get(ts.URL + "/api/registry/agents/com.unknown.Agent")
		assert.NoError(t, err)
		assert.Equal(t, http.StatusNotFound, res.StatusCode)
	})
}
