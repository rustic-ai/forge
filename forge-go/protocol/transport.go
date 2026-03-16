package protocol

import "strings"

type AgentTransportMode string

const (
	AgentTransportDirect        AgentTransportMode = "direct"
	AgentTransportSupervisorZMQ AgentTransportMode = "supervisor-zmq"
)

const (
	ClientPropertyAgentTransport = "agent_transport"

	EnvForgeAgentTransport          = "FORGE_AGENT_TRANSPORT"
	EnvForgeSupervisorZMQEndpoint   = "FORGE_SUPERVISOR_ZMQ_ENDPOINT"
	EnvForgeSupervisorZMQIdentity   = "FORGE_SUPERVISOR_ZMQ_IDENTITY"
	EnvForgeSupervisorZMQConfigJSON = "FORGE_SUPERVISOR_ZMQ_CONFIG_JSON"

	SupervisorZMQBackendModule = "rustic_ai.forge.messaging.supervisor_backend"
	SupervisorZMQBackendClass  = "SupervisorZmqMessagingBackend"
)

func NormalizeAgentTransportMode(raw string) AgentTransportMode {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(AgentTransportDirect):
		return AgentTransportDirect
	case string(AgentTransportSupervisorZMQ):
		return AgentTransportSupervisorZMQ
	default:
		return AgentTransportDirect
	}
}

func AgentTransportFromClientProperties(props JSONB) AgentTransportMode {
	if props == nil {
		return AgentTransportDirect
	}
	value, _ := props[ClientPropertyAgentTransport].(string)
	return NormalizeAgentTransportMode(value)
}
