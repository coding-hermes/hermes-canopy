package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/coding-hermes/hermes-canopy/migrations"
)

// ── helpers ──────────────────────────────────────────────────────────────────────────
//
// Every test owns its own database file under t.TempDir(); nothing here reads or writes
// the live store (`~/.hermes/canopy/...`) or any path outside the test's temp dir.

func newTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "canopy.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	// Registered after t.TempDir()'s own cleanup, so the pool is closed (and the WAL
	// side files released) before the directory is removed.
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close(%q): %v", path, err)
		}
	})
	return s
}

func newTestStoreWithSchema(t *testing.T) *Store {
	t.Helper()
	s := newTestStore(t)
	if err := s.ApplyCoreSchema(context.Background()); err != nil {
		t.Fatalf("ApplyCoreSchema: %v", err)
	}
	return s
}

// listTables returns the user tables of the store (sqlite_schema_migrations included,
// SQLite's own `sqlite_*` internals excluded).
func listTables(t *testing.T, ctx context.Context, s *Store) []string {
	t.Helper()
	rows, err := s.DB().QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list tables: %v", err)
	}
	sort.Strings(out)
	return out
}

type columnInfo struct {
	name     string
	declType string
	notNull  bool
	defaultV sql.NullString
	primaryK int
}

// tableInfo reads PRAGMA table_info for table.
func tableInfo(t *testing.T, ctx context.Context, s *Store, table string) map[string]columnInfo {
	t.Helper()
	rows, err := s.DB().QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer rows.Close()

	cols := map[string]columnInfo{}
	for rows.Next() {
		var (
			cid       int
			name      string
			declType  string
			notNull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &declType, &notNull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan PRAGMA table_info(%s): %v", table, err)
		}
		cols[name] = columnInfo{name: name, declType: declType, notNull: notNull == 1, defaultV: dfltValue, primaryK: pk}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	if len(cols) == 0 {
		t.Fatalf("table %s has no columns (missing?)", table)
	}
	return cols
}

// createTableRe extracts the table name from a `CREATE TABLE [IF NOT EXISTS] <name>` line
// of the embedded migrations — the independent derivation of the expected table set.
var createTableRe = regexp.MustCompile(`(?mi)^CREATE TABLE (?:IF NOT EXISTS )?([a-z_]+)`)

func mustID(t *testing.T) string {
	t.Helper()
	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	return id.String()
}

// insertTree inserts a minimal trees row (title/description/metadata all have DDL
// defaults) and returns its id.
func insertTree(t *testing.T, ctx context.Context, s *Store) string {
	t.Helper()
	id := mustID(t)
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO trees (id, owner_id) VALUES (?, ?)`, id, mustID(t)); err != nil {
		t.Fatalf("insert tree: %v", err)
	}
	return id
}

// insertNodeSQL mirrors what the wave-2 repo layer will write: every Go-owned column
// (id, sequence_num, content_hash) is supplied explicitly, because the SQLite DDL declares
// no default for any of them.
const insertNodeSQL = `INSERT INTO nodes (id, tree_id, author_id, content, sequence_num, content_hash)
VALUES (?, ?, ?, ?, ?, ?)`

func insertNode(ctx context.Context, s *Store, treeID, content string, seq int64) error {
	_, err := s.DB().ExecContext(ctx, insertNodeSQL, freshID(), treeID, freshID(), content, seq, ContentHash(content))
	return err
}

// freshID is NewID for paths that cannot call t.Fatalf; it panics only if the system
// CSPRNG is unavailable, which is not a condition under test.
func freshID() string {
	id, err := NewID()
	if err != nil {
		panic(err)
	}
	return id.String()
}

// ── Open / pragmas ───────────────────────────────────────────────────────────────────

func TestOpenAppliesD6PragmaSet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Two connections held open at the same time, deliberately: foreign_keys and
	// busy_timeout are per-connection settings, so this asserts the driver applies the
	// set to every connection it opens, not once to a single pooled connection.
	c1, err := s.DB().Conn(ctx)
	if err != nil {
		t.Fatalf("acquire connection 1: %v", err)
	}
	t.Cleanup(func() { _ = c1.Close() })
	c2, err := s.DB().Conn(ctx)
	if err != nil {
		t.Fatalf("acquire connection 2: %v", err)
	}
	t.Cleanup(func() { _ = c2.Close() })

	want := []struct{ pragma, value string }{
		{"PRAGMA journal_mode", "wal"},
		{"PRAGMA foreign_keys", "1"},
		{"PRAGMA busy_timeout", "5000"},
		{"PRAGMA synchronous", "1"},
	}
	for i, c := range []*sql.Conn{c1, c2} {
		for _, w := range want {
			var got any
			if err := c.QueryRowContext(ctx, w.pragma).Scan(&got); err != nil {
				t.Fatalf("connection %d: %s: %v", i+1, w.pragma, err)
			}
			if g := fmt.Sprintf("%v", got); g != w.value {
				t.Errorf("connection %d: %s = %q, want %q", i+1, w.pragma, g, w.value)
			}
		}
	}
}

func TestOpenRejectsUnusablePaths(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
	}{
		{"empty", ""},
		{"blank", "   "},
		{"question mark", filepath.Join(t.TempDir(), "a?b.db")},
		{"directory", t.TempDir()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Open(tc.path)
			if err == nil {
				_ = s.Close()
				t.Fatalf("Open(%q) succeeded, want an error", tc.path)
			}
		})
	}
}

// ── ApplyCoreSchema ──────────────────────────────────────────────────────────────────

func TestApplyCoreSchemaCreatesCoreTables(t *testing.T) {
	s := newTestStoreWithSchema(t)
	ctx := context.Background()
	tables := listTables(t, ctx, s)
	have := map[string]bool{}
	for _, name := range tables {
		have[name] = true
	}

	// The core graph the wave-2 repo layer swaps onto.
	for _, name := range []string{
		"trees", "nodes", "edges", "tree_snapshots", "tree_events", "tree_event_seq",
	} {
		if !have[name] {
			t.Errorf("core table %q missing after ApplyCoreSchema; tables: %v", name, tables)
		}
	}
	// Migrations 000008/000010 tables ride along in the same batch — a skipped file would
	// show up here as well as in the applied ledger below.
	for _, name := range []string{"users", "profiles", "tree_members", "profile_route", "approvals"} {
		if !have[name] {
			t.Errorf("table %q missing after ApplyCoreSchema; tables: %v", name, tables)
		}
	}

	// Expected set derived from the embedded DDL itself (+ the store's own ledger table).
	expected := map[string]bool{schemaMigrationsTable: true}
	files, err := coreMigrationFiles()
	if err != nil {
		t.Fatalf("coreMigrationFiles: %v", err)
	}
	for _, f := range files {
		body, err := fs.ReadFile(migrations.SQLiteFS(), f.name)
		if err != nil {
			t.Fatalf("read %s: %v", f.name, err)
		}
		for _, m := range createTableRe.FindAllStringSubmatch(string(body), -1) {
			expected[m[1]] = true
		}
	}
	for name := range expected {
		if !have[name] {
			t.Errorf("table %q declared in the migrations is absent from the store", name)
		}
	}
	for name := range have {
		if !expected[name] {
			t.Errorf("table %q exists in the store but is declared by no migration", name)
		}
	}

	// Every up migration is recorded exactly once, in order.
	applied, err := s.AppliedMigrations(ctx)
	if err != nil {
		t.Fatalf("AppliedMigrations: %v", err)
	}
	if len(applied) != len(files) {
		t.Fatalf("applied %d migrations, want %d: %v", len(applied), len(files), applied)
	}
	for i, f := range files {
		if applied[i] != f.name {
			t.Errorf("applied[%d] = %q, want %q (ledger must be in migration order)", i, applied[i], f.name)
		}
	}
}

func TestNodesColumnShapeIsTheGoObligationShape(t *testing.T) {
	s := newTestStoreWithSchema(t)
	cols := tableInfo(t, context.Background(), s, "nodes")

	// Translation rules (docs/SQLITE-PIVOT.md §2): uuid → TEXT, jsonb → TEXT, timestamps →
	// TEXT, bigint → INTEGER.
	for _, name := range []string{
		"id", "tree_id", "parent_id", "author_id", "content", "content_format",
		"node_type", "sequence_num", "metadata", "created_at", "edited_at", "deleted_at",
		"content_hash",
	} {
		if _, ok := cols[name]; !ok {
			t.Errorf("nodes.%s is missing", name)
		}
	}
	if got := cols["sequence_num"].declType; got != "INTEGER" {
		t.Errorf("nodes.sequence_num type = %q, want INTEGER", got)
	}
	if !cols["sequence_num"].notNull {
		t.Error("nodes.sequence_num must be NOT NULL (the Go obligation depends on it)")
	}
	if !cols["content_hash"].notNull {
		t.Error("nodes.content_hash must be NOT NULL (a missing hash must fail loudly)")
	}
	// The Go-owned columns must have NO DDL default: a forgotten value then fails loudly on
	// NOT NULL instead of being silently filled with a random or empty one (that absence is
	// exactly why NewID/NextNodeSequence/ContentHash exist).
	for _, name := range []string{"id", "sequence_num", "content_hash"} {
		if cols[name].defaultV.Valid && strings.TrimSpace(cols[name].defaultV.String) != "" {
			t.Errorf("nodes.%s has DDL default %q; that value is a Go write-path obligation", name, cols[name].defaultV.String)
		}
	}
	// Created/edited timestamps stay in the database (D4) and their DDL default is the exact
	// shape EncodeTime produces, so Go-written and DDL-default timestamps are comparable as
	// plain TEXT.
	if got := cols["created_at"].defaultV.String; !strings.Contains(got, "strftime('%Y-%m-%dT%H:%M:%fZ','now')") {
		t.Errorf("nodes.created_at DDL default = %q, want the RFC3339-millisecond strftime default", got)
	}
}

func TestApplyCoreSchemaIsIdempotent(t *testing.T) {
	s := newTestStoreWithSchema(t)
	ctx := context.Background()

	before, err := s.AppliedMigrations(ctx)
	if err != nil {
		t.Fatalf("AppliedMigrations: %v", err)
	}
	tablesBefore := listTables(t, ctx, s)
	if len(before) == 0 {
		t.Fatal("no migrations recorded after the first ApplyCoreSchema")
	}

	if err := s.ApplyCoreSchema(ctx); err != nil {
		t.Fatalf("second ApplyCoreSchema: %v", err)
	}

	after, err := s.AppliedMigrations(ctx)
	if err != nil {
		t.Fatalf("AppliedMigrations: %v", err)
	}
	if strings.Join(before, ",") != strings.Join(after, ",") {
		t.Errorf("ledger changed across a second ApplyCoreSchema:\n before: %v\n after:  %v", before, after)
	}

	var ledgerRows int
	if err := s.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM "+schemaMigrationsTable).Scan(&ledgerRows); err != nil {
		t.Fatalf("count ledger rows: %v", err)
	}
	if ledgerRows != len(after) {
		t.Errorf("ledger holds %d rows for %d distinct names (duplicate re-application)", ledgerRows, len(after))
	}

	tablesAfter := listTables(t, ctx, s)
	if strings.Join(tablesBefore, ",") != strings.Join(tablesAfter, ",") {
		t.Errorf("table set changed across a second ApplyCoreSchema:\n before: %v\n after:  %v", tablesBefore, tablesAfter)
	}
}

// ── foreign keys (the property the D6 pragma set exists for) ─────────────────────────

func TestForeignKeysAreEnforced(t *testing.T) {
	s := newTestStoreWithSchema(t)
	ctx := context.Background()

	// 1. A node in a tree that does not exist must be refused. With foreign_keys=OFF this
	//    insert succeeds silently and the FK clauses become documentation.
	err := insertNode(ctx, s, "00000000-0000-7000-8000-000000000000", "orphan", 1)
	if err == nil {
		t.Fatal("inserting a node with an unknown tree_id succeeded; foreign key enforcement is off")
	}
	if !isForeignKeyError(err) {
		t.Errorf("want a foreign-key error for the orphan node, got: %v", err)
	}

	// 2. Parent first: the supported write order.
	treeID := insertTree(t, ctx, s)
	tx, err := s.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	seq, err := NextNodeSequence(ctx, tx, treeID)
	if err != nil {
		t.Fatalf("NextNodeSequence: %v", err)
	}
	if seq != 1 {
		t.Fatalf("first node sequence in a fresh tree = %d, want 1", seq)
	}
	if _, err := tx.ExecContext(ctx, insertNodeSQL, mustID(t), treeID, mustID(t), "hello", seq, ContentHash("hello")); err != nil {
		t.Fatalf("insert node under a real tree: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// 3. The self-referencing FK (nodes.parent_id → nodes.id) is enforced too.
	_, err = s.DB().ExecContext(ctx,
		`INSERT INTO nodes (id, tree_id, parent_id, author_id, content, sequence_num, content_hash)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		mustID(t), treeID, "00000000-0000-7000-8000-000000000001", mustID(t), "child", 2, ContentHash("child"))
	if err == nil {
		t.Fatal("inserting a node with an unknown parent_id succeeded")
	}
	if !isForeignKeyError(err) {
		t.Errorf("want a foreign-key error for the unknown parent, got: %v", err)
	}

	// 4. And the edge table's core-graph FKs behave the same way for an unknown tree.
	_, err = s.DB().ExecContext(ctx,
		`INSERT INTO edges (id, tree_id, source_id, target_id, sequence_num) VALUES (?, ?, ?, ?, ?)`,
		mustID(t), "00000000-0000-7000-8000-000000000000", mustID(t), mustID(t), 1)
	if err == nil {
		t.Fatal("inserting an edge with an unknown tree_id succeeded")
	}
	if !isForeignKeyError(err) {
		t.Errorf("want a foreign-key error for the orphan edge, got: %v", err)
	}
}

// isForeignKeyError reports whether err is SQLite's foreign-key enforcement failure. The
// driver wraps the engine message, so match on the engine text, not on a prefix.
func isForeignKeyError(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "foreign key constraint failed")
}
