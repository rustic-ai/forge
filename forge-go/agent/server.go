package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rustic-ai/forge/forge-go/api"
	"github.com/rustic-ai/forge/forge-go/control"
	"github.com/rustic-ai/forge/forge-go/embed"
	"github.com/rustic-ai/forge/forge-go/filesystem"
	"github.com/rustic-ai/forge/forge-go/guild/store"
	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/scheduler"
	"github.com/rustic-ai/forge/forge-go/scheduler/leader"
)

const defaultEmbeddedRedisAddr = "127.0.0.1:6379"

func StartServer(ctx context.Context, cfg *ServerConfig) error {
	serverCtx, cancelServer := context.WithCancel(ctx)
	defer cancelServer()

	l := slog.Default()
	defer l.Info("Forge server completely shut down")
	l.Info("Starting Forge distributed server", "listen", cfg.ListenAddress)

	driverName, dbDSN := store.ResolveDriverAndDSN(cfg.DatabaseURL)
	l.Info("Connecting to database", "url", cfg.DatabaseURL, "driver", driverName)
	_ = os.Setenv("FORGE_DATABASE_URL", cfg.DatabaseURL)
	_ = os.Setenv("FORGE_MANAGER_API_BASE_URL", deriveManagerAPIBaseURL(cfg.ListenAddress, cfg.ManagerAPIBaseURL))
	if strings.TrimSpace(cfg.DependencyConfig) != "" {
		_ = os.Setenv("FORGE_DEPENDENCY_CONFIG", cfg.DependencyConfig)
	}
	db, err := store.NewGormStore(driverName, dbDSN)
	if err != nil {
		return fmt.Errorf("database connection failed: %w", err)
	}
	defer db.Close()

	var redisClient *redis.Client
	redisAddr := cfg.RedisURL
	if redisAddr == "" {
		embeddedAddr := strings.TrimSpace(cfg.EmbeddedRedisAddr)
		if embeddedAddr == "" {
			embeddedAddr = defaultEmbeddedRedisAddr
		}
		l.Info("No redis address provided. Booting Embedded Miniredis...", "bind_addr", embeddedAddr)
		mredis, err := embed.StartEmbeddedRedisAt(embeddedAddr)
		if err != nil {
			return fmt.Errorf("failed to start miniredis: %w", err)
		}
		defer mredis.Close()
		redisAddr = mredis.Addr()
		l.Info("Embedded miniredis started", "redis_addr", redisAddr)
		redisClient = mredis.Client()
	} else {
		l.Info("Using external redis", "redis_addr", redisAddr)
		redisClient = redis.NewClient(&redis.Options{Addr: redisAddr})
	}

	defer redisClient.Close()
	if host, port, err := net.SplitHostPort(redisAddr); err == nil {
		_ = os.Setenv("REDIS_HOST", host)
		_ = os.Setenv("REDIS_PORT", port)
	}

	var wg sync.WaitGroup
	queueListener := control.NewControlQueueListener(redisClient)

	queueListener.OnSpawn = func(ctx context.Context, req *protocol.SpawnRequest) {
		// Load guild model once; used for both messaging config and spec attachment.
		gm, err := db.GetGuild(req.GuildID)
		if err != nil {
			slog.Default().Warn("failed to load guild spec for spawn dispatch", "guild", req.GuildID, "error", err)
		}
		if gm != nil {
			if req.MessagingConfig == nil {
				req.MessagingConfig = &protocol.MessagingConfig{
					BackendModule: gm.BackendModule,
					BackendClass:  gm.BackendClass,
					BackendConfig: map[string]interface{}(gm.BackendConfig),
				}
			}
			// Distributed workers may not have DB access. Attach the full guild spec so the
			// runtime can build FORGE_GUILD_JSON from the spawn payload alone.
			if req.ClientProperties == nil {
				req.ClientProperties = protocol.JSONB{}
			}
			if _, exists := req.ClientProperties["guild_spec"]; !exists {
				req.ClientProperties["guild_spec"] = store.ToGuildSpec(gm)
			}
		}

		nodeID, err := scheduler.GlobalScheduler.Schedule(req.AgentSpec)
		if err != nil {
			slog.Default().Error("Failed to schedule agent", "guild", req.GuildID, "agent", req.AgentSpec.ID, "error", err)
			return
		}
		payloadBytes, _ := json.Marshal(req)
		scheduler.GlobalPlacementMap.Place(req.GuildID, req.AgentSpec.ID, nodeID, payloadBytes)

		wrapper := control.ControlMessageWrapper{
			Command: "spawn",
			Payload: payloadBytes,
		}

		wrapperBytes, _ := json.Marshal(wrapper)
		redisClient.LPush(ctx, "forge:control:node:"+nodeID, wrapperBytes)
		slog.Default().Info("Scheduled agent to node", "agent", req.AgentSpec.ID, "node_id", nodeID)
	}
	queueListener.OnStop = func(ctx context.Context, req *protocol.StopRequest) {
		placement, ok := scheduler.GlobalPlacementMap.Find(req.GuildID, req.AgentID)
		if !ok {
			slog.Default().Warn("No placement found for stop request", "guild", req.GuildID, "agent", req.AgentID)
			return
		}
		payloadBytes, _ := json.Marshal(req)
		wrapper := control.ControlMessageWrapper{
			Command: "stop",
			Payload: payloadBytes,
		}
		wrapperBytes, _ := json.Marshal(wrapper)
		redisClient.LPush(ctx, "forge:control:node:"+placement.NodeID, wrapperBytes)
		scheduler.GlobalPlacementMap.Remove(req.GuildID, req.AgentID)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		queueListener.Start(serverCtx)
		slog.Default().Info("Control queue listener shut down")
	}()

	hostname, _ := os.Hostname()
	nodeID := fmt.Sprintf("server-%s-%s", hostname, cfg.ListenAddress)

	var elector leader.LeaderElector

	if cfg.LeaderElectionMode == "raft" {
		raftCfg := leader.RaftConfig{
			NodeID:          nodeID,
			RaftBindAddr:    cfg.RaftBindAddr,
			GossipBindAddr:  cfg.GossipBindAddr,
			GossipJoinPeers: cfg.GossipJoinPeers,
		}
		var err error
		elector, err = leader.NewRaftElector(raftCfg)
		if err != nil {
			return fmt.Errorf("failed to start raft elector: %w", err)
		}
	} else {
		elector = leader.NewRedisElector(redisClient, nodeID, "forge:control:leader", 5*time.Second)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := elector.Acquire(serverCtx); err != nil && err != context.Canceled {
			slog.Default().Error("LeaderElector failed", "error", err)
		}
	}()

	reconciler := scheduler.NewReconciler(scheduler.GlobalNodeRegistry, scheduler.GlobalPlacementMap, redisClient, elector)
	wg.Add(1)
	go func() {
		defer wg.Done()
		reconciler.Start(serverCtx)
		slog.Default().Info("Node reconciler shut down")
	}()

	fsRoot := strings.TrimSpace(cfg.DataDir)
	homeDir, _ := os.UserHomeDir()
	switch {
	case fsRoot == "":
		fsRoot = filepath.Join(homeDir, ".forge", "data")
	case fsRoot == "~":
		fsRoot = homeDir
	case strings.HasPrefix(fsRoot, "~/"):
		fsRoot = filepath.Join(homeDir, fsRoot[2:])
	}
	fsBasePath := filepath.Join(fsRoot, "workspaces")
	if err := os.MkdirAll(fsBasePath, 0755); err != nil {
		return fmt.Errorf("failed to create filesystem workspace base path: %w", err)
	}
	if strings.TrimSpace(os.Getenv("FORGE_FILESYSTEM_GLOBAL_ROOT")) == "" {
		_ = os.Setenv("FORGE_FILESYSTEM_GLOBAL_ROOT", fsBasePath)
	}
	resolver := filesystem.NewFileSystemResolver(fsBasePath)
	fileStore := filesystem.NewLocalFileStore(resolver)

	msgClient := messaging.NewClient(redisClient)

	httpServer := api.NewServer(db, redisClient, msgClient, fileStore, cfg.ListenAddress)
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := httpServer.Start(serverCtx); err != nil {
			l.Error("HTTP API exited with error", "error", err)
		}
		l.Info("HTTP API Placeholder listening on", "address", cfg.ListenAddress)
	}()

	var (
		cancelClient         context.CancelFunc
		embeddedClientNodeID string
	)
	if cfg.WithClient {
		clientServerURL := deriveManagerAPIBaseURL(cfg.ListenAddress, "")
		clientMetricsAddr := strings.TrimSpace(cfg.ClientMetricsAddr)
		if clientMetricsAddr == "" {
			clientMetricsAddr = ":9091"
		}
		clientCfg := &ClientConfig{
			ServerURL:         clientServerURL,
			RedisURL:          redisAddr,
			DataDir:           cfg.DataDir,
			CPUs:              cfg.ClientCPUs,
			Memory:            cfg.ClientMemory,
			GPUs:              cfg.ClientGPUs,
			NodeID:            cfg.ClientNodeID,
			MetricsAddr:       clientMetricsAddr,
			DefaultSupervisor: cfg.ClientDefaultSupervisor,
		}
		l.Info("In-process Forge client enabled",
			"server_url", clientServerURL,
			"node_id", clientCfg.NodeID,
			"metrics_addr", clientCfg.MetricsAddr,
		)
		clientCtx, clientCancel := context.WithCancel(serverCtx)
		cancelClient = clientCancel
		embeddedClientNodeID = clientCfg.NodeID
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := waitForServerReady(clientCtx, clientServerURL, 5*time.Second); err != nil {
				if err != context.Canceled {
					l.Error("Failed waiting for server readiness before starting in-process client", "error", err)
				}
				return
			}
			if err := StartClient(clientCtx, clientCfg); err != nil && err != context.Canceled {
				l.Error("In-process client exited with error", "error", err)
			}
		}()
	}

	<-serverCtx.Done()

	l.Info("Received cancellation signal. Commencing graceful shutdown...")
	if cancelClient != nil {
		if embeddedClientNodeID != "" {
			scheduler.GlobalNodeRegistry.Deregister(embeddedClientNodeID)
			l.Info("Embedded client node deregistered during server shutdown", "node_id", embeddedClientNodeID)
		}
		cancelClient()
	}

	wg.Wait()

	return nil
}

func deriveManagerAPIBaseURL(listenAddress, explicit string) string {
	explicit = strings.TrimSpace(explicit)
	if explicit != "" {
		return strings.TrimRight(explicit, "/")
	}

	normalized := strings.TrimSpace(listenAddress)
	if normalized == "" {
		return "http://127.0.0.1:9090"
	}
	if strings.HasPrefix(normalized, "http://") || strings.HasPrefix(normalized, "https://") {
		return strings.TrimRight(normalized, "/")
	}
	if strings.HasPrefix(normalized, ":") {
		return "http://127.0.0.1" + normalized
	}
	return "http://" + normalized
}

func waitForServerReady(ctx context.Context, baseURL string, timeout time.Duration) error {
	client := &http.Client{Timeout: 1 * time.Second}
	deadline := time.Now().Add(timeout)
	readyURL := strings.TrimRight(baseURL, "/") + "/readyz"

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for server readiness at %s", readyURL)
		}

		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, readyURL, nil)
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
}
