package store

import "testing"

func TestResolveDriverAndDSN(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantDriver string
		wantDSN    string
	}{
		{
			name:       "postgres uri",
			input:      "postgres://admin:pw@localhost:5432/rustic?sslmode=disable",
			wantDriver: DriverPostgres,
			wantDSN:    "postgres://admin:pw@localhost:5432/rustic?sslmode=disable",
		},
		{
			name:       "postgresql uri",
			input:      "postgresql://admin:pw@localhost:5432/rustic?sslmode=disable",
			wantDriver: DriverPostgres,
			wantDSN:    "postgres://admin:pw@localhost:5432/rustic?sslmode=disable",
		},
		{
			name:       "sqlalchemy psycopg uri",
			input:      "postgresql+psycopg://admin:pw@localhost:5432/rustic?sslmode=disable",
			wantDriver: DriverPostgres,
			wantDSN:    "postgres://admin:pw@localhost:5432/rustic?sslmode=disable",
		},
		{
			name:       "sqlite uri",
			input:      "sqlite:///tmp/forge.db",
			wantDriver: DriverSQLite,
			wantDSN:    "/tmp/forge.db",
		},
		{
			name:       "sqlite memory file",
			input:      "file::memory:",
			wantDriver: DriverSQLite,
			wantDSN:    "file::memory:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver, dsn := ResolveDriverAndDSN(tt.input)
			if driver != tt.wantDriver {
				t.Fatalf("driver mismatch: got %q want %q", driver, tt.wantDriver)
			}
			if dsn != tt.wantDSN {
				t.Fatalf("dsn mismatch: got %q want %q", dsn, tt.wantDSN)
			}
		})
	}
}
