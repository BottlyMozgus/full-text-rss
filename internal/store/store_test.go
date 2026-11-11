package store

import (
	"path/filepath"
	"testing"
)

func TestMigrationVersion(t *testing.T) {
	if v, err := migrationVersion("002_feed_max_items.sql"); err != nil || v != 2 {
		t.Errorf("migrationVersion = %d, %v; want 2, nil", v, err)
	}
	for _, bad := range []string{"init.sql", "abc_init.sql", "000_zero.sql"} {
		if _, err := migrationVersion(bad); err == nil {
			t.Errorf("migrationVersion(%q) accepted", bad)
		}
	}
}

// Reopening a database must skip migrations that are already recorded.
func TestOpenRecordsSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v.db")
	for pass := 1; pass <= 2; pass++ {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("Open pass %d: %v", pass, err)
		}
		var version int
		if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
			t.Fatalf("pass %d user_version: %v", pass, err)
		}
		if version < 1 {
			t.Errorf("pass %d user_version = %d, want >= 1", pass, version)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("pass %d Close: %v", pass, err)
		}
	}
}
