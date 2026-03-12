package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rustic-ai/forge/forge-go/protocol"
	gopsprocess "github.com/shirou/gopsutil/v3/process"
	"github.com/stretchr/testify/require"
)

func getSleepCmd() []string {
	if runtime.GOOS == "windows" {
		return []string{"ping", "-n", "10", "127.0.0.1"}
	}
	return []string{"sleep", "10"}
}

func getEchoCmd() []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd", "/C", "echo", "hello"}
	}
	return []string{"echo", "hello"}
}

func getWorkDirProbeCmd() []string {
	if runtime.GOOS == "windows" {
		return []string{
			"cmd",
			"/C",
			`cd > cwd.txt & (echo %FORGE_AGENT_WORKDIR%& echo %HOME%& echo %TMP%& echo %XDG_CACHE_HOME%& echo %XDG_DATA_HOME%& echo %USERPROFILE%) > env.txt & ping -n 10 127.0.0.1 >NUL`,
		}
	}
	return []string{
		"sh",
		"-c",
		`pwd > cwd.txt; printf "%s\n%s\n%s\n%s\n%s\n%s\n" "$FORGE_AGENT_WORKDIR" "$HOME" "$TMPDIR" "$XDG_CACHE_HOME" "$XDG_DATA_HOME" "$USERPROFILE" > env.txt; sleep 10`,
	}
}

func getChildTreeCmd() []string {
	if runtime.GOOS == "windows" {
		return []string{"cmd", "/C", "ping -n 10 127.0.0.1 >NUL"}
	}
	return []string{"sh", "-c", `sleep 30 & echo $! > child.pid; wait`}
}

func TestProcessSupervisorLaunchAndStop(t *testing.T) {
	sup := NewProcessSupervisor(nil, WithWorkDirBase(t.TempDir()))
	ctx := context.Background()
	guildID := "test-guild"

	agent1 := NewManagedAgent(guildID, "agent1")
	sup.mu.Lock()
	sup.agents[scopedAgentKey(guildID, "agent1")] = agent1
	sup.mu.Unlock()

	err := sup.startProcess(ctx, guildID, agent1, &protocol.AgentSpec{}, getSleepCmd(), []string{"FOO=bar"})
	if err != nil {
		t.Fatalf("Failed to launch process: %v", err)
	}

	status, err := sup.Status(ctx, guildID, "agent1")
	if err != nil {
		t.Fatalf("Failed to get status: %v", err)
	}
	if status != string(StateRunning) {
		t.Errorf("Expected status to be running, got %s", status)
	}

	// Wait briefly so process spawns
	time.Sleep(100 * time.Millisecond)

	err = sup.Stop(ctx, guildID, "agent1")
	if err != nil {
		t.Fatalf("Failed to stop process: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	status, _ = sup.Status(ctx, guildID, "agent1")
	if status != string(StateStopped) {
		t.Errorf("Expected status to be stopped, got %s", status)
	}
}

func TestProcessSupervisorCrashRestart(t *testing.T) {
	sup := NewProcessSupervisor(nil, WithWorkDirBase(t.TempDir()))
	ctx := context.Background()
	guildID := "test-guild"

	// process that exits immediately
	agentCrash := NewManagedAgent(guildID, "agent-crash")
	sup.mu.Lock()
	sup.agents[scopedAgentKey(guildID, "agent-crash")] = agentCrash
	sup.mu.Unlock()

	err := sup.startProcess(ctx, guildID, agentCrash, &protocol.AgentSpec{}, getEchoCmd(), nil)
	if err != nil {
		t.Fatalf("Failed to launch process: %v", err)
	}

	// Wait for process to exit and background monitor to catch it
	time.Sleep(200 * time.Millisecond)

	status, _ := sup.Status(ctx, guildID, "agent-crash")
	if status != string(StateRestarting) {
		t.Errorf("Expected status to be restarting, got %s", status)
	}

	require.NoError(t, sup.Stop(ctx, guildID, "agent-crash"))
	time.Sleep(200 * time.Millisecond)

	status, _ = sup.Status(ctx, guildID, "agent-crash")
	if status != string(StateStopped) {
		t.Errorf("Expected status to be stopped after Stop(), got %s", status)
	}
}

func TestProcessSupervisorGuildScopedAgentKeys(t *testing.T) {
	sup := NewProcessSupervisor(nil, WithWorkDirBase(t.TempDir()))
	ctx := context.Background()

	agentID := "upa-dummyuserid"
	guildA := "guild-a"
	guildB := "guild-b"

	a := NewManagedAgent(guildA, agentID)
	a.SetState(StateRunning)
	b := NewManagedAgent(guildB, agentID)
	b.SetState(StateStopped)

	sup.mu.Lock()
	sup.agents[scopedAgentKey(guildA, agentID)] = a
	sup.agents[scopedAgentKey(guildB, agentID)] = b
	sup.mu.Unlock()

	statusA, err := sup.Status(ctx, guildA, agentID)
	if err != nil {
		t.Fatalf("status for guild A failed: %v", err)
	}
	if statusA != string(StateRunning) {
		t.Fatalf("expected guild A status running, got %s", statusA)
	}

	statusB, err := sup.Status(ctx, guildB, agentID)
	if err != nil {
		t.Fatalf("status for guild B failed: %v", err)
	}
	if statusB != string(StateStopped) {
		t.Fatalf("expected guild B status stopped, got %s", statusB)
	}
}

func TestProcessSupervisorLaunchesIntoPerAgentWorkDir(t *testing.T) {
	baseDir := t.TempDir()
	sup := NewProcessSupervisor(
		nil,
		WithWorkDirBase(baseDir),
		WithOrganizationID("org-1"),
	)
	ctx := context.Background()
	guildID := "guild-1"

	agent := NewManagedAgent(guildID, "agent-1")
	sup.mu.Lock()
	sup.agents[scopedAgentKey(guildID, "agent-1")] = agent
	sup.mu.Unlock()

	require.NoError(t, sup.startProcess(ctx, guildID, agent, &protocol.AgentSpec{}, getWorkDirProbeCmd(), nil))
	defer func() {
		_ = sup.Stop(ctx, guildID, "agent-1")
	}()

	workDir := sup.resolveAgentWorkDir(guildID, "agent-1")
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(workDir, "cwd.txt"))
		return err == nil
	}, 5*time.Second, 50*time.Millisecond)

	cwdBytes, err := os.ReadFile(filepath.Join(workDir, "cwd.txt"))
	require.NoError(t, err)
	require.Equal(t, filepath.Clean(workDir), filepath.Clean(strings.TrimSpace(string(cwdBytes))))

	envBytes, err := os.ReadFile(filepath.Join(workDir, "env.txt"))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(envBytes)), "\n")
	require.GreaterOrEqual(t, len(lines), 6)
	require.Equal(t, filepath.Clean(workDir), filepath.Clean(strings.TrimSpace(lines[0])))
	require.Equal(t, filepath.Clean(workDir), filepath.Clean(strings.TrimSpace(lines[1])))
	require.Equal(t, filepath.Clean(filepath.Join(workDir, "tmp")), filepath.Clean(strings.TrimSpace(lines[2])))
	require.Equal(t, filepath.Clean(filepath.Join(workDir, ".cache")), filepath.Clean(strings.TrimSpace(lines[3])))
	require.Equal(t, filepath.Clean(filepath.Join(workDir, ".local", "share")), filepath.Clean(strings.TrimSpace(lines[4])))
	require.Equal(t, filepath.Clean(workDir), filepath.Clean(strings.TrimSpace(lines[5])))
}

func TestProcessSupervisorAttachedProcessTreeStopsSubprocesses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("attached process tree semantics are only asserted on unix in this test")
	}

	baseDir := t.TempDir()
	sup := NewProcessSupervisor(
		nil,
		WithWorkDirBase(baseDir),
		WithAttachedProcessTree(),
	)
	require.False(t, sup.detachGroup)

	ctx := context.Background()
	guildID := "guild-attach"
	agentID := "agent-attach"

	agent := NewManagedAgent(guildID, agentID)
	sup.mu.Lock()
	sup.agents[scopedAgentKey(guildID, agentID)] = agent
	sup.mu.Unlock()

	require.NoError(t, sup.startProcess(ctx, guildID, agent, &protocol.AgentSpec{}, getChildTreeCmd(), nil))

	workDir := sup.resolveAgentWorkDir(guildID, agentID)
	childPIDPath := filepath.Join(workDir, "child.pid")
	require.Eventually(t, func() bool {
		_, err := os.Stat(childPIDPath)
		return err == nil
	}, 5*time.Second, 50*time.Millisecond)

	childPIDRaw, err := os.ReadFile(childPIDPath)
	require.NoError(t, err)

	childPID, err := strconv.Atoi(strings.TrimSpace(string(childPIDRaw)))
	require.NoError(t, err)

	childAlive, err := gopsprocess.PidExists(int32(childPID))
	require.NoError(t, err)
	require.True(t, childAlive)

	require.NoError(t, sup.Stop(ctx, guildID, agentID))

	require.Eventually(t, func() bool {
		alive, err := gopsprocess.PidExists(int32(childPID))
		return err == nil && !alive
	}, 5*time.Second, 50*time.Millisecond)

	status, err := sup.Status(ctx, guildID, agentID)
	require.NoError(t, err)
	require.Equal(t, string(StateStopped), status)
}
