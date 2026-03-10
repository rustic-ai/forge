package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rustic-ai/forge/forge-go/scheduler"
)

func TestNodeHeartbeatHandler_UnknownNodeReturnsNotFound(t *testing.T) {
	orig := scheduler.GlobalNodeRegistry
	t.Cleanup(func() { scheduler.GlobalNodeRegistry = orig })
	scheduler.GlobalNodeRegistry = scheduler.NewNodeRegistry()

	req := httptest.NewRequest(http.MethodPost, "/nodes/node-missing/heartbeat", nil)
	req.SetPathValue("node_id", "node-missing")
	rr := httptest.NewRecorder()

	NodeHeartbeatHandler(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, rr.Code)
	}
}

func TestNodeHeartbeatHandler_RegisteredNodeReturnsOK(t *testing.T) {
	orig := scheduler.GlobalNodeRegistry
	t.Cleanup(func() { scheduler.GlobalNodeRegistry = orig })
	scheduler.GlobalNodeRegistry = scheduler.NewNodeRegistry()
	scheduler.GlobalNodeRegistry.Register("node-1", scheduler.ResourceCapacity{CPUs: 2, Memory: 1024})

	req := httptest.NewRequest(http.MethodPost, "/nodes/node-1/heartbeat", nil)
	req.SetPathValue("node_id", "node-1")
	rr := httptest.NewRecorder()

	NodeHeartbeatHandler(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rr.Code)
	}
}

func TestNodeDeregisterHandler_RemovesRegisteredNode(t *testing.T) {
	orig := scheduler.GlobalNodeRegistry
	t.Cleanup(func() { scheduler.GlobalNodeRegistry = orig })
	scheduler.GlobalNodeRegistry = scheduler.NewNodeRegistry()
	scheduler.GlobalNodeRegistry.Register("node-1", scheduler.ResourceCapacity{CPUs: 2, Memory: 1024})

	req := httptest.NewRequest(http.MethodDelete, "/nodes/node-1", nil)
	req.SetPathValue("node_id", "node-1")
	rr := httptest.NewRecorder()

	NodeDeregisterHandler(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d", http.StatusNoContent, rr.Code)
	}
	if scheduler.GlobalNodeRegistry.IsHealthy("node-1") {
		t.Fatal("expected node-1 to be deregistered")
	}
}
