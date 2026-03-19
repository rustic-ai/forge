package embed

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rustic-ai/forge/forge-go/guild/store"
)

func TestStartSQLite(t *testing.T) {
	// Create a temporary directory for the database
	dataDir, err := os.MkdirTemp("", "forge-sqlite-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(dataDir) }()

	s, err := StartSQLite(dataDir)
	if err != nil {
		t.Fatalf("StartSQLite failed: %v", err)
	}
	defer func() { _ = s.Close() }()

	// Verify that the database file was created
	dbPath := filepath.Join(dataDir, "forge.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Errorf("Expected database file to exist at %s", dbPath)
	}

	// Verify basic database operations work to prove initialization was successful
	newGuild := &store.GuildModel{
		ID:             "test-guild-123",
		Name:           "Test Guild",
		OrganizationID: "org-1",
		Status:         store.GuildStatusRequested,
	}

	if err := s.CreateGuild(newGuild); err != nil {
		t.Fatalf("Failed to create guild in embedded store: %v", err)
	}

	retrieved, err := s.GetGuild("test-guild-123")
	if err != nil {
		t.Fatalf("Failed to retrieve guild: %v", err)
	}

	if retrieved.Name != "Test Guild" {
		t.Errorf("Expected guild name 'Test Guild', got '%s'", retrieved.Name)
	}
}
