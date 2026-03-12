package supervisor

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/redis/go-redis/v9"
	"github.com/shirou/gopsutil/v3/process"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/rustic-ai/forge/forge-go/filesystem"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/registry"
	"github.com/rustic-ai/forge/forge-go/telemetry"
)

type ProcessSupervisor struct {
	mu          sync.RWMutex
	agents      map[string]*ManagedAgent
	rdb         *redis.Client
	workDirBase string
	orgID       string
	detachGroup bool
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

func NewProcessSupervisor(rdb *redis.Client, opts ...ProcessSupervisorOption) *ProcessSupervisor {
	p := &ProcessSupervisor{
		agents:      make(map[string]*ManagedAgent),
		rdb:         rdb,
		workDirBase: resolveProcessWorkDirBase(""),
		orgID:       "default-org",
		detachGroup: true,
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

	return p.startProcess(ctx, guildID, agent, agentSpec, runtimeCmd, env)
}

func (p *ProcessSupervisor) startProcess(ctx context.Context, guildID string, agent *ManagedAgent, agentSpec *protocol.AgentSpec, runtimeCmd []string, env []string) error {
	ctx, span := otel.Tracer("forge.supervisor").Start(ctx, "supervisor.spawn")
	defer span.End()

	if len(runtimeCmd) == 0 {
		return fmt.Errorf("runtimeCmd is empty")
	}

	cmd := exec.CommandContext(ctx, runtimeCmd[0], runtimeCmd[1:]...)

	workDir, err := p.ensureAgentWorkDir(guildID, agent.ID)
	if err != nil {
		agent.SetState(StateFailed)
		agent.LastError = err
		return fmt.Errorf("failed to prepare working directory for agent %s: %w", agent.ID, err)
	}

	cmd.Dir = workDir
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
		agent.SetState(StateFailed)
		agent.LastError = err
		return fmt.Errorf("failed to start agent process %s: %w", agent.ID, err)
	}

	telemetry.SupervisorBootDuration.WithLabelValues("local-node", "process").Observe(time.Since(startBootTime).Seconds())

	agent.SetPID(cmd.Process.Pid)
	agent.SetState(StateRunning)

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

	if p.rdb != nil {
		_ = WriteStatusKey(ctx, p.rdb, guildID, agent.ID, "local-node", cmd.Process.Pid)
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
	homeDir, _ := os.UserHomeDir()

	switch {
	case root == "":
		if homeDir != "" {
			root = filepath.Join(homeDir, ".forge", "data")
		} else {
			root = filepath.Join(os.TempDir(), "forge-data")
		}
	case root == "~":
		root = homeDir
	case strings.HasPrefix(root, "~/"):
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
					if p.rdb != nil {
						_ = RefreshStatusKey(ctx, p.rdb, guildID, agent.ID)
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
		if p.rdb != nil {
			_ = DeleteStatusKey(ctx, p.rdb, guildID, agent.ID)
		}
		return
	}

	agent.SetState(StateRestarting)
	agent.LastError = err

	if p.rdb != nil {
		_ = SetRestartingStatus(ctx, p.rdb, guildID, agent.ID)
	}

	if time.Since(agent.StartedAt) > StableTime {
		agent.RestartCount = 0
	}

	agent.RestartCount++

	delay := ComputeBackoff(agent.RestartCount)
	if delay == 0 {
		agent.SetState(StateFailed)
		if p.rdb != nil {
			_ = SetFailedStatus(ctx, p.rdb, guildID, agent.ID)
		}
		return
	}

	slog.Info("agent crashed, restarting", "agent_id", agent.ID, "delay", delay, "attempt", agent.RestartCount)

	select {
	case <-time.After(delay):
		if !agent.IsStopRequested() {
			if err := p.startProcess(ctx, guildID, agent, agentSpec, runtimeCmd, env); err != nil {
				slog.Error("failed to restart process-managed agent", "guild_id", guildID, "agent_id", agent.ID, "error", err)
				agent.SetState(StateFailed)
				agent.LastError = err
				if p.rdb != nil {
					_ = SetFailedStatus(ctx, p.rdb, guildID, agent.ID)
				}
			}
		}
	case <-agent.stopCh:
		agent.SetState(StateStopped)
		if p.rdb != nil {
			_ = DeleteStatusKey(ctx, p.rdb, guildID, agent.ID)
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

	if p.rdb != nil {
		_ = DeleteStatusKey(ctx, p.rdb, agent.GuildID, agent.ID)
		_ = DeleteStatusKey(ctx, p.rdb, unknownGuildKey, agent.ID)
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
