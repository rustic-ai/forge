package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTemp writes content to a file named name inside a fresh temp dir and
// returns its path.
func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write temp file: %v", err)
	}
	return path
}

func TestLoadGuild_DirectJSONSpec(t *testing.T) {
	path := writeTemp(t, "guild.json", `{"name":"Echo Guild","description":"d","agents":[]}`)

	spec, err := (&GuildRuntime{}).LoadGuild(path)
	if err != nil {
		t.Fatalf("LoadGuild returned error: %v", err)
	}
	if spec.Name != "Echo Guild" {
		t.Fatalf("expected name %q, got %q", "Echo Guild", spec.Name)
	}
}

func TestLoadGuild_BlueprintWrapper(t *testing.T) {
	// A blueprint wrapper nests the guild under a "spec" field; LoadGuild must
	// unwrap it and return the inner spec, not the wrapper.
	path := writeTemp(t, "blueprint.json",
		`{"name":"Wrapper","exposure":"public","spec":{"name":"Inner Guild","description":"d","agents":[]}}`)

	spec, err := (&GuildRuntime{}).LoadGuild(path)
	if err != nil {
		t.Fatalf("LoadGuild returned error: %v", err)
	}
	if spec.Name != "Inner Guild" {
		t.Fatalf("expected unwrapped name %q, got %q", "Inner Guild", spec.Name)
	}
}

func TestLoadGuild_YAMLSpecFallsThrough(t *testing.T) {
	// YAML is not valid JSON, so the blueprint sniff fails and LoadGuild must
	// fall through to the extension-based parser.
	path := writeTemp(t, "guild.yaml", "name: YAML Guild\ndescription: d\nagents: []\n")

	spec, err := (&GuildRuntime{}).LoadGuild(path)
	if err != nil {
		t.Fatalf("LoadGuild returned error: %v", err)
	}
	if spec.Name != "YAML Guild" {
		t.Fatalf("expected name %q, got %q", "YAML Guild", spec.Name)
	}
}

func TestLoadGuild_MissingFile(t *testing.T) {
	_, err := (&GuildRuntime{}).LoadGuild(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err == nil {
		t.Fatal("expected an error for a missing file, got nil")
	}
}

func TestLoadGuild_UnsupportedExtension(t *testing.T) {
	// Not a blueprint (no "spec") and an extension ParseFile rejects.
	path := writeTemp(t, "guild.txt", "not a guild")
	_, err := (&GuildRuntime{}).LoadGuild(path)
	if err == nil {
		t.Fatal("expected an error for an unsupported extension, got nil")
	}
}
