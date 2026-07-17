package secrets

import (
	"errors"
	"testing"
)

func TestManager_SetAndList(t *testing.T) {
	m := NewManager(NewInMemorySecretStore())

	if err := m.Set("org1", "API_KEY", "s3cr3t"); err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	if !m.Exists("org1", "API_KEY") {
		t.Fatal("expected secret to exist after Set")
	}

	names, err := m.List("org1")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(names) != 1 || names[0] != "API_KEY" {
		t.Fatalf("unexpected list: %+v", names)
	}
}

func TestManager_SetDuplicateReturnsExists(t *testing.T) {
	m := NewManager(NewInMemorySecretStore())
	if err := m.Set("org1", "TOKEN", "a"); err != nil {
		t.Fatalf("first Set failed: %v", err)
	}
	if err := m.Set("org1", "TOKEN", "b"); !errors.Is(err, ErrSecretExists) {
		t.Fatalf("expected ErrSecretExists, got %v", err)
	}
}

func TestManager_UpdateMissingReturnsNotFound(t *testing.T) {
	m := NewManager(NewInMemorySecretStore())
	if err := m.Update("org1", "NOPE", "x"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("expected ErrSecretNotFound, got %v", err)
	}
}

func TestManager_UpdateExisting(t *testing.T) {
	m := NewManager(NewInMemorySecretStore())
	if err := m.Set("org1", "K", "v1"); err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	if err := m.Update("org1", "K", "v2"); err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if !m.Exists("org1", "K") {
		t.Fatal("expected secret to still exist after Update")
	}
}

func TestManager_Delete(t *testing.T) {
	m := NewManager(NewInMemorySecretStore())
	if err := m.Set("org1", "K", "v"); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	if !m.Delete("org1", "K") {
		t.Fatal("expected Delete to report true")
	}
	if m.Delete("org1", "K") {
		t.Fatal("expected second Delete to report false")
	}
	if m.Exists("org1", "K") {
		t.Fatal("expected secret to be gone after delete")
	}
	names, _ := m.List("org1")
	if len(names) != 0 {
		t.Fatalf("expected empty list after delete, got %+v", names)
	}
}

func TestManager_OrgIsolation(t *testing.T) {
	m := NewManager(NewInMemorySecretStore())
	m.Set("org1", "K", "one") //nolint:errcheck
	m.Set("org2", "K", "two") //nolint:errcheck

	if !m.Exists("org1", "K") || !m.Exists("org2", "K") {
		t.Fatal("both orgs should have their own secret")
	}
	names, _ := m.List("org1")
	if len(names) != 1 || names[0] != "K" {
		t.Fatalf("expected org1 to see only its own secret, got %+v", names)
	}
}

func TestManager_ListSorted(t *testing.T) {
	m := NewManager(NewInMemorySecretStore())
	m.Set("org1", "B", "1") //nolint:errcheck
	m.Set("org1", "A", "2") //nolint:errcheck
	m.Set("org1", "C", "3") //nolint:errcheck

	names, _ := m.List("org1")
	if len(names) != 3 || names[0] != "A" || names[1] != "B" || names[2] != "C" {
		t.Fatalf("expected sorted [A B C], got %+v", names)
	}
}

func TestParseSecretKey(t *testing.T) {
	orgID, name, ok := ParseSecretKey(SecretStoreKey("org1", "MY_KEY"))
	if !ok || orgID != "org1" || name != "MY_KEY" {
		t.Fatalf("round-trip failed: org=%q name=%q ok=%v", orgID, name, ok)
	}
	if _, _, ok := ParseSecretKey("oauth:org1|github"); ok {
		t.Fatal("oauth key must not parse as a secret key")
	}
	if _, _, ok := ParseSecretKey("plain-key"); ok {
		t.Fatal("plain key must not parse as a secret key")
	}
}
