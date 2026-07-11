package command

import (
	"os"
	"path/filepath"
	"testing"
)

const forgeGoModule = "module github.com/rustic-ai/forge/forge-go\n\ngo 1.25.0\n"

func mkGoMod(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(content), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
}

func TestFindForgeRoot_WalksUpToModule(t *testing.T) {
	root := t.TempDir()
	mkGoMod(t, root, forgeGoModule)
	nested := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	if got := findForgeRoot(nested); got != root {
		t.Fatalf("expected forge root %q, got %q", root, got)
	}
}

func TestFindForgeRoot_FindsSubdirFromRepoRoot(t *testing.T) {
	base := t.TempDir()
	forgeGo := filepath.Join(base, "forge-go")
	mkGoMod(t, forgeGo, forgeGoModule)

	if got := findForgeRoot(base); got != forgeGo {
		t.Fatalf("expected forge-go subdir %q, got %q", forgeGo, got)
	}
}

func TestFindForgeRoot_IgnoresUnrelatedModule(t *testing.T) {
	root := t.TempDir()
	// A go.mod that is not the forge-go module must not be treated as the root.
	mkGoMod(t, root, "module example.com/other\n\ngo 1.25.0\n")

	if got := findForgeRoot(root); got != "" {
		t.Fatalf("expected empty result for unrelated module, got %q", got)
	}
}

func TestFindForgeRoot_NotFound(t *testing.T) {
	if got := findForgeRoot(t.TempDir()); got != "" {
		t.Fatalf("expected empty result when no go.mod exists, got %q", got)
	}
}
