package command

import (
	"context"
	"os"

	"github.com/rustic-ai/forge/forge-go/agent"
	"github.com/rustic-ai/forge/forge-go/helper/logging"
	"github.com/spf13/cobra"
)

var (
	serverDB               string
	serverRedis            string
	serverEmbeddedRedis    string
	serverListen           string
	serverManagerAPIBase   string
	serverDataDir          string
	serverDependencyConfig string
	serverWithClient       bool
	serverClientNodeID     string
	serverClientMetrics    string
	serverClientCPUs       int
	serverClientMemory     int
	serverClientGPUs       int
	serverClientSupervisor string
	serverClientAttachTree bool
)

func init() {
	ServerCmd.Flags().StringVar(&serverDB, "db", "sqlite://~/.forge/data/forge.db", "Database DSN")
	ServerCmd.Flags().StringVar(&serverRedis, "redis", "", "Redis URL (default: embedded miniredis)")
	ServerCmd.Flags().StringVar(&serverEmbeddedRedis, "embedded-redis-addr", "127.0.0.1:6379", "Bind address for embedded Redis when --redis is not set")
	ServerCmd.Flags().StringVar(&serverListen, "listen", ":9090", "HTTP server bind address")
	ServerCmd.Flags().StringVar(&serverManagerAPIBase, "manager-api-base-url", "", "Externally reachable Forge manager API base URL (e.g. http://forge.example.com:9090)")
	ServerCmd.Flags().StringVar(&serverDataDir, "data-dir", "~/.forge/data", "Base path for central file storage")
	ServerCmd.Flags().StringVar(&serverDependencyConfig, "dependency-config", "./conf/agent-dependencies.yaml", "Path to dependency map config")
	ServerCmd.Flags().BoolVar(&serverWithClient, "with-client", false, "Start an in-process Forge client/node")
	ServerCmd.Flags().StringVar(&serverClientNodeID, "client-node-id", "", "Node ID for in-process client (default: hostname)")
	ServerCmd.Flags().StringVar(&serverClientMetrics, "client-metrics-addr", ":9091", "Metrics bind address for in-process client")
	ServerCmd.Flags().IntVar(&serverClientCPUs, "client-cpus", 0, "Override CPUs for in-process client")
	ServerCmd.Flags().IntVar(&serverClientMemory, "client-memory", 0, "Override memory (MB) for in-process client")
	ServerCmd.Flags().IntVar(&serverClientGPUs, "client-gpus", 0, "Override GPUs for in-process client")
	ServerCmd.Flags().StringVar(&serverClientSupervisor, "client-default-supervisor", "", "Default supervisor for in-process client (docker, bwrap)")
	ServerCmd.Flags().BoolVar(&serverClientAttachTree, "client-attach-process-tree", false, "When used with --with-client and process supervisor, launch agent processes in the server process tree so they exit with the server")

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

		cfg := &agent.ServerConfig{
			DatabaseURL:             serverDB,
			RedisURL:                serverRedis,
			EmbeddedRedisAddr:       serverEmbeddedRedis,
			ListenAddress:           serverListen,
			ManagerAPIBaseURL:       serverManagerAPIBase,
			DataDir:                 serverDataDir,
			DependencyConfig:        serverDependencyConfig,
			WithClient:              serverWithClient,
			ClientNodeID:            serverClientNodeID,
			ClientMetricsAddr:       serverClientMetrics,
			ClientCPUs:              serverClientCPUs,
			ClientMemory:            serverClientMemory,
			ClientGPUs:              serverClientGPUs,
			ClientDefaultSupervisor: serverClientSupervisor,
			ClientAttachProcessTree: serverClientAttachTree,
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		if err := agent.StartServer(ctx, cfg); err != nil {
			l.Error("Server exited with error", "error", err)
			os.Exit(1)
		}
	},
}
