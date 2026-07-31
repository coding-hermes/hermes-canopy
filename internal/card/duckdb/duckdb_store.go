// Package duckdb implements the CardRepository interface backed by an
// in-process DuckDB database. This provides an alternative to the SQLite
// backend with stronger analytical capabilities.
package duckdb

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "github.com/marcboeker/go-duckdb" // register duckdb database/sql driver
)

// Store manages a single in-process DuckDB database for card storage.
// It handles connection lifecycle, schema migration, and provides
// a *sql.DB for repository operations.
type Store struct {
	mu sync.Mutex
	db *sql.DB
}

// NewStore opens or creates a DuckDB database at the given path.
// Pass an empty string for an in-memory database (useful for testing).
func NewStore(dbPath string) (*Store, error) {
	dsn := dbPath
	if dbPath != "" {
		// Ensure the parent directory exists for file-backed databases.
		dir := filepath.Dir(dbPath)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("duckdb: mkdir %s: %w", dir, err)
		}
	}

	db, err := sql.Open("duckdb", dsn)
	if err != nil {
		return nil, fmt.Errorf("duckdb: open: %w", err)
	}

	store := &Store{db: db}

	if err := store.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("duckdb: migrate: %w", err)
	}

	return store, nil
}

// DB returns the underlying *sql.DB for use by repositories.
func (s *Store) DB() *sql.DB {
	return s.db
}

// Close shuts down the DuckDB database cleanly.
func (s *Store) Close() error {
	return s.db.Close()
}

// migrate creates the cards and events tables, sequences, and indexes
// using IF NOT EXISTS so it is safe to call on every startup.
func (s *Store) migrate() error {
	migration := `
		CREATE SEQUENCE IF NOT EXISTS events_seq START 1;

		CREATE TABLE IF NOT EXISTS cards (
			id              TEXT PRIMARY KEY NOT NULL,
			tree_id         TEXT NOT NULL DEFAULT '',
			node_id         TEXT NOT NULL DEFAULT '',
			app_id          TEXT NOT NULL,
			card_type       TEXT NOT NULL,
			data            TEXT NOT NULL DEFAULT '{}',
			actions         TEXT NOT NULL DEFAULT '[]',
			status          TEXT NOT NULL DEFAULT 'active',
			context_hash    TEXT NOT NULL,
			revision        INTEGER NOT NULL DEFAULT 1,
			created_at      TIMESTAMP NOT NULL,
			updated_at      TIMESTAMP NOT NULL,
			dismissed_at    TIMESTAMP,
			archived_at     TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS events (
			sequence         BIGINT PRIMARY KEY DEFAULT nextval('events_seq'),
			event_id         TEXT NOT NULL UNIQUE,
			card_id          TEXT NOT NULL,
			event_type       TEXT NOT NULL,
			actor_kind       TEXT NOT NULL,
			actor_id         TEXT NOT NULL,
			payload          TEXT NOT NULL DEFAULT '{}',
			created_at       TIMESTAMP NOT NULL
		);

		CREATE INDEX IF NOT EXISTS idx_cards_status_created ON cards(status, created_at);
		CREATE INDEX IF NOT EXISTS idx_cards_app_status     ON cards(app_id, status);
		CREATE INDEX IF NOT EXISTS idx_cards_context_hash   ON cards(context_hash);
		CREATE INDEX IF NOT EXISTS idx_events_card_sequence ON events(card_id, sequence);
		CREATE INDEX IF NOT EXISTS idx_events_type_created  ON events(event_type, created_at);
	`

	_, err := s.db.Exec(migration)
	return err
}
