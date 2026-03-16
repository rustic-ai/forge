//go:build windows

package supervisor

import (
	"context"
	"fmt"

	"github.com/rustic-ai/forge/forge-go/messaging"
	"github.com/rustic-ai/forge/forge-go/protocol"
	"github.com/rustic-ai/forge/forge-go/registry"
)

type BubblewrapSupervisor struct{}

// BubblewrapSupervisorOption configures a BubblewrapSupervisor (no-op on Windows).
type BubblewrapSupervisorOption func(*BubblewrapSupervisor)

// WithBubblewrapDefaultTransport is a no-op on Windows.
func WithBubblewrapDefaultTransport(mode string) BubblewrapSupervisorOption {
	return func(*BubblewrapSupervisor) {}
}

// WithBubblewrapMessagingBackend is a no-op on Windows.
func WithBubblewrapMessagingBackend(backend messaging.Backend) BubblewrapSupervisorOption {
	return func(*BubblewrapSupervisor) {}
}

// WithBubblewrapZMQBridgeMode is a no-op on Windows.
func WithBubblewrapZMQBridgeMode(mode BridgeTransportMode) BubblewrapSupervisorOption {
	return func(*BubblewrapSupervisor) {}
}

func NewBubblewrapSupervisor(statusStore AgentStatusStore, opts ...BubblewrapSupervisorOption) *BubblewrapSupervisor {
	return &BubblewrapSupervisor{}
}

func (p *BubblewrapSupervisor) Available() bool {
	return false
}

func (p *BubblewrapSupervisor) Launch(ctx context.Context, guildID string, agentSpec *protocol.AgentSpec, reg *registry.Registry, env []string) error {
	return fmt.Errorf("bubblewrap supervisor is not supported on windows")
}

func (p *BubblewrapSupervisor) Stop(ctx context.Context, guildID, agentID string) error {
	return fmt.Errorf("bubblewrap supervisor is not supported on windows")
}

func (p *BubblewrapSupervisor) Status(ctx context.Context, guildID, agentID string) (string, error) {
	return "unknown", nil
}

func (p *BubblewrapSupervisor) GetPID(ctx context.Context, guildID, agentID string) (int, error) {
	return 0, fmt.Errorf("bubblewrap supervisor is not supported on windows")
}

func (p *BubblewrapSupervisor) StopAll(ctx context.Context) error {
	return nil
}
