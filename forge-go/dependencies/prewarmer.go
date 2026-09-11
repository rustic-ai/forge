package dependencies

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rustic-ai/forge/forge-go/forgepath"
	"github.com/rustic-ai/forge/forge-go/infraevents"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/registry"
)

const (
	ModeOff   = "off"
	ModeGuild = "guild"
)

// ValidateMode keeps dependency preparation opt-in for generic Forge clients.
func ValidateMode(mode string) error {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", ModeOff, ModeGuild:
		return nil
	default:
		return fmt.Errorf("unsupported client dependency prewarm mode %q (expected off or guild)", mode)
	}
}

func Enabled(mode string) bool {
	return strings.EqualFold(strings.TrimSpace(mode), ModeGuild)
}

// SupportsRuntimePreparation reports whether this worker can honor the v1
// preparation contract with a uv-managed interpreter request.
func SupportsRuntimePreparation(mode, python string) bool {
	python = strings.TrimSpace(python)
	return Enabled(mode) && python != "" && !filepath.IsAbs(python)
}

type Metadata struct {
	GuildID        string
	AgentID        string
	OrganizationID string
	RequestID      string
}

type Config struct {
	Context       context.Context
	Registry      *registry.Registry
	Publisher     *infraevents.Publisher
	NodeID        string
	Workers       int
	UVXPath       string
	UVPath        string
	Python        string
	UVVersion     string
	ForgeRevision string
	Run           func(context.Context, string, []string, []string) error
}

type preparation struct {
	done chan struct{}
	once sync.Once
	err  error
}

func (p *preparation) complete(err error) {
	p.once.Do(func() {
		p.err = err
		close(p.done)
	})
}

type work struct {
	key          string
	requirements []string
	args         []string
	metadata     Metadata
	preparation  *preparation
}

// PreparationError identifies dependency gating failures without changing
// supervisor error contracts.
type PreparationError struct {
	Err error
}

func (e *PreparationError) Error() string {
	return "dependency_preparation_failed: " + e.Err.Error()
}

func (e *PreparationError) Unwrap() error { return e.Err }

// Coordinator owns process-wide single-flight dependency preparation.
type Coordinator struct {
	ctx           context.Context
	registry      *registry.Registry
	publisher     *infraevents.Publisher
	nodeID        string
	uvxPath       string
	uvPath        string
	python        string
	uvVersion     string
	forgeRevision string
	run           func(context.Context, string, []string, []string) error
	queue         chan work

	mu                sync.Mutex
	inflight          map[string]*preparation
	ready             map[string]struct{}
	pythonPreparation *preparation
	pythonReady       bool
}

func NewCoordinator(cfg Config) (*Coordinator, error) {
	if cfg.Context == nil {
		return nil, errors.New("dependency prewarmer context is required")
	}
	if cfg.Registry == nil {
		return nil, errors.New("dependency prewarmer registry is required")
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 4
	}
	if cfg.UVXPath == "" {
		cfg.UVXPath = registry.ResolveUVXCommand()
	}
	if cfg.UVPath == "" {
		cfg.UVPath = registry.ResolveUVCommand()
	}
	if cfg.Python == "" {
		cfg.Python = registry.UVPython()
	}
	if cfg.UVVersion == "" {
		cfg.UVVersion = detectUVVersion(cfg.Context, cfg.UVXPath)
	}
	if cfg.ForgeRevision == "" {
		cfg.ForgeRevision = strings.TrimSpace(os.Getenv("FORGE_REVISION"))
	}
	if cfg.Run == nil {
		cfg.Run = runCommand
	}

	c := &Coordinator{
		ctx:           cfg.Context,
		registry:      cfg.Registry,
		publisher:     cfg.Publisher,
		nodeID:        cfg.NodeID,
		uvxPath:       cfg.UVXPath,
		uvPath:        cfg.UVPath,
		python:        cfg.Python,
		uvVersion:     cfg.UVVersion,
		forgeRevision: cfg.ForgeRevision,
		run:           cfg.Run,
		queue:         make(chan work, cfg.Workers*8),
		inflight:      make(map[string]*preparation),
		ready:         make(map[string]struct{}),
	}
	for range cfg.Workers {
		go c.worker()
	}
	go c.cancelInflight()
	return c, nil
}

func (c *Coordinator) cancelInflight() {
	<-c.ctx.Done()
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, prep := range c.inflight {
		prep.complete(c.ctx.Err())
		delete(c.inflight, key)
	}
}

func detectUVVersion(ctx context.Context, uvxPath string) string {
	versionCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(versionCtx, uvxPath, "--version").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(output))
}

func runCommand(ctx context.Context, executable string, args, env []string) error {
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		detail := sanitizeOutput(string(output), env)
		if detail == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, detail)
	}
	return nil
}

func sanitizeOutput(output string, env []string) string {
	output = strings.TrimSpace(output)
	if len(output) > 2048 {
		output = output[len(output)-2048:]
	}
	words := strings.Fields(output)
	for i, word := range words {
		if scheme := strings.Index(word, "://"); scheme >= 0 {
			rest := word[scheme+3:]
			if at := strings.Index(rest, "@"); at >= 0 {
				words[i] = word[:scheme+3] + "***@" + rest[at+1:]
			}
		}
	}
	output = strings.Join(words, " ")
	for _, assignment := range env {
		key, value, ok := strings.Cut(assignment, "=")
		if !ok || value == "" {
			continue
		}
		upperKey := strings.ToUpper(key)
		if strings.Contains(upperKey, "TOKEN") || strings.Contains(upperKey, "PASSWORD") || strings.Contains(upperKey, "SECRET") || strings.Contains(upperKey, "CREDENTIAL") {
			output = strings.ReplaceAll(output, value, "***")
		}
	}
	return output
}

// WarmSystem primes the shared download cache without delaying client readiness.
func (c *Coordinator) WarmSystem() {
	entry := &registry.AgentRegistryEntry{Runtime: registry.RuntimeUVX}
	requirements := registry.DependencyRequirements(entry, nil)
	args := c.argsForEntry(entry, nil)
	_, _ = c.schedule(Metadata{}, requirements, args)
}

// WarmPython starts managed-Python preparation without affecting worker
// readiness. A later explicit preparation joins this work or retries it.
func (c *Coordinator) WarmPython() {
	_, _ = c.preparePython(context.Background())
}

// PreparePython ensures the configured interpreter is available. Version
// requests are installed through uv. Preparation-capable workers reject
// system interpreter paths so prepared and spawned environments share the
// Forge-owned managed runtime.
func (c *Coordinator) PreparePython(ctx context.Context) error {
	prep, err := c.preparePython(ctx)
	if err != nil {
		return &PreparationError{Err: fmt.Errorf("python_download_failed: %w", err)}
	}
	if err := wait(ctx, prep); err != nil {
		return fmt.Errorf("python_download_failed: %w", err)
	}
	return nil
}

func (c *Coordinator) PythonPrepared() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pythonReady
}

func (c *Coordinator) preparePython(_ context.Context) (*preparation, error) {
	c.mu.Lock()
	if err := c.ctx.Err(); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	if c.pythonReady {
		c.mu.Unlock()
		prep := &preparation{done: make(chan struct{})}
		close(prep.done)
		return prep, nil
	}
	if c.pythonPreparation != nil {
		prep := c.pythonPreparation
		c.mu.Unlock()
		return prep, nil
	}
	prep := &preparation{done: make(chan struct{})}
	c.pythonPreparation = prep
	c.mu.Unlock()

	go func() {
		python := strings.TrimSpace(c.python)
		var err error
		if python == "" || filepath.IsAbs(python) {
			err = errors.New("runtime preparation requires a managed Python version")
		} else {
			err = c.run(c.ctx, c.uvPath, []string{"python", "install", python}, c.runtimeEnv())
		}
		c.mu.Lock()
		if err == nil && c.ctx.Err() == nil {
			c.pythonReady = true
		}
		c.pythonPreparation = nil
		prep.complete(err)
		c.mu.Unlock()
	}()
	return prep, nil
}

// PrepareGuild schedules and waits for the manager and every static UVX agent.
func (c *Coordinator) PrepareGuild(ctx context.Context, metadata Metadata, managerEntry *registry.AgentRegistryEntry, managerExtraDeps []string, spec *protocol.GuildSpec) error {
	managerPreparation, err := c.schedule(metadata, registry.DependencyRequirements(managerEntry, managerExtraDeps), c.argsForEntry(managerEntry, managerExtraDeps))
	if err != nil {
		return &PreparationError{Err: err}
	}
	preparations := []*preparation{managerPreparation}
	for i := range spec.Agents {
		agentSpec := &spec.Agents[i]
		entry, lookupErr := c.registry.Lookup(agentSpec.ClassName)
		if lookupErr != nil {
			c.emit(metadataForAgent(metadata, agentSpec.ID), "dependency.prepare.failed", infraevents.SeverityError, "dependency preparation could not resolve agent class", map[string]any{"error": lookupErr.Error()})
			return &PreparationError{Err: lookupErr}
		}
		if entry.Runtime != registry.RuntimeUVX {
			continue
		}
		agentMetadata := metadataForAgent(metadata, agentSpec.ID)
		agentPreparation, scheduleErr := c.schedule(agentMetadata, registry.DependencyRequirements(entry, agentSpec.ForgeExtraDeps), c.argsForEntry(entry, agentSpec.ForgeExtraDeps))
		if scheduleErr != nil {
			c.emit(agentMetadata, "dependency.prepare.failed", infraevents.SeverityError, "dependency preparation could not be scheduled", map[string]any{"error": scheduleErr.Error()})
			return &PreparationError{Err: scheduleErr}
		}
		preparations = append(preparations, agentPreparation)
	}
	for _, prep := range preparations {
		if err := wait(ctx, prep); err != nil {
			return err
		}
	}
	return nil
}

// PrepareAgent gates one UVX spawn on its exact environment.
func (c *Coordinator) PrepareAgent(ctx context.Context, metadata Metadata, entry *registry.AgentRegistryEntry, extraDeps []string) error {
	if entry.Runtime != registry.RuntimeUVX {
		return nil
	}
	prep, err := c.schedule(metadata, registry.DependencyRequirements(entry, extraDeps), c.argsForEntry(entry, extraDeps))
	if err != nil {
		return &PreparationError{Err: err}
	}
	return wait(ctx, prep)
}

func (c *Coordinator) AgentPrepared(entry *registry.AgentRegistryEntry, extraDeps []string) bool {
	if entry.Runtime != registry.RuntimeUVX {
		return true
	}
	requirements, err := normalizeRequirements(registry.DependencyRequirements(entry, extraDeps))
	if err != nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ready := c.ready[c.key(requirements)]
	return ready
}

func metadataForAgent(metadata Metadata, agentID string) Metadata {
	metadata.AgentID = agentID
	return metadata
}

func wait(ctx context.Context, prep *preparation) error {
	select {
	case <-ctx.Done():
		return &PreparationError{Err: ctx.Err()}
	case <-prep.done:
		if prep.err != nil {
			return &PreparationError{Err: prep.err}
		}
		return nil
	}
}

func (c *Coordinator) schedule(metadata Metadata, requirements, args []string) (*preparation, error) {
	normalized, err := normalizeRequirements(requirements)
	if err != nil {
		return nil, err
	}
	key := c.key(normalized)

	c.mu.Lock()
	if err := c.ctx.Err(); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	if _, ok := c.ready[key]; ok {
		c.mu.Unlock()
		prep := &preparation{done: make(chan struct{})}
		close(prep.done)
		c.emit(metadata, "dependency.prepare.completed", infraevents.SeverityInfo, "dependency environment already prepared", map[string]any{"key": key, "cache_state": "memory", "duration_ms": 0})
		return prep, nil
	}
	if prep := c.inflight[key]; prep != nil {
		c.mu.Unlock()
		return prep, nil
	}
	prep := &preparation{done: make(chan struct{})}
	c.inflight[key] = prep
	c.mu.Unlock()

	item := work{key: key, requirements: normalized, args: append([]string(nil), args...), metadata: metadata, preparation: prep}
	select {
	case c.queue <- item:
		return prep, nil
	case <-c.ctx.Done():
		c.mu.Lock()
		delete(c.inflight, key)
		prep.complete(c.ctx.Err())
		c.mu.Unlock()
		return nil, c.ctx.Err()
	}
}

func (c *Coordinator) argsForEntry(entry *registry.AgentRegistryEntry, extraDeps []string) []string {
	command := registry.ResolveCommand(entry, extraDeps)
	if len(command) < 4 {
		return nil
	}
	args := append([]string(nil), command[1:len(command)-3]...)
	return append(args, "python", "-c", "import rustic_ai.forge.agent_runner")
}

func normalizeRequirements(requirements []string) ([]string, error) {
	normalized := make([]string, 0, len(requirements))
	seen := make(map[string]struct{}, len(requirements))
	for _, requirement := range requirements {
		requirement = strings.TrimSpace(requirement)
		if requirement == "" {
			continue
		}
		if strings.ContainsAny(requirement, "\r\n\x00") {
			return nil, fmt.Errorf("dependency requirement contains prohibited control characters")
		}
		if _, ok := seen[requirement]; ok {
			continue
		}
		seen[requirement] = struct{}{}
		normalized = append(normalized, requirement)
	}
	if len(normalized) == 0 {
		return nil, errors.New("dependency requirements are empty")
	}
	slices.Sort(normalized)
	return normalized, nil
}

func (c *Coordinator) key(requirements []string) string {
	indexFingerprint := sha256.Sum256([]byte(strings.Join([]string{
		os.Getenv("UV_INDEX"), os.Getenv("UV_INDEX_URL"), os.Getenv("UV_DEFAULT_INDEX"), os.Getenv("UV_EXTRA_INDEX_URL"),
	}, "\x00")))
	material := strings.Join([]string{
		runtime.GOOS, runtime.GOARCH, c.uvxPath, c.uvVersion, c.python,
		c.forgeRevision, os.Getenv("FORGE_PYTHON_PKG"), hex.EncodeToString(indexFingerprint[:]),
		strings.Join(requirements, "\x00"),
	}, "\x00")
	sum := sha256.Sum256([]byte(material))
	return hex.EncodeToString(sum[:])
}

func (c *Coordinator) worker() {
	for {
		select {
		case <-c.ctx.Done():
			return
		case item := <-c.queue:
			c.execute(item)
		}
	}
}

func (c *Coordinator) execute(item work) {
	start := time.Now()
	c.emit(item.metadata, "dependency.prepare.started", infraevents.SeverityInfo, "dependency preparation started", map[string]any{"key": item.key, "cache_state": "uv"})
	err := c.run(c.ctx, c.uvxPath, item.args, c.runtimeEnv())
	duration := time.Since(start)

	detail := map[string]any{"key": item.key, "cache_state": "uv", "duration_ms": duration.Milliseconds()}
	if err != nil {
		detail["error"] = err.Error()
		c.emit(item.metadata, "dependency.prepare.failed", infraevents.SeverityError, "dependency preparation failed", detail)
	} else {
		c.emit(item.metadata, "dependency.prepare.completed", infraevents.SeverityInfo, "dependency preparation completed", detail)
	}

	c.mu.Lock()
	delete(c.inflight, item.key)
	if err == nil && c.ctx.Err() == nil {
		c.ready[item.key] = struct{}{}
	}
	item.preparation.complete(err)
	c.mu.Unlock()
}

func (c *Coordinator) runtimeEnv() []string {
	env := os.Environ()
	values := map[string]string{
		"UV_CACHE_DIR":               forgepath.Resolve("uv_cache"),
		"UV_PYTHON_INSTALL_DIR":      forgepath.Resolve("python"),
		"UV_PYTHON_DOWNLOADS":        "automatic",
		"UV_MANAGED_PYTHON":          "1",
		"UV_PYTHON_INSTALL_REGISTRY": "0",
		"UV_NO_PROGRESS":             "1",
	}
	for key, value := range values {
		prefix := key + "="
		replaced := false
		for i := range env {
			if strings.HasPrefix(env[i], prefix) {
				env[i] = prefix + value
				replaced = true
			}
		}
		if !replaced {
			env = append(env, prefix+value)
		}
	}
	return env
}

func (c *Coordinator) emit(metadata Metadata, kind, severity, message string, detail map[string]any) {
	_ = c.publisher.Emit(c.ctx, infraevents.EmitParams{
		Kind: kind, Severity: severity, GuildID: metadata.GuildID, AgentID: metadata.AgentID,
		OrganizationID: metadata.OrganizationID, RequestID: metadata.RequestID, NodeID: c.nodeID,
		SourceComponent: "forge-go.dependency-prewarmer", SourceInstanceID: c.nodeID,
		Message: message, Detail: detail,
	})
}
