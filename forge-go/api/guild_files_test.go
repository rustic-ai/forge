package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/rustic-ai/forge/forge-go/filesystem"
	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleFileUploadAndDownload(t *testing.T) {
	_, _, _, mux, cleanup := setupTestServer(t)
	defer cleanup()

	createReq := CreateGuildRequest{
		Spec: &protocol.GuildSpec{
			ID:          "test-guild-abc",
			Name:        "File API Test Guild",
			Description: "guild for file tests",
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
	assert.Equal(t, http.StatusCreated, rrCreate.Code)

	// 1. Upload Test
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	fw, err := w.CreateFormFile("file", "hello.txt")
	require.NoError(t, err)
	_, err = fw.Write([]byte("Hello, FileSystem!"))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	req := httptest.NewRequest(http.MethodPost, "/api/guilds/test-guild-abc/files/", &b)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rr := httptest.NewRecorder()

	mux.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)

	// 2. List Test
	reqList := httptest.NewRequest(http.MethodGet, "/api/guilds/test-guild-abc/files/", nil)
	rrList := httptest.NewRecorder()
	mux.ServeHTTP(rrList, reqList)

	assert.Equal(t, http.StatusOK, rrList.Code)
	var files []MediaLink
	err = json.Unmarshal(rrList.Body.Bytes(), &files)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, "hello.txt", files[0].Name)
	assert.Equal(t, "application/octet-stream", files[0].MimeType)

	// 3. Download Test
	reqDown := httptest.NewRequest(http.MethodGet, "/api/guilds/test-guild-abc/files/hello.txt", nil)
	rrDown := httptest.NewRecorder()
	mux.ServeHTTP(rrDown, reqDown)

	assert.Equal(t, http.StatusOK, rrDown.Code)
	assert.Contains(t, rrDown.Header().Get("Content-Type"), "text/plain")
	assert.Equal(t, "Hello, FileSystem!", rrDown.Body.String())

	// 4. Delete Test
	reqDel := httptest.NewRequest(http.MethodDelete, "/api/guilds/test-guild-abc/files/hello.txt", nil)
	rrDel := httptest.NewRecorder()
	mux.ServeHTTP(rrDel, reqDel)

	assert.Equal(t, http.StatusNoContent, rrDel.Code)

	// Verify Deletion
	reqList2 := httptest.NewRequest(http.MethodGet, "/api/guilds/test-guild-abc/files/", nil)
	rrList2 := httptest.NewRecorder()
	mux.ServeHTTP(rrList2, reqList2)
	var filesAfter []MediaLink
	require.NoError(t, json.Unmarshal(rrList2.Body.Bytes(), &filesAfter))
	assert.Len(t, filesAfter, 0)
}

func TestHandleFileDownload_UsesRewrittenFilesystemPaths(t *testing.T) {
	globalRoot := filepath.Join(t.TempDir(), "global_root")
	t.Setenv("FORGE_FILESYSTEM_GLOBAL_ROOT", globalRoot)

	_, _, dbStore, mux, cleanup := setupTestServer(t)
	defer cleanup()

	const (
		guildID = "test-guild-paths"
		agentID = "test-guild-paths#a-0"
	)

	createGuildWithFilesystemDependency(t, mux, guildID, agentID, "base_path")

	persistedBase := persistedFilesystemPathBase(t, dbStore, guildID)
	require.Equal(t, filepath.Join(globalRoot, "base_path"), persistedBase)

	guildFilePath := filepath.Join(persistedBase, "org-1", guildID, filesystem.GuildGlobalScope, "guild.txt")
	agentFilePath := filepath.Join(persistedBase, "org-1", guildID, agentID, "agent.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(guildFilePath), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Dir(agentFilePath), 0o755))
	require.NoError(t, os.WriteFile(guildFilePath, []byte("guild file body"), 0o644))
	require.NoError(t, os.WriteFile(agentFilePath, []byte("agent file body"), 0o644))

	reqGuild := httptest.NewRequest(http.MethodGet, "/api/guilds/"+guildID+"/files/guild.txt", nil)
	rrGuild := httptest.NewRecorder()
	mux.ServeHTTP(rrGuild, reqGuild)
	require.Equal(t, http.StatusOK, rrGuild.Code)
	assert.Contains(t, rrGuild.Header().Get("Content-Type"), "text/plain")
	assert.Equal(t, "guild file body", rrGuild.Body.String())

	reqAgent := httptest.NewRequest(http.MethodGet, "/api/guilds/"+guildID+"/agents/"+agentID+"/files/agent.txt", nil)
	rrAgent := httptest.NewRecorder()
	mux.ServeHTTP(rrAgent, reqAgent)
	require.Equal(t, http.StatusOK, rrAgent.Code)
	assert.Contains(t, rrAgent.Header().Get("Content-Type"), "text/plain")
	assert.Equal(t, "agent file body", rrAgent.Body.String())
}

func TestHandleFileUpload_WritesToRewrittenFilesystemPaths(t *testing.T) {
	globalRoot := filepath.Join(t.TempDir(), "global_root")
	t.Setenv("FORGE_FILESYSTEM_GLOBAL_ROOT", globalRoot)

	_, _, dbStore, mux, cleanup := setupTestServer(t)
	defer cleanup()

	const (
		guildID = "test-guild-uploads"
		agentID = "test-guild-uploads#a-0"
	)

	createGuildWithFilesystemDependency(t, mux, guildID, agentID, "base_path")

	persistedBase := persistedFilesystemPathBase(t, dbStore, guildID)
	require.Equal(t, filepath.Join(globalRoot, "base_path"), persistedBase)

	guildResp := uploadFile(t, mux, "/api/guilds/"+guildID+"/files/", "guild-upload.txt", "guild upload body")
	require.Equal(t, http.StatusOK, guildResp.Code)

	agentResp := uploadFile(t, mux, "/api/guilds/"+guildID+"/agents/"+agentID+"/files/", "agent-upload.txt", "agent upload body")
	require.Equal(t, http.StatusOK, agentResp.Code)

	guildFilePath := filepath.Join(persistedBase, "org-1", guildID, filesystem.GuildGlobalScope, "guild-upload.txt")
	agentFilePath := filepath.Join(persistedBase, "org-1", guildID, agentID, "agent-upload.txt")

	guildContent, err := os.ReadFile(guildFilePath)
	require.NoError(t, err)
	assert.Equal(t, "guild upload body", string(guildContent))

	agentContent, err := os.ReadFile(agentFilePath)
	require.NoError(t, err)
	assert.Equal(t, "agent upload body", string(agentContent))

	guildMetaPath := filepath.Join(filepath.Dir(guildFilePath), ".guild-upload.txt.meta")
	agentMetaPath := filepath.Join(filepath.Dir(agentFilePath), ".agent-upload.txt.meta")

	guildMeta, err := os.ReadFile(guildMetaPath)
	require.NoError(t, err)
	assert.Contains(t, string(guildMeta), "\"content_length\":17")
	assert.Contains(t, string(guildMeta), "\"content_type\":\"application/octet-stream\"")

	agentMeta, err := os.ReadFile(agentMetaPath)
	require.NoError(t, err)
	assert.Contains(t, string(agentMeta), "\"content_length\":17")
	assert.Contains(t, string(agentMeta), "\"content_type\":\"application/octet-stream\"")
}

func createGuildWithFilesystemDependency(t *testing.T, mux *http.ServeMux, guildID, agentID, pathBase string) {
	t.Helper()

	createReq := CreateGuildRequest{
		Spec: &protocol.GuildSpec{
			ID:          guildID,
			Name:        "File API Path Test Guild",
			Description: "guild for rewritten filesystem path tests",
			Agents: []protocol.AgentSpec{
				{
					ID:        agentID,
					Name:      "Path Test Agent",
					ClassName: "test.Agent",
				},
			},
			Properties: map[string]any{},
			DependencyMap: map[string]protocol.DependencySpec{
				"filesystem": {
					ClassName: "rustic_ai.core.guild.agent_ext.depends.filesystem.filesystem.FileSystemResolver",
					Properties: map[string]any{
						"path_base": pathBase,
						"protocol":  "file",
						"storage_options": map[string]any{
							"auto_mkdir": true,
						},
					},
				},
			},
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
}

func persistedFilesystemPathBase(t *testing.T, dbStore store.Store, guildID string) string {
	t.Helper()

	model, err := dbStore.GetGuild(guildID)
	require.NoError(t, err)

	spec := store.ToGuildSpec(model)
	dep, ok := spec.DependencyMap["filesystem"]
	require.True(t, ok, "expected persisted filesystem dependency")

	pathBase, ok := dep.Properties["path_base"].(string)
	require.True(t, ok, "expected persisted filesystem path_base")

	protocolName, ok := dep.Properties["protocol"].(string)
	require.True(t, ok, "expected persisted filesystem protocol")
	require.Equal(t, "file", protocolName)

	return pathBase
}

func uploadFile(t *testing.T, mux *http.ServeMux, targetPath, filename, content string) *httptest.ResponseRecorder {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	require.NoError(t, err)
	_, err = part.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, targetPath, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}
