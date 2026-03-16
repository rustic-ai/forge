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
) control.SupervisorFactory {
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
		if ds, err := supervisor.NewDockerSupervisor(statusStore); err == nil && ds.Available() {
			dockerSup = ds
		}

		var bwrapSup *supervisor.BubblewrapSupervisor
		bs := supervisor.NewBubblewrapSupervisor(statusStore)
		if bs.Available() {
			bwrapSup = bs
		}

		return supervisor.NewDispatchingSupervisor(defaultSupervisor, defaultTransport, processSup, dockerSup, bwrapSup)
	}
}
