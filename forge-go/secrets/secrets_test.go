package secrets

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestEnvSecretProvider(t *testing.T) {
	p := NewEnvSecretProvider()
	ctx := context.Background()

	_ = os.Setenv("TEST_SECRET_ENV", "env_value")
	defer func() { _ = os.Unsetenv("TEST_SECRET_ENV") }()

	val, err := p.Resolve(ctx, "TEST_SECRET_ENV")
	if err != nil {
		t.Fatalf("Expected to find env secret but got error: %v", err)
	}
	if val != "env_value" {
		t.Errorf("Expected env_value, got %s", val)
	}

	_, err = p.Resolve(ctx, "NONEXISTENT_ENV")
	if err != ErrSecretNotFound {
		t.Errorf("Expected ErrSecretNotFound, got %v", err)
	}
}

func TestFileSecretProvider(t *testing.T) {
	tmpDir := t.TempDir()
	p := NewFileSecretProvider(tmpDir)
	ctx := context.Background()

	secretPath := filepath.Join(tmpDir, "API_KEY")
	if err := os.WriteFile(secretPath, []byte("  file_value_with_spaces  \n"), 0600); err != nil {
		t.Fatalf("Failed to write mock secret file: %v", err)
	}

	val, err := p.Resolve(ctx, "API_KEY")
	if err != nil {
		t.Fatalf("Expected to find file secret but got error: %v", err)
	}
	if val != "file_value_with_spaces" {
		t.Errorf("Expected trimmed 'file_value_with_spaces', got '%s'", val)
	}

	_, err = p.Resolve(ctx, "UNKNOWN_FILE")
	if err != ErrSecretNotFound {
		t.Errorf("Expected ErrSecretNotFound, got %v", err)
	}
}

func TestChainSecretProvider(t *testing.T) {
	tmpDir := t.TempDir()

	// Create file secret
	secretPath := filepath.Join(tmpDir, "SHARED_KEY")
	if err := os.WriteFile(secretPath, []byte("file_wins"), 0600); err != nil {
		t.Fatalf("Failed to write SHARED_KEY secret file: %v", err)
	}

	envP := NewEnvSecretProvider()
	fileP := NewFileSecretProvider(tmpDir)

	// Chain 1: Env -> File
	chain1 := NewChainSecretProvider(envP, fileP)

	ctx := context.Background()

	val, err := chain1.Resolve(ctx, "SHARED_KEY")
	if err != nil {
		t.Fatalf("chain1 failed to resolve SHARED_KEY: %v", err)
	}
	if val != "file_wins" {
		t.Errorf("Expected file_wins, got %s", val)
	}

	// Now set ENV var, which should take precedence
	_ = os.Setenv("SHARED_KEY", "env_wins")
	defer func() { _ = os.Unsetenv("SHARED_KEY") }()

	val2, err := chain1.Resolve(ctx, "SHARED_KEY")
	if err != nil {
		t.Fatalf("chain1 failed to resolve SHARED_KEY again: %v", err)
	}
	if val2 != "env_wins" {
		t.Errorf("Expected env_wins, got %s", val2)
	}

	// Unknown key
	_, err = chain1.Resolve(ctx, "NONEXISTENT")
	if err != ErrSecretNotFound {
		t.Errorf("Expected ErrSecretNotFound, got %v", err)
	}
}

func TestDotEnvSecretProvider(t *testing.T) {
	tmpDir := t.TempDir()
	dotenvPath := filepath.Join(tmpDir, ".env")
	content := "# comment\nAPI_KEY=from_dotenv\nexport OTHER_KEY = other_value\nQUOTED=\"quoted value\"\n"
	if err := os.WriteFile(dotenvPath, []byte(content), 0600); err != nil {
		t.Fatalf("Failed to write dotenv file: %v", err)
	}

	p := NewDotEnvSecretProvider(dotenvPath)
	ctx := context.Background()

	val, err := p.Resolve(ctx, "API_KEY")
	if err != nil {
		t.Fatalf("Expected to resolve API_KEY from dotenv: %v", err)
	}
	if val != "from_dotenv" {
		t.Errorf("Expected from_dotenv, got %s", val)
	}

	val, err = p.Resolve(ctx, "OTHER_KEY")
	if err != nil {
		t.Fatalf("Expected to resolve OTHER_KEY from dotenv: %v", err)
	}
	if val != "other_value" {
		t.Errorf("Expected other_value, got %s", val)
	}

	val, err = p.Resolve(ctx, "QUOTED")
	if err != nil {
		t.Fatalf("Expected to resolve QUOTED from dotenv: %v", err)
	}
	if val != "quoted value" {
		t.Errorf("Expected quoted value, got %s", val)
	}

	_, err = p.Resolve(ctx, "MISSING")
	if err != ErrSecretNotFound {
		t.Errorf("Expected ErrSecretNotFound, got %v", err)
	}
}

func TestDefaultProvider_PreferenceOrder(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	secretsDir := filepath.Join(homeDir, ".forge", "secrets")
	if err := os.MkdirAll(secretsDir, 0o755); err != nil {
		t.Fatalf("Failed to create secrets dir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(secretsDir, ".env"), []byte("SHARED_KEY=dotenv_value\n"), 0o600); err != nil {
		t.Fatalf("Failed to write dotenv file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(secretsDir, "SHARED_KEY"), []byte("file_value\n"), 0o600); err != nil {
		t.Fatalf("Failed to write file secret: %v", err)
	}

	ctx := context.Background()
	provider := DefaultProvider()

	val, err := provider.Resolve(ctx, "SHARED_KEY")
	if err != nil {
		t.Fatalf("Expected to resolve SHARED_KEY from default provider: %v", err)
	}
	if val != "dotenv_value" {
		t.Fatalf("Expected dotenv_value before file fallback, got %s", val)
	}

	_ = os.Setenv("SHARED_KEY", "env_value")
	defer func() { _ = os.Unsetenv("SHARED_KEY") }()

	val, err = provider.Resolve(ctx, "SHARED_KEY")
	if err != nil {
		t.Fatalf("Expected to resolve SHARED_KEY from env: %v", err)
	}
	if val != "env_value" {
		t.Fatalf("Expected env_value to override dotenv and file, got %s", val)
	}
}
