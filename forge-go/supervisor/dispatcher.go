package supervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"

	"github.com/rustic-ai/forge/forge-go/helper/logging"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/registry"
)

type DispatchingSupervisor struct {
	nodeDefault   string
	nodeTransport string
	processSup    AgentSupervisor
	dockerSup     AgentSupervisor
	bwrapSup      AgentSupervisor

	mu        sync.RWMutex
	ownership map[string]AgentSupervisor
}

func NewDispatchingSupervisor(
	nodeDefault string,
	nodeTransport string,
	process AgentSupervisor,
	docker AgentSupervisor,
	bwrap AgentSupervisor,
) *DispatchingSupervisor {
	return &DispatchingSupervisor{
		nodeDefault:   nodeDefault,
		nodeTransport: nodeTransport,
		processSup:    process,
		dockerSup:     docker,
		bwrapSup:      bwrap,
		ownership:     make(map[string]AgentSupervisor),
	}
}

func hasSupervisor(sup AgentSupervisor) bool {
	if sup == nil {
		return false
	}

	value := reflect.ValueOf(sup)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return !value.IsNil()
	default:
		return true
	}
}

func (d *DispatchingSupervisor) selectSupervisor(entry *registry.AgentRegistryEntry) (AgentSupervisor, error) {
	if d.nodeDefault == "docker" && hasSupervisor(d.dockerSup) {
		return d.dockerSup, nil
	}
	if d.nodeDefault == "bwrap" && hasSupervisor(d.bwrapSup) {
		return d.bwrapSup, nil
	}
	if d.nodeDefault == "process" && hasSupervisor(d.processSup) {
		return d.processSup, nil
	}

	requested := entry.Runtime
	if requested == registry.RuntimeDocker && hasSupervisor(d.dockerSup) {
		return d.dockerSup, nil
	}
	if requested == "bwrap" && hasSupervisor(d.bwrapSup) {
		return d.bwrapSup, nil
	}

	if hasSupervisor(d.processSup) {
		return d.processSup, nil
	}

	return nil, fmt.Errorf("no suitable supervisor found for requested runtime: %s", requested)
}

func (d *DispatchingSupervisor) Launch(ctx context.Context, guildID string, agentSpec *protocol.AgentSpec, reg *registry.Registry, env []string) error {
	log := logging.FromContext(ctx, slog.Default())

	entry, err := reg.Lookup(agentSpec.ClassName)
	if err != nil {
		return err
	}

	sup, err := d.selectSupervisor(entry)
	if err != nil {
		return err
	}

	transport := resolvedTransportFromEnv(env, d.nodeTransport)
	if transport == protocol.AgentTransportSupervisorZMQ {
		if _, ok := sup.(*ProcessSupervisor); !ok {
			return fmt.Errorf("agent transport %q is only supported by the process supervisor", transport)
		}
	}

	log.Debug("Dispatching agent launch", "agent_id", agentSpec.ID, "runtime", entry.Runtime, "supervisor", fmt.Sprintf("%T", sup))

	if err := sup.Launch(ctx, guildID, agentSpec, reg, env); err != nil {
		return err
	}

	key := scopedAgentKey(guildID, agentSpec.ID)
	d.mu.Lock()
	d.ownership[key] = sup
	d.mu.Unlock()

	return nil
}

func (d *DispatchingSupervisor) Stop(ctx context.Context, guildID, agentID string) error {
	key := scopedAgentKey(guildID, agentID)
	d.mu.RLock()
	sup, exists := d.ownership[key]
	d.mu.RUnlock()

	if !exists {
		return fmt.Errorf("agent %s not found in any supervisor for guild %s", agentID, normalizeGuildID(guildID))
	}

	err := sup.Stop(ctx, guildID, agentID)
	if err == nil {
		d.mu.Lock()
		delete(d.ownership, key)
		d.mu.Unlock()
	}
	return err
}

func (d *DispatchingSupervisor) Status(ctx context.Context, guildID, agentID string) (string, error) {
	key := scopedAgentKey(guildID, agentID)
	d.mu.RLock()
	sup, exists := d.ownership[key]
	d.mu.RUnlock()

	if !exists {
		return "", fmt.Errorf("agent %s not found in any supervisor for guild %s", agentID, normalizeGuildID(guildID))
	}

	return sup.Status(ctx, guildID, agentID)
}

func (d *DispatchingSupervisor) StopAll(ctx context.Context) error {
	var errs []error
	for _, sup := range []AgentSupervisor{d.processSup, d.dockerSup, d.bwrapSup} {
		if hasSupervisor(sup) {
			if err := sup.StopAll(ctx); err != nil {
				errs = append(errs, err)
			}
		}
	}

	d.mu.Lock()
	d.ownership = make(map[string]AgentSupervisor)
	d.mu.Unlock()

	return errors.Join(errs...)
}

func resolvedTransportFromEnv(env []string, defaultTransport string) protocol.AgentTransportMode {
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, protocol.EnvForgeAgentTransport+"="); ok {
			return protocol.NormalizeAgentTransportMode(value)
		}
	}
	return protocol.NormalizeAgentTransportMode(defaultTransport)
}
