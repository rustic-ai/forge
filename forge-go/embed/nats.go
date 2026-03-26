package embed

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"github.com/rustic-ai/forge/forge-go/forgepath"
)

// EmbeddedNATS wraps an in-process NATS server with JetStream enabled.
type EmbeddedNATS struct {
	server   *natsserver.Server
	storeDir string
}

// StartEmbeddedNATS spins up a new in-process NATS server on an ephemeral port.
func StartEmbeddedNATS() (*EmbeddedNATS, error) {
	return StartEmbeddedNATSAt("")
}

// StartEmbeddedNATSAt spins up a new in-process NATS server on a specific address.
// If addr is empty, an ephemeral port is used.
func StartEmbeddedNATSAt(addr string) (*EmbeddedNATS, error) {
	storeRoot, err := resolveEmbeddedNATSStoreRoot()
	if err != nil {
		return nil, err
	}

	storeDir, err := os.MkdirTemp(storeRoot, "embedded-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create embedded NATS store dir under %s: %w", storeRoot, err)
	}

	opts := &natsserver.Options{
		JetStream: true,
		StoreDir:  storeDir,
		Port:      -1,
	}

	addr = strings.TrimSpace(addr)
	if addr != "" {
		host, portStr, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("invalid address %q: %w", addr, err)
		}
		var port int
		if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
			return nil, fmt.Errorf("invalid port in address %q: %w", addr, err)
		}
		opts.Host = host
		opts.Port = port
	}

	s, err := natsserver.NewServer(opts)
	if err != nil {
		_ = os.RemoveAll(storeDir)
		return nil, fmt.Errorf("failed to create embedded NATS server: %w", err)
	}

	go s.Start()

	if !s.ReadyForConnections(15 * time.Second) {
		s.Shutdown()
		_ = os.RemoveAll(storeDir)
		return nil, fmt.Errorf("embedded NATS server did not become ready within 15s")
	}

	return &EmbeddedNATS{server: s, storeDir: storeDir}, nil
}

func resolveEmbeddedNATSStoreRoot() (string, error) {
	storeRoot := forgepath.Resolve("nats")
	if err := os.MkdirAll(storeRoot, 0o755); err == nil {
		probeDir, probeErr := os.MkdirTemp(storeRoot, ".probe-*")
		if probeErr == nil {
			_ = os.RemoveAll(probeDir)
			return storeRoot, nil
		}
	}

	fallbackRoot, err := os.MkdirTemp("", "forge-nats-*")
	if err != nil {
		return "", fmt.Errorf("failed to create NATS store dir %s and fallback temp dir: %w", storeRoot, err)
	}
	return fallbackRoot, nil
}

// Host returns the bound hostname.
func (e *EmbeddedNATS) Host() string {
	return e.server.Addr().(*net.TCPAddr).IP.String()
}

// Port returns the bound port.
func (e *EmbeddedNATS) Port() int {
	return e.server.Addr().(*net.TCPAddr).Port
}

// Addr returns host:port.
func (e *EmbeddedNATS) Addr() string {
	return fmt.Sprintf("%s:%d", e.Host(), e.Port())
}

// ClientURL returns the NATS client URL (nats://host:port).
func (e *EmbeddedNATS) ClientURL() string {
	return e.server.ClientURL()
}

// Client returns a new nats.Conn connected to this instance. The caller is
// responsible for closing the connection.
func (e *EmbeddedNATS) Client() (*nats.Conn, error) {
	return nats.Connect(e.ClientURL())
}

// Close shuts down the embedded server and removes its isolated JetStream store directory.
func (e *EmbeddedNATS) Close() {
	if e.server != nil {
		e.server.Shutdown()
	}
	if e.storeDir != "" {
		_ = os.RemoveAll(filepath.Clean(e.storeDir))
	}
}
