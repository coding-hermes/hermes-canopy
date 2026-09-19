package main

import (
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	_ "modernc.org/sqlite" // pure-Go SQLite driver for the fixture database
)

// cliFixtureSchema is the Hermes state.db subset the session CLI reads.
const cliFixtureSchema = `
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    source TEXT NOT NULL,
    display_name TEXT,
    title TEXT,
    model TEXT,
    started_at REAL NOT NULL,
    ended_at REAL,
    archived INTEGER NOT NULL DEFAULT 0,
    parent_session_id TEXT
);
CREATE TABLE messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    role TEXT NOT NULL,
    content TEXT,
    tool_name TEXT,
    token_count INTEGER,
    timestamp REAL NOT NULL,
    active INTEGER NOT NULL DEFAULT 1
);`

// writeCLIFixture creates a Hermes-shaped state.db at path.
func writeCLIFixture(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir fixture dir: %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(cliFixtureSchema); err != nil {
		t.Fatalf("fixture schema: %v", err)
	}
	inserts := []string{
		`INSERT INTO sessions (id, source, title, model, started_at) VALUES ('sess_a', 'cli', 'Alpha Session', 'm1', 1000.5)`,
		`INSERT INTO sessions (id, source, title, model, started_at) VALUES ('sess_b', 'telegram', 'Beta Session', 'm2', 2000.5)`,
		`INSERT INTO messages (session_id, role, content, tool_name, timestamp) VALUES ('sess_a', 'user', 'hello world', '', 1001)`,
		`INSERT INTO messages (session_id, role, content, tool_name, timestamp) VALUES ('sess_a', 'assistant', 'hi there', '', 1002)`,
		`INSERT INTO messages (session_id, role, content, tool_name, timestamp) VALUES ('sess_b', 'user', 'from telegram', '', 2001)`,
	}
	for _, q := range inserts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("fixture insert %q: %v", q, err)
		}
	}
	return path
}

// writeCLISnapshot compresses src into dir under the producer's snapshot name
// for stamp and pins the mtime to the stamp.
func writeCLISnapshot(t *testing.T, dir string, stamp time.Time, src string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir snapshot dir: %v", err)
	}
	path := filepath.Join(dir, "state_"+stamp.Format("20060102-150405")+".db.zst")
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("open fixture for snapshot: %v", err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(path)
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}
	zw, err := zstd.NewWriter(out)
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	if _, err := io.Copy(zw, in); err != nil {
		t.Fatalf("zstd copy: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zstd close: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("close snapshot: %v", err)
	}
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	return path
}

// sessionCLIHome points $HOME (snapshot location) and $TMPDIR (materialized
// copies) at test-owned directories.
func sessionCLIHome(t *testing.T) (home, snapDir, tmpRoot string) {
	t.Helper()
	home = t.TempDir()
	tmpRoot = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TMPDIR", tmpRoot)
	return home, filepath.Join(home, ".hermes", "state-backups"), tmpRoot
}

// TestSessionBrowseReadsSnapshotList proves the browse entry path reads the
// 6-hourly snapshot by default, prints the provenance, and reports the
// snapshot's contents.
func TestSessionBrowseReadsSnapshotList(t *testing.T) {
	_, snapDir, tmpRoot := sessionCLIHome(t)
	stamp := time.Now().Truncate(time.Second).Add(-time.Hour)
	snapPath := writeCLISnapshot(t, snapDir, stamp, writeCLIFixture(t, filepath.Join(t.TempDir(), "state.db")))

	code, stdout, stderr := captureOutput(t, func() int { return sessionBrowseE(nil) })
	if code != 0 {
		t.Fatalf("browse exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	if !strings.Contains(stdout, "Source: snapshot "+snapPath) {
		t.Errorf("stdout = %q, want the resolved snapshot as the source", stdout)
	}
	if !strings.Contains(stdout, "Sessions: 2 of 2") {
		t.Errorf("stdout = %q, want both snapshot sessions listed", stdout)
	}
	for _, want := range []string{"sess_a", "sess_b", "Alpha Session", "msgs=2", "msgs=1", "telegram"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, want)
		}
	}
	if strings.Contains(stdout, "direct database") {
		t.Errorf("stdout = %q, want the snapshot source by default", stdout)
	}
	// The materialized copy is cleaned up when the command returns.
	leftovers, err := filepath.Glob(filepath.Join(tmpRoot, "canopy-snapshot-*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(leftovers) != 0 {
		t.Errorf("leftover snapshot copies: %v", leftovers)
	}
}

// TestSessionBrowseSessionMessages proves --session prints the snapshot's
// messages for that session.
func TestSessionBrowseSessionMessages(t *testing.T) {
	_, snapDir, _ := sessionCLIHome(t)
	stamp := time.Now().Truncate(time.Second).Add(-time.Hour)
	writeCLISnapshot(t, snapDir, stamp, writeCLIFixture(t, filepath.Join(t.TempDir(), "state.db")))

	code, stdout, stderr := captureOutput(t, func() int {
		return sessionBrowseE([]string{"--session", "sess_a"})
	})
	if code != 0 {
		t.Fatalf("browse --session exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	for _, want := range []string{"Session sess_a", "Alpha Session", "Messages: 2", "hello world", "hi there", "user", "assistant"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to contain %q", stdout, want)
		}
	}
	if strings.Contains(stdout, "from telegram") {
		t.Errorf("stdout = %q, must not include another session's messages", stdout)
	}

	t.Run("unknown session", func(t *testing.T) {
		code, stdout, stderr := captureOutput(t, func() int {
			return sessionBrowseE([]string{"--session", "sess_missing"})
		})
		if code != 1 {
			t.Fatalf("exit = %d, want 1", code)
		}
		if !strings.Contains(stderr, "sess_missing") || !strings.Contains(stderr, "not found") {
			t.Errorf("stderr = %q, want a not-found error naming the session", stderr)
		}
		if strings.Contains(stdout, "Messages:") {
			t.Errorf("stdout = %q, want no message listing for an unknown session", stdout)
		}
	})
}

// TestSessionBrowseMissingSnapshotFailsLoud proves the CLI never falls back to
// the live state.db: with no snapshot available the command exits non-zero and
// names the override, even though a live database exists in the default place.
func TestSessionBrowseMissingSnapshotFailsLoud(t *testing.T) {
	home, _, _ := sessionCLIHome(t)
	livePath := writeCLIFixture(t, filepath.Join(home, ".hermes", "state.db"))

	code, stdout, stderr := captureOutput(t, func() int { return sessionBrowseE(nil) })
	if code != 1 {
		t.Fatalf("exit = %d, want 1 with no snapshot available (stdout: %s)", code, stdout)
	}
	if !strings.Contains(stderr, "snapshot directory") || !strings.Contains(stderr, "--db") {
		t.Errorf("stderr = %q, want it to name the snapshot directory and the --db override", stderr)
	}
	if strings.Contains(stdout, "sess_a") || strings.Contains(stdout, "Sessions:") {
		t.Errorf("stdout = %q, want nothing read from the live database %s", stdout, livePath)
	}
}

// TestSessionBrowseDirectDBOverride covers the explicit override: it reads the
// named file and says so.
func TestSessionBrowseDirectDBOverride(t *testing.T) {
	home, _, _ := sessionCLIHome(t)
	livePath := writeCLIFixture(t, filepath.Join(home, ".hermes", "state.db"))

	code, stdout, stderr := captureOutput(t, func() int {
		return sessionBrowseE([]string{"--db", livePath})
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	if !strings.Contains(stdout, "Source: direct database "+livePath) {
		t.Errorf("stdout = %q, want the direct-database provenance line", stdout)
	}
	if !strings.Contains(stdout, "Sessions: 2 of 2") {
		t.Errorf("stdout = %q, want the fixture's sessions", stdout)
	}
}

// TestSessionBrowseSnapshotOverrides covers --limit, --snapshot-dir, and the
// age bound (default rejects a stale snapshot; 0 accepts it).
func TestSessionBrowseSnapshotOverrides(t *testing.T) {
	_, _, _ = sessionCLIHome(t)
	altDir := t.TempDir()
	now := time.Now().Truncate(time.Second)
	writeCLISnapshot(t, altDir, now.Add(-time.Hour), writeCLIFixture(t, filepath.Join(t.TempDir(), "state.db")))
	staleDir := t.TempDir()
	writeCLISnapshot(t, staleDir, now.Add(-48*time.Hour), writeCLIFixture(t, filepath.Join(t.TempDir(), "state.db")))

	t.Run("limit", func(t *testing.T) {
		code, stdout, stderr := captureOutput(t, func() int {
			return sessionBrowseE([]string{"--snapshot-dir", altDir, "--limit", "1"})
		})
		if code != 0 {
			t.Fatalf("exit = %d, want 0 (stderr: %s)", code, stderr)
		}
		if !strings.Contains(stdout, "Sessions: 1 of 2") {
			t.Errorf("stdout = %q, want the limit applied", stdout)
		}
	})

	t.Run("stale snapshot rejected by default", func(t *testing.T) {
		code, _, stderr := captureOutput(t, func() int {
			return sessionBrowseE([]string{"--snapshot-dir", staleDir})
		})
		if code != 1 {
			t.Fatalf("exit = %d, want 1 for a stale snapshot", code)
		}
		if !strings.Contains(stderr, "too old") {
			t.Errorf("stderr = %q, want the age-bound error", stderr)
		}
	})

	t.Run("age bound disabled", func(t *testing.T) {
		code, stdout, stderr := captureOutput(t, func() int {
			return sessionBrowseE([]string{"--snapshot-dir", staleDir, "--max-snapshot-age", "0"})
		})
		if code != 0 {
			t.Fatalf("exit = %d, want 0 with the age bound disabled (stderr: %s)", code, stderr)
		}
		if !strings.Contains(stdout, "Sessions: 2 of 2") {
			t.Errorf("stdout = %q, want the snapshot read despite its age", stdout)
		}
	})
}

// TestSessionUsageDocumentsSnapshotSource keeps the help text honest about the
// read source and the override flags.
func TestSessionUsageDocumentsSnapshotSource(t *testing.T) {
	_, _, _ = sessionCLIHome(t)
	code, _, stderr := captureOutput(t, func() int { return runSessionCmdE([]string{"--help"}) })
	if code != 0 {
		t.Fatalf("session --help exit = %d, want 0", code)
	}
	for _, want := range []string{"browse", "state-backups", "ATTACH", "--db", "--snapshot-dir", "--max-snapshot-age"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("usage = %q, want it to mention %q", stderr, want)
		}
	}
}
