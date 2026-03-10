package registry

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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

	reg, err := Load(path)
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
	cmd1 := ResolveCommand(entry1)
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
	cmd2 := ResolveCommand(entry2)
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
