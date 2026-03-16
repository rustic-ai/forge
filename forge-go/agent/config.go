package agent

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
	ClientAttachProcessTree bool
	LeaderElectionMode      string
	RaftBindAddr            string
	GossipBindAddr          string
	GossipJoinPeers         []string
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
	AttachProcessTree bool
}
