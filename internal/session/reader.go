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
package session

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no CGO)
)

// Session is one row from the Hermes sessions table (schema subset).
type Session struct {
	ID          string
	Source      string
	DisplayName string
	Title       string
	Model       string
	StartedAt   time.Time
	EndedAt     *time.Time
	Archived    bool
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
	db *sql.DB
}

// OpenReader opens path strictly read-only. mode=ro guarantees the live
// Hermes store is never mutated; busy_timeout keeps queries from failing
// with SQLITE_BUSY while Hermes writes concurrently.
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
	return &Reader{db: db}, nil
}

// Close releases the underlying database handle.
func (r *Reader) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}

// ListSessions returns all sessions ordered by (started_at, id) ascending —
// a stable total order the importer uses to walk sessions oldest-first.
// Archived filtering is the importer's policy, so every row is returned.
func (r *Reader) ListSessions(ctx context.Context) ([]Session, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, source, display_name, title, model, started_at, ended_at, archived
		FROM sessions
		ORDER BY started_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("session: list sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Session
	for rows.Next() {
		var s Session
		var displayName, title, model sql.NullString
		var endedAt sql.NullFloat64
		var archived int
		var startedAt float64
		if err := rows.Scan(&s.ID, &s.Source, &displayName, &title, &model,
			&startedAt, &endedAt, &archived); err != nil {
			return nil, fmt.Errorf("session: scan session: %w", err)
		}
		s.DisplayName = displayName.String
		s.Title = title.String
		s.Model = model.String
		s.StartedAt = unixToTime(startedAt)
		if endedAt.Valid {
			t := unixToTime(endedAt.Float64)
			s.EndedAt = &t
		}
		s.Archived = archived != 0
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("session: list sessions: %w", err)
	}
	return out, nil
}

// ListMessages returns the active messages of a session in (timestamp, id)
// ascending order.
func (r *Reader) ListMessages(ctx context.Context, sessionID string) ([]Message, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, session_id, role, content, tool_name, token_count, timestamp
		FROM messages
		WHERE session_id = ? AND active = 1
		ORDER BY timestamp ASC, id ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("session: list messages for %s: %w", sessionID, err)
	}
	defer func() { _ = rows.Close() }()

	var out []Message
	for rows.Next() {
		var m Message
		var content, toolName sql.NullString
		var tokenCount sql.NullInt64
		var ts float64
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
		m.Timestamp = unixToTime(ts)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("session: list messages for %s: %w", sessionID, err)
	}
	return out, nil
}

// unixToTime converts SQLite REAL unix seconds to UTC time.Time.
func unixToTime(f float64) time.Time {
	sec := int64(f)
	nsec := int64((f - float64(sec)) * 1e9)
	return time.Unix(sec, nsec).UTC()
}

// timeToUnix converts a time.Time back to SQLite REAL unix seconds.
// Round-trips exactly for values produced by unixToTime (both sides apply
// the same sec/nsec split), which keeps watermark comparisons exact.
func timeToUnix(t time.Time) float64 {
	return float64(t.Unix()) + float64(t.Nanosecond())/1e9
}
