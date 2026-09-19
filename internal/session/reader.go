// Package session implements ingestion of Hermes sessions into Canopy
// trees (WIRE-003). It reads the live Hermes SQLite session store
// (~/.hermes/state.db) strictly read-only, maps each session to a Canopy
// tree (messages to nodes), and imports new sessions incrementally so
// re-runs never duplicate.
//
// Layout:
//
//   - Reader     — read-only SQLite access to the Hermes state.db schema
//     (sessions + messages tables), via the pure-Go modernc.org/sqlite
//     driver (no CGO).
//   - MapSession — pure mapping of one session + its messages onto a tree
//     spec (title, description, root content, child nodes).
//   - Importer   — orchestrates read → map → create through small service
//     interfaces (TreeCreator / NodeCreator), and persists an incremental
//     watermark.
//
// Mapping rules (documented contract):
//
//   - One session → one tree. Title = session title, else display_name,
//     else "Hermes session <id>" (truncated to 200 chars).
//   - Description = "Imported Hermes session <id> · model=<model> ·
//     source=<source> · started=<RFC3339>" (truncated to 2000 chars).
//   - Root node content = the session's first non-empty user message
//     (truncated to 100000 chars), or the derived title when no such
//     message exists. Root node type "message", content format "markdown".
//   - Remaining messages become child nodes in (timestamp, id) order,
//     chained with "reply" edges. Content is role-tagged
//     (**user:** / **assistant:** / **tool (name):** / **system:**) and
//     truncated to 4000 chars with a "… (truncated)" suffix. Node metadata
//     carries {"session_id", "role", "message_id", "tool_name",
//     "token_count"}. Inactive (active=0) messages are excluded, mirroring
//     what Hermes shows in the live session.
//   - Archived sessions (archived=1) are skipped unless IncludeArchived is
//     set.
//   - Incremental: a watermark (last imported session id + started_at) is
//     persisted to a JSON state file. A session is new iff its
//     (started_at, id) pair sorts strictly after the watermark. The
//     watermark advances only for imported sessions — a session skipped as
//     archived remains eligible for a later --include-archived run as long
//     as no newer session has been imported in the meantime.
//
// Schema tolerance (IMP-008, hermes-webui pattern): state.db evolves between
// agent versions (source column, messages table, ISO-8601 text timestamps,
// indexes). Fixed-column SELECTs break imports on older/minimal schemas —
// BUG-034/035/037 were content edge cases of the same drift class. Every
// table read here is therefore introspected at open time via PRAGMA
// table_info and its SELECT built from the observed column set, with
// NULL-fallback expressions for missing optional columns. Degradation paths:
//
//   - missing sessions.source      → warn + Source="" (only feeds the tree
//     description; whole sessions are NOT skipped)
//   - missing other session cols   → NULL-equivalent zero values
//   - no messages table            → ListMessages returns empty, sessions
//     import as title-rooted trees
//   - ISO-8601 TEXT timestamps     → parsed alongside SQLite REAL unix floats
//   - missing idx_messages_session → irrelevant: mode=ro forbids the index
//     self-heal (it writes), so queries simply run un-indexed
//
// Read sources (GAP-077). The production source is the 6-hourly Hermes state
// SNAPSHOT, not the live database: OpenSnapshotReader resolves the newest
// file under ~/.hermes/state-backups (snapshot.go documents the naming,
// ordering, recency and failure rules), decompresses it to a private 0444
// copy when it is compressed, and reads it through a read-only
// ATTACH DATABASE against a scratch in-memory primary connection. The live
// state.db is never opened by that path, so a Canopy import cannot take a
// lock on — or write to — the file the gateway is holding open.
// OpenReader(path) remains for the explicit --db override and tests: it opens
// exactly the path it is given, read-only, and never substitutes another file.
package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)
)

// attachedSchema is the schema alias the snapshot database is ATTACHed as
// (the GAP-077 contract spells it `ATTACH DATABASE 'file:<snapshot>?mode=ro'
// AS hermes`). Every snapshot-mode query is written against it, which is what
// keeps the scratch primary (:memory:) from ever being mistaken for the
// source.
const attachedSchema = "hermes"

// Session is one row from the Hermes sessions table (schema subset).
type Session struct {
	ID              string
	Source          string
	DisplayName     string
	Title           string
	Model           string
	StartedAt       time.Time
	EndedAt         *time.Time
	Archived        bool
	ParentSessionID string // sessions.parent_session_id; empty when NULL (WIRE-006)
}

// Message is one row from the Hermes messages table (schema subset).
type Message struct {
	ID         int64
	SessionID  string
	Role       string
	Content    string
	ToolName   string
	TokenCount *int
	Timestamp  time.Time
}

// querier is the read surface both reader modes use: a pooled *sql.DB for a
// direct open, or a single dedicated *sql.Conn that carries the ATTACH (and
// the query_only pragma) for a snapshot open.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Reader provides read-only access to a Hermes state database, either directly
// (OpenReader) or through a materialized snapshot (OpenSnapshotReader).
type Reader struct {
	// db owns the connection the queries run on. In snapshot mode it is a
	// one-connection scratch pool whose only connection is held by q, so a
	// caller must never query db directly there: it would block on the
	// exhausted pool and would not see the attached schema anyway.
	db      *sql.DB
	conn    *sql.Conn                  // dedicated connection, snapshot mode only
	q       querier                    // db (direct open) or conn (snapshot)
	schema  string                     // SQL schema qualifier: "" (main) or attachedSchema
	source  *snapshotSource            // materialized snapshot, snapshot mode only
	spec    SnapshotSpec               // resolved snapshot metadata (zero for direct)
	warn    *log.Logger                // degradation warnings; nil discards them
	schemas map[string]map[string]bool // table -> observed column set
}

// readerTables are the state.db tables the reader consumes, in
// introspection order.
var readerTables = [...]string{"sessions", "messages", "async_delegations"}

// OpenReader opens path strictly read-only. mode=ro guarantees the live
// Hermes store is never mutated; busy_timeout keeps queries from failing
// with SQLITE_BUSY while Hermes writes concurrently. query_only is a second,
// SQL-level guard: no statement on this handle can write, whatever the file
// mode would allow.
//
// This is the explicit single-file override (CLI --db, tests). The production
// path is OpenSnapshotReader, which reads the 6-hourly snapshot instead.
//
// On open the columns of sessions, messages, and async_delegations are
// introspected (PRAGMA table_info). A missing table or column never fails
// the open — each list method degrades per its own contract, so one
// old-schema state.db cannot break an import run.
func OpenReader(path string) (*Reader, error) {
	dsn := SnapshotURI(path) + "?mode=ro&_pragma=busy_timeout(10000)&_pragma=query_only(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("session: open %s: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("session: open %s: %w", path, err)
	}
	r := &Reader{db: db, q: db, warn: log.Default()}
	r.schemas = r.introspect()
	return r, nil
}

// OpenSnapshotReader opens the newest usable Hermes state snapshot under
// opts.Dir (default ~/.hermes/state-backups) as the read source (GAP-077).
//
// It never opens the live ~/.hermes/state.db: the snapshot is resolved
// (ResolveSnapshot), decompressed to a private read-only copy when needed, and
// ATTACHed read-only:
//
//	ATTACH DATABASE 'file:<copy>?mode=ro[&immutable=1]' AS hermes
//
// All queries are then written against hermes.<table>. A missing/unreadable
// snapshot directory, no matching snapshot, or an out-of-date newest snapshot
// is returned as an error (ErrSnapshotDir / ErrNoSnapshot / ErrSnapshotStale)
// — there is no silent fallback. Callers that want a specific file pass it to
// OpenReader instead.
func OpenSnapshotReader(opts SnapshotOptions) (*Reader, error) {
	spec, err := ResolveSnapshot(opts)
	if err != nil {
		return nil, err
	}
	source, err := spec.Materialize(opts)
	if err != nil {
		return nil, err
	}
	r, err := openAttachedReader(source, opts.Warn)
	if err != nil {
		_ = source.Close()
		return nil, err
	}
	return r, nil
}

// openAttachedReader opens the prepared snapshot as a read-only ATTACH against
// a scratch in-memory primary database. A single dedicated connection carries
// the ATTACH and the query_only pragma, so no pooled-connection recycling can
// ever produce a handle without them.
func openAttachedReader(source *snapshotSource, warn *log.Logger) (*Reader, error) {
	if source == nil {
		return nil, errors.New("session: snapshot source is nil")
	}
	// The primary database is scratch space only; the queried data lives in
	// the attached snapshot. It must not be a file: nothing here needs to
	// persist, and a file primary would be a write target.
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, fmt.Errorf("session: open scratch db: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("session: reserve scratch connection: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA busy_timeout(10000)"); err != nil {
		_ = conn.Close()
		_ = db.Close()
		return nil, fmt.Errorf("session: scratch pragma: %w", err)
	}
	uri := SnapshotURI(source.Path) + "?mode=ro"
	if source.Immutable {
		// The copy is private and nothing else can write it, so SQLite may
		// skip locking entirely.
		uri += "&immutable=1"
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("ATTACH DATABASE '%s' AS %s", uri, attachedSchema)); err != nil {
		_ = conn.Close()
		_ = db.Close()
		// ATTACH itself validates the file header, so a torn, truncated or
		// non-SQLite snapshot fails right here.
		return nil, fmt.Errorf("%w: %s: %v", ErrSnapshotInvalid, source.Path, err)
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA query_only(1)"); err != nil {
		_ = conn.Close()
		_ = db.Close()
		return nil, fmt.Errorf("session: snapshot query_only: %w", err)
	}
	r := &Reader{
		db:     db,
		conn:   conn,
		q:      conn,
		schema: attachedSchema,
		source: source,
		spec:   source.Spec,
		warn:   log.Default(),
	}
	if warn != nil {
		r.warn = warn
	}
	if err := r.verifyReadable(ctx); err != nil {
		_ = conn.Close()
		_ = db.Close()
		return nil, err
	}
	r.schemas = r.introspect()
	return r, nil
}

// verifyReadable proves the attached file really is a SQLite database before
// any query is trusted. A torn snapshot (the backup timer mid-write) fails
// here with ErrSnapshotInvalid instead of degrading into "no sessions".
func (r *Reader) verifyReadable(ctx context.Context) error {
	var version int
	if err := r.q.QueryRowContext(ctx, fmt.Sprintf("PRAGMA %s.schema_version", r.schema)).Scan(&version); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrSnapshotInvalid, r.spec.Path, err)
	}
	return nil
}

// Snapshot reports the resolved snapshot a snapshot-mode reader is reading.
// ok is false for a direct OpenReader, whose data comes from the path the
// caller passed.
func (r *Reader) Snapshot() (SnapshotSpec, bool) {
	if r == nil || r.source == nil {
		return SnapshotSpec{}, false
	}
	return r.spec, true
}

// SetWarnLogger routes degradation warnings to l (e.g. a test buffer or a
// CLI stderr writer). Passing a nil logger discards warnings.
func (r *Reader) SetWarnLogger(l *log.Logger) {
	if r == nil {
		return
	}
	r.warn = l
}

// Close releases the underlying database handle and removes the private
// snapshot copy, if one was materialized.
func (r *Reader) Close() error {
	if r == nil {
		return nil
	}
	var errs []error
	if r.conn != nil {
		if err := r.conn.Close(); err != nil {
			errs = append(errs, err)
		}
		r.conn = nil
	}
	if r.db != nil {
		if err := r.db.Close(); err != nil {
			errs = append(errs, err)
		}
		r.db = nil
	}
	if r.source != nil {
		if err := r.source.Close(); err != nil {
			errs = append(errs, err)
		}
		r.source = nil
	}
	return errors.Join(errs...)
}

// table qualifies a table name for the active read source.
func (r *Reader) table(name string) string {
	if r.schema == "" {
		return name
	}
	return r.schema + "." + name
}

// tableInfo returns the introspection statement for a table, schema-qualified
// on the attached snapshot so the scratch primary can never answer for it.
func (r *Reader) tableInfo(name string) string {
	if r.schema == "" {
		return fmt.Sprintf("PRAGMA table_info(%s)", name)
	}
	return fmt.Sprintf("PRAGMA %s.table_info(%s)", r.schema, name)
}

// --- Schema introspection -----------------------------------------------------

// introspect returns {table -> set(columns)} for every table the reader
// consumes. A missing (or unqueryable) table maps to an empty non-nil set so
// the optional-column builder degrades instead of crashing. Mirrors the
// PRAGMA table_info(sessions) / table_info(messages) walk in the
// hermes-webui reference implementation.
func (r *Reader) introspect() map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(readerTables))
	for _, table := range readerTables {
		cols := map[string]bool{}
		rows, err := r.q.QueryContext(context.Background(), r.tableInfo(table))
		if err == nil {
			for rows.Next() {
				var cid, notNull, pk int
				var name, cType string
				var dflt sql.NullString
				if err := rows.Scan(&cid, &name, &cType, &notNull, &dflt, &pk); err == nil {
					cols[name] = true
				}
			}
			_ = rows.Err()
			_ = rows.Close()
		}
		out[table] = cols
	}
	return out
}

// hasCol reports whether table was observed to have col. A table absent
// from the schema reports false for every column.
func (r *Reader) hasCol(table, col string) bool {
	return r.schemas[table][col]
}

// hasTable reports whether table was observed in the schema.
func (r *Reader) hasTable(table string) bool {
	return len(r.schemas[table]) > 0
}

// optExpr builds the SELECT expression for an optional column: the bare
// column name when present in the introspected set, otherwise a literal
// fallback aliased to the column name (the _optional_col pattern from the
// hermes-webui reference). Single-table queries, so no alias qualification
// is needed.
func (r *Reader) optExpr(table, col, fallback string) string {
	if r.hasCol(table, col) {
		return col
	}
	return fmt.Sprintf("%s AS %s", fallback, col)
}

// warnf emits one degradation warning. Warnings are advisory: every
// degraded path below still returns usable data rather than an error.
func (r *Reader) warnf(format string, args ...any) {
	if r.warn != nil {
		r.warn.Printf(format, args...)
	}
}

// --- Timestamp parsing ---------------------------------------------------------

// timestampLayouts are the accepted ISO-8601 / SQLite datetime TEXT forms,
// tried in order. RFC3339(Nano) covers "2026-08-22T12:34:56[.fff][Z|±hh:mm]";
// the space-separated forms cover SQLite's own datetime strings
// ("YYYY-MM-DD HH:MM:SS[.fff]" with an optional trailing zone).
var timestampLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

// parseTimestamp parses a state.db timestamp cell of any stored type:
// SQLite REAL unix seconds (float64), INTEGER epoch seconds (int64), or
// ISO-8601 / datetime TEXT (string / []byte). Numeric-looking text is tried
// as unix seconds first (the reference implementation coerces numerics
// before falling back), then the text layouts above. Returns ok=false for
// NULL or anything unparseable; callers keep their zero value.
func parseTimestamp(v any) (time.Time, bool) {
	switch x := v.(type) {
	case nil:
		return time.Time{}, false
	case float64:
		return unixToTime(x), true
	case int64:
		return unixToTime(float64(x)), true
	case []byte:
		return parseTimestampText(string(x))
	case string:
		return parseTimestampText(x)
	case time.Time:
		return x.UTC(), true
	default:
		return time.Time{}, false
	}
}

// parseTimestampText parses TEXT timestamp cells: numeric unix-second
// strings first, then ISO-8601 / SQLite datetime layouts. All results are
// normalized to UTC.
func parseTimestampText(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return unixToTime(f), true
	}
	for _, layout := range timestampLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// unixToTime converts SQLite REAL unix seconds to UTC time.Time.
func unixToTime(f float64) time.Time {
	sec := int64(f)
	nsec := int64((f - float64(sec)) * 1e9)
	return time.Unix(sec, nsec).UTC()
}

// timeToUnix converts a time.Time back to SQLite REAL unix seconds.
// Round-trips exactly for values produced by unixToTime (both sides apply
// the same sec/nsec split), which keeps watermark comparisons exact. Values
// parsed from TEXT timestamps normalize through the same float representation.
func timeToUnix(t time.Time) float64 {
	return float64(t.Unix()) + float64(t.Nanosecond())/1e9
}

// --- Sessions ------------------------------------------------------------------

// ListSessions returns all sessions ordered by (started_at, id) ascending —
// a stable total order the importer uses to walk sessions oldest-first.
// Archived filtering is the importer's policy, so every row is returned.
//
// Degradation (IMP-008): only id is assumed present. Missing source warns
// and yields Source="" (source feeds just the tree description — sessions
// are NOT skipped); title/model/display_name/parent_session_id degrade to
// NULL-equivalents; missing ended_at leaves EndedAt nil; a missing archived
// column reads as not-archived. Without started_at the stable total order
// falls back to id ASC. A DB with no sessions table at all yields an empty
// result (nothing importable) instead of failing the run.
func (r *Reader) ListSessions(ctx context.Context) ([]Session, error) {
	if !r.hasTable("sessions") {
		r.warnf("session: state.db has no sessions table; nothing importable")
		return nil, nil
	}
	orderClause := "ORDER BY started_at ASC, id ASC"
	if !r.hasCol("sessions", "started_at") {
		orderClause = "ORDER BY id ASC"
	}
	query := fmt.Sprintf(`
		SELECT id, %s, %s, %s, %s, %s, %s, %s, %s
		FROM %s
		%s`,
		r.optExpr("sessions", "source", "''"),
		r.optExpr("sessions", "display_name", "NULL"),
		r.optExpr("sessions", "title", "NULL"),
		r.optExpr("sessions", "model", "NULL"),
		r.optExpr("sessions", "started_at", "NULL"),
		r.optExpr("sessions", "ended_at", "NULL"),
		r.optExpr("sessions", "archived", "0"),
		r.optExpr("sessions", "parent_session_id", "NULL"),
		r.table("sessions"),
		orderClause,
	)
	if !r.hasCol("sessions", "source") {
		r.warnf("session: state.db sessions table has no source column " +
			"(older hermes-agent schema); importing with empty Source values")
	}
	rows, err := r.q.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("session: list sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Session
	for rows.Next() {
		var s Session
		var displayName, title, model, parentSessionID sql.NullString
		var archived int
		var startedAt, endedAt any
		if err := rows.Scan(&s.ID, &s.Source, &displayName, &title, &model,
			&startedAt, &endedAt, &archived, &parentSessionID); err != nil {
			return nil, fmt.Errorf("session: scan session: %w", err)
		}
		s.DisplayName = displayName.String
		s.Title = title.String
		s.Model = model.String
		s.ParentSessionID = parentSessionID.String
		if t, ok := parseTimestamp(startedAt); ok {
			s.StartedAt = t
		}
		if t, ok := parseTimestamp(endedAt); ok {
			s.EndedAt = &t
		}
		s.Archived = archived != 0
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("session: list sessions: %w", err)
	}
	// Re-order in Go by the PARSED (started_at, id): SQL ORDER BY cannot
	// order mixed REAL/TEXT timestamp representations chronologically (TEXT
	// comparison is byte-wise, so "2026-08-01 10:.." and "2026-08-01T10:.."
	// interleave wrongly). The importer's incremental walk depends on true
	// oldest-first order — a byte-wise mis-order could advance the watermark
	// past a session that appears later in the list but happened earlier,
	// skipping it permanently.
	sortSessionOrder(out)
	return out, nil
}

// sortSessionOrder applies the (StartedAt, ID) ascending total order.
func sortSessionOrder(s []Session) {
	sort.SliceStable(s, func(i, j int) bool {
		if s[i].StartedAt.Equal(s[j].StartedAt) {
			return s[i].ID < s[j].ID
		}
		return s[i].StartedAt.Before(s[j].StartedAt)
	})
}

// --- Messages ------------------------------------------------------------------

// ListMessages returns the active messages of a session in (timestamp, id)
// ascending order.
//
// Degradation (IMP-008): when the messages TABLE itself is absent (very old
// schemas) or lacks a session_id column (messages cannot be attributed to
// sessions), it returns an empty slice without error — sessions still import
// as title-rooted trees. The board's "denormalized counts" fallback is not
// implementable here: this method returns []Message and Canopy nodes are
// built from message rows, so a pre-aggregated count cannot materialize
// content (nor does read-only mode allow writing one). When the table
// exists, individual missing columns fall back to NULL equivalents and a
// missing active column disables the active filter (all rows returned).
func (r *Reader) ListMessages(ctx context.Context, sessionID string) ([]Message, error) {
	if !r.hasTable("messages") {
		r.warnf("session: state.db has no messages table (older schema); "+
			"importing session %s without messages", sessionID)
		return nil, nil
	}
	if !r.hasCol("messages", "session_id") {
		r.warnf("session: state.db messages table has no session_id column; "+
			"importing session %s without messages", sessionID)
		return nil, nil
	}
	activeFilter := ""
	if r.hasCol("messages", "active") {
		activeFilter = " AND active = 1"
	}
	orderClause := "ORDER BY timestamp ASC, id ASC"
	if !r.hasCol("messages", "timestamp") {
		orderClause = "ORDER BY id ASC"
	}
	query := fmt.Sprintf(`
		SELECT id, session_id, role, %s, %s, %s, %s
		FROM %s
		WHERE session_id = ?%s
		%s`,
		r.optExpr("messages", "content", "NULL"),
		r.optExpr("messages", "tool_name", "NULL"),
		r.optExpr("messages", "token_count", "NULL"),
		r.optExpr("messages", "timestamp", "0"),
		r.table("messages"),
		activeFilter,
		orderClause,
	)
	rows, err := r.q.QueryContext(ctx, query, sessionID)
	if err != nil {
		return nil, fmt.Errorf("session: list messages for %s: %w", sessionID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []Message
	for rows.Next() {
		var m Message
		var content, toolName sql.NullString
		var tokenCount sql.NullInt64
		var ts any
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Role, &content, &toolName,
			&tokenCount, &ts); err != nil {
			return nil, fmt.Errorf("session: scan message: %w", err)
		}
		m.Content = content.String
		m.ToolName = toolName.String
		if tokenCount.Valid {
			v := int(tokenCount.Int64)
			m.TokenCount = &v
		}
		if t, ok := parseTimestamp(ts); ok {
			m.Timestamp = t
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("session: list messages for %s: %w", sessionID, err)
	}
	// Same mixed-representation caveat as ListSessions: TEXT timestamps of
	// differing forms do not sort chronologically in SQL, so the parsed
	// (timestamp, id) order is enforced here.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Timestamp.Equal(out[j].Timestamp) {
			return out[i].ID < out[j].ID
		}
		return out[i].Timestamp.Before(out[j].Timestamp)
	})
	return out, nil
}

// --- Delegations ---------------------------------------------------------------

// Delegation is one row from the Hermes async_delegations table (schema
// subset). The TaskGoal field is extracted from task_json->>'goal' at
// read time (best-effort — empty when the JSON is absent or malformed).
type Delegation struct {
	DelegationID    string
	OriginSession   string
	ParentSessionID string
	State           string
	TaskGoal        string
}

// ListDelegations returns all async_delegations rows ordered by
// dispatched_at ascending. The task goal is extracted from task_json
// (best-effort). Rows whose task_json is absent or malformed still
// appear — with an empty TaskGoal — so the caller never loses the
// delegation record itself.
//
// Degradation (IMP-008): when the async_delegations table is absent, an
// empty slice is returned without error (the importer already treats this
// as best-effort; returning empty keeps that explicit rather than
// error-shaped). Missing parent_session_id / task_json fall back to NULL
// equivalents; a missing dispatched_at degrades the order to delegation_id.
func (r *Reader) ListDelegations(ctx context.Context) ([]Delegation, error) {
	if !r.hasTable("async_delegations") {
		r.warnf("session: state.db has no async_delegations table; proceeding without delegations")
		return nil, nil
	}
	orderClause := "ORDER BY dispatched_at ASC, delegation_id ASC"
	if !r.hasCol("async_delegations", "dispatched_at") {
		orderClause = "ORDER BY delegation_id ASC"
	}
	query := fmt.Sprintf(`
		SELECT delegation_id, origin_session, %s, state, %s
		FROM %s
		%s`,
		r.optExpr("async_delegations", "parent_session_id", "NULL"),
		r.optExpr("async_delegations", "task_json", "NULL"),
		r.table("async_delegations"),
		orderClause,
	)
	rows, err := r.q.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("session: list delegations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Delegation
	for rows.Next() {
		var d Delegation
		var parentSessionID, taskJSON sql.NullString
		if err := rows.Scan(&d.DelegationID, &d.OriginSession, &parentSessionID,
			&d.State, &taskJSON); err != nil {
			return nil, fmt.Errorf("session: scan delegation: %w", err)
		}
		d.ParentSessionID = parentSessionID.String
		d.TaskGoal = extractDelegationGoal(taskJSON.String)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("session: list delegations: %w", err)
	}
	return out, nil
}

// extractDelegationGoal parses the "goal" field from a delegation's
// task_json column. The live Hermes schema stores the task payload as a
// JSON object with a top-level "goal" key (and sometimes a "goals"
// array). We extract "goal" only — it is always present on real rows
// and is the human-readable summary the association layer needs.
func extractDelegationGoal(taskJSON string) string {
	if taskJSON == "" {
		return ""
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(taskJSON), &raw); err != nil {
		return ""
	}
	goalBytes, ok := raw["goal"]
	if !ok {
		return ""
	}
	var goal string
	if err := json.Unmarshal(goalBytes, &goal); err != nil {
		return ""
	}
	return goal
}
