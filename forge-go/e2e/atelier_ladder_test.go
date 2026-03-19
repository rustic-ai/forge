package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

const (
	ladderEnvFlag         = "FORGE_E2E_ATELIER"
	defaultUserID         = "dummyuserid"
	defaultUserName       = "Anonymous User"
	defaultOrganizationID = "acmeorganizationid"
)

type wsTranscript struct {
	mu   sync.Mutex
	sent []string
	recv []string
}

func (w *wsTranscript) addSent(msg []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sent = append(w.sent, string(msg))
}

func (w *wsTranscript) addRecv(msg []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.recv = append(w.recv, string(msg))
}

func (w *wsTranscript) dump(path string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	payload := map[string]interface{}{
		"sent": w.sent,
		"recv": w.recv,
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

type atelierHarness struct {
	t          *testing.T
	projectDir string
	atelierDir string
	composeYML string
	apiBase    string
	proxyBase  string
	uiBase     string
}

func requireAtelierHarness(t *testing.T) *atelierHarness {
	t.Helper()
	if os.Getenv(ladderEnvFlag) != "1" {
		t.Skipf("set %s=1 to run atelier ladder e2e tests", ladderEnvFlag)
	}

	cwd, err := os.Getwd()
	require.NoError(t, err)

	projectDir := filepath.Clean(filepath.Join(cwd, "..", "..", ".."))
	atelierDir := filepath.Join(projectDir, "atelier")
	composeYML := filepath.Join(atelierDir, "docker-compose.forge.yml")
	if _, err := os.Stat(composeYML); err != nil {
		t.Fatalf("compose file missing: %s", composeYML)
	}

	h := &atelierHarness{
		t:          t,
		projectDir: projectDir,
		atelierDir: atelierDir,
		composeYML: composeYML,
		apiBase:    "http://localhost:8880",
		proxyBase:  "http://localhost:3001",
		uiBase:     "http://localhost:3000",
	}
	t.Cleanup(func() {
		if t.Failed() {
			h.captureLogs()
		}
	})
	return h
}

func (h *atelierHarness) compose(ctx context.Context, args ...string) (string, error) {
	fullArgs := []string{"compose", "-f", h.composeYML}
	fullArgs = append(fullArgs, args...)
	cmd := exec.CommandContext(ctx, "docker", fullArgs...)
	cmd.Dir = h.atelierDir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

func (h *atelierHarness) resetAndStartStack() {
	ctxDown, cancelDown := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancelDown()
	_, _ = h.compose(ctxDown, "down", "-v", "--remove-orphans")

	ctxUp, cancelUp := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancelUp()
	out, err := h.compose(
		ctxUp,
		"up",
		"--build",
		"-d",
		"postgres",
		"redis",
		"rustic_api",
		"forge_client",
		"data_loader",
		"api_proxy",
		"ui",
	)
	require.NoError(h.t, err, "compose up failed:\n%s", out)

	require.NoError(h.t, waitForHTTPStatus(h.apiBase+"/healthz", http.StatusOK, 4*time.Minute))
	require.NoError(h.t, waitForHTTPStatus(h.proxyBase+"/__health", http.StatusOK, 3*time.Minute))
	require.NoError(h.t, waitForHTTPStatus(h.uiBase, http.StatusOK, 3*time.Minute))
}

func (h *atelierHarness) captureLogs() {
	ts := time.Now().UTC().Format("20060102-150405")
	testName := strings.ReplaceAll(h.t.Name(), "/", "_")
	outDir := filepath.Join("/tmp", "forge-e2e-artifacts", testName+"-"+ts)
	_ = os.MkdirAll(outDir, 0o755)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	logs, err := h.compose(ctx, "logs", "--no-color", "api_server", "forge_client", "api_proxy", "ui", "postgres", "redis")
	if err == nil {
		_ = os.WriteFile(filepath.Join(outDir, "docker-compose-logs.txt"), []byte(logs), 0o644)
	}
	h.t.Logf("failure artifacts: %s", outDir)
}

func waitForHTTPStatus(rawURL string, statusCode int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 10 * time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(rawURL)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == statusCode {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %s status=%d", rawURL, statusCode)
}

func loadEchoBlueprintTemplate(t *testing.T, projectDir string) map[string]interface{} {
	t.Helper()
	p := filepath.Join(projectDir, "echo_app.json")
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(b, &payload))
	return payload
}

func createBlueprint(
	t *testing.T,
	client *http.Client,
	base string,
	blueprintName string,
	projectDir string,
) string {
	t.Helper()
	payload := loadEchoBlueprintTemplate(t, projectDir)
	payload["name"] = blueprintName
	if spec, ok := payload["spec"].(map[string]interface{}); ok {
		spec["name"] = blueprintName
	}
	respBody, status := postJSON(t, client, base+"/catalog/blueprints/", payload, nil)
	require.Equal(t, http.StatusCreated, status, "create blueprint failed: %s", string(respBody))
	var idResp struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(respBody, &idResp))
	require.NotEmpty(t, idResp.ID)
	return idResp.ID
}

func launchGuildFromBlueprint(
	t *testing.T,
	client *http.Client,
	base string,
	blueprintID string,
	guildName string,
) string {
	t.Helper()
	body := map[string]interface{}{
		"guild_name": guildName,
		"user_id":    defaultUserID,
		"org_id":     defaultOrganizationID,
	}
	endpoint := fmt.Sprintf("%s/catalog/blueprints/%s/guilds", base, url.PathEscape(blueprintID))
	respBody, status := postJSON(t, client, endpoint, body, nil)
	require.Equal(t, http.StatusCreated, status, "launch guild failed: %s", string(respBody))
	var idResp struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(respBody, &idResp))
	require.NotEmpty(t, idResp.ID)
	return idResp.ID
}

func waitGuildRunning(t *testing.T, client *http.Client, base string, guildID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	guildURL := fmt.Sprintf("%s/api/guilds/%s", base, url.PathEscape(guildID))
	for time.Now().Before(deadline) {
		resp, err := client.Get(guildURL)
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var parsed map[string]interface{}
				if json.Unmarshal(b, &parsed) == nil {
					if status, _ := parsed["status"].(string); status == "running" {
						return
					}
				}
			}
		}
		time.Sleep(700 * time.Millisecond)
	}
	t.Fatalf("guild %s did not reach running status on %s", guildID, base)
}

func postJSON(
	t *testing.T,
	client *http.Client,
	rawURL string,
	body interface{},
	headers map[string]string,
) ([]byte, int) {
	t.Helper()
	reqBody, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, rawURL, bytes.NewReader(reqBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return respBody, resp.StatusCode
}

func openBackendSockets(t *testing.T, guildID string) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	userURL := fmt.Sprintf(
		"ws://localhost:8880/ws/guilds/%s/usercomms/%s/%s",
		url.PathEscape(guildID),
		url.PathEscape(defaultUserID),
		url.PathEscape(defaultUserName),
	)
	sysURL := fmt.Sprintf(
		"ws://localhost:8880/ws/guilds/%s/syscomms/%s",
		url.PathEscape(guildID),
		url.PathEscape(defaultUserID),
	)
	userConn, _, err := websocket.DefaultDialer.Dial(userURL, nil)
	require.NoError(t, err)
	sysConn, _, err := websocket.DefaultDialer.Dial(sysURL, nil)
	require.NoError(t, err)
	return userConn, sysConn
}

func getProxyWSID(t *testing.T, client *http.Client, guildID string) string {
	t.Helper()
	wsIDURL := fmt.Sprintf(
		"http://localhost:3001/rustic/guilds/%s/ws?user=%s",
		url.PathEscape(guildID),
		url.QueryEscape(defaultUserName),
	)
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(wsIDURL)
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			var wsResp struct {
				WsID string `json:"wsId"`
			}
			if json.Unmarshal(body, &wsResp) == nil && wsResp.WsID != "" {
				return wsResp.WsID
			}
		}
		time.Sleep(700 * time.Millisecond)
	}
	t.Fatalf("timed out fetching proxy wsId for guild=%s", guildID)
	return ""
}

func openProxySockets(t *testing.T, client *http.Client, wsID string) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	u, err := url.Parse("http://localhost:3001")
	require.NoError(t, err)
	headers := http.Header{}
	if client.Jar != nil {
		cookies := client.Jar.Cookies(u)
		if len(cookies) > 0 {
			parts := make([]string, 0, len(cookies))
			for _, c := range cookies {
				parts = append(parts, c.Name+"="+c.Value)
			}
			headers.Set("Cookie", strings.Join(parts, "; "))
		}
	}

	userURL := fmt.Sprintf("ws://localhost:3001/rustic/ws/%s/usercomms", url.PathEscape(wsID))
	sysURL := fmt.Sprintf("ws://localhost:3001/rustic/ws/%s/syscomms", url.PathEscape(wsID))

	dialer := websocket.Dialer{HandshakeTimeout: 12 * time.Second}
	userConn, _, err := dialer.Dial(userURL, headers)
	require.NoError(t, err)
	sysConn, _, err := dialer.Dial(sysURL, headers)
	require.NoError(t, err)
	return userConn, sysConn
}

func waitForSysMessage(t *testing.T, conn *websocket.Conn, tr *wsTranscript, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))
		_, msg, err := conn.ReadMessage()
		if err != nil {
			if isTimeoutErr(err) {
				continue
			}
			t.Fatalf("syscomms read failed: %v", err)
		}
		tr.addRecv(msg)
		return
	}
	t.Fatalf("did not receive syscomms message within %s", timeout)
}

func sendChatRequest(t *testing.T, conn *websocket.Conn, tr *wsTranscript, text string) {
	t.Helper()
	msg := map[string]interface{}{
		"id":             fmt.Sprintf("%d", time.Now().UnixNano()),
		"timestamp":      time.Now().UTC().Format(time.RFC3339Nano),
		"conversationId": "default",
		"format":         "chatCompletionRequest",
		"data": map[string]interface{}{
			"messages": []interface{}{
				map[string]interface{}{
					"role": "user",
					"name": defaultUserID,
					"content": []interface{}{
						map[string]interface{}{
							"type": "text",
							"text": text,
						},
					},
				},
			},
		},
	}
	b, err := json.Marshal(msg)
	require.NoError(t, err)
	tr.addSent(b)
	_ = conn.SetWriteDeadline(time.Now().Add(8 * time.Second))
	require.NoError(t, conn.WriteMessage(websocket.TextMessage, b))
}

func waitForExactlyOneEchoPerToken(
	t *testing.T,
	conn *websocket.Conn,
	tr *wsTranscript,
	tokens []string,
	waitTimeout time.Duration,
	dupGuard time.Duration,
) {
	t.Helper()
	counts := map[string]int{}
	for _, token := range tokens {
		counts[token] = 0
	}

	allSeen := func() bool {
		for _, token := range tokens {
			if counts[token] < 1 {
				return false
			}
		}
		return true
	}

	readUntil := func(deadline time.Time) {
		for time.Now().Before(deadline) {
			_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))
			_, msg, err := conn.ReadMessage()
			if err != nil {
				if isTimeoutErr(err) {
					continue
				}
				t.Fatalf("usercomms read failed: %v", err)
			}
			tr.addRecv(msg)
			if !senderIsEchoAgent(msg) {
				continue
			}
			raw := string(msg)
			for _, token := range tokens {
				if strings.Contains(raw, token) {
					counts[token]++
				}
			}
			if allSeen() {
				return
			}
		}
	}

	readUntil(time.Now().Add(waitTimeout))
	for _, token := range tokens {
		require.GreaterOrEqual(t, counts[token], 1, "missing echo for token=%s", token)
	}

	readUntil(time.Now().Add(dupGuard))
	for _, token := range tokens {
		require.Equal(t, 1, counts[token], "duplicate echo count for token=%s", token)
	}
}

func senderIsEchoAgent(msg []byte) bool {
	var parsed map[string]interface{}
	if err := json.Unmarshal(msg, &parsed); err != nil {
		return false
	}
	sender, ok := parsed["sender"].(map[string]interface{})
	if !ok {
		return false
	}
	name, _ := sender["name"].(string)
	return name == "Echo Agent"
}

func isTimeoutErr(err error) bool {
	var ne interface{ Timeout() bool }
	if errors.As(err, &ne) {
		return ne.Timeout()
	}
	return strings.Contains(strings.ToLower(err.Error()), "timeout")
}

func recordArtifactsOnFailure(t *testing.T, h *atelierHarness, userTr, sysTr *wsTranscript) {
	t.Helper()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		ts := time.Now().UTC().Format("20060102-150405")
		testName := strings.ReplaceAll(t.Name(), "/", "_")
		outDir := filepath.Join("/tmp", "forge-e2e-artifacts", testName+"-"+ts)
		_ = os.MkdirAll(outDir, 0o755)
		_ = userTr.dump(filepath.Join(outDir, "usercomms_transcript.json"))
		_ = sysTr.dump(filepath.Join(outDir, "syscomms_transcript.json"))
		h.t.Logf("ws transcripts: %s", outDir)
	})
}

func TestE2ELadder_BackendDirectEcho(t *testing.T) {
	h := requireAtelierHarness(t)
	h.resetAndStartStack()

	client := &http.Client{Timeout: 30 * time.Second}
	suffix := time.Now().UTC().Format("150405")
	blueprintName := "Simple Echo Ladder Backend " + suffix
	blueprintID := createBlueprint(t, client, h.apiBase, blueprintName, h.projectDir)
	guildName := "Echo Backend Session " + suffix
	guildID := launchGuildFromBlueprint(t, client, h.apiBase, blueprintID, guildName)
	waitGuildRunning(t, client, h.apiBase, guildID, 2*time.Minute)

	userConn, sysConn := openBackendSockets(t, guildID)
	defer func() { _ = userConn.Close() }()
	defer func() { _ = sysConn.Close() }()

	userTr := &wsTranscript{}
	sysTr := &wsTranscript{}
	recordArtifactsOnFailure(t, h, userTr, sysTr)

	waitForSysMessage(t, sysConn, sysTr, 30*time.Second)

	token1 := "ladder-backend-1-" + suffix
	token2 := "ladder-backend-2-" + suffix
	sendChatRequest(t, userConn, userTr, token1)
	sendChatRequest(t, userConn, userTr, token2)
	waitForExactlyOneEchoPerToken(t, userConn, userTr, []string{token1, token2}, 45*time.Second, 4*time.Second)
}

func TestE2ELadder_ProxyEcho(t *testing.T) {
	h := requireAtelierHarness(t)
	h.resetAndStartStack()

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	client := &http.Client{Timeout: 30 * time.Second, Jar: jar}
	suffix := time.Now().UTC().Format("150405")
	blueprintName := "Simple Echo Ladder Proxy " + suffix
	blueprintID := createBlueprint(t, client, h.proxyBase+"/rustic", blueprintName, h.projectDir)
	guildName := "Echo Proxy Session " + suffix
	guildID := launchGuildFromBlueprint(t, client, h.proxyBase+"/rustic", blueprintID, guildName)
	waitGuildRunning(t, client, h.proxyBase+"/rustic", guildID, 2*time.Minute)

	wsID := getProxyWSID(t, client, guildID)
	userConn, sysConn := openProxySockets(t, client, wsID)
	defer func() { _ = userConn.Close() }()
	defer func() { _ = sysConn.Close() }()

	userTr := &wsTranscript{}
	sysTr := &wsTranscript{}
	recordArtifactsOnFailure(t, h, userTr, sysTr)

	waitForSysMessage(t, sysConn, sysTr, 30*time.Second)

	token1 := "ladder-proxy-1-" + suffix
	token2 := "ladder-proxy-2-" + suffix
	sendChatRequest(t, userConn, userTr, token1)
	sendChatRequest(t, userConn, userTr, token2)
	waitForExactlyOneEchoPerToken(t, userConn, userTr, []string{token1, token2}, 45*time.Second, 4*time.Second)
}

func TestE2ELadder_GoldenUIProxyEcho(t *testing.T) {
	h := requireAtelierHarness(t)
	h.resetAndStartStack()

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	client := &http.Client{Timeout: 30 * time.Second, Jar: jar}
	suffix := time.Now().UTC().Format("150405")
	blueprintName := "Simple Echo Ladder Golden " + suffix
	blueprintID := createBlueprint(t, client, h.proxyBase+"/rustic", blueprintName, h.projectDir)
	guildName := "Echo Golden Session " + suffix
	guildID := launchGuildFromBlueprint(t, client, h.proxyBase+"/rustic", blueprintID, guildName)
	waitGuildRunning(t, client, h.proxyBase+"/rustic", guildID, 2*time.Minute)

	// Golden check: the UI route for this guild should resolve.
	uiRoute := fmt.Sprintf("%s/conversations/%s", h.uiBase, url.PathEscape(guildID))
	resp, err := client.Get(uiRoute)
	require.NoError(t, err)
	uiBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, strings.ToLower(string(uiBody)), "<html")

	wsID := getProxyWSID(t, client, guildID)
	userConn, sysConn := openProxySockets(t, client, wsID)
	defer func() { _ = userConn.Close() }()
	defer func() { _ = sysConn.Close() }()

	userTr := &wsTranscript{}
	sysTr := &wsTranscript{}
	recordArtifactsOnFailure(t, h, userTr, sysTr)

	waitForSysMessage(t, sysConn, sysTr, 30*time.Second)

	token1 := "ladder-golden-1-" + suffix
	token2 := "ladder-golden-2-" + suffix
	sendChatRequest(t, userConn, userTr, token1)
	sendChatRequest(t, userConn, userTr, token2)
	waitForExactlyOneEchoPerToken(t, userConn, userTr, []string{token1, token2}, 45*time.Second, 4*time.Second)
}
