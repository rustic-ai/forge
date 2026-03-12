package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/rustic-ai/forge/forge-go/guild"
	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/protocol"
)

func (s *Server) authorizeManagerRequest(w http.ResponseWriter, r *http.Request) bool {
	required := strings.TrimSpace(os.Getenv("FORGE_MANAGER_API_TOKEN"))
	if required == "" {
		return true
	}

	if provided := strings.TrimSpace(r.Header.Get("X-Forge-Manager-Token")); provided == required {
		return true
	}

	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(auth, "Bearer ") && strings.TrimSpace(strings.TrimPrefix(auth, "Bearer ")) == required {
		return true
	}

	ReplyError(w, http.StatusUnauthorized, "unauthorized manager request")
	return false
}

func normalizeManagerSpecIDs(spec *protocol.GuildSpec) {
	if spec == nil {
		return
	}
	spec.Normalize()
	if spec.ID == "" {
		return
	}
	for i := range spec.Agents {
		if spec.Agents[i].ID == "" || spec.Agents[i].ID == "a-"+strconv.Itoa(i) {
			spec.Agents[i].ID = spec.ID + "#a-" + strconv.Itoa(i)
		}
	}
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, out interface{}) bool {
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			ReplyError(w, http.StatusUnprocessableEntity, "request body required")
			return false
		}
		ReplyError(w, http.StatusUnprocessableEntity, "invalid json payload: "+err.Error())
		return false
	}
	return true
}

func (s *Server) HandleManagerEnsureGuild(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeManagerRequest(w, r) {
		return
	}

	var req EnsureGuildRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.GuildSpec == nil || strings.TrimSpace(req.OrganizationID) == "" {
		ReplyError(w, http.StatusUnprocessableEntity, "guild_spec and organization_id are required")
		return
	}
	if strings.TrimSpace(req.GuildSpec.ID) == "" {
		ReplyError(w, http.StatusUnprocessableEntity, "guild_spec.id is required")
		return
	}

	guildID := req.GuildSpec.ID
	_, err := s.store.GetGuild(guildID)
	if err == nil {
		if err := s.store.UpdateGuildStatus(guildID, store.GuildStatusStarting); err != nil {
			ReplyError(w, http.StatusInternalServerError, "failed to update guild status")
			return
		}
		model, err := s.store.GetGuild(guildID)
		if err != nil {
			ReplyError(w, http.StatusInternalServerError, "failed to fetch guild")
			return
		}
		ReplyJSON(w, http.StatusOK, EnsureGuildResponse{
			GuildSpec:  store.ToGuildSpec(model),
			WasCreated: false,
			Status:     model.Status,
		})
		return
	}
	if !errors.Is(err, store.ErrNotFound) {
		ReplyError(w, http.StatusInternalServerError, "failed to fetch guild")
		return
	}

	spec := req.GuildSpec
	normalizeManagerSpecIDs(spec)
	if err := guild.ApplyFilesystemGlobalRoot(spec, strings.TrimSpace(os.Getenv("FORGE_FILESYSTEM_GLOBAL_ROOT"))); err != nil {
		ReplyError(w, http.StatusUnprocessableEntity, "invalid filesystem dependency: "+err.Error())
		return
	}

	guildModel := store.FromGuildSpec(spec, req.OrganizationID)
	guildModel.Status = store.GuildStatusPendingLaunch
	agents := append([]store.AgentModel{}, guildModel.Agents...)
	guildModel.Agents = nil
	if err := s.store.CreateGuildWithAgents(guildModel, agents); err != nil {
		ReplyError(w, http.StatusInternalServerError, "failed to create guild")
		return
	}
	created, err := s.store.GetGuild(guildID)
	if err != nil {
		ReplyError(w, http.StatusInternalServerError, "failed to fetch created guild")
		return
	}
	ReplyJSON(w, http.StatusOK, EnsureGuildResponse{
		GuildSpec:  store.ToGuildSpec(created),
		WasCreated: true,
		Status:     created.Status,
	})
}

func (s *Server) HandleManagerGetGuildSpec(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeManagerRequest(w, r) {
		return
	}
	guildID := strings.TrimSpace(r.PathValue("guild_id"))
	if guildID == "" {
		ReplyError(w, http.StatusBadRequest, "guild_id required")
		return
	}
	model, err := s.store.GetGuild(guildID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			ReplyError(w, http.StatusNotFound, "guild not found")
			return
		}
		ReplyError(w, http.StatusInternalServerError, "failed to fetch guild")
		return
	}
	ReplyJSON(w, http.StatusOK, GuildSpecWithStatusResponse{
		GuildSpec: store.ToGuildSpec(model),
		Status:    model.Status,
	})
}

func (s *Server) HandleManagerUpdateGuildStatus(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeManagerRequest(w, r) {
		return
	}
	guildID := strings.TrimSpace(r.PathValue("guild_id"))
	if guildID == "" {
		ReplyError(w, http.StatusBadRequest, "guild_id required")
		return
	}
	var req UpdateGuildStatusRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.Status == "" {
		ReplyError(w, http.StatusUnprocessableEntity, "status is required")
		return
	}
	if err := s.store.UpdateGuildStatus(guildID, req.Status); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			ReplyError(w, http.StatusNotFound, "guild not found")
			return
		}
		ReplyError(w, http.StatusInternalServerError, "failed to update guild status")
		return
	}
	ReplyJSON(w, http.StatusOK, UpdateGuildStatusResponse{
		GuildID: guildID,
		Status:  req.Status,
	})
}

func (s *Server) HandleManagerEnsureAgent(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeManagerRequest(w, r) {
		return
	}
	guildID := strings.TrimSpace(r.PathValue("guild_id"))
	if guildID == "" {
		ReplyError(w, http.StatusBadRequest, "guild_id required")
		return
	}
	if _, err := s.store.GetGuild(guildID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			ReplyError(w, http.StatusNotFound, "guild not found")
			return
		}
		ReplyError(w, http.StatusInternalServerError, "failed to fetch guild")
		return
	}

	var spec protocol.AgentSpec
	if !decodeJSONBody(w, r, &spec) {
		return
	}
	spec.Normalize()
	if strings.TrimSpace(spec.ID) == "" {
		ReplyError(w, http.StatusUnprocessableEntity, "agent id is required")
		return
	}

	if _, err := s.store.GetAgent(guildID, spec.ID); err == nil {
		ReplyJSON(w, http.StatusOK, EnsureAgentResponse{
			AgentID: spec.ID,
			Created: false,
		})
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		ReplyError(w, http.StatusInternalServerError, "failed to fetch agent")
		return
	}

	if err := s.store.CreateAgent(store.FromAgentSpec(&spec, guildID)); err != nil {
		ReplyError(w, http.StatusInternalServerError, "failed to create agent")
		return
	}
	ReplyJSON(w, http.StatusCreated, EnsureAgentResponse{
		AgentID: spec.ID,
		Created: true,
	})
}

func (s *Server) HandleManagerUpdateAgentStatus(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeManagerRequest(w, r) {
		return
	}
	guildID := strings.TrimSpace(r.PathValue("guild_id"))
	agentID := strings.TrimSpace(r.PathValue("agent_id"))
	if guildID == "" || agentID == "" {
		ReplyError(w, http.StatusBadRequest, "guild_id and agent_id required")
		return
	}
	var req UpdateAgentStatusRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.Status == "" {
		ReplyError(w, http.StatusUnprocessableEntity, "status is required")
		return
	}

	err := s.store.UpdateAgentStatus(guildID, agentID, req.Status)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			ReplyJSON(w, http.StatusOK, UpdateAgentStatusResponse{
				AgentID: agentID,
				Status:  req.Status,
				Found:   false,
			})
			return
		}
		ReplyError(w, http.StatusInternalServerError, "failed to update agent status")
		return
	}

	ReplyJSON(w, http.StatusOK, UpdateAgentStatusResponse{
		AgentID: agentID,
		Status:  req.Status,
		Found:   true,
	})
}

func (s *Server) HandleManagerAddRoutingRule(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeManagerRequest(w, r) {
		return
	}
	guildID := strings.TrimSpace(r.PathValue("guild_id"))
	if guildID == "" {
		ReplyError(w, http.StatusBadRequest, "guild_id required")
		return
	}
	if _, err := s.store.GetGuild(guildID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			ReplyError(w, http.StatusNotFound, "guild not found")
			return
		}
		ReplyError(w, http.StatusInternalServerError, "failed to fetch guild")
		return
	}

	var req AddRouteRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.RoutingRule == nil {
		ReplyError(w, http.StatusUnprocessableEntity, "routing_rule is required")
		return
	}
	req.RoutingRule.Normalize()

	model := store.FromRoutingRule(guildID, req.RoutingRule)
	model.Status = store.RouteStatusActive
	if err := s.store.CreateGuildRoute(model); err != nil {
		ReplyError(w, http.StatusInternalServerError, "failed to create route")
		return
	}

	hashID, err := store.RoutingRuleHash(req.RoutingRule)
	if err != nil {
		ReplyError(w, http.StatusInternalServerError, "failed to hash routing rule")
		return
	}
	ReplyJSON(w, http.StatusCreated, AddRouteResponse{RuleHashID: hashID})
}

func (s *Server) HandleManagerRemoveRoutingRule(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeManagerRequest(w, r) {
		return
	}
	guildID := strings.TrimSpace(r.PathValue("guild_id"))
	ruleHashID := strings.TrimSpace(r.PathValue("rule_hashid"))
	if guildID == "" || ruleHashID == "" {
		ReplyError(w, http.StatusBadRequest, "guild_id and rule_hashid required")
		return
	}

	model, err := s.store.GetGuild(guildID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			ReplyJSON(w, http.StatusOK, RemoveRouteResponse{Deleted: false})
			return
		}
		ReplyError(w, http.StatusInternalServerError, "failed to fetch guild")
		return
	}

	for i := range model.Routes {
		route := &model.Routes[i]
		if route.Status == store.RouteStatusDeleted {
			continue
		}
		rule := store.ToRoutingRule(route)
		hashID, err := store.RoutingRuleHash(rule)
		if err != nil {
			ReplyError(w, http.StatusInternalServerError, "failed to hash persisted route")
			return
		}
		if hashID != ruleHashID {
			continue
		}
		if err := s.store.UpdateGuildRouteStatus(guildID, route.ID, store.RouteStatusDeleted); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				ReplyJSON(w, http.StatusOK, RemoveRouteResponse{Deleted: false})
				return
			}
			ReplyError(w, http.StatusInternalServerError, "failed to delete route")
			return
		}
		ReplyJSON(w, http.StatusOK, RemoveRouteResponse{Deleted: true})
		return
	}

	ReplyJSON(w, http.StatusOK, RemoveRouteResponse{Deleted: false})
}

func (s *Server) HandleManagerProcessHeartbeat(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeManagerRequest(w, r) {
		return
	}
	guildID := strings.TrimSpace(r.PathValue("guild_id"))
	if guildID == "" {
		ReplyError(w, http.StatusBadRequest, "guild_id required")
		return
	}
	var req HeartbeatStatusUpdateRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.AgentID == "" || req.AgentStatus == "" || req.GuildStatus == "" {
		ReplyError(w, http.StatusUnprocessableEntity, "agent_id, agent_status and guild_status are required")
		return
	}

	effective, found, err := s.store.ProcessHeartbeatStatus(guildID, req.AgentID, req.AgentStatus, req.GuildStatus)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			ReplyError(w, http.StatusNotFound, "guild not found")
			return
		}
		ReplyError(w, http.StatusInternalServerError, "failed to process heartbeat")
		return
	}

	ReplyJSON(w, http.StatusOK, HeartbeatStatusUpdateResponse{
		AgentID:     req.AgentID,
		AgentStatus: effective,
		GuildStatus: req.GuildStatus,
		AgentFound:  found,
	})
}
