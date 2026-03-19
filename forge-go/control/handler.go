package control

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/helper/envvars"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/registry"
	"github.com/rustic-ai/forge/forge-go/secrets"
	"github.com/rustic-ai/forge/forge-go/supervisor"
)

const defaultOrganizationID = "default-org"

type SupervisorFactory func(orgID string) supervisor.AgentSupervisor

// ControlQueueHandler wiring layer connecting the control transport to the localized ProcessSupervisor.
type ControlQueueHandler struct {
	registry    *registry.Registry
	secrets     secrets.SecretProvider
	sup         supervisor.AgentSupervisor
	supByOrg    map[string]supervisor.AgentSupervisor
	supMu       sync.RWMutex
	supFactory  SupervisorFactory
	agentOrg    map[string]string
	agentMu     sync.RWMutex
	store       store.Store
	listener    *ControlQueueListener
	responder   *ControlQueueResponder
	statusStore      supervisor.AgentStatusStore
	nodeID           string
	stopAgentsOnExit bool
}

// NewControlQueueHandler creates a fully integrated control handler.
func NewControlQueueHandler(
	cp ControlPlane,
	reg *registry.Registry,
	sec secrets.SecretProvider,
	sup supervisor.AgentSupervisor,
	db store.Store,
	opts ...HandlerOption,
) *ControlQueueHandler {
	return NewControlQueueHandlerWithQueue(cp, reg, sec, sup, db, ControlQueueRequestKey, opts...)
}

// NewControlQueueHandlerWithFactory creates a handler that builds per-org supervisors via the factory.
func NewControlQueueHandlerWithFactory(
	cp ControlPlane,
	reg *registry.Registry,
	sec secrets.SecretProvider,
	factory SupervisorFactory,
	db store.Store,
	opts ...HandlerOption,
) *ControlQueueHandler {
	return NewControlQueueHandlerWithQueueFactory(cp, reg, sec, factory, db, ControlQueueRequestKey, opts...)
}

// NewControlQueueHandlerWithQueue creates a control handler bound to a specific queue key.
func NewControlQueueHandlerWithQueue(
	cp ControlPlane,
	reg *registry.Registry,
	sec secrets.SecretProvider,
	sup supervisor.AgentSupervisor,
	db store.Store,
	queueKey string,
	opts ...HandlerOption,
) *ControlQueueHandler {
	return newControlQueueHandler(cp, reg, sec, sup, nil, db, queueKey, opts...)
}

// NewControlQueueHandlerWithQueueFactory creates a handler with per-org factory bound to a specific queue key.
func NewControlQueueHandlerWithQueueFactory(
	cp ControlPlane,
	reg *registry.Registry,
	sec secrets.SecretProvider,
	factory SupervisorFactory,
	db store.Store,
	queueKey string,
	opts ...HandlerOption,
) *ControlQueueHandler {
	return newControlQueueHandler(cp, reg, sec, nil, factory, db, queueKey, opts...)
}

// HandlerOption configures optional ControlQueueHandler fields.
type HandlerOption func(*ControlQueueHandler)

// WithStatusStore sets the AgentStatusStore used for cross-node idempotency and ack writes.
func WithStatusStore(ss supervisor.AgentStatusStore) HandlerOption {
	return func(h *ControlQueueHandler) { h.statusStore = ss }
}

// WithNodeID sets the node identifier written into ack status entries.
func WithNodeID(id string) HandlerOption {
	return func(h *ControlQueueHandler) { h.nodeID = id }
}

// WithStopAgentsOnExit controls whether Stop() terminates all managed agents.
// When false (default), Stop() only halts the listener — agents keep running.
func WithStopAgentsOnExit(v bool) HandlerOption {
	return func(h *ControlQueueHandler) { h.stopAgentsOnExit = v }
}

func newControlQueueHandler(
	cp ControlPlane,
	reg *registry.Registry,
	sec secrets.SecretProvider,
	sup supervisor.AgentSupervisor,
	factory SupervisorFactory,
	db store.Store,
	queueKey string,
	opts ...HandlerOption,
) *ControlQueueHandler {
	listener := NewControlQueueListenerWithQueue(cp, queueKey)
	responder := NewControlQueueResponder(cp)

	handler := &ControlQueueHandler{
		registry:   reg,
		secrets:    sec,
		sup:        sup,
		supFactory: factory,
		supByOrg:   make(map[string]supervisor.AgentSupervisor),
		agentOrg:   make(map[string]string),
		store:      db,
		listener:   listener,
		responder:  responder,
	}

	for _, opt := range opts {
		opt(handler)
	}

	listener.OnSpawn = handler.handleSpawn
	listener.OnStop = handler.handleStop

	return handler
}

// Start spawns the background Redis BRPOP polling loop
func (h *ControlQueueHandler) Start(ctx context.Context) error {
	go h.listener.Start(ctx)
	return nil
}

// Stop terminates the background listener blocking routine.
// If stopAgentsOnExit is false (the default for standalone clients),
// only the listener is stopped — agents keep running across client restarts.
func (h *ControlQueueHandler) Stop() {
	h.listener.Stop()
	if !h.stopAgentsOnExit {
		return
	}
	if h.supFactory == nil {
		return
	}

	h.supMu.RLock()
	supervisors := make([]supervisor.AgentSupervisor, 0, len(h.supByOrg))
	for _, sup := range h.supByOrg {
		supervisors = append(supervisors, sup)
	}
	h.supMu.RUnlock()

	for _, sup := range supervisors {
		_ = sup.StopAll(context.Background())
	}
}

func (h *ControlQueueHandler) sendError(ctx context.Context, requestID, detail string) {
	if err := h.responder.SendError(ctx, requestID, detail); err != nil {
		slog.Error("failed to send control error response", "request_id", requestID, "detail", detail, "error", err)
	}
}

// handleSpawn orchestrates booting an agent based on the remote SpawnRequest
func (h *ControlQueueHandler) handleSpawn(ctx context.Context, req *protocol.SpawnRequest) {
	slog.Info("handleSpawn: received spawn request", "agent_id", req.AgentSpec.ID, "class", req.AgentSpec.ClassName, "guild", req.GuildID, "request_id", req.RequestID)

	// IDEMPOTENCY GATE: check StatusStore for cross-node awareness
	if h.statusStore != nil {
		existing, err := h.statusStore.GetStatus(ctx, req.GuildID, req.AgentSpec.ID)
		if err == nil && existing != nil &&
			(existing.State == "running" || existing.State == "starting") &&
			existing.NodeID != "" && existing.NodeID != h.nodeID {
			slog.Info("handleSpawn: agent active on different node, skipping",
				"agent_id", req.AgentSpec.ID,
				"other_node", existing.NodeID, "state", existing.State)
			_ = h.responder.SendResponse(ctx, req.RequestID, &protocol.SpawnResponse{
				RequestID: req.RequestID, Success: true,
				Message: fmt.Sprintf("agent already %s on node %s", existing.State, existing.NodeID),
			})
			return
		}
	}

	// ACK: Write "starting" to StatusStore immediately (distributed signal)
	if h.statusStore != nil {
		_ = h.statusStore.WriteStatus(ctx, req.GuildID, req.AgentSpec.ID,
			&supervisor.AgentStatusJSON{
				State:     "starting",
				NodeID:    h.nodeID,
				Timestamp: time.Now(),
			}, 120*time.Second)
	}

	entry, err := h.registry.Lookup(req.AgentSpec.ClassName)
	if err != nil {
		slog.Error("handleSpawn: registry lookup failed", "class", req.AgentSpec.ClassName, "error", err)
		h.sendError(ctx, req.RequestID, fmt.Sprintf("failed to lookup agent class %s from registry: %v", req.AgentSpec.ClassName, err))
		return
	}
	slog.Info("handleSpawn: registry lookup OK", "agent_id", req.AgentSpec.ID, "entry_id", entry.ID)

	var guildSpec *protocol.GuildSpec
	var guildOrgID string
	if h.store != nil {
		guildModel, err := h.store.GetGuild(req.GuildID)
		if err == nil {
			slog.Info("handleSpawn: guild store lookup OK", "guild", req.GuildID)
			guildOrgID = guildModel.OrganizationID
			guildSpec = store.ToGuildSpec(guildModel)
		} else {
			slog.Warn("handleSpawn: guild store lookup failed, using spawn payload fallback", "guild", req.GuildID, "error", err)
		}
	}
	if guildSpec == nil {
		guildSpec = extractGuildSpec(req.ClientProperties)
	}
	if guildSpec == nil {
		guildSpec = extractGuildSpec(req.AgentSpec.Properties)
	}
	if guildSpec == nil {
		guildSpec = &protocol.GuildSpec{
			ID: req.GuildID,
			Properties: map[string]interface{}{
				"messaging": map[string]interface{}{
					"backend_module": "rustic_ai.redis.messaging.backend",
					"backend_class":  "RedisMessagingBackend",
					"backend_config": map[string]interface{}{},
				},
			},
		}
	}

	if req.MessagingConfig != nil {
		if guildSpec.Properties == nil {
			guildSpec.Properties = make(map[string]interface{})
		}
		guildSpec.Properties["messaging"] = map[string]interface{}{
			"backend_module": req.MessagingConfig.BackendModule,
			"backend_class":  req.MessagingConfig.BackendClass,
			"backend_config": req.MessagingConfig.BackendConfig,
		}
	}

	orgID := h.resolveOrganizationForSpawn(req, guildOrgID)

	envVars, err := envvars.BuildAgentEnv(ctx, guildSpec, &req.AgentSpec, entry, h.secrets)
	if err != nil {
		slog.Error("handleSpawn: env var build failed", "agent_id", req.AgentSpec.ID, "error", err)
		h.sendError(ctx, req.RequestID, fmt.Sprintf("failed to build environment variables: %v", err))
		return
	}

	// Inject organization_id into FORGE_CLIENT_PROPERTIES_JSON so the Python
	// agent_runner can extract it (client_props.pop("organization_id")).
	envVars = injectClientProperty(envVars, "organization_id", orgID)

	slog.Info("handleSpawn: env vars built OK", "agent_id", req.AgentSpec.ID, "env_count", len(envVars))
	sup := h.supervisorForOrganization(orgID)
	if sup == nil {
		h.sendError(ctx, req.RequestID, "no supervisor available for organization")
		return
	}

	err = sup.Launch(ctx, req.GuildID, &req.AgentSpec, h.registry, envVars)
	if err != nil {
		if strings.Contains(err.Error(), "already managed") {
			slog.Warn("handleSpawn: agent already managed locally", "agent_id", req.AgentSpec.ID, "org", orgID, "error", err)
		} else {
			slog.Error("handleSpawn: supervisor launch failed", "agent_id", req.AgentSpec.ID, "org", orgID, "error", err)
		}
		h.sendError(ctx, req.RequestID, fmt.Sprintf("failed to launch process via supervisor: %v", err))
		return
	}
	h.recordAgentOrganization(req.GuildID, req.AgentSpec.ID, orgID)
	slog.Info("handleSpawn: supervisor launch OK", "agent_id", req.AgentSpec.ID, "org", orgID)

	msg := &protocol.SpawnResponse{
		RequestID: req.RequestID,
		Success:   true,
		Message:   "agent process spawned successfully",
	}

	if pSup, ok := sup.(*supervisor.ProcessSupervisor); ok {
		if status, _ := pSup.Status(ctx, req.GuildID, req.AgentSpec.ID); status == "running" {
			nodeID, _ := os.Hostname()
			if nodeID == "" {
				nodeID = "localhost"
			}
			msg.NodeID = nodeID

			for retries := 0; retries < 5; retries++ {
				actualPid, err := pSup.GetPID(ctx, req.GuildID, req.AgentSpec.ID)
				if err == nil && actualPid > 0 {
					msg.PID = actualPid
					break
				}
				time.Sleep(100 * time.Millisecond)
			}

			if msg.PID <= 0 {
				h.sendError(ctx, req.RequestID, "timed out waiting to retrieve valid PID for spawned agent")
				return
			}
		}
	}

	_ = h.responder.SendResponse(ctx, req.RequestID, msg)
}

func (h *ControlQueueHandler) handleStop(ctx context.Context, req *protocol.StopRequest) {
	orgID := h.resolveOrganizationForStop(req)
	sup := h.supervisorForOrganization(orgID)
	if sup == nil {
		h.sendError(ctx, req.RequestID, "no supervisor available for organization")
		return
	}

	err := sup.Stop(ctx, req.GuildID, req.AgentID)
	if err != nil {
		h.sendError(ctx, req.RequestID, fmt.Sprintf("failed to stop agent %s: %v", req.AgentID, err))
		return
	}
	h.forgetAgentOrganization(req.GuildID, req.AgentID)

	msg := &protocol.StopResponse{
		RequestID: req.RequestID,
		Success:   true,
		Message:   "agent process stopped gracefully",
	}

	_ = h.responder.SendResponse(ctx, req.RequestID, msg)
}

func normalizeOrganizationID(orgID string) string {
	if strings.TrimSpace(orgID) == "" {
		return defaultOrganizationID
	}
	return orgID
}

func organizationFromValue(v interface{}) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func agentOrgKey(guildID, agentID string) string {
	return guildID + "::" + agentID
}

// injectClientProperty patches FORGE_CLIENT_PROPERTIES_JSON in an env slice
// to include an additional key-value pair. The Python agent_runner reads client
// properties from this JSON blob, so this is the channel for passing metadata
// like organization_id to spawned agent processes.
func injectClientProperty(envVars []string, key, value string) []string {
	const prefix = "FORGE_CLIENT_PROPERTIES_JSON="
	for i, entry := range envVars {
		if strings.HasPrefix(entry, prefix) {
			raw := entry[len(prefix):]
			var props map[string]interface{}
			if err := json.Unmarshal([]byte(raw), &props); err != nil {
				props = make(map[string]interface{})
			}
			props[key] = value
			if patched, err := json.Marshal(props); err == nil {
				envVars[i] = prefix + string(patched)
			}
			return envVars
		}
	}
	// No existing entry — create one.
	blob, _ := json.Marshal(map[string]interface{}{key: value})
	return append(envVars, prefix+string(blob))
}

func (h *ControlQueueHandler) resolveOrganizationForSpawn(req *protocol.SpawnRequest, guildOrgID string) string {
	if orgID := strings.TrimSpace(req.OrganizationID); orgID != "" {
		return orgID
	}
	if orgID := organizationFromValue(req.ClientProperties["organization_id"]); orgID != "" {
		return orgID
	}
	if req.AgentSpec.Properties != nil {
		if orgID := organizationFromValue(req.AgentSpec.Properties["organization_id"]); orgID != "" {
			return orgID
		}
	}
	if orgID := strings.TrimSpace(guildOrgID); orgID != "" {
		return orgID
	}
	return defaultOrganizationID
}

func (h *ControlQueueHandler) resolveOrganizationForStop(req *protocol.StopRequest) string {
	if orgID := strings.TrimSpace(req.OrganizationID); orgID != "" {
		return orgID
	}

	key := agentOrgKey(req.GuildID, req.AgentID)
	h.agentMu.RLock()
	if orgID, ok := h.agentOrg[key]; ok && orgID != "" {
		h.agentMu.RUnlock()
		return orgID
	}
	h.agentMu.RUnlock()

	if h.store != nil {
		if guildModel, err := h.store.GetGuild(req.GuildID); err == nil && strings.TrimSpace(guildModel.OrganizationID) != "" {
			return guildModel.OrganizationID
		}
	}

	return defaultOrganizationID
}

func (h *ControlQueueHandler) supervisorForOrganization(orgID string) supervisor.AgentSupervisor {
	orgID = normalizeOrganizationID(orgID)
	if h.supFactory == nil {
		return h.sup
	}

	h.supMu.RLock()
	sup, ok := h.supByOrg[orgID]
	h.supMu.RUnlock()
	if ok {
		return sup
	}

	created := h.supFactory(orgID)
	if created == nil {
		return nil
	}

	h.supMu.Lock()
	if existing, exists := h.supByOrg[orgID]; exists {
		h.supMu.Unlock()
		return existing
	}
	h.supByOrg[orgID] = created
	h.supMu.Unlock()
	return created
}

func (h *ControlQueueHandler) recordAgentOrganization(guildID, agentID, orgID string) {
	if h.supFactory == nil {
		return
	}
	h.agentMu.Lock()
	h.agentOrg[agentOrgKey(guildID, agentID)] = normalizeOrganizationID(orgID)
	h.agentMu.Unlock()
}

func (h *ControlQueueHandler) forgetAgentOrganization(guildID, agentID string) {
	if h.supFactory == nil {
		return
	}
	h.agentMu.Lock()
	delete(h.agentOrg, agentOrgKey(guildID, agentID))
	h.agentMu.Unlock()
}

// extractGuildSpec attempts to unmarshal a guild spec from a properties map.
func extractGuildSpec(props map[string]interface{}) *protocol.GuildSpec {
	gsRaw, ok := props["guild_spec"]
	if !ok || gsRaw == nil {
		return nil
	}
	gsBytes, err := json.Marshal(gsRaw)
	if err != nil {
		return nil
	}
	var parsed protocol.GuildSpec
	if err := json.Unmarshal(gsBytes, &parsed); err != nil {
		return nil
	}
	return &parsed
}
