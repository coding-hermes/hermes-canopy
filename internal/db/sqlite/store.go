// Package sqlite is the SQLite core-graph store for Canopy.
//
// Board row GAP-076, wave 2, slice 1. docs/SQLITE-PIVOT.md rules that SQLite
// (modernc.org/sqlite, pure Go, WAL) becomes the authoritative graph store; wave 1 landed
// the translated DDL (migrations/sqlite/000001..000010), migrations.SQLiteFS() and the
// parity harness. This package is the FOUNDATION of wave 2 and nothing more: it opens a
// SQLite core-graph store, applies the wave-1 SQLite migrations, and owns the four
// obligations that wave-1 DDL could not express in SQLite (docs/SQLITE-PIVOT.md §5):
//
//  1. id generation          — repo layer generates RFC 9562 UUIDv7 on insert (NewID)
//  2. node sequence          — `set_node_sequence`/`trg_node_sequence` become
//     NextNodeSequence, computed inside the caller's write transaction
//  3. content hashing        — `set_content_hash`/`trg_node_content_hash` become
//     ContentHash (SHA-256 hex over UTF-8, matching PG's
//     encode(sha256(convert_to(content,'UTF8')),'hex'))
//  4. timestamp representation — D1: TEXT RFC3339 UTC with millisecond precision and a
//     `Z` suffix, encode/decode via EncodeTime/DecodeTime
//
// # Scope
//
// IN scope (this slice): opening the store with the D6 pragma set, applying the core
// schema idempotently, and the four Go obligations — all additive, no existing behavior
// changes.
//
// OUT of scope (later waves, deliberately untouched): the repo-layer swap (the 17
// internal/db/*_repo.go files and the ~75 pgx importers repo-wide — wave 2 proper); the
// boot path (cmd/canopyd/main.go still gates on PostgreSQL — wave 3); retiring PostgreSQL
// (wave 4). internal/db/db.go (the pgx pool + golang-migrate database/postgres path) is
// untouched by this package and nothing here is wired into the running server yet.
//
// # Pragmas (docs/SQLITE-PIVOT.md §6, D6)
//
// Open applies journal_mode=WAL, foreign_keys=ON, busy_timeout=5000 and
// synchronous=NORMAL. The four pragmas are supplied as DSN parameters, so the DRIVER
// applies them to every connection it opens — `foreign_keys` and `busy_timeout` are
// per-connection settings, and a one-shot db.Exec("PRAGMA foreign_keys=ON") would only
// configure whichever pooled connection happened to serve that statement.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite" // CGo-free SQLite driver; registers the "sqlite" driver name

	"github.com/coding-hermes/hermes-canopy/migrations"
)

// DriverName is the database/sql driver name registered by modernc.org/sqlite.
const DriverName = "sqlite"

// corePragmas is the D6 connection-level pragma set (docs/SQLITE-PIVOT.md §6, option (a)).
// Each entry is a `_pragma` DSN value, i.e. the argument of `PRAGMA <value>`, so the
// driver executes it on every new connection.
//
//   - journal_mode=WAL      — concurrent readers with a single writer
//   - foreign_keys=ON       — without it the 40+ surviving FK clauses are documentation
//   - busy_timeout=5000     — a contending writer waits 5s instead of failing on SQLITE_BUSY
//   - synchronous=NORMAL    — the safe WAL companion (FULL is not needed with WAL)
var corePragmas = []string{
	"journal_mode(WAL)",
	"foreign_keys(ON)",
	"busy_timeout(5000)",
	"synchronous(NORMAL)",
}

// schemaMigrationsTable records which migration files this store has applied. It is
// store-owned bookkeeping, named `canopy_schema_migrations` for two reasons: SQLite
// reserves the `sqlite_` prefix for internal objects (so `sqlite_schema_migrations` is
// rejected with "object name reserved for internal use"), and golang-migrate uses the bare
// name `schema_migrations` for its own (version, dirty) ledger, which wave 2 still runs the
// PostgreSQL store through. Sharing that name would make either tool misread the other.
const schemaMigrationsTable = "canopy_schema_migrations"

const createSchemaMigrationsTable = `CREATE TABLE IF NOT EXISTS ` + schemaMigrationsTable + ` (
    name       TEXT NOT NULL PRIMARY KEY,
    applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
)`

// migrationNameRe matches the canonical SQLite migration file name
// (`NNNNNN_<name>.up.sql`), the same shape the wave-1 parity harness validates.
var migrationNameRe = regexp.MustCompile(`^(\d{6})_([a-z0-9_]+)\.up\.sql$`)

// Store is a handle on one SQLite core-graph database file.
//
// Concurrency contract: the pool is left at database/sql's defaults. WAL allows any number
// of concurrent readers alongside a single writer, and busy_timeout=5000 makes a
// contending writer wait rather than fail immediately. Sequence allocation
// (NextNodeSequence) MUST run inside the same transaction as the INSERT it feeds — see its
// documentation.
type Store struct {
	db   *sql.DB
	path string
}

// Open opens (creating if necessary) the SQLite store at path with the D6 pragma set
// applied at connection level, and verifies that a connection can actually be established
// and configured.
//
// The parent directory is created if it does not exist (0700, matching the card store's
// convention), so a caller can pass a path under a not-yet-existing data directory.
//
// Open does not apply the schema; call ApplyCoreSchema. Nothing is written to the file
// beyond what SQLite itself does on first connect (page 1 plus the WAL side files).
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("sqlite: Open: empty database path")
	}
	// The driver splits the DSN at the first '?'; a path containing one would be
	// silently truncated instead of failing, so reject it here.
	if strings.Contains(path, "?") {
		return nil, fmt.Errorf("sqlite: Open: database path %q must not contain '?'", path)
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("sqlite: Open: mkdir %s: %w", dir, err)
		}
	}

	db, err := sql.Open(DriverName, dsn(path))
	if err != nil {
		return nil, fmt.Errorf("sqlite: Open %s: %w", path, err)
	}
	// sql.Open is lazy: nothing has touched the file or the pragmas yet. Ping forces a
	// real connection, so an unusable path or a rejected pragma fails here rather than at
	// the first query.
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: Open %s: %w", path, err)
	}
	return &Store{db: db, path: path}, nil
}

// dsn builds the DSN for path: the plain filesystem path plus the D6 pragma set as
// repeated `_pragma` query parameters. The path is passed through verbatim (the driver
// only rewrites the DSN when it starts with `file:`), so no percent-encoding is involved.
func dsn(path string) string {
	q := url.Values{}
	for _, p := range corePragmas {
		q.Add("_pragma", p)
	}
	return path + "?" + q.Encode()
}

// DB returns the underlying pool. Callers use it for queries; they must not reconfigure
// pragmas through it — every connection the driver opens already carries the D6 set, and a
// one-shot db.Exec would only affect one pooled connection.
func (s *Store) DB() *sql.DB { return s.db }

// Path returns the filesystem path this store was opened at.
func (s *Store) Path() string { return s.path }

// Close closes the pool. It is safe to call once; the caller owns the lifetime.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// ApplyCoreSchema applies every `NNNNNN_*.up.sql` in migrations.SQLiteFS() — the wave-1
// SQLite translation of PostgreSQL migrations 000001..000010 — in numeric order.
//
// It is idempotent: applied file names are recorded in sqlite_schema_migrations, already
// applied files are skipped, and each file is applied in its own transaction (SQLite DDL
// is transactional), so a failing migration leaves neither partial DDL nor a ledger row
// behind.
func (s *Store) ApplyCoreSchema(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, createSchemaMigrationsTable); err != nil {
		return fmt.Errorf("sqlite: ApplyCoreSchema: create %s: %w", schemaMigrationsTable, err)
	}

	already, err := s.AppliedMigrations(ctx)
	if err != nil {
		return err
	}
	done := make(map[string]bool, len(already))
	for _, name := range already {
		done[name] = true
	}

	files, err := coreMigrationFiles()
	if err != nil {
		return err
	}
	for _, f := range files {
		if done[f.name] {
			continue
		}
		body, err := fs.ReadFile(migrations.SQLiteFS(), f.name)
		if err != nil {
			return fmt.Errorf("sqlite: ApplyCoreSchema: read %s: %w", f.name, err)
		}
		if err := s.applyMigration(ctx, f.name, string(body)); err != nil {
			return err
		}
	}
	return nil
}

// applyMigration runs one migration file's SQL and records it, atomically.
func (s *Store) applyMigration(ctx context.Context, name, body string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: ApplyCoreSchema: begin %s: %w", name, err)
	}
	// No-op once Commit has succeeded; it is the rollback for every early return.
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, body); err != nil {
		return fmt.Errorf("sqlite: ApplyCoreSchema: apply %s: %w", name, err)
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO "+schemaMigrationsTable+" (name) VALUES (?)", name); err != nil {
		return fmt.Errorf("sqlite: ApplyCoreSchema: record %s: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: ApplyCoreSchema: commit %s: %w", name, err)
	}
	return nil
}

// AppliedMigrations returns the names of the migrations this store has applied, ordered by
// name. An empty store returns an empty slice, not an error. It is a read helper for
// tests, operators and the wave-2 repo layer; ApplyCoreSchema is the only writer.
func (s *Store) AppliedMigrations(ctx context.Context) ([]string, error) {
	// Nothing is applied yet if the ledger table does not exist (fresh file).
	var exists int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
		schemaMigrationsTable).Scan(&exists); err != nil {
		return nil, fmt.Errorf("sqlite: AppliedMigrations: %w", err)
	}
	if exists == 0 {
		return []string{}, nil
	}

	rows, err := s.db.QueryContext(ctx,
		"SELECT name FROM "+schemaMigrationsTable+" ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("sqlite: AppliedMigrations: %w", err)
	}
	defer rows.Close()

	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("sqlite: AppliedMigrations: scan: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: AppliedMigrations: %w", err)
	}
	return names, nil
}

// migrationFile is one `NNNNNN_<name>.up.sql` entry of the embedded SQLite batch.
type migrationFile struct {
	name string // file name, e.g. "000002_trees.up.sql"
	num  int    // numeric prefix, for ordering
}

// coreMigrationFiles lists the wave-1 SQLite migrations in apply order (ascending numeric
// prefix, then name). It is derived from the embedded FS rather than hardcoded, so a new
// translated migration is picked up automatically. Duplicate numbers or an empty batch are
// reported as errors rather than silently skipped.
func coreMigrationFiles() ([]migrationFile, error) {
	entries, err := fs.ReadDir(migrations.SQLiteFS(), ".")
	if err != nil {
		return nil, fmt.Errorf("sqlite: coreMigrationFiles: read embedded sqlite/ dir: %w", err)
	}

	files := make([]migrationFile, 0, len(entries))
	seen := make(map[int]string, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := migrationNameRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue // down files and anything else non-matching are not part of the up pass
		}
		num, err := strconv.Atoi(m[1])
		if err != nil {
			return nil, fmt.Errorf("sqlite: coreMigrationFiles: non-numeric prefix in %q: %w", e.Name(), err)
		}
		if other, dup := seen[num]; dup {
			return nil, fmt.Errorf("sqlite: coreMigrationFiles: duplicate migration number %06d (%s and %s)", num, other, e.Name())
		}
		seen[num] = e.Name()
		files = append(files, migrationFile{name: e.Name(), num: num})
	}
	if len(files) == 0 {
		return nil, errors.New("sqlite: coreMigrationFiles: no NNNNNN_*.up.sql files in the embedded sqlite/ dir")
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].num != files[j].num {
			return files[i].num < files[j].num
		}
		return files[i].name < files[j].name
	})
	return files, nil
}
