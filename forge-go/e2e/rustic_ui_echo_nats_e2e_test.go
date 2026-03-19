package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

const rusticUINATSEchoBlueprintName = "Simple Echo NATS"

// startInProcessNATSServerE2E launches an in-process JetStream-enabled NATS server
// on a random port and registers test cleanup.
func startInProcessNATSServerE2E(t *testing.T) *natsserver.Server {
	t.Helper()
	opts := &natsserver.Options{
		Port:      -1,
		JetStream: true,
		StoreDir:  t.TempDir(),
	}
	s, err := natsserver.NewServer(opts)
	require.NoError(t, err, "failed to create in-process NATS server for e2e test")
	go s.Start()
	if !s.ReadyForConnections(10 * time.Second) {
		t.Fatal("in-process NATS server did not become ready within 10s")
	}
	t.Cleanup(func() { s.Shutdown() })
	return s
}

// TestE2E_RusticUIEchoLaunchSingleProcess_NATS is the NATS-backend counterpart of
// TestE2E_RusticUIEchoLaunchSingleProcess.  It starts a single-process Forge server
// configured with --nats (NATS data plane, NATS control plane) and runs the same
// full guild/WebSocket/echo flow to verify end-to-end correctness with the NATS
// messaging and control backends.
func TestE2E_RusticUIEchoLaunchSingleProcess_NATS(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Rustic UI NATS single-process echo e2e test in short mode")
	}

	ns := startInProcessNATSServerE2E(t)
	natsURL := ns.ClientURL()

	binPath := requireE2EForgeBin(t)

	cwd, err := os.Getwd()
	require.NoError(t, err)
	forgeRoot := filepath.Clean(filepath.Join(cwd, "..", ".."))

	server := startSingleProcessForgeServer(t, binPath, forgeRoot, natsURL)
	client := &http.Client{Timeout: 30 * time.Second}

	seededBlueprintID := seedRusticUINATSEchoCatalog(t, client, server.rusticBase)

	blueprintID := selectRusticUIBlueprintByName(t, client, server.rusticBase, rusticUINATSEchoBlueprintName)
	require.Equal(t, seededBlueprintID, blueprintID)

	blueprintDetails := getJSONMap(t, client, fmt.Sprintf("%s/catalog/blueprints/%s", server.rusticBase, url.PathEscape(blueprintID)))
	require.Equal(t, rusticUINATSEchoBlueprintName, blueprintDetails["name"])

	guildName := "Rustic UI Echo NATS " + time.Now().UTC().Format("150405")
	guildID := launchGuildFromBlueprint(t, client, server.rusticBase, blueprintID, guildName)
	createRusticUIBoards(t, client, server.rusticBase, guildID, guildName)

	relaunchRespBody, relaunchStatus := postJSON(
		t,
		client,
		fmt.Sprintf("%s/api/guilds/%s/relaunch", server.rusticBase, url.PathEscape(guildID)),
		map[string]interface{}{},
		nil,
	)
	require.Equal(t, http.StatusOK, relaunchStatus, "relaunch failed: %s", string(relaunchRespBody))

	waitGuildRunning(t, client, server.publicBase, guildID, 2*time.Minute)

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

	const promptText = "hello nats"
	sendChatRequest(t, userConn, userTranscript, promptText)
	echoMsg := waitForEchoTextFromEvents(t, userEvents, promptText, 45*time.Second, 4*time.Second)
	require.True(t, senderIsEchoAgent(echoMsg), "expected echo response from Echo Agent")
	require.Contains(t, string(echoMsg), promptText, "expected echoed prompt on usercomms socket")

	assertGuildAgentProcessesNATS(t, natsURL, guildID, []string{
		guildID + "#manager_agent",
		asString(t, echoParticipant["id"], "echo participant id"),
		asString(t, userParticipant["id"], "user participant id"),
	})
}

// assertGuildAgentProcessesNATS verifies that all expectedAgentIDs have a "running"
// status with valid PIDs in the NATS KV agent-status bucket, and that the PIDs
// are running the forge agent_runner under uvx.
func assertGuildAgentProcessesNATS(t *testing.T, natsURL, guildID string, expectedAgentIDs []string) {
	t.Helper()
	sort.Strings(expectedAgentIDs)

	nc, err := nats.Connect(natsURL)
	require.NoError(t, err)
	defer nc.Close()

	js, err := nc.JetStream()
	require.NoError(t, err)

	var statuses map[string]agentStatusSnapshot
	err = waitFor(30*time.Second, 250*time.Millisecond, func() error {
		kv, kvErr := js.KeyValue("agent-status")
		if kvErr != nil {
			return fmt.Errorf("failed to open agent-status KV: %w", kvErr)
		}
		statuses = make(map[string]agentStatusSnapshot, len(expectedAgentIDs))
		for _, agentID := range expectedAgentIDs {
			key := natsKVSanitize(guildID) + "." + natsKVSanitize(agentID)
			entry, getErr := kv.Get(key)
			if getErr != nil {
				return fmt.Errorf("missing status for agent %s (key=%s): %w", agentID, key, getErr)
			}
			var status agentStatusSnapshot
			if jsonErr := json.Unmarshal(entry.Value(), &status); jsonErr != nil {
				return fmt.Errorf("failed to parse status for agent %s: %w", agentID, jsonErr)
			}
			if status.State != "running" {
				return fmt.Errorf("agent %s status is %s, want running", agentID, status.State)
			}
			if status.PID <= 0 {
				return fmt.Errorf("agent %s has invalid pid %d", agentID, status.PID)
			}
			statuses[agentID] = status
		}
		return nil
	})
	require.NoError(t, err)

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

// natsKVSanitize matches supervisor.kvSanitize: allow only [a-zA-Z0-9-_.],
// replacing any other character with '_'.
func natsKVSanitize(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			return r
		default:
			return '_'
		}
	}, name)
}

// seedRusticUINATSEchoCatalog seeds the agent catalog entry and the NATS echo blueprint,
// returning the created blueprint ID.
func seedRusticUINATSEchoCatalog(t *testing.T, client *http.Client, rusticBase string) string {
	t.Helper()

	agentPayload := loadRusticUIEchoAgentPayload(t)
	status, body := postRawJSON(t, client, rusticBase+"/catalog/agents", agentPayload)
	// 409 Conflict is acceptable if the agent was already registered by a prior test run.
	require.True(t, status == http.StatusCreated || status == http.StatusConflict,
		"register echo agent for NATS test failed (%d): %s", status, string(body))

	blueprintPayload := loadRusticUINATSEchoBlueprint(t)
	respBody, respStatus := postJSON(t, client, rusticBase+"/catalog/blueprints/", blueprintPayload, nil)
	require.Equal(t, http.StatusCreated, respStatus, "create NATS echo blueprint failed: %s", string(respBody))

	var created struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(respBody, &created))
	require.NotEmpty(t, created.ID)
	return created.ID
}

func loadRusticUINATSEchoBlueprint(t *testing.T) map[string]interface{} {
	t.Helper()
	raw := mustReadRusticUITestdata(t, "echo_app_nats.json")
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &payload))
	return payload
}

// selectRusticUIBlueprintByName finds a blueprint in the accessible list by its name.
func selectRusticUIBlueprintByName(t *testing.T, client *http.Client, rusticBase, blueprintName string) string {
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
		if name != blueprintName {
			continue
		}
		blueprintID, _ := entry["id"].(string)
		require.NotEmpty(t, blueprintID)
		return blueprintID
	}
	t.Fatalf("could not find %q in accessible blueprints", blueprintName)
	return ""
}
