package store

import (
	"database/sql"
	"embed"
	"fmt"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, no cgo
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path and applies migrations.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// modernc sqlite benefits from limited connections
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		sqlBytes, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return err
		}
		if _, err := s.db.Exec(string(sqlBytes)); err != nil {
			return fmt.Errorf("migration %s: %w", e.Name(), err)
		}
	}
	return nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }
