package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
	"github.com/rustic-ai/forge/forge-go/forgepath"
	"github.com/stretchr/testify/require"
)

const rusticUIEchoBlueprintName = "Simple Echo"

type singleProcessForgeServer struct {
	publicBase string
	rusticBase string
	wsBase     string
	redisAddr  string
	stdout     *bytes.Buffer
	stderr     *bytes.Buffer
}

type wsReadEvent struct {
	msg []byte
	err error
}

func TestE2E_RusticUIEchoLaunchSingleProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Rustic UI single-process echo e2e test in short mode")
	}

	binPath := requireE2EForgeBin(t)

	cwd, err := os.Getwd()
	require.NoError(t, err)
	forgeRoot := filepath.Clean(filepath.Join(cwd, "..", ".."))

	server := startSingleProcessForgeServer(t, binPath, forgeRoot, "")
	client := &http.Client{Timeout: 30 * time.Second}

	seededBlueprintID := seedRusticUIEchoCatalog(t, client, server.rusticBase)

	blueprintID := selectRusticUIEchoBlueprint(t, client, server.rusticBase)
	require.Equal(t, seededBlueprintID, blueprintID)

	blueprintDetails := getJSONMap(t, client, fmt.Sprintf("%s/catalog/blueprints/%s", server.rusticBase, url.PathEscape(blueprintID)))
	require.Equal(t, rusticUIEchoBlueprintName, blueprintDetails["name"])

	guildName := "Rustic UI Echo " + time.Now().UTC().Format("150405")
	guildID := launchGuildFromBlueprint(t, client, server.rusticBase, blueprintID, guildName)
	createRusticUIBoards(t, client, server.rusticBase, guildID, guildName)

	guilds := getJSONArray(
		t,
		client,
		fmt.Sprintf(
			"%s/catalog/users/%s/guilds/?org_id=%s",
			server.rusticBase,
			url.PathEscape(defaultUserID),
			url.QueryEscape(defaultOrganizationID),
		),
	)
	require.True(t, guildListContains(guilds, guildID), "launched guild %s missing from user guild list", guildID)

	relaunchRespBody, relaunchStatus := postJSON(
		t,
		client,
		fmt.Sprintf("%s/api/guilds/%s/relaunch", server.rusticBase, url.PathEscape(guildID)),
		map[string]interface{}{},
		nil,
	)
	require.Equal(t, http.StatusOK, relaunchStatus, "relaunch failed: %s", string(relaunchRespBody))

	waitGuildRunning(t, client, server.publicBase, guildID, 2*time.Minute)

	_ = getJSONMap(t, client, fmt.Sprintf("%s/catalog/guilds/%s/blueprints/", server.rusticBase, url.PathEscape(guildID)))

	historicalMessages := getJSONArray(
		t,
		client,
		fmt.Sprintf(
			"%s/api/guilds/%s/%s/messages",
			server.rusticBase,
			url.PathEscape(guildID),
			url.PathEscape(defaultUserID),
		),
	)
	require.NotNil(t, historicalMessages)

	boardsPayload := getJSONMap(
		t,
		client,
		fmt.Sprintf("%s/addons/boards/?guild_id=%s", server.rusticBase, url.QueryEscape(guildID)),
	)
	require.True(t, boardListContains(boardsPayload, guildName+" Board"))
	require.True(t, boardListContains(boardsPayload, "Goal"))

	wsID := getRusticUIWSID(t, client, server.rusticBase, guildID)

	sysConn := openRusticUISocket(t, server.wsBase, wsID, "syscomms")
	defer func() { _ = sysConn.Close() }()

	sysTranscript := &wsTranscript{}
	userTranscript := &wsTranscript{}
	sysEvents := startWSReader(sysConn, sysTranscript)

	waitForRusticUIHealthOK(t, sysConn, sysTranscript, sysEvents, 90*time.Second)

	userConn := openRusticUISocket(t, server.wsBase, wsID, "usercomms")
	defer func() { _ = userConn.Close() }()
	userEvents := startWSReader(userConn, userTranscript)

	participants := waitForRusticUIParticipants(t, sysEvents, 60*time.Second)
	echoParticipant := requireParticipant(t, participants, "bot", "Echo Agent")
	userParticipant := requireParticipant(t, participants, "human", "")

	const promptText = "hello"
	sendChatRequest(t, userConn, userTranscript, promptText)
	echoMsg := waitForEchoTextFromEvents(t, userEvents, promptText, 45*time.Second, 4*time.Second)
	require.True(t, senderIsEchoAgent(echoMsg), "expected echo response from Echo Agent")
	require.Contains(t, string(echoMsg), promptText, "expected echoed prompt on usercomms socket")

	assertGuildAgentProcesses(t, server.redisAddr, guildID, []string{
		guildID + "#manager_agent",
		asString(t, echoParticipant["id"], "echo participant id"),
		asString(t, userParticipant["id"], "user participant id"),
	})
}

// startSingleProcessForgeServer launches a forge server subprocess.
// When natsURL is non-empty, the server is started with --nats and
// FORGE_EXTRA_DEPS is set to rusticai-nats so that Python agents
// install the NATS messaging backend from PyPI via uvx.
func startSingleProcessForgeServer(t *testing.T, binPath, forgeRoot string, natsURL string, extraArgs ...string) *singleProcessForgeServer {
	t.Helper()

	listenAddr, err := reserveLocalAddr()
	require.NoError(t, err)

	embeddedRedisAddr, err := reserveLocalAddr()
	require.NoError(t, err)

	dbPath := filepath.Join(t.TempDir(), "forge-rustic-ui-echo.db")
	dataDir := filepath.Join(t.TempDir(), "forge-data")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))

	registryPath := filepath.Join(forgeRoot, "forge-go", "conf", "forge-agent-registry.yaml")
	dependencyConfigPath := filepath.Join(forgeRoot, "forge-go", forgepath.DefaultDependencyConfigPath)
	forgePythonPath := filepath.Join(forgeRoot, "forge-python")

	args := []string{
		"server",
		"--listen", listenAddr,
		"--db", "sqlite:///" + dbPath,
		"--embedded-redis-addr", embeddedRedisAddr,
		"--data-dir", dataDir,
		"--dependency-config", dependencyConfigPath,
		"--with-client",
		"--client-node-id", "rustic-ui-single-node",
		"--client-metrics-addr", "127.0.0.1:0",
		"--client-default-supervisor", "process",
	}
	if natsURL != "" {
		args = append(args, "--nats", natsURL)
	}
	args = append(args, extraArgs...)

	cmd := exec.Command(binPath, args...)
	cmd.Dir = forgeRoot
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	env := append(
		os.Environ(),
		"FORGE_AGENT_REGISTRY="+registryPath,
		"FORGE_PYTHON_PKG="+forgePythonPath,
		"FORGE_ENABLE_PUBLIC_API=true",
		"FORGE_ENABLE_UI_API=true",
		"FORGE_IDENTITY_MODE=local",
		"FORGE_QUOTA_MODE=local",
		"PYTHONUNBUFFERED=1",
	)
	if natsURL != "" {
		// Use the published rusticai-nats package from PyPI for Python agents.
		env = append(env, "FORGE_EXTRA_DEPS=rusticai-nats")
	}
	cmd.Env = env

	require.NoError(t, cmd.Start(), "failed to start forge server process")
	pid := cmd.Process.Pid

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
	}()

	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		select {
		case <-waitDone:
		case <-time.After(5 * time.Second):
		}
		if t.Failed() {
			t.Logf("forge server stdout:\n%s", stdout.String())
			t.Logf("forge server stderr:\n%s", stderr.String())
		}
	})

	publicBase := "http://" + listenAddr
	if err := waitFor(20*time.Second, 200*time.Millisecond, func() error {
		resp, err := http.Get(publicBase + "/readyz")
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("readyz returned %d", resp.StatusCode)
		}
		return nil
	}); err != nil {
		t.Fatalf("forge server did not become ready: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	if err := waitFor(20*time.Second, 250*time.Millisecond, func() error {
		resp, err := http.Get(publicBase + "/nodes")
		if err != nil {
			return err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("nodes returned %d", resp.StatusCode)
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		if !strings.Contains(string(body), "rustic-ui-single-node") {
			return fmt.Errorf("in-process client node not yet registered")
		}
		return nil
	}); err != nil {
		t.Fatalf("in-process client node did not register: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}

	return &singleProcessForgeServer{
		publicBase: publicBase,
		rusticBase: publicBase + "/rustic",
		wsBase:     "ws://" + listenAddr,
		redisAddr:  embeddedRedisAddr,
		stdout:     stdout,
		stderr:     stderr,
	}
}

func seedRusticUIEchoCatalog(t *testing.T, client *http.Client, rusticBase string) string {
	t.Helper()

	agentPayload := loadRusticUIEchoAgentPayload(t)
	status, body := postRawJSON(t, client, rusticBase+"/catalog/agents", agentPayload)
	require.Equal(t, http.StatusCreated, status, "register echo agent failed: %s", string(body))

	blueprintPayload := loadRusticUIEchoBlueprint(t)
	respBody, respStatus := postJSON(t, client, rusticBase+"/catalog/blueprints/", blueprintPayload, nil)
	require.Equal(t, http.StatusCreated, respStatus, "create echo blueprint failed: %s", string(respBody))

	var created struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(respBody, &created))
	require.NotEmpty(t, created.ID)
	return created.ID
}

func loadRusticUIEchoAgentPayload(t *testing.T) json.RawMessage {
	t.Helper()

	return mustReadRusticUITestdata(t, "echo_agent.json")
}

func loadRusticUIEchoBlueprint(t *testing.T) map[string]interface{} {
	t.Helper()

	raw := mustReadRusticUITestdata(t, "echo_app.json")
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &payload))
	return payload
}

func mustReadRusticUITestdata(t *testing.T, filename string) json.RawMessage {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("testdata", "rustic_ui_seed", filename))
	require.NoError(t, err)
	return raw
}

func selectRusticUIEchoBlueprint(t *testing.T, client *http.Client, rusticBase string) string {
	t.Helper()

	accessible := getJSONArray(
		t,
		client,
		fmt.Sprintf(
			"%s/catalog/users/%s/blueprints/accessible/?org_id=%s",
			rusticBase,
			url.PathEscape(defaultUserID),
			url.QueryEscape(defaultOrganizationID),
		),
	)

	for _, entry := range accessible {
		name, _ := entry["name"].(string)
		if name != rusticUIEchoBlueprintName {
			continue
		}
		blueprintID, _ := entry["id"].(string)
		require.NotEmpty(t, blueprintID)
		return blueprintID
	}

	t.Fatalf("could not find %q in accessible blueprints", rusticUIEchoBlueprintName)
	return ""
}

func createRusticUIBoards(t *testing.T, client *http.Client, rusticBase, guildID, guildName string) {
	t.Helper()

	defaultBoardBody, defaultBoardStatus := postJSON(
		t,
		client,
		rusticBase+"/addons/boards/",
		map[string]interface{}{
			"guild_id":   guildID,
			"name":       guildName + " Board",
			"created_by": defaultUserID,
			"is_default": true,
		},
		nil,
	)
	require.Equal(t, http.StatusCreated, defaultBoardStatus, "default board create failed: %s", string(defaultBoardBody))

	goalBoardBody, goalBoardStatus := postJSON(
		t,
		client,
		rusticBase+"/addons/boards/",
		map[string]interface{}{
			"guild_id":   guildID,
			"name":       "Goal",
			"created_by": defaultUserID,
			"is_default": false,
		},
		nil,
	)
	require.Equal(t, http.StatusCreated, goalBoardStatus, "goal board create failed: %s", string(goalBoardBody))
}

func getRusticUIWSID(t *testing.T, client *http.Client, rusticBase, guildID string) string {
	t.Helper()

	body := getJSONMap(
		t,
		client,
		fmt.Sprintf(
			"%s/guilds/%s/ws?user=%s",
			rusticBase,
			url.PathEscape(guildID),
			url.QueryEscape(defaultUserName),
		),
	)
	wsID, _ := body["wsId"].(string)
	require.NotEmpty(t, wsID)
	return wsID
}

func openRusticUISocket(t *testing.T, wsBase, wsID, channel string) *websocket.Conn {
	t.Helper()

	conn, _, err := websocket.DefaultDialer.Dial(
		fmt.Sprintf("%s/rustic/ws/%s/%s", wsBase, url.PathEscape(wsID), channel),
		nil,
	)
	require.NoError(t, err)
	return conn
}

func startWSReader(conn *websocket.Conn, transcript *wsTranscript) <-chan wsReadEvent {
	events := make(chan wsReadEvent, 16)
	go func() {
		defer close(events)
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				events <- wsReadEvent{err: err}
				return
			}
			transcript.addRecv(msg)
			events <- wsReadEvent{msg: msg}
		}
	}()
	return events
}

func waitForRusticUIHealthOK(
	t *testing.T,
	sysConn *websocket.Conn,
	transcript *wsTranscript,
	events <-chan wsReadEvent,
	timeout time.Duration,
) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	lastHealthPing := time.Time{}

	for time.Now().Before(deadline) {
		if lastHealthPing.IsZero() || time.Since(lastHealthPing) >= time.Second {
			sendRusticUIHealthCheck(t, sysConn, transcript)
			lastHealthPing = time.Now()
		}

		select {
		case event, ok := <-events:
			if !ok {
				t.Fatalf("syscomms closed while waiting for healthReport")
			}
			if event.err != nil {
				t.Fatalf("syscomms read failed while waiting for healthReport: %v", event.err)
			}
			format, data := parseProxyCompatMessage(t, event.msg)
			if format != "healthReport" {
				continue
			}

			dataMap, ok := data.(map[string]interface{})
			if !ok {
				continue
			}
			if guildHealth, _ := dataMap["guild_health"].(string); guildHealth == "ok" {
				return
			}
		case <-time.After(250 * time.Millisecond):
		}
	}

	t.Fatalf("did not receive healthReport with guild_health=ok within %s", timeout)
}

func waitForRusticUIParticipants(t *testing.T, events <-chan wsReadEvent, timeout time.Duration) []map[string]interface{} {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatalf("syscomms closed while waiting for participants")
			}
			if event.err != nil {
				t.Fatalf("syscomms read failed while waiting for participants: %v", event.err)
			}

			format, data := parseProxyCompatMessage(t, event.msg)
			if format != "participants" {
				continue
			}

			participants, ok := data.([]interface{})
			if !ok {
				continue
			}
			if hasHumanParticipant(participants) {
				return normalizeParticipants(t, participants)
			}
		case <-time.After(250 * time.Millisecond):
		}
	}

	t.Fatalf("did not receive a participants payload with a human participant within %s", timeout)
	return nil
}

func sendRusticUIHealthCheck(t *testing.T, sysConn *websocket.Conn, transcript *wsTranscript) {
	t.Helper()

	msg := map[string]interface{}{
		"id":             fmt.Sprintf("%d", time.Now().UnixNano()),
		"format":         "healthcheck",
		"data":           map[string]interface{}{"dummy": 1},
		"conversationId": "default",
		"sender": map[string]interface{}{
			"id": defaultUserID,
		},
		"topic": "guild_status_topic",
	}

	b, err := json.Marshal(msg)
	require.NoError(t, err)
	transcript.addSent(b)

	_ = sysConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	require.NoError(t, sysConn.WriteMessage(websocket.TextMessage, b))
}

func waitForEchoTextFromEvents(
	t *testing.T,
	events <-chan wsReadEvent,
	expectedText string,
	waitTimeout time.Duration,
	dupGuard time.Duration,
) []byte {
	t.Helper()

	matchCount := 0
	var matchedMessage []byte

	readUntil := func(timeout time.Duration, failMessage string) {
		timer := time.NewTimer(timeout)
		defer timer.Stop()

		for {
			select {
			case event, ok := <-events:
				if !ok {
					t.Fatalf("%s: websocket closed", failMessage)
				}
				if event.err != nil {
					t.Fatalf("%s: %v", failMessage, event.err)
				}
				if !senderIsEchoAgent(event.msg) {
					continue
				}
				raw := string(event.msg)
				if strings.Contains(raw, expectedText) {
					matchCount++
					if matchedMessage == nil {
						matchedMessage = append([]byte(nil), event.msg...)
					}
					return
				}
			case <-timer.C:
				return
			}
		}
	}

	readUntil(waitTimeout, "usercomms read failed while waiting for echo")
	require.GreaterOrEqual(t, matchCount, 1, "missing echo for text=%s", expectedText)

	readUntil(dupGuard, "usercomms read failed during duplicate guard")
	require.Equal(t, 1, matchCount, "duplicate echo count for text=%s", expectedText)
	require.NotNil(t, matchedMessage, "matched echo message missing for text=%s", expectedText)
	return matchedMessage
}

func assertGuildAgentProcesses(t *testing.T, redisAddr, guildID string, expectedAgentIDs []string) {
	t.Helper()

	sort.Strings(expectedAgentIDs)

	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	defer func() { _ = rdb.Close() }()

	ctx := context.Background()
	var statuses map[string]agentStatusSnapshot
	err := waitFor(30*time.Second, 250*time.Millisecond, func() error {
		var err error
		statuses, err = loadGuildAgentStatuses(ctx, rdb, guildID)
		if err != nil {
			return err
		}
		if len(statuses) != len(expectedAgentIDs) {
			return fmt.Errorf("expected %d agent statuses, got %d", len(expectedAgentIDs), len(statuses))
		}
		for _, agentID := range expectedAgentIDs {
			status, ok := statuses[agentID]
			if !ok {
				return fmt.Errorf("missing status for agent %s", agentID)
			}
			if status.State != "running" {
				return fmt.Errorf("agent %s status is %s", agentID, status.State)
			}
			if status.PID <= 0 {
				return fmt.Errorf("agent %s has invalid pid %d", agentID, status.PID)
			}
		}
		return nil
	})
	require.NoError(t, err)

	actualAgentIDs := make([]string, 0, len(statuses))
	for agentID := range statuses {
		actualAgentIDs = append(actualAgentIDs, agentID)
	}
	sort.Strings(actualAgentIDs)
	require.Equal(t, expectedAgentIDs, actualAgentIDs)

	pidSeen := map[int]bool{}
	for _, agentID := range expectedAgentIDs {
		status := statuses[agentID]
		require.False(t, pidSeen[status.PID], "duplicate pid %d shared by multiple agents", status.PID)
		pidSeen[status.PID] = true

		cmdline := readPIDCommandLine(t, status.PID)
		require.Contains(t, cmdline, "uvx", "agent %s is not running under uvx: %s", agentID, cmdline)
		require.Contains(t, cmdline, "rustic_ai.forge.agent_runner", "agent %s is not running the forge agent runner: %s", agentID, cmdline)
	}
}

type agentStatusSnapshot struct {
	State string `json:"state"`
	PID   int    `json:"pid"`
}

func loadGuildAgentStatuses(ctx context.Context, rdb *redis.Client, guildID string) (map[string]agentStatusSnapshot, error) {
	const statusPrefix = "forge:agent:status:"

	pattern := fmt.Sprintf("%s%s:*", statusPrefix, guildID)
	keys, err := rdb.Keys(ctx, pattern).Result()
	if err != nil {
		return nil, err
	}

	statuses := make(map[string]agentStatusSnapshot, len(keys))
	for _, key := range keys {
		raw, err := rdb.Get(ctx, key).Result()
		if err != nil {
			return nil, err
		}
		var status agentStatusSnapshot
		if err := json.Unmarshal([]byte(raw), &status); err != nil {
			return nil, err
		}
		agentID := strings.TrimPrefix(key, statusPrefix+guildID+":")
		statuses[agentID] = status
	}
	return statuses, nil
}

func readPIDCommandLine(t *testing.T, pid int) string {
	t.Helper()

	cmd := exec.Command("ps", "-p", fmt.Sprintf("%d", pid), "-o", "command=")
	output, err := cmd.Output()
	require.NoError(t, err)

	cmdline := strings.TrimSpace(string(output))
	require.NotEmpty(t, cmdline, "ps returned empty command line for pid %d", pid)
	return cmdline
}

func parseProxyCompatMessage(t *testing.T, msg []byte) (string, interface{}) {
	t.Helper()

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(msg, &parsed))
	format, _ := parsed["format"].(string)
	return format, parsed["data"]
}

func normalizeParticipants(t *testing.T, participants []interface{}) []map[string]interface{} {
	t.Helper()

	normalized := make([]map[string]interface{}, 0, len(participants))
	for _, raw := range participants {
		participant, ok := raw.(map[string]interface{})
		require.True(t, ok, "participant payload entry is not an object: %T", raw)
		normalized = append(normalized, participant)
	}
	return normalized
}

func hasHumanParticipant(participants []interface{}) bool {
	for _, raw := range participants {
		p, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if participantType, _ := p["type"].(string); participantType == "human" {
			return true
		}
	}
	return false
}

func requireParticipant(
	t *testing.T,
	participants []map[string]interface{},
	participantType string,
	name string,
) map[string]interface{} {
	t.Helper()

	for _, participant := range participants {
		gotType, _ := participant["type"].(string)
		gotName, _ := participant["name"].(string)
		if gotType != participantType {
			continue
		}
		if name != "" && gotName != name {
			continue
		}
		require.NotEmpty(t, participant["id"])
		return participant
	}

	t.Fatalf("missing %s participant %q in payload: %#v", participantType, name, participants)
	return nil
}

func asString(t *testing.T, value interface{}, label string) string {
	t.Helper()

	str, ok := value.(string)
	require.True(t, ok, "%s is not a string: %T", label, value)
	require.NotEmpty(t, str, "%s is empty", label)
	return str
}

func guildListContains(guilds []map[string]interface{}, guildID string) bool {
	for _, entry := range guilds {
		if id, _ := entry["id"].(string); id == guildID {
			return true
		}
	}
	return false
}

func boardListContains(payload map[string]interface{}, boardName string) bool {
	rawBoards, ok := payload["boards"].([]interface{})
	if !ok {
		return false
	}
	for _, rawBoard := range rawBoards {
		board, ok := rawBoard.(map[string]interface{})
		if !ok {
			continue
		}
		if name, _ := board["name"].(string); name == boardName {
			return true
		}
	}
	return false
}

func getJSONMap(t *testing.T, client *http.Client, rawURL string) map[string]interface{} {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "GET %s failed: %s", rawURL, string(body))

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(body, &payload))
	return payload
}

func getJSONArray(t *testing.T, client *http.Client, rawURL string) []map[string]interface{} {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	require.NoError(t, err)

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "GET %s failed: %s", rawURL, string(body))

	var payload []map[string]interface{}
	require.NoError(t, json.Unmarshal(body, &payload))
	return payload
}

func postRawJSON(t *testing.T, client *http.Client, rawURL string, body []byte) (int, []byte) {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, rawURL, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, respBody
}
