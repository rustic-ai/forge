package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/registry"
	"github.com/rustic-ai/forge/forge-go/telemetry"
)

type DockerAgent struct {
	ID           string
	GuildID      string
	ContainerID  string
	State        AgentState
	RestartCount int
	StartedAt    time.Time
	LastExitAt   time.Time
	LastError    error
	stopCh       chan struct{}
	stopOnce     sync.Once
}

func (a *DockerAgent) RequestStop() {
	a.stopOnce.Do(func() { close(a.stopCh) })
}

func (a *DockerAgent) IsStopRequested() bool {
	select {
	case <-a.stopCh:
		return true
	default:
		return false
	}
}

type DockerSupervisor struct {
	cli              *client.Client
	statusStore      AgentStatusStore
	managed          map[string]*DockerAgent
	bridges          map[string]*AgentMessagingBridge
	mu               sync.RWMutex
	msgBackend       messaging.Backend
	defaultTransport protocol.AgentTransportMode
	zmqBridgeMode    BridgeTransportMode
}

// DockerSupervisorOption configures a DockerSupervisor.
type DockerSupervisorOption func(*DockerSupervisor)

// WithDockerDefaultTransport sets the default agent transport mode.
func WithDockerDefaultTransport(mode string) DockerSupervisorOption {
	return func(d *DockerSupervisor) {
		d.defaultTransport = protocol.NormalizeAgentTransportMode(mode)
	}
}

// WithDockerMessagingBackend sets the messaging backend used for ZMQ bridges.
func WithDockerMessagingBackend(backend messaging.Backend) DockerSupervisorOption {
	return func(d *DockerSupervisor) {
		d.msgBackend = backend
	}
}

// WithDockerZMQBridgeMode sets whether ZMQ bridges use IPC or TCP.
func WithDockerZMQBridgeMode(mode BridgeTransportMode) DockerSupervisorOption {
	return func(d *DockerSupervisor) {
		d.zmqBridgeMode = mode
	}
}

func NewDockerSupervisor(statusStore AgentStatusStore, opts ...DockerSupervisorOption) (*DockerSupervisor, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithVersion("1.44"))
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := cli.Ping(ctx); err != nil {
		return nil, fmt.Errorf("docker daemon is unreachable: %w", err)
	}

	d := &DockerSupervisor{
		cli:              cli,
		statusStore:      statusStore,
		managed:          make(map[string]*DockerAgent),
		bridges:          make(map[string]*AgentMessagingBridge),
		defaultTransport: protocol.AgentTransportDirect,
		zmqBridgeMode:    BridgeTransportIPC,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(d)
		}
	}
	return d, nil
}

func (d *DockerSupervisor) Available() bool {
	if d.cli == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := d.cli.Ping(ctx)
	return err == nil
}

func (d *DockerSupervisor) ensureImage(ctx context.Context, imageRef string) error {
	_, err := d.cli.ImageInspect(ctx, imageRef)
	if err == nil {
		return nil
	}

	out, err := d.cli.ImagePull(ctx, imageRef, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("failed to pull image %s: %w", imageRef, err)
	}
	defer func() { _ = out.Close() }()

	_, _ = io.Copy(io.Discard, out)
	return nil
}

func BuildContainerConfig(agentSpec *protocol.AgentSpec, entry *registry.AgentRegistryEntry,
	guildID string, imageRef string, cmd []string, env []string) (*container.Config, *container.HostConfig) {

	containerCfg := &container.Config{
		Image:      imageRef,
		Env:        append(env, "HOME=/tmp"),
		Entrypoint: []string{},
		WorkingDir: "/",
	}

	if currentUser, err := user.Current(); err == nil {
		containerCfg.User = fmt.Sprintf("%s:%s", currentUser.Uid, currentUser.Gid)
	}

	if len(cmd) > 0 {
		containerCfg.Cmd = cmd
	}
	containerCfg.Labels = map[string]string{
		"ai.forge.agent": agentSpec.ID,
		"ai.forge.guild": guildID,
	}

	hostCfg := &container.HostConfig{}

	if len(entry.Network) == 0 || (len(entry.Network) == 1 && entry.Network[0] == "none") {
		hostCfg.NetworkMode = "none"
	} else if len(entry.Network) == 1 && entry.Network[0] == "host" {
		hostCfg.NetworkMode = "host"
	} else {
		hostCfg.NetworkMode = "bridge"
	}

	if agentSpec.Resources.NumCPUs != nil && *agentSpec.Resources.NumCPUs > 0 {
		hostCfg.NanoCPUs = int64(*agentSpec.Resources.NumCPUs * 1e9)
	}

	var binds []string
	for _, fs := range entry.Filesystem {
		mode := "ro"
		if fs.Mode == "rw" {
			mode = "rw"
		}
		binds = append(binds, fmt.Sprintf("%s:%s:%s,z", fs.Path, fs.Path, mode))
	}
	hostCfg.Binds = binds

	return containerCfg, hostCfg
}

func (d *DockerSupervisor) Launch(ctx context.Context, guildID string, agentSpec *protocol.AgentSpec, reg *registry.Registry, env []string) error {
	guildID = normalizeGuildID(guildID)
	key := scopedAgentKey(guildID, agentSpec.ID)
	d.mu.Lock()
	if existing, exists := d.managed[key]; exists {
		if existing.State != StateStopped && existing.State != StateFailed {
			d.mu.Unlock()
			return fmt.Errorf("agent %s is already managed in guild %s", agentSpec.ID, normalizeGuildID(guildID))
		}
		delete(d.managed, key)
	}
	d.mu.Unlock()

	entry, err := reg.Lookup(agentSpec.ClassName)
	if err != nil {
		return err
	}

	imageRef := entry.Image
	if imageRef == "" {
		imageRef = "ghcr.io/astral-sh/uv:python3.12-bookworm-slim"
	}

	if err := d.ensureImage(ctx, imageRef); err != nil {
		return fmt.Errorf("failed to resolve container image: %w", err)
	}

	env = append(env, "UV_PROJECT_ENVIRONMENT=/tmp/.venv")

	var cleanEnv []string
	for _, e := range env {
		if !strings.HasPrefix(e, "UV_CACHE_DIR=") {
			cleanEnv = append(cleanEnv, e)
		}
	}
	env = cleanEnv

	var cmd []string
	if entry.Runtime == registry.RuntimeDocker {
		if entry.Executable != "" {
			cmd = append(cmd, entry.Executable)
		}
		cmd = append(cmd, entry.Args...)
	} else {
		cmd = registry.ResolveCommand(entry)
	}

	if len(cmd) > 0 {
		base := filepath.Base(cmd[0])
		if base == "uvx" || base == "uvx.exe" {
			cmd = append([]string{"uv", "tool", "run"}, cmd[1:]...)
		}
	}

	// Create ZMQ bridge when transport requires it.
	var bridge *AgentMessagingBridge
	transport := transportFromEnv(env, d.defaultTransport)
	if transport == protocol.AgentTransportSupervisorZMQ {
		if d.msgBackend == nil {
			return fmt.Errorf("supervisor-zmq transport requires a messaging backend")
		}

		var bridgeErr error
		bridge, bridgeErr = NewAgentMessagingBridgeWithMode(ctx, guildID, agentSpec.ID, "", d.msgBackend, d.zmqBridgeMode)
		if bridgeErr != nil {
			return fmt.Errorf("failed to create agent messaging bridge: %w", bridgeErr)
		}

		env, err = applySupervisorTransportEnv(env, bridge)
		if err != nil {
			bridge.Close()
			return fmt.Errorf("failed to configure supervisor transport env: %w", err)
		}
	}

	containerCfg, hostCfg := BuildContainerConfig(agentSpec, entry, guildID, imageRef, cmd, env)

	// Adjust container config for bridge connectivity.
	if bridge != nil {
		if bridge.Mode() == BridgeTransportIPC {
			// Volume-mount the socket file into the container.
			hostCfg.Binds = append(hostCfg.Binds, fmt.Sprintf("%s:%s:rw", bridge.SocketPath(), bridge.SocketPath()))
		} else if bridge.Mode() == BridgeTransportTCP && hostCfg.NetworkMode == "none" {
			// TCP bridge requires host network for loopback access.
			hostCfg.NetworkMode = "host"
		}
	}

	resp, err := d.cli.ContainerCreate(ctx, containerCfg, hostCfg, nil, nil, "")
	if err != nil {
		if bridge != nil {
			bridge.Close()
		}
		return fmt.Errorf("failed to create container: %w", err)
	}

	startBootTime := time.Now()
	if err := d.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		if bridge != nil {
			bridge.Close()
		}
		return fmt.Errorf("failed to start container: %w", err)
	}
	telemetry.SupervisorBootDuration.WithLabelValues("local-node", "docker").Observe(time.Since(startBootTime).Seconds())

	d.mu.Lock()
	agent := &DockerAgent{
		ID:          agentSpec.ID,
		GuildID:     guildID,
		ContainerID: resp.ID,
		State:       StateRunning,
		StartedAt:   time.Now(),
		stopCh:      make(chan struct{}),
	}
	d.managed[key] = agent
	d.mu.Unlock()

	if bridge != nil {
		d.setBridge(guildID, agentSpec.ID, bridge)
	}

	if d.statusStore != nil {
		_ = d.statusStore.WriteStatus(ctx, normalizeGuildID(guildID), agentSpec.ID, &AgentStatusJSON{State: "running", NodeID: "local-docker", PID: -1, Timestamp: time.Now()}, 30*time.Second)
	}

	go d.streamLogs(context.Background(), guildID, agentSpec.ID, resp.ID)
	go d.pollStats(context.Background(), guildID, agentSpec.ID, resp.ID)
	go d.monitorContainer(context.Background(), guildID, agent, agentSpec, reg, env)

	return nil
}

func (d *DockerSupervisor) monitorContainer(ctx context.Context, guildID string, agent *DockerAgent, agentSpec *protocol.AgentSpec, reg *registry.Registry, env []string) {
	guildID = normalizeGuildID(guildID)
	logger := slog.With("agent_id", agent.ID, "guild_id", guildID, "node_id", "local-docker")

	statusCh, errCh := d.cli.ContainerWait(ctx, agent.ContainerID, container.WaitConditionNotRunning)

	select {
	case result := <-statusCh:
		agent.LastExitAt = time.Now()
		exitCode := fmt.Sprintf("%d", result.StatusCode)
		telemetry.AgentExitCodes.WithLabelValues(guildID, agent.ID, "local-docker", exitCode).Inc()
		logger.Warn("container exited", "exit_code", result.StatusCode)

	case err := <-errCh:
		agent.LastExitAt = time.Now()
		agent.LastError = err
		logger.Error("container wait failed", "error", err)
		telemetry.AgentExitCodes.WithLabelValues(guildID, agent.ID, "local-docker", "error").Inc()
	}

	d.stopBridge(guildID, agent.ID)

	if agent.IsStopRequested() {
		d.mu.Lock()
		agent.State = StateStopped
		d.mu.Unlock()
		if d.statusStore != nil {
			_ = d.statusStore.DeleteStatus(ctx, guildID, agent.ID)
		}
		return
	}

	d.mu.Lock()
	agent.State = StateRestarting
	d.mu.Unlock()

	if d.statusStore != nil {
		_ = d.statusStore.WriteStatus(ctx, guildID, agent.ID, &AgentStatusJSON{State: "restarting", Timestamp: time.Now()}, 30*time.Second)
	}

	if time.Since(agent.StartedAt) > StableTime {
		agent.RestartCount = 0
	}
	agent.RestartCount++

	delay := ComputeBackoff(agent.RestartCount)
	if delay == 0 {
		d.mu.Lock()
		agent.State = StateFailed
		d.mu.Unlock()
		if d.statusStore != nil {
			_ = d.statusStore.WriteStatus(ctx, guildID, agent.ID, &AgentStatusJSON{State: "failed", Timestamp: time.Now()}, 300*time.Second)
		}
		logger.Error("agent exceeded max restart attempts, giving up")
		return
	}

	logger.Info("container crashed, restarting", "delay", delay, "attempt", agent.RestartCount)

	select {
	case <-time.After(delay):
		if agent.IsStopRequested() {
			return
		}
		_ = d.cli.ContainerRemove(ctx, agent.ContainerID, container.RemoveOptions{Force: true})

		if err := d.relaunchContainer(ctx, guildID, agent, agentSpec, reg, env); err != nil {
			logger.Error("failed to restart container", "error", err)
			d.mu.Lock()
			agent.State = StateFailed
			agent.LastError = err
			d.mu.Unlock()
		}

	case <-agent.stopCh:
		d.mu.Lock()
		agent.State = StateStopped
		d.mu.Unlock()
		if d.statusStore != nil {
			_ = d.statusStore.DeleteStatus(ctx, guildID, agent.ID)
		}
	}
}

func (d *DockerSupervisor) relaunchContainer(ctx context.Context, guildID string, agent *DockerAgent, agentSpec *protocol.AgentSpec, reg *registry.Registry, env []string) error {
	entry, err := reg.Lookup(agentSpec.ClassName)
	if err != nil {
		return err
	}

	imageRef := entry.Image
	if imageRef == "" {
		imageRef = "ghcr.io/astral-sh/uv:python3.12-bookworm-slim"
	}

	var cmd []string
	if entry.Runtime == registry.RuntimeDocker {
		if entry.Executable != "" {
			cmd = append(cmd, entry.Executable)
		}
		cmd = append(cmd, entry.Args...)
	} else {
		cmd = registry.ResolveCommand(entry)
	}

	if len(cmd) > 0 {
		base := filepath.Base(cmd[0])
		if base == "uvx" || base == "uvx.exe" {
			cmd = append([]string{"uv", "tool", "run"}, cmd[1:]...)
		}
	}

	// Recreate bridge for the new container when transport is supervisor-zmq.
	var bridge *AgentMessagingBridge
	transport := transportFromEnv(env, d.defaultTransport)
	if transport == protocol.AgentTransportSupervisorZMQ && d.msgBackend != nil {
		bridge, err = NewAgentMessagingBridgeWithMode(ctx, guildID, agent.ID, "", d.msgBackend, d.zmqBridgeMode)
		if err != nil {
			return fmt.Errorf("failed to create agent messaging bridge: %w", err)
		}
		env, err = applySupervisorTransportEnv(env, bridge)
		if err != nil {
			bridge.Close()
			return fmt.Errorf("failed to configure supervisor transport env: %w", err)
		}
	}

	containerCfg, hostCfg := BuildContainerConfig(agentSpec, entry, guildID, imageRef, cmd, env)

	if bridge != nil {
		if bridge.Mode() == BridgeTransportIPC {
			hostCfg.Binds = append(hostCfg.Binds, fmt.Sprintf("%s:%s:rw", bridge.SocketPath(), bridge.SocketPath()))
		} else if bridge.Mode() == BridgeTransportTCP && hostCfg.NetworkMode == "none" {
			hostCfg.NetworkMode = "host"
		}
	}

	resp, err := d.cli.ContainerCreate(ctx, containerCfg, hostCfg, nil, nil, "")
	if err != nil {
		if bridge != nil {
			bridge.Close()
		}
		return fmt.Errorf("failed to create container: %w", err)
	}

	if err := d.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		if bridge != nil {
			bridge.Close()
		}
		return fmt.Errorf("failed to start container: %w", err)
	}

	d.mu.Lock()
	agent.ContainerID = resp.ID
	agent.State = StateRunning
	agent.StartedAt = time.Now()
	d.mu.Unlock()

	if bridge != nil {
		d.setBridge(guildID, agent.ID, bridge)
	}

	if d.statusStore != nil {
		_ = d.statusStore.WriteStatus(ctx, guildID, agent.ID, &AgentStatusJSON{State: "running", NodeID: "local-docker", PID: -1, Timestamp: time.Now()}, 30*time.Second)
	}

	go d.streamLogs(context.Background(), guildID, agent.ID, resp.ID)
	go d.pollStats(context.Background(), guildID, agent.ID, resp.ID)
	go d.monitorContainer(context.Background(), guildID, agent, agentSpec, reg, env)

	return nil
}

func (d *DockerSupervisor) streamLogs(ctx context.Context, guildID, agentID, containerID string) {
	logger := slog.With("agent_id", agentID, "guild_id", guildID, "node_id", "local-docker")

	options := container.LogsOptions{ShowStdout: true, ShowStderr: true, Follow: true}
	out, err := d.cli.ContainerLogs(ctx, containerID, options)
	if err != nil {
		logger.Error("failed to attach to container logs", "error", err)
		return
	}
	defer func() { _ = out.Close() }()

	stdoutReader, stdoutWriter := io.Pipe()
	stderrReader, stderrWriter := io.Pipe()

	go func() {
		scanner := bufio.NewScanner(stdoutReader)
		for scanner.Scan() {
			logger.Info(scanner.Text(), "source", "agent_stdout")
		}
	}()

	go func() {
		scanner := bufio.NewScanner(stderrReader)
		for scanner.Scan() {
			logger.Error(scanner.Text(), "source", "agent_stderr")
		}
	}()

	_, _ = stdcopy.StdCopy(stdoutWriter, stderrWriter, out)
	_ = stdoutWriter.Close()
	_ = stderrWriter.Close()
}

func (d *DockerSupervisor) pollStats(ctx context.Context, guildID, agentID, containerID string) {
	guildID = normalizeGuildID(guildID)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			key := scopedAgentKey(guildID, agentID)
			d.mu.RLock()
			agent, exists := d.managed[key]
			d.mu.RUnlock()
			if !exists || agent.State != StateRunning {
				return
			}

			stats, err := d.cli.ContainerStats(ctx, containerID, false)
			if err != nil {
				continue
			}

			var v container.StatsResponse
			if err := json.NewDecoder(stats.Body).Decode(&v); err == nil {
				cpuDelta := v.CPUStats.CPUUsage.TotalUsage - v.PreCPUStats.CPUUsage.TotalUsage
				systemDelta := v.CPUStats.SystemUsage - v.PreCPUStats.SystemUsage

				if systemDelta > 0 && cpuDelta > 0 {
					cpuPct := (float64(cpuDelta) / float64(systemDelta)) * float64(len(v.CPUStats.CPUUsage.PercpuUsage)) * 100.0
					telemetry.AgentCPUCores.WithLabelValues(guildID, agentID, "local-docker").Set(cpuPct)
				}
				telemetry.AgentMemoryBytes.WithLabelValues(guildID, agentID, "local-docker").Set(float64(v.MemoryStats.Usage))
			}
			_ = stats.Body.Close()

			if d.statusStore != nil {
				_ = d.statusStore.RefreshStatus(ctx, guildID, agentID, 30*time.Second)
			}
		}
	}
}

func (d *DockerSupervisor) Stop(ctx context.Context, guildID, agentID string) error {
	key := scopedAgentKey(guildID, agentID)
	d.mu.RLock()
	agent, exists := d.managed[key]
	d.mu.RUnlock()

	if !exists {
		return fmt.Errorf("agent %s not managed by docker supervisor in guild %s", agentID, normalizeGuildID(guildID))
	}

	agent.RequestStop()

	timeout := 5
	err := d.cli.ContainerStop(ctx, agent.ContainerID, container.StopOptions{Timeout: &timeout})
	if err != nil {
		slog.Warn("failed to stop container", "container_id", agent.ContainerID, "error", err)
	}

	_ = d.cli.ContainerRemove(ctx, agent.ContainerID, container.RemoveOptions{Force: true})

	d.stopBridge(guildID, agentID)

	d.mu.Lock()
	agent.State = StateStopped
	d.mu.Unlock()

	if d.statusStore != nil {
		_ = d.statusStore.DeleteStatus(ctx, agent.GuildID, agent.ID)
	}

	return nil
}

func (d *DockerSupervisor) Status(ctx context.Context, guildID, agentID string) (string, error) {
	key := scopedAgentKey(guildID, agentID)
	d.mu.RLock()
	agent, exists := d.managed[key]
	d.mu.RUnlock()

	if !exists {
		return "", fmt.Errorf("agent %s not managed by docker supervisor in guild %s", agentID, normalizeGuildID(guildID))
	}

	return string(agent.State), nil
}

func (d *DockerSupervisor) StopAll(ctx context.Context) error {
	d.mu.RLock()
	agents := make([]*DockerAgent, 0, len(d.managed))
	for _, agent := range d.managed {
		agents = append(agents, agent)
	}
	d.mu.RUnlock()

	var lastErr error
	for _, agent := range agents {
		if err := d.Stop(ctx, agent.GuildID, agent.ID); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

func (d *DockerSupervisor) setBridge(guildID, agentID string, bridge *AgentMessagingBridge) {
	key := scopedAgentKey(guildID, agentID)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.bridges[key] = bridge
}

func (d *DockerSupervisor) stopBridge(guildID, agentID string) {
	key := scopedAgentKey(guildID, agentID)
	d.mu.Lock()
	bridge := d.bridges[key]
	delete(d.bridges, key)
	d.mu.Unlock()
	if bridge != nil {
		bridge.Close()
	}
}
