package command

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/rustic-ai/forge/forge-go/agent"
	"github.com/rustic-ai/forge/forge-go/forgepath"
	"github.com/rustic-ai/forge/forge-go/helper/logging"
	"github.com/spf13/cobra"
)

var (
	serverDB                  string
	serverRedis               string
	serverNATS                string
	serverEmbeddedRedis       string
	serverListen              string
	serverManagerAPIBase      string
	serverDataDir             string
	serverDependencyConfig    string
	serverWithClient          bool
	serverClientNodeID        string
	serverClientMetrics       string
	serverClientCPUs          int
	serverClientMemory        int
	serverClientGPUs          int
	serverClientSupervisor    string
	serverClientTransport     string
	serverClientAttachTree    bool
	serverClientZMQBridgeMode string
	serverBackend             string
	serverEmbeddedNATSAddr    string
	serverStateStore          string
)

func init() {
	ServerCmd.Flags().StringVar(&serverDB, "db", "", "Database DSN (default: sqlite://<forge-home>/data/forge.db)")
	ServerCmd.Flags().StringVar(&serverRedis, "redis", "", "Redis URL (default: embedded miniredis)")
	ServerCmd.Flags().StringVar(&serverNATS, "nats", "", "NATS URL for data-plane messaging (e.g. nats://localhost:4222); omit to use Redis")
	ServerCmd.Flags().StringVar(&serverEmbeddedRedis, "embedded-redis-addr", "127.0.0.1:6379", "Bind address for embedded Redis when --redis is not set")
	ServerCmd.Flags().StringVar(&serverListen, "listen", ":9090", "HTTP server bind address")
	ServerCmd.Flags().StringVar(&serverManagerAPIBase, "manager-api-base-url", "", "Externally reachable Forge manager API base URL (e.g. http://forge.example.com:9090)")
	ServerCmd.Flags().StringVar(&serverDataDir, "data-dir", "", "Base path for central file storage (default: <forge-home>/data)")
	ServerCmd.Flags().StringVar(&serverDependencyConfig, "dependency-config", forgepath.DefaultDependencyConfigPath, "Path to dependency map config")
	ServerCmd.Flags().BoolVar(&serverWithClient, "with-client", false, "Start an in-process Forge client/node")
	ServerCmd.Flags().StringVar(&serverClientNodeID, "client-node-id", "", "Node ID for in-process client (default: hostname)")
	ServerCmd.Flags().StringVar(&serverClientMetrics, "client-metrics-addr", ":9091", "Metrics bind address for in-process client")
	ServerCmd.Flags().IntVar(&serverClientCPUs, "client-cpus", 0, "Override CPUs for in-process client")
	ServerCmd.Flags().IntVar(&serverClientMemory, "client-memory", 0, "Override memory (MB) for in-process client")
	ServerCmd.Flags().IntVar(&serverClientGPUs, "client-gpus", 0, "Override GPUs for in-process client")
	ServerCmd.Flags().StringVar(&serverClientSupervisor, "client-default-supervisor", "", "Default supervisor for in-process client (docker, bwrap)")
	ServerCmd.Flags().StringVar(&serverClientTransport, "client-default-agent-transport", "direct", `Default local agent dataplane transport for the in-process client (direct, supervisor-zmq)`)
	ServerCmd.Flags().BoolVar(&serverClientAttachTree, "client-attach-process-tree", false, "When used with --with-client and process supervisor, launch agent processes in the server process tree so they exit with the server")
	ServerCmd.Flags().StringVar(&serverClientZMQBridgeMode, "client-zmq-bridge-mode", "ipc", `ZMQ bridge transport for non-process supervisors: "ipc" or "tcp"`)
	ServerCmd.Flags().StringVar(&serverBackend, "backend", "redis", `Messaging backend: "redis" or "nats"`)
	ServerCmd.Flags().StringVar(&serverEmbeddedNATSAddr, "embedded-nats-addr", "", "Bind address for embedded NATS (default: ephemeral port)")
	ServerCmd.Flags().StringVar(&serverStateStore, "state-store", "", `State store backend: "diskcache" (default: in-memory)`)

	RootCmd.AddCommand(ServerCmd)
}

var ServerCmd = &cobra.Command{
	Use:   "server",
	Short: "Start the Forge distributed server",
	Long:  `Starts the server core with an HTTP API, metastore, and central queue management.`,
	Run: func(cmd *cobra.Command, args []string) {
		out := os.Stdout
		l := logging.NewLogger(out, logLevel)
		logging.SetGlobalLogger(l)

		db := serverDB
		if db == "" {
			db = "sqlite://" + forgepath.Resolve("data/forge.db")
		}
		dataDir := serverDataDir
		if dataDir == "" {
			dataDir = forgepath.Resolve("data")
		}

		cfg := &agent.ServerConfig{
			DatabaseURL:             db,
			RedisURL:                serverRedis,
			NATSUrl:                 serverNATS,
			Backend:                 serverBackend,
			EmbeddedRedisAddr:       serverEmbeddedRedis,
			EmbeddedNATSAddr:        serverEmbeddedNATSAddr,
			ListenAddress:           serverListen,
			ManagerAPIBaseURL:       serverManagerAPIBase,
			DataDir:                 dataDir,
			DependencyConfig:        serverDependencyConfig,
			WithClient:              serverWithClient,
			ClientNodeID:            serverClientNodeID,
			ClientMetricsAddr:       serverClientMetrics,
			ClientCPUs:              serverClientCPUs,
			ClientMemory:            serverClientMemory,
			ClientGPUs:              serverClientGPUs,
			ClientDefaultSupervisor: serverClientSupervisor,
			ClientDefaultTransport:  serverClientTransport,
			ClientZMQBridgeMode:     serverClientZMQBridgeMode,
			ClientAttachProcessTree: serverClientAttachTree,
			StateStore:              serverStateStore,
		}

		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		if err := agent.StartServer(ctx, cfg); err != nil {
			l.Error("Server exited with error", "error", err)
			os.Exit(1)
		}
	},
}
