package registry

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/rustic-ai/forge/forge-go/oauth"
)

func TestParseRegistry(t *testing.T) {
	yml := `entries:
  - id: RouterAgent
    class_name: "rustic_ai.core.agents.system.router_agent.RouterAgent"
    description: "Handles dynamic message routing between agents"
    runtime: "uvx"
    package: "rusticai-core"
  - id: TestEchoAgent
    class_name: "test.echo.EchoAgent"
    description: "Test binary entry"
    runtime: "binary"
    executable: "python"
    args: ["-m", "test.echo"]`

	tmp := t.TempDir()
	path := filepath.Join(tmp, "registry.yaml")
	if err := os.WriteFile(path, []byte(yml), 0644); err != nil {
		t.Fatal(err)
	}

	reg, err := Load(path, nil)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// 1. Lookup known uvx class
	entry1, err := reg.Lookup("rustic_ai.core.agents.system.router_agent.RouterAgent")
	if err != nil {
		t.Fatalf("Expected to find entry1, got: %v", err)
	}
	if entry1.ID != "RouterAgent" || entry1.Runtime != RuntimeUVX || entry1.Package != "rusticai-core" {
		t.Errorf("Unexpected entry1 fields: %+v", entry1)
	}

	// 2. Resolve uvx command
	cmd1 := ResolveCommand(entry1, nil)
	if len(cmd1) != 8 || filepath.Base(cmd1[0]) != uvxExecutableName() || cmd1[1] != "--with" || cmd1[2] != "rusticai-forge" || cmd1[3] != "--with" || cmd1[4] != "rusticai-core" || cmd1[5] != "python" {
		t.Errorf("Unexpected uvx command resolution: %v", cmd1)
	}

	// 3. Lookup known binary class
	entry2, err := reg.Lookup("test.echo.EchoAgent")
	if err != nil {
		t.Fatalf("Expected to find entry2, got: %v", err)
	}
	if entry2.Runtime != RuntimeBinary || entry2.Executable != "python" {
		t.Errorf("Unexpected entry2 fields: %+v", entry2)
	}

	// 4. Resolve binary command
	cmd2 := ResolveCommand(entry2, nil)
	if len(cmd2) != 3 || cmd2[0] != "python" || cmd2[1] != "-m" || cmd2[2] != "test.echo" {
		t.Errorf("Unexpected binary command resolution: %v", cmd2)
	}

	// 5. Lookup unknown class
	_, err = reg.Lookup("nonexistent.Agent")
	if err == nil {
		t.Fatal("Expected error looking up unknown class, got nil")
	}
}

func TestResolveUVXCommand_PrefersBundledBinary(t *testing.T) {
	origExecLookPath := execLookPath
	origCurrentExecutablePath := currentExecutablePath
	defer func() {
		execLookPath = origExecLookPath
		currentExecutablePath = origCurrentExecutablePath
	}()

	tmpDir := t.TempDir()
	bundledForge := filepath.Join(tmpDir, "forge")
	bundledUVX := filepath.Join(tmpDir, uvxExecutableName())
	if err := os.WriteFile(bundledUVX, []byte(""), 0755); err != nil {
		t.Fatal(err)
	}

	execLookPath = func(file string) (string, error) {
		return "", exec.ErrNotFound
	}
	currentExecutablePath = func() (string, error) {
		return bundledForge, nil
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FORGE_UVX_PATH", filepath.Join(t.TempDir(), "missing-uvx"))

	if got := ResolveUVXCommand(); got != bundledUVX {
		t.Fatalf("expected bundled uvx path %q, got %q", bundledUVX, got)
	}
}

func TestResolveUVXCommand_UsesConfiguredFallback(t *testing.T) {
	origExecLookPath := execLookPath
	origCurrentExecutablePath := currentExecutablePath
	defer func() {
		execLookPath = origExecLookPath
		currentExecutablePath = origCurrentExecutablePath
	}()

	tmpDir := t.TempDir()
	configuredUVX := filepath.Join(tmpDir, "custom-uvx")
	if err := os.WriteFile(configuredUVX, []byte(""), 0755); err != nil {
		t.Fatal(err)
	}

	execLookPath = func(file string) (string, error) {
		return "", exec.ErrNotFound
	}
	currentExecutablePath = func() (string, error) {
		return filepath.Join(tmpDir, "forge"), nil
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FORGE_UVX_PATH", configuredUVX)

	if got := ResolveUVXCommand(); got != configuredUVX {
		t.Fatalf("expected configured uvx path %q, got %q", configuredUVX, got)
	}
}

func TestValidate_RemovesEntryWithUnknownOAuthProvider(t *testing.T) {
	yml := `entries:
  - id: AgentA
    class_name: "test.AgentA"
    runtime: "binary"
    executable: "python"
    oauth:
      - provider: "github"
  - id: AgentB
    class_name: "test.AgentB"
    runtime: "binary"
    executable: "python"
    oauth:
      - provider: "google"`

	tmp := t.TempDir()
	path := filepath.Join(tmp, "registry.yaml")
	if err := os.WriteFile(path, []byte(yml), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &oauth.ProvidersConfig{
		Providers: map[string]oauth.ProviderConfig{
			"github": {},
		},
	}
	mgr := oauth.NewManager(cfg)
	reg, err := Load(path, mgr)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if _, err := reg.Lookup("test.AgentA"); err != nil {
		t.Errorf("AgentA (valid provider) should remain in registry, got: %v", err)
	}
	if _, err := reg.Lookup("test.AgentB"); err == nil {
		t.Error("AgentB (unknown provider) should have been removed from registry")
	}
}

func TestValidate_NilManagerIsNoop(t *testing.T) {
	yml := `entries:
  - id: AgentA
    class_name: "test.AgentA"
    runtime: "binary"
    executable: "python"
    oauth:
      - provider: "github"`

	tmp := t.TempDir()
	path := filepath.Join(tmp, "registry.yaml")
	if err := os.WriteFile(path, []byte(yml), 0644); err != nil {
		t.Fatal(err)
	}

	reg, err := Load(path, nil)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if _, err := reg.Lookup("test.AgentA"); err != nil {
		t.Errorf("Load(nil) should be a no-op; AgentA should still be present: %v", err)
	}
}

func TestValidate_NoOAuthEntryUnaffected(t *testing.T) {
	yml := `entries:
  - id: AgentA
    class_name: "test.AgentA"
    runtime: "binary"
    executable: "python"`

	tmp := t.TempDir()
	path := filepath.Join(tmp, "registry.yaml")
	if err := os.WriteFile(path, []byte(yml), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &oauth.ProvidersConfig{}
	mgr := oauth.NewManager(cfg)
	reg, err := Load(path, mgr)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if _, err := reg.Lookup("test.AgentA"); err != nil {
		t.Errorf("Agent with no oauth needs should not be removed: %v", err)
	}
}

func TestGetUVReleaseURL(t *testing.T) {
	url, dir, err := getUVReleaseURL()
	if err != nil {
		// Only fail if we are on one of the supported archs
		t.Logf("getUVReleaseURL returned error (may be unsupported arch): %v", err)
	} else {
		if url == "" || dir == "" {
			t.Errorf("Expected non-empty URL and dir, got url=%q, dir=%q", url, dir)
		}
		t.Logf("URL: %s, Dir: %s", url, dir)
	}
}

// TestResolveCommand_ForgeExtraDeps covers the per-agent package requirements declared via
// AgentSpec.ForgeExtraDeps, including how they interact with the guild-wide
// FORGE_EXTRA_DEPS environment variable.
func TestResolveCommand_ForgeExtraDeps(t *testing.T) {
	entry := &AgentRegistryEntry{Runtime: RuntimeUVX, Package: "rusticai-core"}

	withArgs := func(cmd []string) []string {
		var deps []string
		for i := 0; i < len(cmd)-1; i++ {
			if cmd[i] == "--with" {
				deps = append(deps, cmd[i+1])
			}
		}
		return deps
	}
	equal := func(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}

	t.Run("nil is a no-op", func(t *testing.T) {
		t.Setenv("FORGE_EXTRA_DEPS", "")
		base := ResolveCommand(entry, nil)
		if got := ResolveCommand(entry, []string{}); !equal(base, got) {
			t.Errorf("empty slice changed the command: %v vs %v", base, got)
		}
		if want := []string{"rusticai-forge", "rusticai-core"}; !equal(withArgs(base), want) {
			t.Errorf("unexpected baseline deps: %v", withArgs(base))
		}
	})

	t.Run("spec deps are installed", func(t *testing.T) {
		t.Setenv("FORGE_EXTRA_DEPS", "")
		cmd := ResolveCommand(entry, []string{"rusticai-pandas-analyst"})
		want := []string{"rusticai-forge", "rusticai-pandas-analyst", "rusticai-core"}
		if got := withArgs(cmd); !equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("entries are trimmed and comma-separated values split", func(t *testing.T) {
		t.Setenv("FORGE_EXTRA_DEPS", "")
		cmd := ResolveCommand(entry, []string{"  rusticai-pandas-analyst  ", "a, b ,, c", ""})
		want := []string{"rusticai-forge", "rusticai-pandas-analyst", "a", "b", "c", "rusticai-core"}
		if got := withArgs(cmd); !equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("env and spec deps coexist", func(t *testing.T) {
		t.Setenv("FORGE_EXTRA_DEPS", "rusticai-nats")
		cmd := ResolveCommand(entry, []string{"rusticai-pandas-analyst"})
		want := []string{"rusticai-forge", "rusticai-nats", "rusticai-pandas-analyst", "rusticai-core"}
		if got := withArgs(cmd); !equal(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("ignored for non-uvx runtimes", func(t *testing.T) {
		binEntry := &AgentRegistryEntry{Runtime: RuntimeBinary, Executable: "python", Args: []string{"-m", "x"}}
		cmd := ResolveCommand(binEntry, []string{"rusticai-pandas-analyst"})
		if len(withArgs(cmd)) != 0 {
			t.Errorf("binary runtime should not receive --with args: %v", cmd)
		}
	})
}
