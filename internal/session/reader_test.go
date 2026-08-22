package session

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver for the fixture DBs
)

// --- Old/minimal-schema fixture helpers (IMP-008) -----------------------------
//
// The main importer tests build the CURRENT schema (fixtureSchema). These
// helpers deliberately build OLDER/MINIMAL state.db shapes so the reader's
// degradation paths can be proven against real SQLite files, not hand-waved.

// openTestReader opens a Reader over path and wires warnings into a captured
// log buffer so tests can assert degradation warnings fired.
func openTestReader(t *testing.T, path string) (*Reader, *strings.Builder) {
	t.Helper()
	r, err := OpenReader(path)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	var buf strings.Builder
	r.SetWarnLogger(log.New(&buf, "", 0))
	return r, &buf
}

// execFixture creates a temp SQLite file, runs schema then inserts, and
// returns the path.
func execFixture(t *testing.T, schema string, insert func(conn *sql.DB)) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	conn, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Exec(schema); err != nil {
		t.Fatalf("create fixture schema: %v", err)
	}
	if insert != nil {
		insert(conn)
	}
	return path
}

// oldSessionsSchema is a pre-source-tracking sessions shape: no source,
// no display_name, ISO-8601 TEXT timestamps instead of REAL unix floats —
// the exact drift class that broke fixed-column SELECTs (IMP-008 board
// reference: BUG-034/035/037).
const oldSessionsSchema = `
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    title TEXT,
    model TEXT,
    started_at TEXT NOT NULL,
    ended_at TEXT,
    archived INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    role TEXT NOT NULL,
    content TEXT,
    tool_name TEXT,
    token_count INTEGER,
    timestamp TEXT NOT NULL,
    active INTEGER NOT NULL DEFAULT 1
);`

func TestReaderOldSchemaNoSourceColumn(t *testing.T) {
	path := execFixture(t, oldSessionsSchema, func(conn *sql.DB) {
		if _, err := conn.Exec(`INSERT INTO sessions (id, title, model, started_at)
			VALUES ('sess_old', 'Legacy Session', 'old-model', '2026-08-01T10:00:00Z')`); err != nil {
			t.Fatalf("insert session: %v", err)
		}
		if _, err := conn.Exec(`INSERT INTO messages (session_id, role, content, tool_name, token_count, timestamp)
			VALUES ('sess_old', 'user', 'hello legacy', 'legacy_tool', 42, '2026-08-01T10:01:00Z')`); err != nil {
			t.Fatalf("insert message: %v", err)
		}
	})
	r, warns := openTestReader(t, path)

	sessions, err := r.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions on no-source schema: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("want 1 session, got %d", len(sessions))
	}
	got := sessions[0]
	if got.Source != "" {
		t.Errorf("Source = %q, want empty on missing source column", got.Source)
	}
	if got.Title != "Legacy Session" {
		t.Errorf("Title = %q, want Legacy Session", got.Title)
	}
	want := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	if !got.StartedAt.Equal(want) {
		t.Errorf("StartedAt = %v, want %v (ISO-8601 text must parse)", got.StartedAt, want)
	}
	if got.Archived {
		t.Error("Archived = true, want false (archived column absent)")
	}
	if got.EndedAt != nil {
		t.Errorf("EndedAt = %v, want nil (column absent)", got.EndedAt)
	}
	if out := warns.String(); !strings.Contains(out, "no source column") {
		t.Errorf("warnings = %q, want a no-source degradation warning", out)
	}

	// Messages still read fine on this schema (all optional cols present).
	msgs, err := r.ListMessages(context.Background(), "sess_old")
	if err != nil {
		t.Fatalf("ListMessages on text-timestamp schema: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Content != "hello legacy" {
		t.Fatalf("messages = %+v, want one 'hello legacy' row", msgs)
	}
	if !msgs[0].Timestamp.Equal(want.Add(time.Minute)) {
		t.Errorf("message Timestamp = %v, want %v", msgs[0].Timestamp, want.Add(time.Minute))
	}
	if msgs[0].ToolName != "legacy_tool" {
		t.Errorf("ToolName = %q, want legacy_tool", msgs[0].ToolName)
	}
	if msgs[0].TokenCount == nil || *msgs[0].TokenCount != 42 {
		t.Errorf("TokenCount = %v, want 42", msgs[0].TokenCount)
	}
}

func TestReaderOldSchemaMessagesISOTextTimestamps(t *testing.T) {
	path := execFixture(t, oldSessionsSchema, func(conn *sql.DB) {
		inserts := []struct {
			ts      string
			role    string
			content string
		}{
			{"2026-08-01 10:00:00", "user", "sqlite-datetime form"},
			{"2026-08-01T10:01:00.500Z", "assistant", "rfc3339 millis"},
			{"2026-08-01T10:02:00+02:00", "user", "rfc3339 zone offset"},
		}
		for _, m := range inserts {
			if _, err := conn.Exec(
				`INSERT INTO messages (session_id, role, content, timestamp) VALUES (?, ?, ?, ?)`,
				"sess_ts", m.role, m.content, m.ts); err != nil {
				t.Fatalf("insert message: %v", err)
			}
		}
	})
	r, _ := openTestReader(t, path)

	msgs, err := r.ListMessages(context.Background(), "sess_ts")
	if err != nil {
		t.Fatalf("ListMessages with TEXT timestamps: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("want 3 messages, got %d (%+v)", len(msgs), msgs)
	}
	// All three TEXT forms parse to correct UTC instants, and the list is
	// re-ordered CHRONOLOGICALLY despite mixed TEXT representations (SQL
	// byte-wise ORDER BY would put "2026-08-01 10:.." before
	// "2026-08-01T.."; the zone-offset row is actually the earliest instant).
	for i, want := range []struct {
		form    string
		instant time.Time
	}{
		{"2026-08-01T10:02:00+02:00", time.Date(2026, 8, 1, 8, 2, 0, 0, time.UTC)}, // earliest instant
		{"2026-08-01 10:00:00", time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)},
		{"2026-08-01T10:01:00.500Z", time.Date(2026, 8, 1, 10, 1, 0, 500000000, time.UTC)},
	} {
		if got := msgs[i]; !got.Timestamp.Equal(want.instant) {
			t.Errorf("msgs[%d].Timestamp = %v, want %v (%s)", i, got.Timestamp, want.instant, want.form)
		}
	}
	// ...and stay in ascending (timestamp) order despite mixed forms.
	for i := 1; i < len(msgs); i++ {
		if msgs[i-1].Timestamp.After(msgs[i].Timestamp) {
			t.Errorf("messages out of order at %d: %v > %v", i, msgs[i-1].Timestamp, msgs[i].Timestamp)
		}
	}
}

func TestReaderMinimalDBOnlySessions(t *testing.T) {
	path := execFixture(t, `
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    title TEXT,
    model TEXT,
    started_at REAL NOT NULL
);`, func(conn *sql.DB) {
		if _, err := conn.Exec(`INSERT INTO sessions (id, title, model, started_at)
			VALUES ('sess_min', NULL, NULL, 1750000000.25)`); err != nil {
			t.Fatalf("insert session: %v", err)
		}
	})
	r, warns := openTestReader(t, path)

	ctx := context.Background()
	sessions, err := r.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions minimal schema: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("want 1 session, got %d", len(sessions))
	}
	got := sessions[0]
	if got.Title != "" || got.Model != "" || got.ParentSessionID != "" {
		t.Errorf("optional columns should degrade to empty: %+v", got)
	}
	if got.Source != "" || got.Archived || got.EndedAt != nil {
		t.Errorf("absent columns should yield zero values: %+v", got)
	}
	wantTS := time.Unix(1750000000, 250000000).UTC()
	if !got.StartedAt.Equal(wantTS) {
		t.Errorf("StartedAt = %v, want %v (REAL unix)", got.StartedAt, wantTS)
	}

	// No messages table → empty without error; importer-level flow below
	// proves the session still imports as a title-rooted tree.
	msgs, err := r.ListMessages(ctx, "sess_min")
	if err != nil {
		t.Fatalf("ListMessages with no messages table: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("want 0 messages, got %d", len(msgs))
	}
	delegations, err := r.ListDelegations(ctx)
	if err != nil {
		t.Fatalf("ListDelegations with no async_delegations table: %v", err)
	}
	if delegations != nil && len(delegations) != 0 {
		t.Errorf("want 0 delegations, got %d", len(delegations))
	}
	if out := warns.String(); !strings.Contains(out, "no messages table") ||
		!strings.Contains(out, "async_delegations") {
		t.Errorf("warnings = %q, want missing-table notes for messages+delegations", out)
	}

	// Importer end-to-end on the minimal DB: the session imports as a
	// tree whose root content is the derived title (mapper contract),
	// proving "no messages table" never blocks import.
	imp, trees, _, store := newTestImporter(t, path)
	sum, err := imp.Run(ctx, ImportOptions{})
	if err != nil {
		t.Fatalf("importer run on minimal db: %v", err)
	}
	if sum.SessionsImported != 1 || sum.NodesCreated != 0 {
		t.Fatalf("summary = %+v, want 1 imported / 0 nodes", sum)
	}
	if len(trees.calls) != 1 || trees.calls[0].RootContent != "Hermes session sess_min" {
		t.Fatalf("tree calls = %+v, want title-rooted tree", trees.calls)
	}
	if store.wm.LastSessionID != "sess_min" {
		t.Errorf("watermark = %+v, want advanced to sess_min", store.wm)
	}
}

func TestReaderEmptyDatabaseFile(t *testing.T) {
	path := execFixture(t, `SELECT 1;`, nil)
	r, warns := openTestReader(t, path)

	ctx := context.Background()
	if sessions, err := r.ListSessions(ctx); err != nil || len(sessions) != 0 {
		t.Fatalf("ListSessions on empty db: sessions=%d err=%v, want empty/no error", len(sessions), err)
	}
	if msgs, err := r.ListMessages(ctx, "x"); err != nil || len(msgs) != 0 {
		t.Fatalf("ListMessages on empty db: msgs=%d err=%v, want empty/no error", len(msgs), err)
	}
	if dels, err := r.ListDelegations(ctx); err != nil || len(dels) != 0 {
		t.Fatalf("ListDelegations on empty db: dels=%d err=%v, want empty/no error", len(dels), err)
	}
	if out := warns.String(); !strings.Contains(out, "sessions table") {
		t.Errorf("warnings = %q, want missing-sessions-table warning", out)
	}
}

// TestReaderNeverWritesFixture pins the mode=ro contract empirically: after
// OpenReader + every list method, the fixture file must be byte-identical
// (SHA-256) to before. A regression that opens writable or writes through
// the handle fails here.
func TestReaderNeverWritesFixture(t *testing.T) {
	path := execFixture(t, oldSessionsSchema, func(conn *sql.DB) {
		if _, err := conn.Exec(`INSERT INTO sessions (id, title, model, started_at)
			VALUES ('sess_ro', 'RO', 'm', '2026-08-01T10:00:00Z')`); err != nil {
			t.Fatalf("insert: %v", err)
		}
	})
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	sumBefore := sha256.Sum256(before)

	r, _ := openTestReader(t, path)
	ctx := context.Background()
	if _, err := r.ListSessions(ctx); err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if _, err := r.ListMessages(ctx, "sess_ro"); err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if _, err := r.ListDelegations(ctx); err != nil {
		t.Fatalf("ListDelegations: %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read fixture: %v", err)
	}
	sumAfter := sha256.Sum256(after)
	if sumBefore != sumAfter {
		t.Error("state.db bytes changed after read session — reader wrote to a mode=ro file")
	}
}

func TestParseTimestampForms(t *testing.T) {
	cases := []struct {
		in   any
		want time.Time
		ok   bool
	}{
		{float64(1750000000.25), time.Unix(1750000000, 250000000).UTC(), true},
		{int64(1750000000), time.Unix(1750000000, 0).UTC(), true},
		{"1750000000", time.Unix(1750000000, 0).UTC(), true}, // numeric TEXT
		{"2026-08-01T10:00:00Z", time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC), true},
		{"2026-08-01T10:00:00.123456789Z", time.Date(2026, 8, 1, 10, 0, 0, 123456789, time.UTC), true},
		{"2026-08-01 10:00:00", time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC), true},
		{"2026-08-01 10:00:00.5", time.Date(2026, 8, 1, 10, 0, 0, 500000000, time.UTC), true},
		{"2026-08-01 10:00:00+02:00", time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC), true},
		{"2026-08-01", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), true},
		{nil, time.Time{}, false},
		{"", time.Time{}, false},
		{"not-a-timestamp", time.Time{}, false},
	}
	for _, tc := range cases {
		got, ok := parseTimestamp(tc.in)
		if ok != tc.ok {
			t.Errorf("parseTimestamp(%#v) ok=%v, want %v", tc.in, ok, tc.ok)
			continue
		}
		if ok && !got.Equal(tc.want) {
			t.Errorf("parseTimestamp(%#v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestWatermarkRoundTripTextTimestamps pins the acceptance criterion that the
// watermark comparison stays stable when started_at came from TEXT cells.
// Persisting a watermark stores a float64 unix value, so any parsed timestamp
// passes through float64 quantization (~238ns ulp at epoch scale): exact
// nanosecond identity cannot survive the hop. The contract that matters is
// the importer's own: sessionAfter compares timeToUnix(s.StartedAt) against
// wm.LastStartedAt = timeToUnix(prev.StartedAt) — both sides apply the same
// conversion, so the comparison is exact iff timeToUnix∘parseTimestamp is
// idempotent at the float64 level. Pin that plus a one-ulp time tolerance.
func TestWatermarkRoundTripTextTimestamps(t *testing.T) {
	texts := []string{
		"2026-08-01T10:00:00Z",
		"2026-08-01 10:00:00",
		"2026-08-01T10:00:00.999999Z",
	}
	const maxQuantizationError = 2 * time.Duration(1<<22) // ~2 float64 ulps at epoch scale, in ns
	for _, s := range texts {
		parsed, ok := parseTimestamp(s)
		if !ok {
			t.Fatalf("parseTimestamp(%q) failed", s)
		}
		stored := timeToUnix(parsed)
		back, ok := parseTimestamp(stored)
		if !ok {
			t.Fatalf("parseTimestamp(%v) failed for %q", stored, s)
		}
		// Idempotence at the float64 level — this is what makes
		// sessionAfter's equality branch exact across runs.
		if again := timeToUnix(back); again != stored {
			t.Errorf("timeToUnix not idempotent for %q: %v != %v", s, again, stored)
		}
		if d := back.Sub(parsed); d > maxQuantizationError || d < -maxQuantizationError {
			t.Errorf("round-trip drift for %q: %v exceeds quantization bound %v", s, d, maxQuantizationError)
		}
	}
}

// TestReaderMissingOptionalColumnsStillImport proves the degradation contract
// end-to-end through the importer: an old-schema session (no source, TEXT
// timestamps) imports with a correct description built around empty Source.
func TestReaderMissingOptionalColumnsStillImport(t *testing.T) {
	path := execFixture(t, oldSessionsSchema, func(conn *sql.DB) {
		if _, err := conn.Exec(`INSERT INTO sessions (id, title, model, started_at, ended_at)
			VALUES ('sess_imp', 'Importable', 'm1', '2026-08-01T09:30:00Z', '2026-08-01T09:35:00Z')`); err != nil {
			t.Fatalf("insert: %v", err)
		}
		if _, err := conn.Exec(`INSERT INTO messages (session_id, role, content, timestamp)
			VALUES ('sess_imp', 'user', 'root question', '2026-08-01T09:30:05Z')`); err != nil {
			t.Fatalf("insert msg: %v", err)
		}
	})

	imp, trees, _, _ := newTestImporter(t, path)
	sum, err := imp.Run(context.Background(), ImportOptions{})
	if err != nil {
		t.Fatalf("importer run: %v", err)
	}
	if sum.SessionsImported != 1 {
		t.Fatalf("summary = %+v, want 1 imported", sum)
	}
	call := trees.calls[0]
	if call.Description == "" || !strings.Contains(call.Description, "source=") {
		t.Errorf("description = %q, want it to carry source= field", call.Description)
	}
	if !strings.Contains(call.Description, "started=2026-08-01T09:30:00Z") {
		t.Errorf("description = %q, want RFC3339 started from TEXT cell", call.Description)
	}
}
