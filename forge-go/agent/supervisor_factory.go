package agent

import (
	"github.com/rustic-ai/forge/forge-go/control"
	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/supervisor"
)

func buildOrgSupervisorFactory(
	statusStore supervisor.AgentStatusStore,
	defaultSupervisor string,
	defaultTransport string,
	msgBackend messaging.Backend,
	dataDir string,
	attachProcessTree bool,
	zmqBridgeMode string,
) control.SupervisorFactory {
	bridgeMode := supervisor.NormalizeBridgeTransportMode(zmqBridgeMode)

	return func(orgID string) supervisor.AgentSupervisor {
		opts := []supervisor.ProcessSupervisorOption{
			supervisor.WithOrganizationID(orgID),
			supervisor.WithWorkDirBase(dataDir),
			supervisor.WithDefaultAgentTransport(defaultTransport),
			supervisor.WithMessagingBackend(msgBackend),
		}
		if attachProcessTree {
			opts = append(opts, supervisor.WithAttachedProcessTree())
		}
		processSup := supervisor.NewProcessSupervisor(statusStore, opts...)

		var dockerSup *supervisor.DockerSupervisor
		if ds, err := supervisor.NewDockerSupervisor(statusStore,
			supervisor.WithDockerDefaultTransport(defaultTransport),
			supervisor.WithDockerMessagingBackend(msgBackend),
			supervisor.WithDockerZMQBridgeMode(bridgeMode),
		); err == nil && ds.Available() {
			dockerSup = ds
		}

		var bwrapSup *supervisor.BubblewrapSupervisor
		bs := supervisor.NewBubblewrapSupervisor(statusStore,
			supervisor.WithBubblewrapDefaultTransport(defaultTransport),
			supervisor.WithBubblewrapMessagingBackend(msgBackend),
			supervisor.WithBubblewrapZMQBridgeMode(bridgeMode),
		)
		if bs.Available() {
			bwrapSup = bs
		}

		return supervisor.NewDispatchingSupervisor(defaultSupervisor, defaultTransport, processSup, dockerSup, bwrapSup)
	}
}
