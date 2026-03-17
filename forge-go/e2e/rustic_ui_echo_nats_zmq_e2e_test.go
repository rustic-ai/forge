package e2e

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const rusticUINATSZMQEchoBlueprintName = "Simple Echo NATS"

// TestE2E_RusticUIEchoLaunchSingleProcess_NATS_ZMQ exercises the full guild/WebSocket/echo
// flow with NATS as the messaging+control backend and supervisor-zmq as the agent transport.
// This verifies that agents communicate through the ZMQ bridge rather than connecting directly
// to the messaging backend.
func TestE2E_RusticUIEchoLaunchSingleProcess_NATS_ZMQ(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS+ZMQ single-process echo e2e test in short mode")
	}

	ns := startInProcessNATSServerE2E(t)
	natsURL := ns.ClientURL()

	binPath := requireE2EForgeBin(t)

	cwd, err := os.Getwd()
	require.NoError(t, err)
	forgeRoot := filepath.Clean(filepath.Join(cwd, "..", ".."))

	server := startSingleProcessForgeServer(t, binPath, forgeRoot, natsURL,
		"--client-default-agent-transport", "supervisor-zmq",
		"--client-zmq-bridge-mode", "tcp",
	)
	client := &http.Client{Timeout: 30 * time.Second}

	// Reuse the NATS echo blueprint (same agent catalog entry + blueprint).
	seededBlueprintID := seedRusticUINATSEchoCatalog(t, client, server.rusticBase)

	blueprintID := selectRusticUIBlueprintByName(t, client, server.rusticBase, rusticUINATSZMQEchoBlueprintName)
	require.Equal(t, seededBlueprintID, blueprintID)

	blueprintDetails := getJSONMap(t, client, fmt.Sprintf("%s/catalog/blueprints/%s", server.rusticBase, url.PathEscape(blueprintID)))
	require.Equal(t, rusticUINATSZMQEchoBlueprintName, blueprintDetails["name"])

	guildName := "Rustic UI Echo NATS ZMQ " + time.Now().UTC().Format("150405")
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
	defer sysConn.Close()

	sysTranscript := &wsTranscript{}
	userTranscript := &wsTranscript{}
	sysEvents := startWSReader(sysConn, sysTranscript)

	waitForRusticUIHealthOK(t, sysConn, sysTranscript, sysEvents, 90*time.Second)

	userConn := openRusticUISocket(t, server.wsBase, wsID, "usercomms")
	defer userConn.Close()
	userEvents := startWSReader(userConn, userTranscript)

	participants := waitForRusticUIParticipants(t, sysEvents, 60*time.Second)
	echoParticipant := requireParticipant(t, participants, "bot", "Echo Agent")
	userParticipant := requireParticipant(t, participants, "human", "")

	const promptText = "hello nats zmq"
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
