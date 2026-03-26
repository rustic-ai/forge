package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/shirou/gopsutil/v3/process"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/rustic-ai/forge/forge-go/filesystem"
	"github.com/rustic-ai/forge/forge-go/forgepath"
	"github.com/rustic-ai/forge/forge-go/infraevents"
	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/registry"
	"github.com/rustic-ai/forge/forge-go/telemetry"
)

type ProcessSupervisor struct {
	mu               sync.RWMutex
	agents           map[string]*ManagedAgent
	bridges          map[string]*AgentMessagingBridge
	statusStore      AgentStatusStore
	msgBackend       messaging.Backend
	infraPublisher   *infraevents.Publisher
	workDirBase      string
	orgID            string
	defaultTransport protocol.AgentTransportMode
	detachGroup      bool
}

type ProcessSupervisorOption func(*ProcessSupervisor)

func WithWorkDirBase(dataDir string) ProcessSupervisorOption {
	return func(p *ProcessSupervisor) {
		p.workDirBase = resolveProcessWorkDirBase(dataDir)
	}
}

func WithOrganizationID(orgID string) ProcessSupervisorOption {
	return func(p *ProcessSupervisor) {
		if strings.TrimSpace(orgID) != "" {
			p.orgID = orgID
		}
	}
}

func WithAttachedProcessTree() ProcessSupervisorOption {
	return func(p *ProcessSupervisor) {
		p.detachGroup = false
	}
}

func WithDefaultAgentTransport(mode string) ProcessSupervisorOption {
	return func(p *ProcessSupervisor) {
		p.defaultTransport = protocol.NormalizeAgentTransportMode(mode)
	}
}

func WithMessagingBackend(b messaging.Backend) ProcessSupervisorOption {
	return func(p *ProcessSupervisor) {
		p.msgBackend = b
	}
}

func WithInfraEventPublisher(pub *infraevents.Publisher) ProcessSupervisorOption {
	return func(p *ProcessSupervisor) {
		p.infraPublisher = pub
	}
}

func NewProcessSupervisor(statusStore AgentStatusStore, opts ...ProcessSupervisorOption) *ProcessSupervisor {
	p := &ProcessSupervisor{
		agents:           make(map[string]*ManagedAgent),
		bridges:          make(map[string]*AgentMessagingBridge),
		statusStore:      statusStore,
		workDirBase:      resolveProcessWorkDirBase(""),
		orgID:            "default-org",
		defaultTransport: protocol.AgentTransportDirect,
		detachGroup:      true,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(p)
		}
	}

	return p
}

func (p *ProcessSupervisor) Launch(ctx context.Context, guildID string, agentSpec *protocol.AgentSpec, reg *registry.Registry, env []string) error {
	guildID = normalizeGuildID(guildID)
	key := scopedAgentKey(guildID, agentSpec.ID)
	p.mu.Lock()
	if existing, exists := p.agents[key]; exists {
		state := existing.GetState()
		if state != StateStopped && state != StateFailed {
			p.mu.Unlock()
			return fmt.Errorf("agent %s is already managed in guild %s", agentSpec.ID, normalizeGuildID(guildID))
		}
		delete(p.agents, key)
	}

	agent := NewManagedAgent(guildID, agentSpec.ID)
	p.agents[key] = agent
	p.mu.Unlock()

	entry, err := reg.Lookup(agentSpec.ClassName)
	if err != nil {
		agent.SetState(StateFailed)
		return fmt.Errorf("failed to lookup agent class %s: %w", agentSpec.ClassName, err)
	}

	runtimeCmd := registry.ResolveCommand(entry)

	if err := p.startProcess(ctx, guildID, agent, agentSpec, runtimeCmd, env); err != nil {
		_ = p.emitProcessEvent(ctx, guildID, agent.ID, "agent.process.failed", infraevents.SeverityError, "agent process failed before startup completed", nil, map[string]any{
			"error": err.Error(),
		})
		return err
	}
	return nil
}

func (p *ProcessSupervisor) startProcess(ctx context.Context, guildID string, agent *ManagedAgent, agentSpec *protocol.AgentSpec, runtimeCmd []string, env []string) error {
	ctx, span := otel.Tracer("forge.supervisor").Start(ctx, "supervisor.spawn")
	defer span.End()

	_ = p.emitProcessEvent(ctx, guildID, agent.ID, "agent.process.starting", infraevents.SeverityInfo, "agent process starting", nil, nil)

	if len(runtimeCmd) == 0 {
		_ = p.emitProcessEvent(ctx, guildID, agent.ID, "agent.process.start_failed", infraevents.SeverityError, "agent process start failed", nil, map[string]any{
			"reason": "empty_runtime_command",
		})
		return fmt.Errorf("runtimeCmd is empty")
	}

	cmd := exec.CommandContext(ctx, runtimeCmd[0], runtimeCmd[1:]...)

	workDir, err := p.ensureAgentWorkDir(guildID, agent.ID)
	if err != nil {
		agent.SetState(StateFailed)
		agent.LastError = err
		_ = p.emitProcessEvent(ctx, guildID, agent.ID, "agent.process.start_failed", infraevents.SeverityError, "agent process start failed", nil, map[string]any{
			"error":  err.Error(),
			"reason": "workdir_prepare_failed",
		})
		return fmt.Errorf("failed to prepare working directory for agent %s: %w", agent.ID, err)
	}

	cmd.Dir = workDir
	env = append([]string{}, env...)
	if transport := transportFromEnv(env, p.defaultTransport); transport == protocol.AgentTransportSupervisorZMQ {
		if p.msgBackend == nil {
			agent.SetState(StateFailed)
			agent.LastError = fmt.Errorf("supervisor-zmq transport requires a messaging backend")
			_ = p.emitProcessEvent(ctx, guildID, agent.ID, "agent.process.start_failed", infraevents.SeverityError, "agent process start failed", nil, map[string]any{
				"reason": "missing_messaging_backend",
			})
			return fmt.Errorf("supervisor-zmq transport requires a messaging backend")
		}

		bridge, err := NewAgentMessagingBridge(ctx, guildID, agent.ID, workDir, p.msgBackend)
		if err != nil {
			agent.SetState(StateFailed)
			agent.LastError = err
			_ = p.emitProcessEvent(ctx, guildID, agent.ID, "agent.process.start_failed", infraevents.SeverityError, "agent process start failed", nil, map[string]any{
				"error":  err.Error(),
				"reason": "bridge_create_failed",
			})
			return fmt.Errorf("failed to create agent messaging bridge: %w", err)
		}

		env, err = applySupervisorTransportEnv(env, bridge)
		if err != nil {
			bridge.Close()
			agent.SetState(StateFailed)
			agent.LastError = err
			_ = p.emitProcessEvent(ctx, guildID, agent.ID, "agent.process.start_failed", infraevents.SeverityError, "agent process start failed", nil, map[string]any{
				"error":  err.Error(),
				"reason": "transport_env_config_failed",
			})
			return fmt.Errorf("failed to configure supervisor transport env: %w", err)
		}
		p.setBridge(guildID, agent.ID, bridge)
	}
	cmd.Env = append(os.Environ(), env...)

	propagator := otel.GetTextMapPropagator()
	carrier := propagation.MapCarrier{}
	propagator.Inject(ctx, carrier)
	if tp, ok := carrier["traceparent"]; ok {
		cmd.Env = append(cmd.Env, fmt.Sprintf("TRACEPARENT=%s", tp))
	}

	cmd.Env = append(cmd.Env, processWorkDirEnv(workDir)...)

	stdoutPipe, _ := cmd.StdoutPipe()
	stderrPipe, _ := cmd.StderrPipe()

	configureCommandForProcessGroup(cmd, p.detachGroup)

	startBootTime := time.Now()
	if err := cmd.Start(); err != nil {
		p.stopBridge(guildID, agent.ID)
		agent.SetState(StateFailed)
		agent.LastError = err
		_ = p.emitProcessEvent(ctx, guildID, agent.ID, "agent.process.start_failed", infraevents.SeverityError, "agent process start failed", nil, map[string]any{
			"error":  err.Error(),
			"reason": "cmd_start_failed",
		})
		return fmt.Errorf("failed to start agent process %s: %w", agent.ID, err)
	}

	telemetry.SupervisorBootDuration.WithLabelValues("local-node", "process").Observe(time.Since(startBootTime).Seconds())

	agent.SetPID(cmd.Process.Pid)
	agent.SetState(StateRunning)
	_ = p.emitProcessEvent(ctx, guildID, agent.ID, "agent.process.started", infraevents.SeverityInfo, "agent process started", nil, map[string]any{
		"pid": cmd.Process.Pid,
	})

	logger := slog.With("agent_id", agent.ID, "guild_id", guildID, "node_id", "local-node")

	go func() {
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			logger.Info(scanner.Text(), "source", "agent_stdout")
		}
	}()
	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			logger.Error(scanner.Text(), "source", "agent_stderr")
		}
	}()

	_ = applyResourceLimits(cmd.Process.Pid, agentSpec)

	if p.statusStore != nil {
		_ = p.statusStore.WriteStatus(ctx, guildID, agent.ID, &AgentStatusJSON{State: "running", NodeID: "local-node", PID: cmd.Process.Pid, Timestamp: time.Now()}, 30*time.Second)
	}

	go p.monitorProcess(guildID, agent, agentSpec, cmd, runtimeCmd, env)

	return nil
}

func (p *ProcessSupervisor) ensureAgentWorkDir(guildID, agentID string) (string, error) {
	workDir := p.resolveAgentWorkDir(guildID, agentID)
	tmpDir := filepath.Join(workDir, "tmp")
	cacheDir := filepath.Join(workDir, ".cache")
	dataDir := filepath.Join(workDir, ".local", "share")

	for _, dir := range []string{workDir, tmpDir, cacheDir, dataDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", err
		}
	}

	return workDir, nil
}

func (p *ProcessSupervisor) resolveAgentWorkDir(guildID, agentID string) string {
	resolver := filesystem.NewFileSystemResolver(p.workDirBase)
	return resolver.ResolvePath(
		sanitizePathComponent(p.orgID),
		sanitizePathComponent(guildID),
		sanitizePathComponent(agentID),
	)
}

func processWorkDirEnv(workDir string) []string {
	tmpDir := filepath.Join(workDir, "tmp")
	cacheDir := filepath.Join(workDir, ".cache")
	dataDir := filepath.Join(workDir, ".local", "share")

	return []string{
		"FORGE_AGENT_WORKDIR=" + workDir,
		"HOME=" + workDir,
		"USERPROFILE=" + workDir,
		"TMPDIR=" + tmpDir,
		"TMP=" + tmpDir,
		"TEMP=" + tmpDir,
		"XDG_CACHE_HOME=" + cacheDir,
		"XDG_DATA_HOME=" + dataDir,
	}
}

func resolveProcessWorkDirBase(dataDir string) string {
	root := strings.TrimSpace(dataDir)

	switch {
	case root == "":
		root = forgepath.Resolve("data")
	case root == "~":
		homeDir, _ := os.UserHomeDir()
		root = homeDir
	case strings.HasPrefix(root, "~/"):
		homeDir, _ := os.UserHomeDir()
		root = filepath.Join(homeDir, root[2:])
	}

	return filepath.Join(filepath.Clean(root), "agents")
}

func sanitizePathComponent(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}

	var b strings.Builder
	for _, r := range value {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(r)
		case strings.ContainsRune("._-#@=", r):
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}

	sanitized := strings.Trim(b.String(), " ._")
	if sanitized == "" || sanitized == "." || sanitized == ".." {
		return "unknown"
	}

	return sanitized
}

func (p *ProcessSupervisor) monitorProcess(guildID string, agent *ManagedAgent, agentSpec *protocol.AgentSpec, cmd *exec.Cmd, runtimeCmd []string, env []string) {
	guildID = normalizeGuildID(guildID)
	ctx, lifecycleSpan := otel.Tracer("forge.supervisor").Start(context.Background(), "agent.lifecycle")
	defer lifecycleSpan.End()

	ticker := time.NewTicker(10 * time.Second)
	done := make(chan struct{})

	go func() {
		for {
			select {
			case <-ticker.C:
				if agent.GetState() == StateRunning {
					if p.statusStore != nil {
						_ = p.statusStore.RefreshStatus(ctx, guildID, agent.ID, 30*time.Second)
					}
					pid := agent.GetPID()
					if pid > 0 {
						if proc, err := process.NewProcess(int32(pid)); err == nil {
							if cpuPct, err := proc.CPUPercent(); err == nil {
								telemetry.AgentCPUCores.WithLabelValues(guildID, agent.ID, "local-node").Set(cpuPct)
							}
							if memInfo, err := proc.MemoryInfo(); err == nil {
								telemetry.AgentMemoryBytes.WithLabelValues(guildID, agent.ID, "local-node").Set(float64(memInfo.RSS))
							}
						}
					}
				}
			case <-done:
				ticker.Stop()
				return
			}
		}
	}()

	err := cmd.Wait()
	close(done)
	p.stopBridge(guildID, agent.ID)

	agent.LastExitAt = time.Now()
	exitCode := "1"
	if err == nil {
		exitCode = "0"
	} else if exitErr, ok := err.(*exec.ExitError); ok {
		exitCode = fmt.Sprintf("%d", exitErr.ExitCode())
	}

	telemetry.AgentExitCodes.WithLabelValues(guildID, agent.ID, "local-node", exitCode).Inc()

	if agent.IsStopRequested() {
		agent.SetState(StateStopped)
		_ = p.emitProcessEvent(ctx, guildID, agent.ID, "agent.process.stopped", infraevents.SeverityInfo, "agent process stopped", nil, map[string]any{
			"exit_code": exitCode,
		})
		if p.statusStore != nil {
			_ = p.statusStore.DeleteStatus(ctx, guildID, agent.ID)
		}
		return
	}

	_ = p.emitProcessEvent(ctx, guildID, agent.ID, "agent.process.exited", infraevents.SeverityWarn, "agent process exited", nil, map[string]any{
		"exit_code":      exitCode,
		"stop_requested": false,
	})

	agent.SetState(StateRestarting)
	agent.LastError = err

	if p.statusStore != nil {
		_ = p.statusStore.WriteStatus(ctx, guildID, agent.ID, &AgentStatusJSON{State: "restarting", Timestamp: time.Now()}, 30*time.Second)
	}

	if time.Since(agent.StartedAt) > StableTime {
		agent.RestartCount = 0
	}

	agent.RestartCount++

	delay := ComputeBackoff(agent.RestartCount)
	if delay == 0 {
		agent.SetState(StateFailed)
		_ = p.emitProcessEvent(ctx, guildID, agent.ID, "agent.process.failed", infraevents.SeverityError, "agent process failed after retry exhaustion", &agent.RestartCount, map[string]any{
			"exit_code": exitCode,
		})
		if p.statusStore != nil {
			_ = p.statusStore.WriteStatus(ctx, guildID, agent.ID, &AgentStatusJSON{State: "failed", Timestamp: time.Now()}, 300*time.Second)
		}
		return
	}

	slog.Info("agent crashed, restarting", "agent_id", agent.ID, "delay", delay, "attempt", agent.RestartCount)
	_ = p.emitProcessEvent(ctx, guildID, agent.ID, "agent.process.restarting", infraevents.SeverityWarn, "agent process restarting after unexpected exit", &agent.RestartCount, map[string]any{
		"delay_ms":  delay.Milliseconds(),
		"exit_code": exitCode,
	})

	select {
	case <-time.After(delay):
		if !agent.IsStopRequested() {
			if err := p.startProcess(ctx, guildID, agent, agentSpec, runtimeCmd, env); err != nil {
				slog.Error("failed to restart process-managed agent", "guild_id", guildID, "agent_id", agent.ID, "error", err)
				agent.SetState(StateFailed)
				agent.LastError = err
				_ = p.emitProcessEvent(ctx, guildID, agent.ID, "agent.process.failed", infraevents.SeverityError, "agent process failed during restart", &agent.RestartCount, map[string]any{
					"error": err.Error(),
				})
				if p.statusStore != nil {
					_ = p.statusStore.WriteStatus(ctx, guildID, agent.ID, &AgentStatusJSON{State: "failed", Timestamp: time.Now()}, 300*time.Second)
				}
			}
		}
	case <-agent.stopCh:
		agent.SetState(StateStopped)
		_ = p.emitProcessEvent(ctx, guildID, agent.ID, "agent.process.stopped", infraevents.SeverityInfo, "agent process stopped", nil, map[string]any{
			"exit_code": exitCode,
		})
		if p.statusStore != nil {
			_ = p.statusStore.DeleteStatus(ctx, guildID, agent.ID)
		}
	}
}

func (p *ProcessSupervisor) Stop(ctx context.Context, guildID, agentID string) error {
	key := scopedAgentKey(guildID, agentID)
	p.mu.RLock()
	agent, exists := p.agents[key]
	p.mu.RUnlock()

	if !exists {
		return fmt.Errorf("agent %s not managed in guild %s", agentID, normalizeGuildID(guildID))
	}

	agent.RequestStop()
	pid := agent.GetPID()

	if pid > 0 {
		_ = terminateProcessTree(pid, p.detachGroup)
	}

	if p.statusStore != nil {
		_ = p.statusStore.DeleteStatus(ctx, agent.GuildID, agent.ID)
		_ = p.statusStore.DeleteStatus(ctx, unknownGuildKey, agent.ID)
	}

	return nil
}

func (p *ProcessSupervisor) Status(ctx context.Context, guildID, agentID string) (string, error) {
	key := scopedAgentKey(guildID, agentID)
	p.mu.RLock()
	agent, exists := p.agents[key]
	p.mu.RUnlock()

	if !exists {
		return "unknown", nil
	}
	return string(agent.GetState()), nil
}

func (p *ProcessSupervisor) GetPID(ctx context.Context, guildID, agentID string) (int, error) {
	key := scopedAgentKey(guildID, agentID)
	p.mu.RLock()
	agent, exists := p.agents[key]
	p.mu.RUnlock()

	if !exists {
		return 0, fmt.Errorf("agent %s not managed in guild %s", agentID, normalizeGuildID(guildID))
	}

	return agent.GetPID(), nil
}

func (p *ProcessSupervisor) StopAll(ctx context.Context) error {
	p.mu.RLock()
	agents := make([]*ManagedAgent, 0, len(p.agents))
	for _, agent := range p.agents {
		agents = append(agents, agent)
	}
	p.mu.RUnlock()

	var firstErr error
	for _, agent := range agents {
		if err := p.Stop(ctx, agent.GuildID, agent.ID); err != nil {
			slog.Warn("failed to stop agent", "guild_id", agent.GuildID, "agent_id", agent.ID, "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	return firstErr
}

func (p *ProcessSupervisor) setBridge(guildID, agentID string, bridge *AgentMessagingBridge) {
	key := scopedAgentKey(guildID, agentID)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bridges[key] = bridge
}

func (p *ProcessSupervisor) stopBridge(guildID, agentID string) {
	key := scopedAgentKey(guildID, agentID)

	p.mu.Lock()
	bridge := p.bridges[key]
	delete(p.bridges, key)
	p.mu.Unlock()

	if bridge != nil {
		bridge.Close()
	}
}

func transportFromEnv(env []string, defaultTransport protocol.AgentTransportMode) protocol.AgentTransportMode {
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, protocol.EnvForgeAgentTransport+"="); ok {
			return protocol.NormalizeAgentTransportMode(value)
		}
	}
	return defaultTransport
}

func applySupervisorTransportEnv(env []string, bridge *AgentMessagingBridge) ([]string, error) {
	configJSON, err := json.Marshal(map[string]interface{}{
		"endpoint": bridge.Endpoint(),
	})
	if err != nil {
		return nil, err
	}

	clientProps := map[string]interface{}{}
	if raw := lookupEnvValue(env, "FORGE_CLIENT_PROPERTIES_JSON"); raw != "" {
		_ = json.Unmarshal([]byte(raw), &clientProps)
	}
	clientProps["backend_config"] = map[string]interface{}{
		"endpoint": bridge.Endpoint(),
	}

	clientPropsJSON, err := json.Marshal(clientProps)
	if err != nil {
		return nil, err
	}

	env = upsertEnv(env, protocol.EnvForgeAgentTransport, string(protocol.AgentTransportSupervisorZMQ))
	env = upsertEnv(env, "FORGE_CLIENT_MODULE", protocol.SupervisorZMQBackendModule)
	env = upsertEnv(env, "FORGE_CLIENT_TYPE", protocol.SupervisorZMQBackendClass)
	env = upsertEnv(env, "FORGE_CLIENT_PROPERTIES_JSON", string(clientPropsJSON))
	env = upsertEnv(env, protocol.EnvForgeSupervisorZMQEndpoint, bridge.Endpoint())
	env = upsertEnv(env, protocol.EnvForgeSupervisorZMQConfigJSON, string(configJSON))

	return env, nil
}

func upsertEnv(env []string, key, value string) []string {
	prefix := key + "="
	updated := make([]string, 0, len(env)+1)
	replaced := false
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			if !replaced {
				updated = append(updated, prefix+value)
				replaced = true
			}
			continue
		}
		updated = append(updated, entry)
	}
	if !replaced {
		updated = append(updated, prefix+value)
	}
	return updated
}

func lookupEnvValue(env []string, key string) string {
	prefix := key + "="
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], prefix) {
			return strings.TrimPrefix(env[i], prefix)
		}
	}
	return ""
}

func (p *ProcessSupervisor) emitProcessEvent(ctx context.Context, guildID, agentID, kind, severity, message string, attempt *int, detail map[string]any) error {
	return p.infraPublisher.Emit(ctx, infraevents.EmitParams{
		Kind:            kind,
		Severity:        severity,
		GuildID:         guildID,
		AgentID:         agentID,
		OrganizationID:  p.orgID,
		SourceComponent: "forge-go.supervisor.process",
		Attempt:         attempt,
		Message:         message,
		Detail:          detail,
	})
}
