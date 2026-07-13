package agent

import "github.com/rustic-ai/forge/forge-go/oauth"

type ServerConfig struct {
	DatabaseURL             string
	RedisURL                string
	NATSUrl                 string
	Backend                 string // "redis" (default) or "nats"
	EmbeddedRedisAddr       string
	EmbeddedNATSAddr        string // Bind address for embedded NATS (default: ephemeral)
	ListenAddress           string
	ManagerAPIBaseURL       string
	DataDir                 string
	DependencyConfig        string
	WithClient              bool
	ClientNodeID            string
	ClientMetricsAddr       string
	ClientCPUs              int
	ClientMemory            int
	ClientGPUs              int
	ClientDefaultSupervisor string
	ClientDefaultTransport  string
	ClientZMQBridgeMode     string
	ClientAttachProcessTree bool
	LeaderElectionMode      string
	RaftBindAddr            string
	GossipBindAddr          string
	GossipJoinPeers         []string
	StateStore              string
	TelemetryEnabled        bool
	TelemetryMode           string
	TelemetryEndpoint       string
	TelemetryServiceName    string
	TelemetrySQLiteBinary   string
	TelemetrySQLiteDBPath   string
	TelemetrySQLitePort     int
}

type ClientConfig struct {
	ServerURL         string
	RedisURL          string
	NATSUrl           string
	DataDir           string
	CPUs              int
	Memory            int
	GPUs              int
	NodeID            string
	MetricsAddr       string
	DefaultSupervisor string
	DefaultTransport  string
	ZMQBridgeMode     string
	AttachProcessTree bool
	StopAgentsOnExit  bool
	OAuthManager      *oauth.Manager
}
