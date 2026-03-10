package store

import (
	"path/filepath"
	"testing"
)

func TestNewGormStore_ConfiguresFileSQLiteForLocalConcurrency(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "forge.db")

	s, err := NewGormStore(DriverSQLite, dbPath)
	if err != nil {
		t.Fatalf("create sqlite store: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
	})

	gs, ok := s.(*gormStore)
	if !ok {
		t.Fatalf("unexpected store type %T", s)
	}

	sqlDB, err := gs.db.DB()
	if err != nil {
		t.Fatalf("get sql db: %v", err)
	}
	if got := sqlDB.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("max open conns = %d, want 1", got)
	}

	var busyTimeout int
	if err := sqlDB.QueryRow("PRAGMA busy_timeout;").Scan(&busyTimeout); err != nil {
		t.Fatalf("read busy_timeout pragma: %v", err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("busy_timeout = %d, want 5000", busyTimeout)
	}

	var journalMode string
	if err := sqlDB.QueryRow("PRAGMA journal_mode;").Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode pragma: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode = %q, want %q", journalMode, "wal")
	}
}
