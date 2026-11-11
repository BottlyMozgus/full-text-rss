package store

import (
	"database/sql"
	"embed"
	"fmt"
	"strconv"
	"strings"

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

// migrate applies the embedded migrations in filename order. The number of
// the last applied file is kept in PRAGMA user_version so each one runs
// once. Databases created before versioning report 0 and replay 001, which
// is idempotent (CREATE ... IF NOT EXISTS).
func (s *Store) migrate() error {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	var current int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		version, err := migrationVersion(e.Name())
		if err != nil {
			return err
		}
		if version <= current {
			continue
		}
		sqlBytes, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return err
		}
		if err := s.applyMigration(version, string(sqlBytes)); err != nil {
			return fmt.Errorf("migration %s: %w", e.Name(), err)
		}
		current = version
	}
	return nil
}

// applyMigration runs one migration and records its version atomically.
func (s *Store) applyMigration(version int, stmts string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(stmts); err != nil {
		return err
	}
	// PRAGMA takes no bound parameters; version is an int parsed from the
	// embedded filename.
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, version)); err != nil {
		return err
	}
	return tx.Commit()
}

// migrationVersion parses the numeric prefix of "NNN_name.sql".
func migrationVersion(name string) (int, error) {
	prefix, _, ok := strings.Cut(name, "_")
	if !ok {
		return 0, fmt.Errorf("migration %s: want NNN_name.sql", name)
	}
	v, err := strconv.Atoi(prefix)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("migration %s: want NNN_name.sql", name)
	}
	return v, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }
