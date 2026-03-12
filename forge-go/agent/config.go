package agent

type ServerConfig struct {
	DatabaseURL             string
	RedisURL                string
	EmbeddedRedisAddr       string
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
	ClientAttachProcessTree bool
	LeaderElectionMode      string
	RaftBindAddr            string
	GossipBindAddr          string
	GossipJoinPeers         []string
}

type ClientConfig struct {
	ServerURL         string
	RedisURL          string
	DataDir           string
	CPUs              int
	Memory            int
	GPUs              int
	NodeID            string
	MetricsAddr       string
	DefaultSupervisor string
	AttachProcessTree bool
}
