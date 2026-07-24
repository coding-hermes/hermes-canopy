package card

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite" // CGo-free SQLite driver
)

// DataDir returns the path for Canopy card storage under the user's Hermes directory.
func DataDir() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "/tmp"
	}
	return filepath.Join(home, ".hermes", "canopy", "cards")
}

// CardDBManager manages per-card-type SQLite databases.
// Each card type gets its own database file under DataDir().
type CardDBManager struct {
	dir   string
	mu    sync.Mutex
	dbs   map[CardType]*sql.DB
	repos map[CardType]CardRepository
}

// NewCardDBManager creates a new CardDBManager that will store databases
// under the given directory (default: DataDir()).
func NewCardDBManager(dir string) *CardDBManager {
	return &CardDBManager{
		dir:   dir,
		dbs:   make(map[CardType]*sql.DB),
		repos: make(map[CardType]CardRepository),
	}
}

// Repository returns (or creates) a CardRepository for the given card type.
func (m *CardDBManager) Repository(ctype CardType) (CardRepository, error) {
	if !IsValidCardType(ctype) {
		return nil, fmt.Errorf("card: invalid card type %q", ctype)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if repo, ok := m.repos[ctype]; ok {
		return repo, nil
	}

	db, err := m.openDB(ctype)
	if err != nil {
		return nil, err
	}

	repo := NewSQLiteCardRepo(db)
	m.dbs[ctype] = db
	m.repos[ctype] = repo
	return repo, nil
}

// openDB creates the data directory, opens/creates the SQLite database,
// sets pragmas, and runs the migration for the given card type.
func (m *CardDBManager) openDB(ctype CardType) (*sql.DB, error) {
	if err := os.MkdirAll(m.dir, 0700); err != nil {
		return nil, fmt.Errorf("card: mkdir %s: %w", m.dir, err)
	}

	dbPath := filepath.Join(m.dir, string(ctype)+".db")

	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_synchronous=NORMAL&_foreign_keys=1&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("card: open %s: %w", dbPath, err)
	}

	// Run pragmas explicitly per SPEC-PL-03 §3.1
	pragmas := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA temp_store = MEMORY",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("card: pragma %q: %w", p, err)
		}
	}

	if err := migrate(db, ctype); err != nil {
		db.Close()
		return nil, fmt.Errorf("card: migrate %s: %w", ctype, err)
	}

	return db, nil
}

// Close closes all open databases.
func (m *CardDBManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var firstErr error
	for ctype, db := range m.dbs {
		if err := db.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("card: close %s: %w", ctype, err)
		}
	}
	m.dbs = make(map[CardType]*sql.DB)
	m.repos = make(map[CardType]CardRepository)
	return firstErr
}

// migrate creates the cards and events tables if they don't exist,
// along with indexes and triggers.
func migrate(db *sql.DB, ctype CardType) error {
	migration := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS cards (
			id              TEXT PRIMARY KEY NOT NULL,
			tree_id         TEXT NOT NULL DEFAULT '',
			node_id         TEXT NOT NULL DEFAULT '',
			app_id          TEXT NOT NULL,
			card_type       TEXT NOT NULL DEFAULT '%s',
			data            TEXT NOT NULL DEFAULT '{}',
			actions         TEXT NOT NULL DEFAULT '[]',
			status          TEXT NOT NULL DEFAULT 'active',
			context_hash    TEXT NOT NULL,
			revision        INTEGER NOT NULL DEFAULT 1,
			created_at      TEXT NOT NULL,
			updated_at      TEXT NOT NULL,
			dismissed_at    TEXT,
			archived_at     TEXT
		);

		CREATE TABLE IF NOT EXISTS events (
			sequence         INTEGER PRIMARY KEY AUTOINCREMENT,
			event_id         TEXT NOT NULL UNIQUE,
			card_id          TEXT NOT NULL,
			event_type       TEXT NOT NULL,
			actor_kind       TEXT NOT NULL,
			actor_id         TEXT NOT NULL,
			payload          TEXT NOT NULL DEFAULT '{}',
			created_at       TEXT NOT NULL,
			FOREIGN KEY (card_id) REFERENCES cards(id) ON DELETE RESTRICT
		);

		CREATE INDEX IF NOT EXISTS idx_cards_status_created ON cards(status, created_at DESC);
		CREATE INDEX IF NOT EXISTS idx_cards_app_status ON cards(app_id, status, updated_at DESC);
		CREATE INDEX IF NOT EXISTS idx_cards_context_hash ON cards(context_hash);
		CREATE INDEX IF NOT EXISTS idx_events_card_sequence ON events(card_id, sequence ASC);
		CREATE INDEX IF NOT EXISTS idx_events_type_created ON events(event_type, created_at DESC);
	`, string(ctype))

	_, err := db.Exec(migration)
	return err
}
