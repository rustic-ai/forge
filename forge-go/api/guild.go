package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/rustic-ai/forge/forge-go/forgepath"
	"github.com/rustic-ai/forge/forge-go/guild"
	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/infraevents"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/supervisor"
)

type CreateGuildRequest struct {
	Spec           *protocol.GuildSpec `json:"spec"`
	OrganizationID string              `json:"org_id"`
}

func dependencyConfigPath() string {
	return forgepath.DependencyConfigPath()
}

func (s *Server) HandleCreateGuild(w http.ResponseWriter, r *http.Request) {
	var req CreateGuildRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if errors.Is(err, io.EOF) {
			ReplyError(w, http.StatusUnprocessableEntity, "request body required")
			return
		}
		ReplyError(w, http.StatusUnprocessableEntity, "invalid json payload: "+err.Error())
		return
	}

	if req.Spec == nil || req.OrganizationID == "" {
		ReplyError(w, http.StatusUnprocessableEntity, "spec and organization_id are required")
		return
	}

	_ = s.infraPublisher.Emit(r.Context(), infraevents.EmitParams{
		Kind:            "guild.launch.requested",
		Severity:        infraevents.SeverityInfo,
		GuildID:         req.Spec.ID,
		OrganizationID:  req.OrganizationID,
		SourceComponent: "forge-go.api",
		Message:         "guild launch requested",
	})

	model, err := guild.Bootstrap(r.Context(), s.store, s.controlPusher, s.infraPublisher, req.Spec, req.OrganizationID, dependencyConfigPath())
	if err != nil {
		ReplyError(w, http.StatusInternalServerError, "failed to bootstrap guild: "+err.Error())
		return
	}

	ReplyJSON(w, http.StatusCreated, map[string]interface{}{
		"id": model.ID,
	})
}

func (s *Server) HandleRelaunchGuild(w http.ResponseWriter, r *http.Request) {
	guildID := r.PathValue("id")
	if guildID == "" {
		ReplyError(w, http.StatusBadRequest, "guild_id required")
		return
	}

	guildModel, err := s.store.GetGuild(guildID)
	if err != nil {
		ReplyError(w, http.StatusNotFound, "Guild not found")
		return
	}
	if guildModel.Status == "stopped" || guildModel.Status == "stopping" {
		ReplyError(w, http.StatusBadRequest, "Guild is already stopped")
		return
	}

	isRunning, err := isManagerAgentRunning(r.Context(), s.statusStore, guildID)
	if err != nil {
		ReplyError(w, http.StatusInternalServerError, "failed to inspect guild runtime state")
		return
	}

	isRelaunching := !isRunning
	if isRelaunching {
		if err := s.store.CreateGuildRelaunch(&store.GuildRelaunchModel{GuildID: guildID}); err != nil {
			ReplyError(w, http.StatusInternalServerError, "failed to record relaunch")
			return
		}

		spec := store.ToGuildSpec(guildModel)
		if err := guild.EnqueueGuildManagerSpawn(r.Context(), s.controlPusher, s.infraPublisher, spec, guildModel.OrganizationID); err != nil {
			ReplyError(w, http.StatusInternalServerError, "failed to enqueue relaunch")
			return
		}
	}

	ReplyJSON(w, http.StatusOK, map[string]interface{}{
		"is_relaunching": isRelaunching,
	})
}

func isManagerAgentRunning(ctx context.Context, statusStore supervisor.AgentStatusStore, guildID string) (bool, error) {
	managerID := guildID + "#manager_agent"
	status, err := statusStore.GetStatus(ctx, guildID, managerID)
	if err != nil {
		return false, err
	}
	if status == nil {
		return false, nil
	}

	switch strings.ToLower(status.State) {
	case "running", "starting", "restarting":
		return true, nil
	default:
		return false, nil
	}
}

func (s *Server) HandleGetHistoricalMessages(w http.ResponseWriter, r *http.Request) {
	guildID := r.PathValue("id")
	userID := r.PathValue("user_id")
	if guildID == "" || userID == "" {
		ReplyError(w, http.StatusBadRequest, "guild_id and user_id required")
		return
	}

	ctx := r.Context()
	userNotifTopic := fmt.Sprintf("user_notifications:%s", userID)
	broadcastTopic := "user_message_broadcast"

	userMsgs, err := s.msgClient.GetMessagesForTopic(ctx, guildID, userNotifTopic)
	if err != nil {
		slog.Error("failed to get user messages", "err", err, "topic", userNotifTopic)
		ReplyError(w, http.StatusInternalServerError, "failed to get user messages")
		return
	}

	broadcastMsgs, err := s.msgClient.GetMessagesForTopic(ctx, guildID, broadcastTopic)
	if err != nil {
		slog.Error("failed to get broadcast messages", "err", err, "topic", broadcastTopic)
		ReplyError(w, http.StatusInternalServerError, "failed to get broadcast messages")
		return
	}

	socketAgentID := fmt.Sprintf("user_socket:%s", userID)

	userMsgIDs := make(map[uint64]bool)
	for _, msg := range userMsgs {
		if msg.ForwardHeader != nil && msg.ForwardHeader.OriginMessageID != 0 {
			userMsgIDs[msg.ForwardHeader.OriginMessageID] = true
		}
	}

	var filteredBroadcastMsgs []protocol.Message
	for _, msg := range broadcastMsgs {
		if userMsgIDs[msg.ID] {
			continue
		}

		if msg.ForwardHeader == nil {
			filteredBroadcastMsgs = append(filteredBroadcastMsgs, msg)
			continue
		}

		if msg.ForwardHeader.OnBehalfOf.ID != nil && *msg.ForwardHeader.OnBehalfOf.ID == socketAgentID {
			continue
		}

		filteredBroadcastMsgs = append(filteredBroadcastMsgs, msg)
	}

	allMsgs := append(userMsgs, filteredBroadcastMsgs...)
	if len(allMsgs) == 0 {
		allMsgs = []protocol.Message{}
	}

	sort.Slice(allMsgs, func(i, j int) bool {
		return allMsgs[i].ID < allMsgs[j].ID
	})

	ReplyJSON(w, http.StatusOK, allMsgs)
}

type GuildSpecResponse struct {
	*protocol.GuildSpec
	Status store.GuildStatus `json:"status"`
}

func (s *Server) HandleGetGuild(w http.ResponseWriter, r *http.Request) {
	guildID := r.PathValue("id")
	if guildID == "" {
		ReplyError(w, http.StatusBadRequest, "guild_id required")
		return
	}

	model, err := s.store.GetGuild(guildID)
	if err != nil {
		ReplyError(w, http.StatusNotFound, "guild not found")
		return
	}

	spec := store.ToGuildSpec(model)
	ReplyJSON(w, http.StatusOK, GuildSpecResponse{
		GuildSpec: spec,
		Status:    model.Status,
	})
}
