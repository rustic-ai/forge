package command

import (
	"context"
	"os"

	"github.com/rustic-ai/forge/forge-go/agent"
	"github.com/rustic-ai/forge/forge-go/helper/logging"
	"github.com/spf13/cobra"
)

var (
	clientServerURL         string
	clientRedisURL          string
	clientNATSUrl           string
	clientDataDir           string
	clientCPUs              int
	clientMemory            int
	clientGPUs              int
	clientNodeID            string
	clientMetricsAddr       string
	clientDefaultSupervisor string
	clientDefaultTransport  string
	clientZMQBridgeMode     string
)

func init() {
	ClientCmd.Flags().StringVar(&clientServerURL, "server", "http://localhost:9090", "Forge server URL for registration")
	ClientCmd.Flags().StringVar(&clientRedisURL, "redis", "", "Redis URL (must point to identical Redis used by server)")
	ClientCmd.Flags().StringVar(&clientNATSUrl, "nats", "", "NATS URL for data-plane messaging (must match server --nats flag)")
	ClientCmd.Flags().StringVar(&clientDataDir, "data-dir", "~/.forge/data", "Base path for local client runtime data")
	ClientCmd.Flags().IntVar(&clientCPUs, "cpus", 0, "Override detected CPU count")
	ClientCmd.Flags().IntVar(&clientMemory, "memory", 0, "Override detected memory capacity in MB")
	ClientCmd.Flags().IntVar(&clientGPUs, "gpus", 0, "Override detected GPU count")
	ClientCmd.Flags().StringVar(&clientNodeID, "node-id", "", "Unique node identifier (default: hostname)")
	ClientCmd.Flags().StringVar(&clientMetricsAddr, "metrics-addr", ":9091", "Address to bind the metrics HTTP server")
	ClientCmd.Flags().StringVar(&clientDefaultSupervisor, "default-supervisor", "", "Force a specific supervisor (docker, bwrap) for all agents on this node")
	ClientCmd.Flags().StringVar(&clientDefaultTransport, "default-agent-transport", "direct", `Default local agent dataplane transport (direct, supervisor-zmq)`)
	ClientCmd.Flags().StringVar(&clientZMQBridgeMode, "zmq-bridge-mode", "ipc", `ZMQ bridge transport for non-process supervisors: "ipc" or "tcp"`)

	RootCmd.AddCommand(ClientCmd)
}

var ClientCmd = &cobra.Command{
	Use:   "client",
	Short: "Start a Forge distributed compute node",
	Long:  `Starts a client daemon that connects to the server and accepts agent spawn requests.`,
	Run: func(cmd *cobra.Command, args []string) {
		out := os.Stdout
		l := logging.NewLogger(out, logLevel)
		logging.SetGlobalLogger(l)

		if clientNodeID == "" {
			hostname, err := os.Hostname()
			if err != nil {
				l.Error("Failed to get hostname for node ID", "error", err)
				os.Exit(1)
			}
			clientNodeID = hostname
		}

		cfg := &agent.ClientConfig{
			ServerURL:         clientServerURL,
			RedisURL:          clientRedisURL,
			NATSUrl:           clientNATSUrl,
			DataDir:           clientDataDir,
			CPUs:              clientCPUs,
			Memory:            clientMemory,
			GPUs:              clientGPUs,
			NodeID:            clientNodeID,
			MetricsAddr:       clientMetricsAddr,
			DefaultSupervisor: clientDefaultSupervisor,
			DefaultTransport:  clientDefaultTransport,
			ZMQBridgeMode:     clientZMQBridgeMode,
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		if err := agent.StartClient(ctx, cfg); err != nil {
			l.Error("Client exited with error", "error", err)
			os.Exit(1)
		}
	},
}
