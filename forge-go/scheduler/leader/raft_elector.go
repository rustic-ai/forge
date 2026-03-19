package leader

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/hashicorp/memberlist"
	"github.com/hashicorp/raft"
)

type RaftElector struct {
	nodeID     string
	raftNode   *raft.Raft
	memberlist *memberlist.Memberlist

	isLeader  atomic.Bool
	watchChan chan bool
}

var _ LeaderElector = (*RaftElector)(nil)

type RaftConfig struct {
	NodeID          string
	RaftBindAddr    string   // e.g., "127.0.0.1:8300"
	GossipBindAddr  string   // e.g., "127.0.0.1:8301"
	GossipJoinPeers []string // e.g., ["127.0.0.1:8301"]
}

// metaDelegate implements memberlist.Delegate to share our Raft TCP address via gossip
type metaDelegate struct {
	raftBindAddr string
}

func (m *metaDelegate) NodeMeta(limit int) []byte {
	return []byte(m.raftBindAddr) // Broadcast our Raft address to peers
}
func (m *metaDelegate) NotifyMsg(b []byte)                         {}
func (m *metaDelegate) GetBroadcasts(overhead, limit int) [][]byte { return nil }
func (m *metaDelegate) LocalState(join bool) []byte                { return nil }
func (m *metaDelegate) MergeRemoteState(buf []byte, join bool)     {}

// gossipEventDelegate listens to cluster topology changes
type gossipEventDelegate struct {
	elector *RaftElector
}

func (g *gossipEventDelegate) NotifyJoin(node *memberlist.Node) {
	// If we are the Raft leader, and a new node joins the gossip ring, add them to Raft consensus
	if g.elector.IsLeader() {
		raftAddr := string(node.Meta)
		slog.Info("RaftElector: new node joined gossip, adding to Raft cluster", "node_id", node.Name, "raft_addr", raftAddr)

		future := g.elector.raftNode.AddVoter(raft.ServerID(node.Name), raft.ServerAddress(raftAddr), 0, 0)
		if err := future.Error(); err != nil {
			slog.Error("RaftElector: failed to add voter", "err", err)
		}
	}
}

func (g *gossipEventDelegate) NotifyLeave(node *memberlist.Node) {
	// If we are the Raft leader, and a node dies, remove them from Raft consensus to maintain quorum easily
	if g.elector.IsLeader() {
		slog.Info("RaftElector: node left gossip, removing from Raft cluster", "node_id", node.Name)
		future := g.elector.raftNode.RemoveServer(raft.ServerID(node.Name), 0, 0)
		if err := future.Error(); err != nil {
			slog.Error("RaftElector: failed to remove server", "err", err)
		}
	}
}

func (g *gossipEventDelegate) NotifyUpdate(node *memberlist.Node) {}

// dummyFSM implements raft.FSM because we only care about leader election, not state machine replication
type dummyFSM struct{}

func (f *dummyFSM) Apply(l *raft.Log) interface{}       { return nil }
func (f *dummyFSM) Snapshot() (raft.FSMSnapshot, error) { return &dummySnapshot{}, nil }
func (f *dummyFSM) Restore(rc io.ReadCloser) error      { return nil }

type dummySnapshot struct{}

func (s *dummySnapshot) Persist(sink raft.SnapshotSink) error { _ = sink.Close(); return nil }
func (s *dummySnapshot) Release()                             {}

func NewRaftElector(cfg RaftConfig) (*RaftElector, error) {
	e := &RaftElector{
		nodeID:    cfg.NodeID,
		watchChan: make(chan bool, 1),
	}

	// 1. Setup Raft Transport
	addr, err := net.ResolveTCPAddr("tcp", cfg.RaftBindAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve raft addr: %w", err)
	}

	transport, err := raft.NewTCPTransport(cfg.RaftBindAddr, addr, 3, 0, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create Raft TCP transport: %w", err)
	}

	// 2. Setup Raft Stores (In-Memory for Leader Election MVP)
	logStore := raft.NewInmemStore()
	stableStore := raft.NewInmemStore()
	snapshotStore := raft.NewDiscardSnapshotStore()

	// 3. Setup Raft Configuration
	rConfig := raft.DefaultConfig()
	rConfig.LocalID = raft.ServerID(cfg.NodeID)
	// Suppress verbose raft logging normally, but let's enable it for debug
	rConfig.LogOutput = io.Discard

	// 4. Initialize Raft
	raftNode, err := raft.NewRaft(rConfig, &dummyFSM{}, logStore, stableStore, snapshotStore, transport)
	if err != nil {
		return nil, fmt.Errorf("failed to create raft node: %w", err)
	}
	e.raftNode = raftNode

	// 5. Setup Memberlist (Gossip)
	mConfig := memberlist.DefaultLocalConfig()
	mConfig.LogOutput = io.Discard

	host, portStr, err := net.SplitHostPort(cfg.GossipBindAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid gossip bind addr: %w", err)
	}
	mConfig.BindAddr = host
	port, _ := strconv.Atoi(portStr)
	mConfig.BindPort = port
	mConfig.Name = cfg.NodeID

	mConfig.Delegate = &metaDelegate{raftBindAddr: cfg.RaftBindAddr}
	mConfig.Events = &gossipEventDelegate{elector: e}

	mlist, err := memberlist.Create(mConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create memberlist: %w", err)
	}
	e.memberlist = mlist

	// 6. Bootstrap Cluster logic (Zero-Conf)
	if len(cfg.GossipJoinPeers) > 0 {
		if _, err := mlist.Join(cfg.GossipJoinPeers); err != nil {
			slog.Warn("RaftElector: failed to join initial gossip peers", "err", err)
		}
	} else {
		// If we joined absolutely nobody, assume we are the cluster seed/genesis node
		slog.Info("RaftElector: no gossip peers provided, bootstrapping as cluster seed", "node_id", cfg.NodeID)
		configuration := raft.Configuration{
			Servers: []raft.Server{
				{
					ID:      rConfig.LocalID,
					Address: transport.LocalAddr(),
				},
			},
		}
		future := raftNode.BootstrapCluster(configuration)
		if err := future.Error(); err != nil {
			slog.Warn("RaftElector: cluster bootstrap error", "err", err)
		}
	}

	// Background routine to monitor Raft leadership changes
	go e.monitorLeadership()

	return e, nil
}

func (e *RaftElector) monitorLeadership() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case isLeader, ok := <-e.raftNode.LeaderCh():
			if !ok {
				return
			}
			e.setLeader(isLeader)
			if isLeader {
				e.reconcileCluster()
			}
		case <-ticker.C:
			if e.IsLeader() {
				e.reconcileCluster()
			}
		}
	}
}

func (e *RaftElector) reconcileCluster() {
	future := e.raftNode.GetConfiguration()
	if err := future.Error(); err != nil {
		slog.Error("RaftElector: failed to get raft config", "err", err)
		return
	}

	raftNodes := make(map[string]bool)
	for _, s := range future.Configuration().Servers {
		raftNodes[string(s.ID)] = true
	}

	for _, m := range e.memberlist.Members() {
		if !raftNodes[m.Name] {
			raftAddr := string(m.Meta)
			if raftAddr == "" {
				continue
			}
			slog.Info("RaftElector: reconciling missing member into raft", "node_id", m.Name, "raft_addr", raftAddr)
			f := e.raftNode.AddVoter(raft.ServerID(m.Name), raft.ServerAddress(raftAddr), 0, 0)
			if err := f.Error(); err != nil {
				slog.Error("RaftElector: async reconcile AddVoter failed", "err", err)
			}
		}
	}
}

func (e *RaftElector) setLeader(state bool) {
	if e.isLeader.CompareAndSwap(!state, state) {
		select {
		case e.watchChan <- state:
		default:
			<-e.watchChan
			e.watchChan <- state
		}
	}
}

func (e *RaftElector) Acquire(ctx context.Context) error {
	// Raft operates asynchronously; Acquire simply blocks until we become the leader or context cancels
	if e.IsLeader() {
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case isLeader := <-e.watchChan:
			if isLeader {
				// Re-emit immediately so subsequent callers know
				e.setLeader(true)
				return nil
			}
		}
	}
}

func (e *RaftElector) IsLeader() bool {
	return e.isLeader.Load()
}

func (e *RaftElector) Resign(ctx context.Context) error {
	if e.IsLeader() {
		// Force leadership handover
		future := e.raftNode.LeadershipTransfer()
		return future.Error()
	}
	return nil
}

func (e *RaftElector) Watch() <-chan bool {
	return e.watchChan
}

func (e *RaftElector) Close() {
	if e.memberlist != nil {
		if err := e.memberlist.Shutdown(); err != nil {
			slog.Warn("RaftElector: memberlist shutdown failed", "err", err)
		}
	}
	if e.raftNode != nil {
		if err := e.raftNode.Shutdown().Error(); err != nil {
			slog.Warn("RaftElector: raft shutdown failed", "err", err)
		}
	}
}
