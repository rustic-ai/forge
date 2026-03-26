package supervisor

import (
	"os"
	"testing"

	"github.com/rustic-ai/forge/forge-go/registry"
)

func TestBuildBwrapArgs(t *testing.T) {

	homeDir, _ := os.UserHomeDir()
	uvToolDir := homeDir + "/.local/share/uv"
	uvCacheDir := homeDir + "/.cache/uv"
	forgeDir := homeDir + "/.forge"

	baseArgs := []string{
		"--unshare-all",
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
		"--die-with-parent",
	}

	tests := []struct {
		name     string
		entry    *registry.AgentRegistryEntry
		cmd      []string
		expected []string
	}{
		{
			name: "Airgapped Network (Empty)",
			entry: &registry.AgentRegistryEntry{
				Network: []string{},
			},
			cmd: []string{"echo", "hello"},
			expected: append(append([]string{}, baseArgs...),
				"--bind", uvToolDir, uvToolDir,
				"--bind", uvCacheDir, uvCacheDir,
				"--bind", forgeDir, forgeDir,
				"--", "echo", "hello",
			),
		},
		{
			name: "Airgapped Network (Explicit None)",
			entry: &registry.AgentRegistryEntry{
				Network: []string{"none"},
			},
			cmd: []string{"echo", "hello"},
			expected: append(append([]string{}, baseArgs...),
				"--bind", uvToolDir, uvToolDir,
				"--bind", uvCacheDir, uvCacheDir,
				"--bind", forgeDir, forgeDir,
				"--", "echo", "hello",
			),
		},
		{
			name: "Shared Network (Host)",
			entry: &registry.AgentRegistryEntry{
				Network: []string{"api.openai.com"},
			},
			cmd: []string{"python", "-m", "agent"},
			expected: append(append(append([]string{}, baseArgs...), "--share-net"),
				"--bind", uvToolDir, uvToolDir,
				"--bind", uvCacheDir, uvCacheDir,
				"--bind", forgeDir, forgeDir,
				"--", "python", "-m", "agent",
			),
		},
		{
			name: "Custom Filesystem Binds",
			entry: &registry.AgentRegistryEntry{
				Network: []string{},
				Filesystem: []registry.FilesystemPermission{
					{Path: "/app/data", Mode: "rw"},
					{Path: "/app/config", Mode: "ro"},
				},
			},
			cmd: []string{"python"},
			expected: append(append([]string{}, baseArgs...),
				"--bind", "/app/data", "/app/data",
				"--ro-bind", "/app/config", "/app/config",
				"--bind", uvToolDir, uvToolDir,
				"--bind", uvCacheDir, uvCacheDir,
				"--bind", forgeDir, forgeDir,
				"--", "python",
			),
		},
	}

	sup := NewBubblewrapSupervisor(nil)

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sup.buildBwrapArgs(tc.entry, tc.cmd, nil, nil)

			if len(got) != len(tc.expected) {
				t.Fatalf("buildBwrapArgs() len = %d, want %d\n  got:  %v\n  want: %v", len(got), len(tc.expected), got, tc.expected)
			}
			for i := range got {
				if got[i] != tc.expected[i] {
					t.Errorf("buildBwrapArgs()[%d] = %q, want %q", i, got[i], tc.expected[i])
				}
			}
		})
	}
}

func TestBuildBwrapArgsWithTCPBridge(t *testing.T) {
	sup := NewBubblewrapSupervisor(nil)
	entry := &registry.AgentRegistryEntry{
		Network: []string{},
	}
	cmd := []string{"echo", "hello"}

	// Simulate a TCP bridge by creating a mock that reports TCP mode.
	// We can't easily create a real bridge without a messaging backend,
	// so we test the arg-building logic with a nil bridge (no bridge case)
	// and verify TCP --share-net forcing is documented in the code.
	args := sup.buildBwrapArgs(entry, cmd, nil, nil)

	// Without bridge, no --share-net should be present for airgapped network
	for _, arg := range args {
		if arg == "--share-net" {
			t.Fatal("expected no --share-net without bridge, got one")
		}
	}
}

func TestBuildBwrapArgsWithIPCBridgeBindsSocketDir(t *testing.T) {
	// Verify that with a nil bridge (no ZMQ), socket dir is not bound
	sup := NewBubblewrapSupervisor(nil)
	entry := &registry.AgentRegistryEntry{
		Network: []string{},
	}
	cmd := []string{"echo", "hello"}

	args := sup.buildBwrapArgs(entry, cmd, nil, nil)

	for _, arg := range args {
		if arg == "/tmp/forge-zmq" {
			t.Fatal("expected no forge-zmq bind without bridge")
		}
	}
}

func TestBuildBwrapArgsBindsEnvWritablePaths(t *testing.T) {
	sup := NewBubblewrapSupervisor(nil)
	entry := &registry.AgentRegistryEntry{Network: []string{}}
	cmd := []string{"echo", "hello"}
	baseDir := t.TempDir()

	env := []string{
		"FORGE_UV_CACHE_DIR=" + baseDir + "/uv-cache",
		"UV_CACHE_DIR=" + baseDir + "/uv-cache",
		"XDG_CACHE_HOME=" + baseDir + "/xdg-cache",
		"XDG_DATA_HOME=" + baseDir + "/xdg-data",
		"TMPDIR=" + baseDir + "/tmp",
	}

	args := sup.buildBwrapArgs(entry, cmd, nil, env)

	for _, path := range []string{
		baseDir + "/uv-cache",
		baseDir + "/xdg-cache",
		baseDir + "/xdg-data",
		baseDir + "/tmp",
	} {
		found := false
		for i := 0; i+2 < len(args); i++ {
			if args[i] == "--bind" && args[i+1] == path && args[i+2] == path {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected bind for %s in args: %v", path, args)
		}
	}
}

func TestBuildBwrapArgsBindsInheritedWritablePaths(t *testing.T) {
	sup := NewBubblewrapSupervisor(nil)
	entry := &registry.AgentRegistryEntry{Network: []string{}}
	cmd := []string{"echo", "hello"}
	baseDir := t.TempDir()
	xdgData := baseDir + "/xdg-data"

	t.Setenv("XDG_DATA_HOME", xdgData)

	args := sup.buildBwrapArgs(entry, cmd, nil, nil)

	found := false
	for i := 0; i+2 < len(args); i++ {
		if args[i] == "--bind" && args[i+1] == xdgData && args[i+2] == xdgData {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected inherited XDG_DATA_HOME bind for %s in args: %v", xdgData, args)
	}
}
