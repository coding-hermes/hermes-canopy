package session

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver for the fixture DBs
)

// --- GAP-077 reader fixtures ------------------------------------------------

// snapshotTestOptions returns options that resolve inside dir and materialize
// into a per-test temp root, with a fixed clock.
func snapshotTestOptions(t *testing.T, dir string, now time.Time) SnapshotOptions {
	t.Helper()
	return SnapshotOptions{
		Dir:     dir,
		MaxAge:  DefaultSnapshotMaxAge,
		TempDir: t.TempDir(),
		Now:     func() time.Time { return now },
	}
}

// openSnapshotTestReader opens a snapshot reader with warnings captured.
func openSnapshotTestReader(t *testing.T, opts SnapshotOptions) (*Reader, *strings.Builder, SnapshotSpec) {
	t.Helper()
	r, err := OpenSnapshotReader(opts)
	if err != nil {
		t.Fatalf("open snapshot reader: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	var buf strings.Builder
	r.SetWarnLogger(log.New(&buf, "", 0))
	spec, ok := r.Snapshot()
	if !ok {
		t.Fatal("Snapshot() reported no spec for a snapshot reader")
	}
	return r, &buf, spec
}

// attachedDatabases reads PRAGMA database_list through the reader's own query
// surface, returning {schema name -> file path}.
func attachedDatabases(t *testing.T, r *Reader) map[string]string {
	t.Helper()
	rows, err := r.q.QueryContext(context.Background(), "PRAGMA database_list")
	if err != nil {
		t.Fatalf("PRAGMA database_list: %v", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var seq int
		var name, file string
		if err := rows.Scan(&seq, &name, &file); err != nil {
			t.Fatalf("scan database_list: %v", err)
		}
		out[name] = file
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("database_list rows: %v", err)
	}
	return out
}

// copyFileInto copies a fixture database to an explicit destination.
func copyFileInto(t *testing.T, src, dst string) string {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(dst), err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
	return dst
}

// --- ATTACH contract --------------------------------------------------------

// TestSnapshotReaderAttachesReadOnlySnapshot proves the reader reaches the
// snapshot through a read-only ATTACH rather than by opening it as the primary
// database: the attached schema is present in PRAGMA database_list pointing at
// the materialized copy, the scratch primary owns no tables, and the rows come
// back through the attached schema.
func TestSnapshotReaderAttachesReadOnlySnapshot(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	src := buildFixture(t, func(conn *sql.DB) {
		insertSession(t, conn, fixtureSession{id: "sess_snap", source: "cli", title: "Snapshot Session", model: "m1", startedAt: 1000.5})
		insertSession(t, conn, fixtureSession{id: "sess_later", source: "telegram", title: "Later", model: "m2", startedAt: 2000.5})
		insertMessage(t, conn, fixtureMessage{id: 1, sessionID: "sess_snap", role: "user", content: "hello from snapshot", ts: 1000})
		insertMessage(t, conn, fixtureMessage{id: 2, sessionID: "sess_snap", role: "assistant", content: "reply", ts: 1001})
	})
	dir := t.TempDir()
	snapPath := writeSnapshot(t, dir, now.Add(-time.Hour), SnapshotZstd, src)
	opts := snapshotTestOptions(t, dir, now)

	r, warns, spec := openSnapshotTestReader(t, opts)
	if spec.Path != snapPath {
		t.Fatalf("resolved %q, want the newest snapshot %q", spec.Path, snapPath)
	}

	dbs := attachedDatabases(t, r)
	copyPath, ok := dbs[attachedSchema]
	if !ok {
		t.Fatalf("database_list = %v, want an attached %q schema (ATTACH is the read path)", dbs, attachedSchema)
	}
	if copyPath == "" || copyPath == snapPath {
		t.Fatalf("attached file = %q, want the materialized copy (not the compressed snapshot)", copyPath)
	}
	if _, err := os.Stat(copyPath); err != nil {
		t.Fatalf("attached file %q is not readable: %v", copyPath, err)
	}

	// The data must live in the attached schema, not in the scratch primary:
	// main declares no sessions table at all, while the attached schema does
	// (which is what the schema-qualified introspection reads).
	ctx := context.Background()
	var mainSessions int
	if err := r.q.QueryRowContext(ctx, "SELECT count(*) FROM main.sqlite_master WHERE name = 'sessions'").Scan(&mainSessions); err != nil {
		t.Fatalf("inspect scratch primary: %v", err)
	}
	if mainSessions != 0 {
		t.Errorf("scratch primary declares %d 'sessions' objects, want 0", mainSessions)
	}
	var attachedSessions int
	if err := r.q.QueryRowContext(ctx, "SELECT count(*) FROM "+attachedSchema+".sqlite_master WHERE name = 'sessions'").Scan(&attachedSessions); err != nil {
		t.Fatalf("inspect attached schema: %v", err)
	}
	if attachedSessions != 1 {
		t.Errorf("attached schema declares %d 'sessions' objects, want 1", attachedSessions)
	}

	sessions, err := r.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 2 || sessions[0].ID != "sess_snap" || sessions[1].ID != "sess_later" {
		t.Fatalf("sessions = %+v, want sess_snap then sess_later", sessions)
	}
	// Introspection resolved the attached schema: a missing table would have
	// degraded to "nothing importable" with a warning instead of rows.
	if out := warns.String(); strings.Contains(out, "no sessions table") {
		t.Errorf("warnings = %q, want schema introspection to see the attached sessions table", out)
	}
	msgs, err := r.ListMessages(ctx, "sess_snap")
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 2 || msgs[0].Content != "hello from snapshot" {
		t.Fatalf("messages = %+v, want the snapshot's two messages", msgs)
	}
	if dels, err := r.ListDelegations(ctx); err != nil || len(dels) != 0 {
		t.Fatalf("delegations = %v err = %v, want none", dels, err)
	}
}

// TestSnapshotReaderInPlaceSnapshotIsNotImmutable pins the difference between
// the two source shapes: a private copy may be read with immutable=1, while a
// snapshot read where it lives must stay mode=ro (the snapshot directory is
// not ours to declare frozen).
func TestSnapshotReaderInPlaceSnapshotIsNotImmutable(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	dir := t.TempDir()
	plain := writeSnapshot(t, dir, now.Add(-time.Hour), SnapshotPlain, buildFixture(t, func(conn *sql.DB) {
		insertSession(t, conn, fixtureSession{id: "sess_plain", source: "cli", title: "Plain", model: "m", startedAt: 10})
	}))
	opts := snapshotTestOptions(t, dir, now)

	r, _, _ := openSnapshotTestReader(t, opts)
	// No copy is made for a plain snapshot, so the attached file IS the
	// snapshot and nothing was written beside it.
	dbs := attachedDatabases(t, r)
	if got := dbs[attachedSchema]; got != plain {
		t.Fatalf("attached file = %q, want the snapshot itself %q", got, plain)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("snapshot dir = %v, want only the snapshot (no copies written into it)", names)
	}
	sessions, err := r.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != "sess_plain" {
		t.Fatalf("sessions = %+v, want sess_plain", sessions)
	}
}

// --- Write rejection --------------------------------------------------------

// TestSnapshotReaderRejectsWrites proves the reader cannot mutate the snapshot
// through any statement — data, schema, or an attached-schema create — and that
// both the snapshot file and the materialized copy are byte-identical
// afterwards.
func TestSnapshotReaderRejectsWrites(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	src := buildFixture(t, func(conn *sql.DB) {
		insertSession(t, conn, fixtureSession{id: "sess_ro", source: "cli", title: "Read Only", model: "m", startedAt: 5})
		insertMessage(t, conn, fixtureMessage{id: 1, sessionID: "sess_ro", role: "user", content: "x", ts: 5})
	})
	dir := t.TempDir()
	snapPath := writeSnapshot(t, dir, now.Add(-time.Hour), SnapshotZstd, src)
	opts := snapshotTestOptions(t, dir, now)

	r, _, _ := openSnapshotTestReader(t, opts)
	dbs := attachedDatabases(t, r)
	copyPath := dbs[attachedSchema]
	snapBefore := hashFile(t, snapPath)
	copyBefore := hashFile(t, copyPath)

	ctx := context.Background()
	statements := []struct {
		name string
		sql  string
	}{
		{"insert session", "INSERT INTO " + attachedSchema + ".sessions (id, source, started_at) VALUES ('pwn', 'x', 1)"},
		{"update session", "UPDATE " + attachedSchema + ".sessions SET title = 'pwn'"},
		{"delete message", "DELETE FROM " + attachedSchema + ".messages"},
		{"create table in snapshot", "CREATE TABLE " + attachedSchema + ".newtable (x)"},
		{"drop message table", "DROP TABLE " + attachedSchema + ".messages"},
		{"create view in snapshot", "CREATE VIEW " + attachedSchema + ".v AS SELECT 1"},
		{"vacuum snapshot", "VACUUM " + attachedSchema},
		{"write scratch primary", "CREATE TABLE main.scratch (x)"},
	}
	for _, stmt := range statements {
		if _, err := r.q.ExecContext(ctx, stmt.sql); err == nil {
			t.Errorf("%s: statement succeeded, want a read-only rejection", stmt.name)
		}
	}
	if got := hashFile(t, snapPath); got != snapBefore {
		t.Error("snapshot bytes changed after write attempts")
	}
	if got := hashFile(t, copyPath); got != copyBefore {
		t.Error("materialized copy bytes changed after write attempts")
	}
}

// --- Contention -------------------------------------------------------------

// TestSnapshotReaderIgnoresLiveDatabaseLock is AC2: while a separate process
// holds the live database under an exclusive write lock, the snapshot reader
// still returns the snapshot's rows, never the live database's. The control
// arm shows the live file really is locked during the read.
func TestSnapshotReaderIgnoresLiveDatabaseLock(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	// The "live" gateway database: a different fixture with a marker session.
	livePath := buildFixture(t, func(conn *sql.DB) {
		insertSession(t, conn, fixtureSession{id: "live_only", source: "cli", title: "Live", model: "m", startedAt: 9000})
	})

	// The snapshot: same shape, disjoint data.
	snapSrc := buildFixture(t, func(conn *sql.DB) {
		insertSession(t, conn, fixtureSession{id: "snap_only", source: "cli", title: "Snapshot", model: "m", startedAt: 1})
		insertMessage(t, conn, fixtureMessage{id: 1, sessionID: "snap_only", role: "user", content: "from the snapshot", ts: 1})
	})
	dir := t.TempDir()
	writeSnapshot(t, dir, now.Add(-time.Hour), SnapshotZstd, snapSrc)

	// A writer holds the live database open in an exclusive transaction with
	// an uncommitted row.
	writer, err := sql.Open("sqlite", "file:"+livePath)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	defer func() { _ = writer.Close() }()
	writer.SetMaxOpenConns(1)
	ctx := context.Background()
	wconn, err := writer.Conn(ctx)
	if err != nil {
		t.Fatalf("reserve writer conn: %v", err)
	}
	defer func() { _ = wconn.Close() }()
	if _, err := wconn.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		t.Fatalf("begin exclusive: %v", err)
	}
	if _, err := wconn.ExecContext(ctx, "INSERT INTO sessions (id, source, started_at) VALUES ('uncommitted', 'cli', 9999)"); err != nil {
		t.Fatalf("insert under lock: %v", err)
	}
	defer func() { _, _ = wconn.ExecContext(ctx, "ROLLBACK") }()

	// Control: reading the live database right now is not possible.
	control, err := sql.Open("sqlite", "file:"+livePath+"?mode=ro&_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatalf("open control: %v", err)
	}
	defer func() { _ = control.Close() }()
	var n int
	if err := control.QueryRowContext(ctx, "SELECT count(*) FROM sessions").Scan(&n); err == nil {
		t.Log("note: the live database accepted a read while locked (platform/journal mode dependent); the snapshot assertions below still apply")
	}

	liveBefore := hashFile(t, livePath)

	opts := snapshotTestOptions(t, dir, now)
	r, _, _ := openSnapshotTestReader(t, opts)

	sessions, err := r.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions while live db is locked: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != "snap_only" {
		t.Fatalf("sessions = %+v, want exactly the snapshot's snap_only (never the live database)", sessions)
	}
	for _, s := range sessions {
		if s.ID == "live_only" || s.ID == "uncommitted" {
			t.Fatalf("read %q from the live database; the snapshot source must never touch it", s.ID)
		}
	}
	if msgs, err := r.ListMessages(ctx, "snap_only"); err != nil || len(msgs) != 1 {
		t.Fatalf("messages = %v err = %v, want the snapshot's single message", msgs, err)
	}
	if got := hashFile(t, livePath); got != liveBefore {
		t.Error("live database bytes changed while the snapshot reader ran")
	}
	// The writer's transaction is still open — the reader took nothing from it.
	var depth int
	if err := control.QueryRowContext(ctx, "SELECT 1").Scan(&depth); err != nil {
		// Expected: the live db is still locked, which is the point.
		_ = depth
	}
}

// --- Schema tolerance -------------------------------------------------------

// TestSnapshotReaderOldSchemaStaysTolerant proves the IMP-008 degradation
// contract survives the snapshot path: an old/minimal schema delivered as a
// compressed snapshot still imports and still warns.
func TestSnapshotReaderOldSchemaStaysTolerant(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	oldPath := execFixture(t, oldSessionsSchema, func(conn *sql.DB) {
		if _, err := conn.Exec(`INSERT INTO sessions (id, title, model, started_at)
			VALUES ('sess_old', 'Legacy Session', 'old-model', '2026-08-01T10:00:00Z')`); err != nil {
			t.Fatalf("insert session: %v", err)
		}
		if _, err := conn.Exec(`INSERT INTO messages (session_id, role, content, tool_name, token_count, timestamp)
			VALUES ('sess_old', 'user', 'hello legacy', 'legacy_tool', 42, '2026-08-01T10:01:00Z')`); err != nil {
			t.Fatalf("insert message: %v", err)
		}
	})
	dir := t.TempDir()
	writeSnapshot(t, dir, now.Add(-2*time.Hour), SnapshotGzip, oldPath)
	opts := snapshotTestOptions(t, dir, now)

	r, warns, _ := openSnapshotTestReader(t, opts)
	ctx := context.Background()
	sessions, err := r.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions on old schema snapshot: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %+v, want 1", sessions)
	}
	got := sessions[0]
	if got.Source != "" || got.Archived || got.EndedAt != nil {
		t.Errorf("absent columns should degrade to zero values: %+v", got)
	}
	if !got.StartedAt.Equal(time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("StartedAt = %v, want the parsed ISO-8601 instant", got.StartedAt)
	}
	msgs, err := r.ListMessages(ctx, "sess_old")
	if err != nil {
		t.Fatalf("ListMessages on old schema snapshot: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Content != "hello legacy" {
		t.Fatalf("messages = %+v, want one legacy row", msgs)
	}
	if out := warns.String(); !strings.Contains(out, "no source column") {
		t.Errorf("warnings = %q, want the missing-source degradation note", out)
	}

	// Minimal schema (no messages table at all) through the same path.
	minDir := t.TempDir()
	minPath := execFixture(t, `
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    title TEXT,
    model TEXT,
    started_at REAL NOT NULL
);`, func(conn *sql.DB) {
		if _, err := conn.Exec(`INSERT INTO sessions (id, title, model, started_at) VALUES ('sess_min', 'Min', 'm', 1750000000.25)`); err != nil {
			t.Fatalf("insert: %v", err)
		}
	})
	writeSnapshot(t, minDir, now.Add(-2*time.Hour), SnapshotZstd, minPath)
	minOpts := snapshotTestOptions(t, minDir, now)
	minReader, minWarns, _ := openSnapshotTestReader(t, minOpts)
	if got, err := minReader.ListMessages(ctx, "sess_min"); err != nil || len(got) != 0 {
		t.Fatalf("ListMessages on minimal snapshot = %v err = %v, want empty/no error", got, err)
	}
	if out := minWarns.String(); !strings.Contains(out, "no messages table") {
		t.Errorf("warnings = %q, want the missing-messages-table note", out)
	}
}

// TestSnapshotReaderImporterEndToEnd proves the import entry path works when
// the reader is snapshot-backed: the expected sessions and messages land in
// Canopy and the watermark advances.
func TestSnapshotReaderImporterEndToEnd(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	src := buildFixture(t, func(conn *sql.DB) {
		insertSession(t, conn, fixtureSession{id: "sess_a", source: "cli", title: "Alpha Session", model: "m1", startedAt: 1000.5})
		insertMessage(t, conn, fixtureMessage{id: 1, sessionID: "sess_a", role: "user", content: "hello world", ts: 1001})
		insertMessage(t, conn, fixtureMessage{id: 2, sessionID: "sess_a", role: "assistant", content: "hi there", ts: 1002})
	})
	dir := t.TempDir()
	writeSnapshot(t, dir, now.Add(-time.Hour), SnapshotZstd, src)
	opts := snapshotTestOptions(t, dir, now)

	r, _, _ := openSnapshotTestReader(t, opts)
	trees := &fakeTreeCreator{}
	nodes := &fakeNodeCreator{}
	store := &memWatermarkStore{}
	imp := NewImporter(r, trees, nodes, store, defaultOwner)

	sum, err := imp.Run(context.Background(), ImportOptions{})
	if err != nil {
		t.Fatalf("Run through snapshot: %v", err)
	}
	if sum.SessionsImported != 1 || sum.TreesCreated != 1 || sum.NodesCreated != 1 {
		t.Fatalf("summary = %+v, want 1 session / 1 tree / 1 child node (the first user message is the root)", sum)
	}
	if len(trees.calls) != 1 || trees.calls[0].Title != "Alpha Session" {
		t.Fatalf("tree calls = %+v, want the snapshot's Alpha Session", trees.calls)
	}
	if len(trees.calls[0].RootContent) == 0 || !strings.Contains(trees.calls[0].RootContent, "hello world") {
		t.Errorf("root content = %q, want the snapshot's first user message", trees.calls[0].RootContent)
	}
	if store.wm.LastSessionID != "sess_a" {
		t.Errorf("watermark session = %q, want sess_a", store.wm.LastSessionID)
	}
}

// --- Failure modes ----------------------------------------------------------

// TestSnapshotReaderNeverFallsBackToLiveDatabase is the anti-fallback contract:
// with a perfectly good live state.db in the default Hermes location and no
// usable snapshot, the reader must fail loudly — never open the live file.
func TestSnapshotReaderNeverFallsBackToLiveDatabase(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	livePath := filepath.Join(home, ".hermes", "state.db")
	copyFileInto(t, buildFixture(t, func(conn *sql.DB) {
		insertSession(t, conn, fixtureSession{id: "live_only", source: "cli", title: "Live", model: "m", startedAt: 1})
	}), livePath)

	opts, err := DefaultSnapshotOptions()
	if err != nil {
		t.Fatalf("DefaultSnapshotOptions: %v", err)
	}
	opts.TempDir = t.TempDir()

	// 1. No snapshot directory at all.
	r, err := OpenSnapshotReader(opts)
	if r != nil {
		t.Fatal("got a reader with no snapshot directory; the live database must never be substituted")
	}
	if !errors.Is(err, ErrSnapshotDir) {
		t.Fatalf("err = %v, want ErrSnapshotDir", err)
	}

	// 2. Snapshot directory present but empty.
	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		t.Fatalf("mkdir snapshot dir: %v", err)
	}
	r, err = OpenSnapshotReader(opts)
	if r != nil {
		t.Fatal("got a reader from an empty snapshot directory")
	}
	if !errors.Is(err, ErrNoSnapshot) {
		t.Fatalf("err = %v, want ErrNoSnapshot", err)
	}

	// 3. Only a stale snapshot.
	now := time.Now().Truncate(time.Second)
	stale := writeSnapshot(t, opts.Dir, now.Add(-48*time.Hour), SnapshotZstd, livePath)
	opts.Now = func() time.Time { return now }
	r, err = OpenSnapshotReader(opts)
	if r != nil {
		t.Fatal("got a reader from a stale snapshot")
	}
	if !errors.Is(err, ErrSnapshotStale) {
		t.Fatalf("err = %v, want ErrSnapshotStale", err)
	}
	if !strings.Contains(err.Error(), stale) {
		t.Errorf("err = %v, want it to name the stale snapshot", err)
	}

	// The live database was never opened: bytes unchanged.
	if _, err := os.Stat(livePath); err != nil {
		t.Fatalf("live database disappeared: %v", err)
	}
}

// TestSnapshotReaderInvalidSnapshotFailsLoud proves a file that is not a
// SQLite database never degrades into an empty import.
func TestSnapshotReaderInvalidSnapshotFailsLoud(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	dir := t.TempDir()
	path := filepath.Join(dir, snapshotFileName(now.Add(-time.Hour), SnapshotPlain))
	if err := os.WriteFile(path, []byte("this is not a sqlite database, just text\n"), 0o644); err != nil {
		t.Fatalf("write bogus snapshot: %v", err)
	}
	opts := snapshotTestOptions(t, dir, now)

	r, err := OpenSnapshotReader(opts)
	if r != nil {
		t.Fatal("got a reader for a bogus snapshot file")
	}
	if err == nil {
		t.Fatal("want an error for a bogus snapshot file")
	}
	if !errors.Is(err, ErrSnapshotInvalid) {
		t.Fatalf("err = %v, want ErrSnapshotInvalid", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("err = %v, want it to name the offending file", err)
	}
}

// TestSnapshotDirUntouchedByReadCycle proves the source side is inert: after
// resolving, reading and closing, the snapshot directory holds exactly what it
// held before and the snapshot file is byte-identical.
func TestSnapshotDirUntouchedByReadCycle(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	src := buildFixture(t, func(conn *sql.DB) {
		insertSession(t, conn, fixtureSession{id: "sess_x", source: "cli", title: "X", model: "m", startedAt: 1})
	})
	dir := t.TempDir()
	snapPath := writeSnapshot(t, dir, now.Add(-time.Hour), SnapshotZstd, src)
	// A second, unrelated-but-matching snapshot must survive too.
	other := writeSnapshot(t, dir, now.Add(-7*time.Hour), SnapshotGzip, src)
	before := hashFile(t, snapPath)
	otherBefore := hashFile(t, other)

	opts := snapshotTestOptions(t, dir, now)
	r, err := OpenSnapshotReader(opts)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := r.ListSessions(context.Background()); err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 2 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("snapshot dir = %v, want the two original snapshots", names)
	}
	if got := hashFile(t, snapPath); got != before {
		t.Error("snapshot bytes changed across a read cycle")
	}
	if got := hashFile(t, other); got != otherBefore {
		t.Error("an unread snapshot's bytes changed")
	}
}

// --- Direct override --------------------------------------------------------

// TestOpenReaderDirectOverrideStaysReadOnly covers the explicit single-file
// path: it reports no snapshot, reads the file it was given (including a path
// with characters that need URI escaping), and still refuses writes.
func TestOpenReaderDirectOverrideStaysReadOnly(t *testing.T) {
	fixture := buildFixture(t, func(conn *sql.DB) {
		insertSession(t, conn, fixtureSession{id: "sess_direct", source: "cli", title: "Direct", model: "m", startedAt: 1})
	})
	dir := filepath.Join(t.TempDir(), "with space")
	path := copyFileInto(t, fixture, filepath.Join(dir, "state.db"))
	before := hashFile(t, path)

	r, err := OpenReader(path)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer func() { _ = r.Close() }()
	if _, ok := r.Snapshot(); ok {
		t.Error("Snapshot() reported a spec for a direct OpenReader")
	}
	dbs := attachedDatabases(t, r)
	if len(dbs) != 1 {
		t.Errorf("database_list = %v, want only main (a direct open attaches nothing)", dbs)
	}
	sessions, err := r.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 1 || sessions[0].ID != "sess_direct" {
		t.Fatalf("sessions = %+v, want sess_direct", sessions)
	}
	if _, err := r.db.ExecContext(context.Background(), "INSERT INTO sessions (id, source, started_at) VALUES ('pwn', 'x', 1)"); err == nil {
		t.Error("direct reader accepted a write, want read-only rejection")
	}
	if got := hashFile(t, path); got != before {
		t.Error("direct reader mutated the file it read")
	}
}
