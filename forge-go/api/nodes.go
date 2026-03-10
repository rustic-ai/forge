package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/rustic-ai/forge/forge-go/scheduler"
)

type NodeRegistrationRequest struct {
	NodeID   string                     `json:"node_id"`
	Capacity scheduler.ResourceCapacity `json:"capacity"`
}

func RegisterNodeHandler(w http.ResponseWriter, r *http.Request) {
	var req NodeRegistrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		ReplyError(w, http.StatusUnprocessableEntity, "invalid request body")
		return
	}

	if req.NodeID == "" {
		ReplyError(w, http.StatusUnprocessableEntity, "node_id is required")
		return
	}

	scheduler.GlobalNodeRegistry.Register(req.NodeID, req.Capacity)
	slog.Default().Info("Node registered", "node_id", req.NodeID, "cpus", req.Capacity.CPUs, "memory", req.Capacity.Memory)

	w.WriteHeader(http.StatusCreated)
}

func NodeHeartbeatHandler(w http.ResponseWriter, r *http.Request) {
	nodeID := r.PathValue("node_id")
	if nodeID == "" {
		ReplyError(w, http.StatusBadRequest, "node_id is required")
		return
	}

	if ok := scheduler.GlobalNodeRegistry.Heartbeat(nodeID); !ok {
		ReplyError(w, http.StatusNotFound, "unknown node")
		return
	}
	w.WriteHeader(http.StatusOK)
}

func NodeDeregisterHandler(w http.ResponseWriter, r *http.Request) {
	nodeID := r.PathValue("node_id")
	if nodeID == "" {
		ReplyError(w, http.StatusBadRequest, "node_id is required")
		return
	}

	scheduler.GlobalNodeRegistry.Deregister(nodeID)
	slog.Default().Info("Node deregistered", "node_id", nodeID)
	w.WriteHeader(http.StatusNoContent)
}

func ListNodesHandler(w http.ResponseWriter, r *http.Request) {
	nodes := scheduler.GlobalNodeRegistry.ListHealthy()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(nodes); err != nil {
		ReplyError(w, http.StatusInternalServerError, "failed to encode response")
	}
}
