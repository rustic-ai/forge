package scheduler

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/scheduler/leader"
	"github.com/rustic-ai/forge/forge-go/supervisor"
)

// ReconcilerConfig holds tuning knobs for the reconciler.
type ReconcilerConfig struct {
	ReconcileInterval time.Duration // how often to run reconciliation (default 15s)
	AckTimeout        time.Duration // time to ack after dispatch (default 30s)
	LaunchTimeout     time.Duration // time to reach "running" after ack (default 120s)
	MaxAttempts       int           // max dispatch attempts before marking failed (default 5)
	DeadNodeTimeout   time.Duration // heartbeat staleness threshold (default 15s)
	FailedCleanupAge  time.Duration // remove failed entries older than this (default 5m)
}

// DefaultReconcilerConfig returns a ReconcilerConfig with sensible defaults.
func DefaultReconcilerConfig() ReconcilerConfig {
	return ReconcilerConfig{
		ReconcileInterval: 15 * time.Second,
		AckTimeout:        30 * time.Second,
		LaunchTimeout:     120 * time.Second,
		MaxAttempts:       5,
		DeadNodeTimeout:   15 * time.Second,
		FailedCleanupAge:  5 * time.Minute,
	}
}

type Reconciler struct {
	registry     *NodeRegistry
	placementMap *PlacementMap
	transport    protocol.ControlPusher
	elector      leader.LeaderElector
	statusStore  supervisor.AgentStatusStore
	config       ReconcilerConfig
}

func NewReconciler(
	r *NodeRegistry,
	p *PlacementMap,
	transport protocol.ControlPusher,
	el leader.LeaderElector,
	statusStore supervisor.AgentStatusStore,
	config ReconcilerConfig,
) *Reconciler {
	return &Reconciler{
		registry:     r,
		placementMap: p,
		transport:    transport,
		elector:      el,
		statusStore:  statusStore,
		config:       config,
	}
}

func (r *Reconciler) Start(ctx context.Context) {
	ticker := time.NewTicker(r.config.ReconcileInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if r.elector != nil && !r.elector.IsLeader() {
				continue
			}
			r.reconcile(ctx)
		}
	}
}

func (r *Reconciler) reconcile(ctx context.Context) {
	r.reconcileDeadNodes(ctx)
	r.reconcileStaleDispatches(ctx)
	r.reconcileStaleAcks(ctx)
	r.cleanupFailedPlacements()
}

// reconcileDeadNodes detects nodes with stale heartbeats and re-enqueues orphaned agents.
func (r *Reconciler) reconcileDeadNodes(ctx context.Context) {
	r.registry.mu.RLock()
	var deadNodes []string
	now := time.Now()
	for nodeID, state := range r.registry.nodes {
		if now.Sub(state.LastHeartbeat) > r.config.DeadNodeTimeout {
			deadNodes = append(deadNodes, nodeID)
		}
	}
	r.registry.mu.RUnlock()

	for _, nodeID := range deadNodes {
		slog.Default().Warn("Detected dead node, reconciling orphaned agents", "node_id", nodeID)

		orphans := r.placementMap.AgentsOnNode(nodeID)
		r.registry.Deregister(nodeID)

		for _, o := range orphans {
			r.placementMap.Remove(o.GuildID, o.AgentID)
			r.reenqueue(ctx, o)
		}
	}
}

// reconcileStaleDispatches checks for dispatches that haven't been acknowledged within AckTimeout.
func (r *Reconciler) reconcileStaleDispatches(ctx context.Context) {
	if r.statusStore == nil {
		return
	}

	stale := r.placementMap.GetStaleDispatches(r.config.AckTimeout)
	for _, p := range stale {
		// Cross-check distributed StatusStore
		status, err := r.statusStore.GetStatus(ctx, p.GuildID, p.AgentID)
		if err == nil && status != nil {
			if status.State == "starting" {
				r.placementMap.MarkAcknowledged(p.GuildID, p.AgentID)
				continue
			}
			if status.State == "running" {
				r.placementMap.MarkRunning(p.GuildID, p.AgentID)
				continue
			}
		}

		// Node didn't process it
		if p.Attempts >= r.config.MaxAttempts {
			r.placementMap.MarkFailed(p.GuildID, p.AgentID)
			slog.Default().Error("Agent spawn exceeded max attempts, marking failed",
				"guild", p.GuildID, "agent", p.AgentID, "attempts", p.Attempts)
			continue
		}

		r.placementMap.Remove(p.GuildID, p.AgentID)
		r.reenqueue(ctx, p)
	}
}

// reconcileStaleAcks checks for acknowledged agents that haven't reached "running" within LaunchTimeout.
func (r *Reconciler) reconcileStaleAcks(ctx context.Context) {
	if r.statusStore == nil {
		return
	}

	stale := r.placementMap.GetStaleAcks(r.config.LaunchTimeout)
	for _, p := range stale {
		status, err := r.statusStore.GetStatus(ctx, p.GuildID, p.AgentID)
		if err == nil && status != nil && status.State == "running" {
			r.placementMap.MarkRunning(p.GuildID, p.AgentID)
			continue
		}

		// Still "starting" or expired after LaunchTimeout → launch failed/stalled
		if p.Attempts >= r.config.MaxAttempts {
			r.placementMap.MarkFailed(p.GuildID, p.AgentID)
			slog.Default().Error("Agent launch exceeded max attempts after ack, marking failed",
				"guild", p.GuildID, "agent", p.AgentID, "attempts", p.Attempts)
			continue
		}

		r.placementMap.Remove(p.GuildID, p.AgentID)
		r.reenqueue(ctx, p)
	}
}

// cleanupFailedPlacements removes old Failed entries from the placement map.
func (r *Reconciler) cleanupFailedPlacements() {
	old := r.placementMap.GetFailedOlderThan(r.config.FailedCleanupAge)
	for _, p := range old {
		r.placementMap.Remove(p.GuildID, p.AgentID)
		slog.Default().Info("Cleaned up old failed placement",
			"guild", p.GuildID, "agent", p.AgentID)
	}
}

// reenqueue wraps the agent's payload in a spawn command and pushes it back to the global control queue.
func (r *Reconciler) reenqueue(ctx context.Context, p AgentPlacement) {
	wrapper := map[string]interface{}{
		"command": "spawn",
	}
	var rawPayload interface{}
	if err := json.Unmarshal(p.Payload, &rawPayload); err == nil {
		wrapper["payload"] = rawPayload
		wrappedBytes, _ := json.Marshal(wrapper)
		_ = r.transport.Push(ctx, "forge:control:requests", wrappedBytes)
		slog.Default().Info("Re-enqueued agent for redistribution",
			"guild", p.GuildID, "agent", p.AgentID, "attempt", p.Attempts)
	} else {
		slog.Default().Error("Failed to deserialize orphaned payload buffer", "error", err)
	}
}
