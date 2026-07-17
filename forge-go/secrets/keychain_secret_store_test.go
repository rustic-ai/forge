package secrets

import (
	"testing"

	"github.com/zalando/go-keyring"
)

func newTestKeychainStore(t *testing.T) *KeychainSecretStore {
	t.Helper()
	keyring.MockInit() // in-memory keychain for tests
	return NewKeychainSecretStoreWithService("forge-test")
}

func TestKeychainSecretStore_CRUDAndList(t *testing.T) {
	s := newTestKeychainStore(t)

	if err := s.Save("org1", "A", "va"); err != nil {
		t.Fatalf("Save A: %v", err)
	}
	if err := s.Save("org1", "B", "vb"); err != nil {
		t.Fatalf("Save B: %v", err)
	}
	if !s.Exists("org1", "A") {
		t.Fatal("expected A to exist")
	}
	if s.Exists("org1", "MISSING") {
		t.Fatal("did not expect MISSING to exist")
	}

	names, err := s.List("org1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(names) != 2 || names[0] != "A" || names[1] != "B" {
		t.Fatalf("expected sorted [A B], got %+v", names)
	}

	if !s.Delete("org1", "A") {
		t.Fatal("expected Delete A to report true")
	}
	names, _ = s.List("org1")
	if len(names) != 1 || names[0] != "B" {
		t.Fatalf("expected [B] after delete, got %+v", names)
	}
	if s.Exists("org1", "A") {
		t.Fatal("A should be gone from the keychain after delete")
	}
}

func TestKeychainSecretStore_OrgIsolation(t *testing.T) {
	s := newTestKeychainStore(t)

	s.Save("org1", "K", "one") //nolint:errcheck
	s.Save("org2", "K", "two") //nolint:errcheck

	n1, _ := s.List("org1")
	n2, _ := s.List("org2")
	if len(n1) != 1 || len(n2) != 1 {
		t.Fatalf("expected one secret per org, got org1=%v org2=%v", n1, n2)
	}
}
