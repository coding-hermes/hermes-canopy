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
package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)
)

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

// Reader provides read-only access to a Hermes state.db.
type Reader struct {
	db      *sql.DB
	warn    *log.Logger                // degradation warnings; nil discards them
	schemas map[string]map[string]bool // table -> observed column set
}

// readerTables are the state.db tables the reader consumes, in
// introspection order.
var readerTables = [...]string{"sessions", "messages", "async_delegations"}

// OpenReader opens path strictly read-only. mode=ro guarantees the live
// Hermes store is never mutated; busy_timeout keeps queries from failing
// with SQLITE_BUSY while Hermes writes concurrently.
//
// On open the columns of sessions, messages, and async_delegations are
// introspected (PRAGMA table_info). A missing table or column never fails
// the open — each list method degrades per its own contract, so one
// old-schema state.db cannot break an import run.
func OpenReader(path string) (*Reader, error) {
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(10000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("session: open %s: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("session: open %s: %w", path, err)
	}
	r := &Reader{db: db, warn: log.Default()}
	r.schemas = r.introspect()
	return r, nil
}

// SetWarnLogger routes degradation warnings to l (e.g. a test buffer or a
// CLI stderr writer). Passing a nil logger discards warnings.
func (r *Reader) SetWarnLogger(l *log.Logger) {
	if r == nil {
		return
	}
	r.warn = l
}

// Close releases the underlying database handle.
func (r *Reader) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
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
		rows, err := r.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
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
			rows.Close()
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
		FROM sessions
		%s`,
		r.optExpr("sessions", "source", "''"),
		r.optExpr("sessions", "display_name", "NULL"),
		r.optExpr("sessions", "title", "NULL"),
		r.optExpr("sessions", "model", "NULL"),
		r.optExpr("sessions", "started_at", "NULL"),
		r.optExpr("sessions", "ended_at", "NULL"),
		r.optExpr("sessions", "archived", "0"),
		r.optExpr("sessions", "parent_session_id", "NULL"),
		orderClause,
	)
	if !r.hasCol("sessions", "source") {
		r.warnf("session: state.db sessions table has no source column " +
			"(older hermes-agent schema); importing with empty Source values")
	}
	rows, err := r.db.QueryContext(ctx, query)
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
		FROM messages
		WHERE session_id = ?%s
		%s`,
		r.optExpr("messages", "content", "NULL"),
		r.optExpr("messages", "tool_name", "NULL"),
		r.optExpr("messages", "token_count", "NULL"),
		r.optExpr("messages", "timestamp", "0"),
		activeFilter,
		orderClause,
	)
	rows, err := r.db.QueryContext(ctx, query, sessionID)
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
		FROM async_delegations
		%s`,
		r.optExpr("async_delegations", "parent_session_id", "NULL"),
		r.optExpr("async_delegations", "task_json", "NULL"),
		orderClause,
	)
	rows, err := r.db.QueryContext(ctx, query)
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
