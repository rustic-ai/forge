package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rustic-ai/forge/forge-go/guild"
	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"gopkg.in/yaml.v3"
)

// RuntimeConfig holds configuration for the guild runtime
type RuntimeConfig struct {
	Backend          string // "redis" or "nats"
	OrgID            string // Organization ID
	UserID           string // User ID for sending messages
	UserName         string // User name for sending messages
	ForgeHome        string // Forge home directory (optional)
	ForgeRoot        string // Forge repository root
	DependencyConfig string // Path to dependency config
	AgentRegistry    string // Path to agent registry
	ForgePythonPath  string // Path to forge-python package
	NATSUrl          string // NATS server URL (if using NATS backend)
	SupervisorType   string // "process", "docker", or "bubblewrap"
	PythonPath       string // Path to Python executable (optional, will auto-detect)
}

// GuildRuntime manages an embedded forge runtime for running guilds
type GuildRuntime struct {
	config      RuntimeConfig
	serverCmd   *exec.Cmd
	serverBase  string
	rusticBase  string
	redisAddr   string
	redisClient *redis.Client
	tempDir     string
	dbPath      string
	dataDir     string
	ctx         context.Context
	cancel      context.CancelFunc
	agentNames  map[string]string // Maps agent ID to agent name
}

// AgentStatus represents the status of an agent
type AgentStatus struct {
	AgentID string
	State   string
	PID     int
}

// NewGuildRuntime creates a new guild runtime instance
func NewGuildRuntime(config RuntimeConfig) (*GuildRuntime, error) {
	// Set defaults
	if config.Backend == "" {
		config.Backend = "nats"
	}
	if config.OrgID == "" {
		config.OrgID = "local-dev"
	}
	if config.UserID == "" {
		config.UserID = "test-user"
	}
	if config.UserName == "" {
		config.UserName = "Test User"
	}
	if config.SupervisorType == "" {
		config.SupervisorType = "process"
	}

	// Create temp directory for runtime
	tempDir, err := os.MkdirTemp("", "forge-cli-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}

	dbPath := filepath.Join(tempDir, "forge-cli.db")
	dataDir := filepath.Join(tempDir, "forge-data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		os.RemoveAll(tempDir)
		return nil, fmt.Errorf("failed to create data dir: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	runtime := &GuildRuntime{
		config:     config,
		tempDir:    tempDir,
		dbPath:     dbPath,
		dataDir:    dataDir,
		ctx:        ctx,
		cancel:     cancel,
		agentNames: make(map[string]string),
	}

	return runtime, nil
}

// Start starts the embedded forge server
func (r *GuildRuntime) Start() error {
	// Reserve a local address for the server
	listenAddr, err := reserveLocalAddr()
	if err != nil {
		return fmt.Errorf("failed to reserve listen address: %w", err)
	}

	embeddedRedisAddr, err := reserveLocalAddr()
	if err != nil {
		return fmt.Errorf("failed to reserve redis address: %w", err)
	}
	r.redisAddr = embeddedRedisAddr

	// Find forge binary
	binPath, err := exec.LookPath("forge")
	if err != nil {
		// Try to build it
		slog.Info("forge binary not found, attempting to build...")
		buildCmd := exec.Command("go", "build", "-o", filepath.Join(r.tempDir, "forge"), "./cmd/forge")
		buildCmd.Dir = r.config.ForgeRoot
		if output, err := buildCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to build forge: %w\n%s", err, output)
		}
		binPath = filepath.Join(r.tempDir, "forge")
	}

	// Build server arguments
	args := []string{
		"server",
		"--listen", listenAddr,
		"--db", "sqlite:///" + r.dbPath,
		"--embedded-redis-addr", embeddedRedisAddr,
		"--data-dir", r.dataDir,
		"--dependency-config", r.config.DependencyConfig,
		"--with-client",
		"--client-node-id", "cli-node",
		"--client-metrics-addr", "127.0.0.1:0",
		"--client-default-supervisor", r.config.SupervisorType,
	}

	if r.config.NATSUrl != "" {
		args = append(args, "--nats", r.config.NATSUrl)
	}

	// Create server command
	cmd := exec.Command(binPath, args...)
	cmd.Dir = r.config.ForgeRoot
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Redirect server logs to temp files to avoid cluttering the CLI
	// Only show them if there's an error
	logFile := filepath.Join(r.tempDir, "server.log")
	if logF, err := os.Create(logFile); err == nil {
		cmd.Stdout = logF
		cmd.Stderr = logF
	}

	// Set environment variables
	env := []string{}

	// If Python path is specified, ensure its directory is first in PATH
	if r.config.PythonPath != "" {
		pythonDir := filepath.Dir(r.config.PythonPath)
		currentPath := os.Getenv("PATH")
		env = append(env, "PATH="+pythonDir+":"+currentPath)
		env = append(env, "FORGE_PYTHON_EXECUTABLE="+r.config.PythonPath)
	}

	// Add all other environment variables
	for _, e := range os.Environ() {
		// Skip PATH if we already set it above
		if r.config.PythonPath != "" && strings.HasPrefix(e, "PATH=") {
			continue
		}
		env = append(env, e)
	}

	// Add Forge-specific variables
	env = append(env,
		"FORGE_AGENT_REGISTRY="+r.config.AgentRegistry,
		"FORGE_PYTHON_PKG="+r.config.ForgePythonPath,
		"FORGE_ENABLE_PUBLIC_API=true",
		"FORGE_ENABLE_UI_API=true",
		"FORGE_IDENTITY_MODE=local",
		"FORGE_QUOTA_MODE=local",
		"PYTHONUNBUFFERED=1",
	)
	if r.config.NATSUrl != "" {
		env = append(env, "FORGE_EXTRA_DEPS=rusticai-nats")
	}

	cmd.Env = env

	// Start server
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start forge server: %w", err)
	}

	r.serverCmd = cmd
	r.serverBase = "http://" + listenAddr
	r.rusticBase = r.serverBase + "/rustic"

	// Wait for server to be ready
	if err := r.waitForReady(30 * time.Second); err != nil {
		r.kill()
		return fmt.Errorf("server did not become ready: %w", err)
	}

	// Create Redis client
	r.redisClient = redis.NewClient(&redis.Options{Addr: embeddedRedisAddr})

	slog.Info("Guild runtime started", "listen_addr", listenAddr, "backend", r.config.Backend)

	// Seed agent registry into catalog
	if err := r.seedAgentRegistry(); err != nil {
		slog.Warn("Failed to seed agent registry", "error", err)
		// Continue anyway - some agents might already be registered
	}

	return nil
}

// seedAgentRegistry loads the agent registry and registers all agents in the catalog
func (r *GuildRuntime) seedAgentRegistry() error {
	registryPath := r.config.AgentRegistry
	content, err := os.ReadFile(registryPath)
	if err != nil {
		return fmt.Errorf("failed to read registry: %w", err)
	}

	var registry struct {
		Entries []map[string]interface{} `yaml:"entries"`
	}
	if err := yaml.Unmarshal(content, &registry); err != nil {
		return fmt.Errorf("failed to parse registry: %w", err)
	}

	slog.Info("Seeding agent registry", "agent_count", len(registry.Entries))

	// Register each agent
	for _, agent := range registry.Entries {
		className, _ := agent["class_name"].(string)
		if className == "" {
			continue
		}

		// Convert to catalog agent entry format
		agentEntry := map[string]interface{}{
			"qualified_class_name": className,
			"agent_name":           agent["id"], // API expects agent_name
			"agent_doc":            agent["description"],
			"agent_props_schema":   map[string]interface{}{}, // Empty schema for now
			"message_handlers":     map[string]interface{}{}, // Empty for now
		}

		// POST to /rustic/catalog/agents
		if err := r.postJSON(r.rusticBase+"/catalog/agents", agentEntry, nil); err != nil {
			// Ignore duplicate errors
			if !strings.Contains(err.Error(), "409") && !strings.Contains(err.Error(), "duplicate") {
				slog.Warn("Failed to register agent", "class_name", className, "error", err)
			}
		}
	}

	return nil
}

// LoadGuild loads a guild spec from a file
func (r *GuildRuntime) LoadGuild(specPath string) (*protocol.GuildSpec, error) {
	// Read file and check if it's a blueprint wrapper
	content, err := os.ReadFile(specPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	// Check if this is a blueprint wrapper (has a "spec" field)
	var checker map[string]interface{}
	json.Unmarshal(content, &checker)

	if specField, hasSpec := checker["spec"]; hasSpec && specField != nil {
		// This is a blueprint wrapper - extract the nested spec
		var wrapper struct {
			Spec json.RawMessage `json:"spec"`
		}
		if err := json.Unmarshal(content, &wrapper); err != nil {
			return nil, fmt.Errorf("failed to parse wrapper: %w", err)
		}

		var nestedSpec protocol.GuildSpec
		if err := json.Unmarshal(wrapper.Spec, &nestedSpec); err != nil {
			return nil, fmt.Errorf("failed to parse nested spec: %w", err)
		}
		return &nestedSpec, nil
	}

	// This is a direct guild spec
	spec, _, err := guild.ParseFile(specPath)
	if err != nil {
		return nil, fmt.Errorf("failed to parse guild spec: %w", err)
	}
	return spec, nil
}

// LaunchGuild launches a guild from a spec
func (r *GuildRuntime) LaunchGuild(spec *protocol.GuildSpec) (string, error) {
	// Create blueprint in catalog
	blueprintResp, err := r.createBlueprint(spec)
	if err != nil {
		return "", fmt.Errorf("failed to create blueprint: %w", err)
	}

	blueprintID, ok := blueprintResp["id"].(string)
	if !ok {
		return "", fmt.Errorf("blueprint response missing id")
	}

	// Launch guild from blueprint
	guildID, err := r.launchFromBlueprint(blueprintID, spec.Name)
	if err != nil {
		return "", fmt.Errorf("failed to launch guild: %w", err)
	}

	// Wait for guild to be running
	if err := r.waitForGuildRunning(guildID, 2*time.Minute); err != nil {
		return "", fmt.Errorf("guild did not start: %w", err)
	}

	// Build agent name mapping for display
	r.buildAgentNameMap(guildID, spec)

	slog.Info("Guild launched successfully", "guild_id", guildID, "name", spec.Name)
	return guildID, nil
}

// buildAgentNameMap creates a mapping from agent IDs to agent names
func (r *GuildRuntime) buildAgentNameMap(guildID string, spec *protocol.GuildSpec) {
	// Add manager agent
	r.agentNames[guildID+"#manager_agent"] = spec.Name + " Manager"

	// Query running agents to get their IDs
	statuses, err := r.GetAgentStatuses(guildID)
	if err != nil {
		return
	}

	// Map running agent IDs to their spec names. Agent IDs embed the agent name
	// (e.g. "<guild>#<agent_name>"), so match on that rather than guessing by
	// index: the previous implementation assigned spec.Agents[0].Name to every
	// non-manager agent, mislabeling every guild with more than one agent. When
	// the match is not unique we leave the ID unmapped and GetAgentName falls
	// back to the raw ID instead of asserting a wrong name.
	for agentID := range statuses {
		if agentID == guildID+"#manager_agent" {
			continue
		}

		matched := ""
		matches := 0
		for _, agent := range spec.Agents {
			if agent.Name != "" && strings.Contains(agentID, agent.Name) {
				matched = agent.Name
				matches++
			}
		}
		if matches == 1 {
			r.agentNames[agentID] = matched
		}
	}
}

// GetAgentName returns the display name for an agent ID
func (r *GuildRuntime) GetAgentName(agentID string) string {
	if name, ok := r.agentNames[agentID]; ok {
		return name
	}
	return agentID
}

// GetAgentStatuses gets the status of all agents in a guild
func (r *GuildRuntime) GetAgentStatuses(guildID string) (map[string]AgentStatus, error) {
	const statusPrefix = "forge:agent:status:"
	pattern := fmt.Sprintf("%s%s:*", statusPrefix, guildID)

	keys, err := r.redisClient.Keys(r.ctx, pattern).Result()
	if err != nil {
		return nil, fmt.Errorf("failed to query agent statuses: %w", err)
	}

	statuses := make(map[string]AgentStatus)
	for _, key := range keys {
		raw, err := r.redisClient.Get(r.ctx, key).Result()
		if err != nil {
			continue
		}

		var status struct {
			State string `json:"state"`
			PID   int    `json:"pid"`
		}
		if err := json.Unmarshal([]byte(raw), &status); err != nil {
			continue
		}

		agentID := strings.TrimPrefix(key, statusPrefix+guildID+":")
		statuses[agentID] = AgentStatus{
			AgentID: agentID,
			State:   status.State,
			PID:     status.PID,
		}
	}

	return statuses, nil
}

// PublishMessage publishes a message to a topic
func (r *GuildRuntime) PublishMessage(namespace, topic string, msg *protocol.Message) error {
	backend, err := r.getMessagingBackend()
	if err != nil {
		return err
	}

	return backend.PublishMessage(r.ctx, namespace, topic, msg)
}

// getMessagingBackend returns the messaging backend for the runtime
func (r *GuildRuntime) getMessagingBackend() (messaging.Backend, error) {
	// For now, we always use the embedded Redis backend
	// The server internally handles NATS if configured
	return messaging.NewRedisBackend(r.redisClient), nil
}

// Shutdown gracefully shuts down the runtime
func (r *GuildRuntime) Shutdown() error {
	slog.Info("Shutting down guild runtime")

	r.cancel()

	if r.redisClient != nil {
		r.redisClient.Close()
	}

	if r.serverCmd != nil && r.serverCmd.Process != nil {
		r.kill()
	}

	if r.tempDir != "" {
		os.RemoveAll(r.tempDir)
	}

	return nil
}

// Helper functions

func (r *GuildRuntime) waitForReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(r.serverBase + "/readyz")
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			return nil
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for server ready")
}

func (r *GuildRuntime) waitForGuildRunning(guildID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		statuses, err := r.GetAgentStatuses(guildID)
		if err == nil && len(statuses) > 0 {
			allRunning := true
			for _, status := range statuses {
				if status.State != "running" {
					allRunning = false
					break
				}
			}
			if allRunning {
				return nil
			}
		}
		time.Sleep(1 * time.Second)
	}
	return fmt.Errorf("timeout waiting for guild to be running")
}

func (r *GuildRuntime) createBlueprint(spec *protocol.GuildSpec) (map[string]interface{}, error) {
	// Convert spec to blueprint format
	blueprint := map[string]interface{}{
		"name":            spec.Name,
		"description":     spec.Description,
		"spec":            spec,     // API expects "spec" not "guild_spec"
		"exposure":        "public", // Make it public so we can launch it
		"author_id":       r.config.UserID,
		"organization_id": r.config.OrgID,
	}

	var result map[string]interface{}
	if err := r.postJSON(r.rusticBase+"/catalog/blueprints/", blueprint, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *GuildRuntime) launchFromBlueprint(blueprintID, guildName string) (string, error) {
	launchReq := map[string]interface{}{
		"guild_name": guildName,
		"user_id":    r.config.UserID,
		"org_id":     r.config.OrgID,
	}

	var result map[string]interface{}
	url := fmt.Sprintf("%s/catalog/blueprints/%s/guilds", r.rusticBase, blueprintID)
	if err := r.postJSON(url, launchReq, &result); err != nil {
		return "", err
	}

	// API returns {"id": "..."} not {"guild_id": "..."}
	guildID, ok := result["id"].(string)
	if !ok {
		return "", fmt.Errorf("launch response missing id")
	}
	return guildID, nil
}

func (r *GuildRuntime) postJSON(url string, payload, result interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	if result != nil {
		return json.Unmarshal(body, result)
	}
	return nil
}

func (r *GuildRuntime) kill() {
	if r.serverCmd != nil && r.serverCmd.Process != nil {
		syscall.Kill(-r.serverCmd.Process.Pid, syscall.SIGKILL)
		r.serverCmd.Wait()
	}
}

// reserveLocalAddr finds and reserves a free local TCP address
func reserveLocalAddr() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := listener.Addr().String()
	listener.Close()
	return addr, nil
}
