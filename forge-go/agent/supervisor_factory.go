package agent

import (
	"github.com/redis/go-redis/v9"
	"github.com/rustic-ai/forge/forge-go/control"
	"github.com/rustic-ai/forge/forge-go/supervisor"
)

func buildOrgSupervisorFactory(rdb *redis.Client, defaultSupervisor, dataDir string) control.SupervisorFactory {
	return func(orgID string) supervisor.AgentSupervisor {
		processSup := supervisor.NewProcessSupervisor(
			rdb,
			supervisor.WithOrganizationID(orgID),
			supervisor.WithWorkDirBase(dataDir),
		)

		var dockerSup *supervisor.DockerSupervisor
		if ds, err := supervisor.NewDockerSupervisor(rdb); err == nil && ds.Available() {
			dockerSup = ds
		}

		var bwrapSup *supervisor.BubblewrapSupervisor
		bs := supervisor.NewBubblewrapSupervisor(rdb)
		if bs.Available() {
			bwrapSup = bs
		}

		return supervisor.NewDispatchingSupervisor(defaultSupervisor, processSup, dockerSup, bwrapSup)
	}
}
