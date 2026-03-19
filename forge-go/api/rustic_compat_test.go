package api

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/rustic-ai/forge/forge-go/control"
	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/supervisor"
	"github.com/stretchr/testify/require"
)

func TestTransformRusticMessage_ChatCompletionResponse(t *testing.T) {
	payload := json.RawMessage(`{"choices":[{"message":{"content":"hello"}}]}`)
	msg := protocol.NewMessage()
	msg.ID = 123
	msg.Format = "rustic_ai.core.guild.agent_ext.depends.llm.models.ChatCompletionResponse"
	msg.Payload = payload
	msg.Priority = int(protocol.PriorityNormal)
	msg.Topics = protocol.TopicsFromString("user_notifications:dummyuserid")
	msg.Thread = []uint64{100, 123}

	req := httptest.NewRequest(http.MethodGet, "http://localhost/rustic/api/guilds/g1/dummyuserid/messages", nil)
	out := transformRusticMessage(msg, "g1", req)

	require.Equal(t, "123", out["id"])
	require.Equal(t, "MarkdownFormat", out["format"])
	data, ok := out["data"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "hello", data["text"])
	require.Equal(t, "NORMAL", out["priority"])
}

func TestTransformRusticMessage_ParticipantListUsesLegacyUIShape(t *testing.T) {
	msg := protocol.NewMessage()
	msg.ID = 124
	msg.Format = "rustic_ai.core.agents.utils.user_proxy_agent.ParticipantList"
	msg.Payload = json.RawMessage(`{"participants":[{"id":"a-1","name":"Echo Agent","type":"bot"}]}`)
	msg.Priority = int(protocol.PriorityImportant)
	msg.Topics = protocol.TopicsFromString("user_system_notification:dummyuserid")

	req := httptest.NewRequest(http.MethodGet, "http://localhost/rustic/api/guilds/g1/dummyuserid/messages", nil)
	out := transformRusticMessage(msg, "g1", req)

	require.Equal(t, "participants", out["format"])
	require.Equal(t, "IMPORTANT", out["priority"])

	data, ok := out["data"].([]interface{})
	require.True(t, ok)
	require.Len(t, data, 1)

	first, ok := data[0].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "a-1", first["id"])
	require.Equal(t, "Echo Agent", first["name"])
	require.Equal(t, "bot", first["type"])
}

func TestRusticMessagesRoute_ShapesLegacyEnvelope(t *testing.T) {
	t.Setenv("FORGE_ENABLE_PUBLIC_API", "false")
	t.Setenv("FORGE_ENABLE_UI_API", "true")
	t.Setenv("FORGE_IDENTITY_MODE", "local")
	t.Setenv("FORGE_QUOTA_MODE", "local")

	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = rdb.Close() }()

	msgClient := messaging.NewClient(rdb)
	dbPath := filepath.Join(t.TempDir(), "rustic-compat.db")
	dbStore, err := store.NewGormStore(store.DriverSQLite, dbPath)
	require.NoError(t, err)
	defer func() { _ = dbStore.Close() }()

	s := NewServer(dbStore, supervisor.NewRedisAgentStatusStore(rdb), control.NewRedisControlTransport(rdb), msgClient, nil, ":0")
	router := s.buildRouter()

	msg := protocol.NewMessage()
	msg.ID = 999
	msg.Format = "rustic_ai.core.guild.agent_ext.depends.llm.models.ChatCompletionResponse"
	msg.Payload = json.RawMessage(`{"choices":[{"message":{"content":"echo"}}]}`)
	msg.Topics = protocol.TopicsFromString("user_notifications:dummyuserid")
	msg.Priority = int(protocol.PriorityNormal)
	msg.Thread = []uint64{999}
	msg.MessageHistory = []protocol.ProcessEntry{}
	msg.Normalize()

	require.NoError(t, msgClient.PublishMessage(context.Background(), "g1", "user_notifications:dummyuserid", &msg))

	req := httptest.NewRequest(http.MethodGet, "/rustic/api/guilds/g1/dummyuserid/messages", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var body []map[string]interface{}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	require.Len(t, body, 1)
	require.Equal(t, "999", body[0]["id"])
	require.Equal(t, "MarkdownFormat", body[0]["format"])
}

func TestRusticFileRoutes_ProxyStyleRewrite(t *testing.T) {
	t.Setenv("FORGE_ENABLE_PUBLIC_API", "false")
	t.Setenv("FORGE_ENABLE_UI_API", "true")
	t.Setenv("FORGE_IDENTITY_MODE", "local")
	t.Setenv("FORGE_QUOTA_MODE", "local")
	t.Setenv("FORGE_RUSTIC_THIS_SERVER", "http://proxy.example:3001/rustic")

	s, _, _, mux, cleanup := setupTestServer(t)
	defer cleanup()
	router := s.buildRouter()

	createReq := CreateGuildRequest{
		Spec: &protocol.GuildSpec{
			ID:          "g-rustic-files",
			Name:        "Rustic Files",
			Description: "guild for rustic file parity tests",
			Agents:      []protocol.AgentSpec{},
			Properties:  map[string]any{},
		},
		OrganizationID: "org-1",
	}
	createBody, err := json.Marshal(createReq)
	require.NoError(t, err)
	reqCreate := httptest.NewRequest(http.MethodPost, "/api/guilds", bytes.NewReader(createBody))
	reqCreate.Header.Set("Content-Type", "application/json")
	rrCreate := httptest.NewRecorder()
	mux.ServeHTTP(rrCreate, reqCreate)
	require.Equal(t, http.StatusCreated, rrCreate.Code)

	var uploadBody bytes.Buffer
	writer := multipart.NewWriter(&uploadBody)
	part, err := writer.CreateFormFile("file", "hello.txt")
	require.NoError(t, err)
	_, err = part.Write([]byte("hello"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	reqUpload := httptest.NewRequest(http.MethodPost, "/rustic/api/guilds/g-rustic-files/files/", &uploadBody)
	reqUpload.Header.Set("Content-Type", writer.FormDataContentType())
	reqUpload.Host = "localhost:3001"
	rrUpload := httptest.NewRecorder()
	router.ServeHTTP(rrUpload, reqUpload)
	require.Equal(t, http.StatusOK, rrUpload.Code)

	var uploadResp map[string]interface{}
	require.NoError(t, json.Unmarshal(rrUpload.Body.Bytes(), &uploadResp))
	require.Equal(t, "http://proxy.example:3001/rustic/api/guilds/g-rustic-files/files/hello.txt", uploadResp["url"])

	reqList := httptest.NewRequest(http.MethodGet, "/rustic/api/guilds/g-rustic-files/files/", nil)
	reqList.Host = "localhost:3001"
	rrList := httptest.NewRecorder()
	router.ServeHTTP(rrList, reqList)
	require.Equal(t, http.StatusOK, rrList.Code)

	var listResp []map[string]interface{}
	require.NoError(t, json.Unmarshal(rrList.Body.Bytes(), &listResp))
	require.Len(t, listResp, 1)
	require.Equal(t, "hello.txt", listResp[0]["name"])
	require.Equal(t, "http://proxy.example:3001/rustic/api/guilds/g-rustic-files/files/hello.txt", listResp[0]["url"])
}
