// Package cardexport writes a deterministic, git-diffable JSONL snapshot of the
// per-type Canopy card stores (GAP-083 phase 1, owner ruling 2026-09-16).
//
// The card store itself stays where it is: one SQLite database per card type
// under card.DataDir() with the write-time invariants the runtime needs. This
// package is the export half of the row's ruling — it READS those databases and
// writes one compact JSON object per card, so card history can live in git and
// a human can diff two exports:
//
//	{"record":"card","card_type":"compact","card":{…},"events":[{…}]}
//
// Contract (pinned — it is the git-diff surface):
//
//   - One line per card, `\n`-terminated, no header, no blank lines, no
//     trailing newline beyond the last record's. An empty store writes zero
//     bytes.
//   - Stable total order: databases are visited in filename-stem order and the
//     cards inside each are ordered by id, i.e. `(card_type, id)` overall; each
//     card's events are ordered by `sequence` ascending.
//     `card_type` on the record is the DATABASE FILENAME STEM, never a
//     whitelist of known types, so a store whose stem is not one of the three
//     built-ins (`compact`, `expanded`, `iteration`) is still exported — see
//     the nested `card.card_type`, which is the value stored in the row.
//   - No volatile field anywhere: no export timestamp, no hostname, no tool
//     version, no absolute path. Two exports of an unchanged store are
//     byte-identical.
//   - Compact encoding with `SetEscapeHTML(false)`: Go escapes `<`, `>`, `&` by
//     default and that churn is exactly the diff noise this row removes.
//
// Read-only guarantee: every database is opened read-only through readOnlyDSN,
// and a write on that connection is refused by SQLite
// (`attempt to write a readonly database (8)`). The exporter never creates a
// database and never migrates. It also refuses to leave anything behind: a
// store whose WAL was already checkpointed (the shape after a clean close) is
// read with `immutable=1`, because a plain `mode=ro` connection to that shape
// would CREATE a 32 KiB `-shm` and a zero-length `-wal` and leave them in the
// data directory. A store that still has a WAL in use is read through it, since
// `immutable=1` there is a silent stale read — see readOnlyDSN for the
// measurements behind both halves.
package cardexport

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite" // CGo-free SQLite driver, same driver as internal/card

	"github.com/coding-hermes/hermes-canopy/internal/card"
)

// recordKindCard is the `record` discriminator of a card line. It exists so a
// future exportable record class (a board row, say) can be appended to the same
// stream without a second file format.
const recordKindCard = "card"

// dbSuffix is the only file suffix this package treats as a card database.
// `-wal` and `-shm` sidecars end in `-wal` / `-shm`, so they never match.
const dbSuffix = ".db"

// Options configures an Export.
type Options struct {
	// DataDir is the directory holding one `<card_type>.db` per card type —
	// normally card.DataDir() ($CANOPY_CARD_DATA_DIR, else
	// ~/.hermes/canopy/cards). Required: a missing directory is an error,
	// never an empty export.
	DataDir string

	// Out receives the JSONL export. Required. Nothing is written until at
	// least one record is encoded, so a caller that needs an atomic file can
	// point this at a `*.tmp` file and rename on success.
	Out io.Writer
}

// Result reports what an Export did. On failure the counts describe the work
// completed before the error.
type Result struct {
	// Files is the number of `*.db` files read.
	Files int
	// Cards is the number of card records written.
	Cards int
	// Events is the number of events written (nested under their cards).
	Events int
	// Bytes is the number of bytes written to Out.
	Bytes int
	// OrphanEvents counts event rows whose card_id has no row in `cards`.
	// They are NOT exported (a record is keyed by its card) and cannot be —
	// the count is reported so the loss is visible rather than silent.
	OrphanEvents int
	// UnparsedTimestamps counts timestamp columns this package could not parse
	// with any known layout. Such a value is exported as the zero time
	// (`0001-01-01T00:00:00Z`) so the row still appears; the count makes the
	// substitution visible instead of silent.
	UnparsedTimestamps int
}

// record is one JSONL line: a card and its events.
type record struct {
	Record   string           `json:"record"`
	CardType string           `json:"card_type"`
	Card     card.Card        `json:"card"`
	Events   []card.CardEvent `json:"events"`
}

// dbFile is one card database to export: its path and the card type the record
// carries (the filename stem).
type dbFile struct {
	Path string
	Type string
}

// Export writes the JSONL snapshot of every card database in opts.DataDir to
// opts.Out and returns what it wrote.
//
// It fails loudly — naming the path and the reason — for a data directory that
// does not exist, a path that is not a directory, a file that is not a SQLite
// database, a database without a `cards` table, and a row whose JSON columns
// cannot be represented. It never writes a partial line: a record is encoded
// into the encoder's buffer by encoding/json and flushed by a single Write.
func Export(ctx context.Context, opts Options) (Result, error) {
	var res Result

	if strings.TrimSpace(opts.DataDir) == "" {
		return res, errors.New("cardexport: no data directory given")
	}
	if opts.Out == nil {
		return res, errors.New("cardexport: no output writer given")
	}

	files, err := dbFiles(opts.DataDir)
	if err != nil {
		return res, err
	}

	out := &countingWriter{w: opts.Out}
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return res, fmt.Errorf("cardexport: %w", err)
		}
		if err := exportDB(ctx, out, f, &res); err != nil {
			return res, err
		}
		res.Files++
	}
	res.Bytes = out.n
	return res, nil
}

// dbFiles lists the card databases in dir, ordered by filename stem (the card
// type) and then by path. The order is the export's total order, so it must not
// depend on directory iteration order — os.ReadDir sorts by filename, and this
// sort makes the (stem, path) key explicit: with case-differing stems
// (`Compact.db` vs `compact.db`) the stem comparison alone would be ambiguous.
func dbFiles(dir string) ([]dbFile, error) {
	info, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("cardexport: data directory %s does not exist", dir)
		}
		return nil, fmt.Errorf("cardexport: stat data directory %s: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("cardexport: data directory %s is not a directory", dir)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("cardexport: read data directory %s: %w", dir, err)
	}

	var files []dbFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, dbSuffix) {
			continue // skips -wal/-shm sidecars and anything else
		}
		files = append(files, dbFile{
			Path: filepath.Join(dir, name),
			Type: strings.TrimSuffix(name, dbSuffix),
		})
	}

	sort.Slice(files, func(i, j int) bool {
		if files[i].Type != files[j].Type {
			return files[i].Type < files[j].Type
		}
		return files[i].Path < files[j].Path
	})
	return files, nil
}

// exportDB reads one database read-only and encodes its card records in id
// order.
//
// Every query is fully drained before the next one starts, so a single read-only
// connection is enough and the package never holds two result sets open at once
// (which is what makes MaxOpenConns(1) a deadlock; it is not set, and the code
// shape does not need it).
func exportDB(ctx context.Context, out io.Writer, f dbFile, res *Result) error {
	db, err := sql.Open("sqlite", readOnlyDSN(f.Path))
	if err != nil {
		return fmt.Errorf("cardexport: open %s: %w", f.Path, err)
	}
	defer func() { _ = db.Close() }()

	ok, err := hasTable(ctx, db, "cards")
	if err != nil {
		return fmt.Errorf("cardexport: read %s: %w", f.Path, err)
	}
	if !ok {
		return fmt.Errorf("cardexport: %s has no cards table — not a Canopy card database (nothing was written to it)", f.Path)
	}

	cards, err := readCards(ctx, db, f.Path, res)
	if err != nil {
		return err
	}

	// The events table is required by the migration, but its absence in a
	// hand-built or partially-created store means "no events", not a broken
	// store: every card then exports `"events":[]`. A database with no cards
	// table is refused above.
	events := map[string][]card.CardEvent{}
	if ok, err := hasTable(ctx, db, "events"); err != nil {
		return fmt.Errorf("cardexport: read %s: %w", f.Path, err)
	} else if ok {
		events, err = readEvents(ctx, db, f.Path, res)
		if err != nil {
			return err
		}
	}

	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)

	claimed := make(map[string]bool, len(cards))
	for _, c := range cards {
		id := c.ID.String()
		claimed[id] = true
		evs := events[id]
		if evs == nil {
			evs = []card.CardEvent{}
		}
		rec := record{Record: recordKindCard, CardType: f.Type, Card: c, Events: evs}
		if err := enc.Encode(rec); err != nil {
			return fmt.Errorf("cardexport: encode %s card %s: %w", f.Path, id, err)
		}
		res.Cards++
		res.Events += len(evs)
	}

	// Events whose card is not in this database are reported, never dropped
	// silently.
	for cardID, evs := range events {
		if !claimed[cardID] {
			res.OrphanEvents += len(evs)
		}
	}
	return nil
}

// readCards loads every card row in id order. It returns the cards as a slice so
// the caller can emit them in a stable order while events (which must be grouped
// by card id before the first line is written) are read afterwards.
func readCards(ctx context.Context, db *sql.DB, path string, res *Result) ([]card.Card, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+cardColumns+` FROM cards ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("cardexport: query %s cards: %w", path, err)
	}
	defer func() { _ = rows.Close() }()

	var cards []card.Card
	for rows.Next() {
		c, err := scanCard(rows, path, res)
		if err != nil {
			return nil, err
		}
		cards = append(cards, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cardexport: read %s cards: %w", path, err)
	}
	return cards, nil
}

// readEvents loads every event row, grouped by card id and ordered by sequence
// ascending (the schema's own ordering: `sequence` is the AUTOINCREMENT primary
// key, so it is both stable and the append order the store recorded).
func readEvents(ctx context.Context, db *sql.DB, path string, res *Result) (map[string][]card.CardEvent, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+eventColumns+` FROM events ORDER BY sequence ASC`)
	if err != nil {
		return nil, fmt.Errorf("cardexport: query %s events: %w", path, err)
	}
	defer func() { _ = rows.Close() }()

	events := map[string][]card.CardEvent{}
	for rows.Next() {
		e, err := scanEvent(rows, path, res)
		if err != nil {
			return nil, err
		}
		key := e.CardID.String()
		events[key] = append(events[key], e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cardexport: read %s events: %w", path, err)
	}
	return events, nil
}

// cardColumns and eventColumns are this package's own explicit column lists,
// written out by hand on purpose: internal/card's row scanners are unexported
// and a schema change must be a visible edit here, not an inherited one. The
// fixture-drift test builds its rows through the REAL writer (card.NewCardDBManager
// → Repository → Create → AppendEvent) and fails if these lists drift.
const (
	cardColumns = `id, tree_id, node_id, app_id, card_type, data, actions, status, ` +
		`context_hash, revision, created_at, updated_at, dismissed_at, archived_at`
	eventColumns = `sequence, event_id, card_id, event_type, actor_kind, actor_id, ` +
		`payload, created_at`
)

// scanCard reads one `cards` row into a card.Card, mirroring the reader
// semantics of internal/card for the tolerant columns (an unparsable tree_id /
// node_id is the zero UUID, malformed actions JSON would be a lie and is an
// error).
func scanCard(rows *sql.Rows, path string, res *Result) (card.Card, error) {
	var (
		id, treeID, nodeID, appID, cardType, dataJSON, actionsJSON string
		status, contextHash                                        string
		revision                                                   int64
		createdAt, updatedAt                                       string
		dismissedAt, archivedAt                                    sql.NullString
	)
	if err := rows.Scan(&id, &treeID, &nodeID, &appID, &cardType, &dataJSON, &actionsJSON,
		&status, &contextHash, &revision, &createdAt, &updatedAt, &dismissedAt, &archivedAt); err != nil {
		return card.Card{}, fmt.Errorf("cardexport: scan %s cards row: %w", path, err)
	}

	cardID, err := uuid.Parse(id)
	if err != nil {
		return card.Card{}, fmt.Errorf("cardexport: %s cards row id %q: %w", path, id, err)
	}

	var treeUUID, nodeUUID uuid.UUID
	if treeID != "" {
		treeUUID, _ = uuid.Parse(treeID)
	}
	if nodeID != "" {
		nodeUUID, _ = uuid.Parse(nodeID)
	}

	data, err := rawJSON(dataJSON)
	if err != nil {
		return card.Card{}, fmt.Errorf("cardexport: %s card %s data: %w", path, id, err)
	}

	actions := []card.CardAction{}
	if trimmed := strings.TrimSpace(actionsJSON); trimmed != "" && trimmed != "null" {
		if err := json.Unmarshal([]byte(trimmed), &actions); err != nil {
			return card.Card{}, fmt.Errorf("cardexport: %s card %s actions: %w", path, id, err)
		}
		if actions == nil {
			actions = []card.CardAction{}
		}
	}

	created := parseTimestamp(createdAt, res)
	updated := parseTimestamp(updatedAt, res)

	return card.Card{
		ID:          cardID,
		TreeID:      treeUUID,
		NodeID:      nodeUUID,
		AppID:       appID,
		CardType:    card.CardType(cardType),
		Data:        data,
		Actions:     actions,
		Status:      card.CardStatus(status),
		ContextHash: contextHash,
		Revision:    revision,
		CreatedAt:   created,
		UpdatedAt:   updated,
		DismissedAt: nullableTimestamp(dismissedAt, res),
		ArchivedAt:  nullableTimestamp(archivedAt, res),
	}, nil
}

// scanEvent reads one `events` row into a card.CardEvent.
func scanEvent(rows *sql.Rows, path string, res *Result) (card.CardEvent, error) {
	var (
		seq                                                     int64
		eventID, cardID, eventType, actorKind, actorID, payload string
		createdAt                                               string
	)
	if err := rows.Scan(&seq, &eventID, &cardID, &eventType, &actorKind, &actorID, &payload, &createdAt); err != nil {
		return card.CardEvent{}, fmt.Errorf("cardexport: scan %s events row: %w", path, err)
	}

	eid, err := uuid.Parse(eventID)
	if err != nil {
		return card.CardEvent{}, fmt.Errorf("cardexport: %s event %q event_id: %w", path, eventID, err)
	}
	cid, err := uuid.Parse(cardID)
	if err != nil {
		return card.CardEvent{}, fmt.Errorf("cardexport: %s event %q card_id: %w", path, eventID, err)
	}

	raw, err := rawJSON(payload)
	if err != nil {
		return card.CardEvent{}, fmt.Errorf("cardexport: %s event %s payload: %w", path, eventID, err)
	}
	created := parseTimestamp(createdAt, res)

	return card.CardEvent{
		Sequence:  seq,
		EventID:   eid,
		CardID:    cid,
		EventType: card.CardEventType(eventType),
		ActorKind: card.CardActorKind(actorKind),
		ActorID:   actorID,
		Payload:   raw,
		CreatedAt: created,
	}, nil
}

// hasTable reports whether the database has a table of that name.
func hasTable(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// rawJSON normalises a JSON text column into a json.RawMessage that is safe to
// embed. The stored bytes are emitted verbatim (byte fidelity to the store, so a
// canonicalising step cannot silently rewrite history), EXCEPT that an empty or
// whitespace-only column becomes `null`: a non-nil zero-length RawMessage is
// JSON poison — `json.Marshal` of it fails the whole enclosing document with
// `unexpected end of JSON input`.
func rawJSON(s string) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return json.RawMessage("null"), nil
	}
	if !json.Valid([]byte(trimmed)) {
		return nil, errors.New("value is not valid JSON")
	}
	return json.RawMessage(trimmed), nil
}

// timestampLayouts are the layouts parseTimestamp accepts, most precise first.
// The first two are what internal/card writes (RFC3339Nano) and its fallback;
// the last two cover a row inserted by hand or by SQLite's own
// `datetime('now')`.
var timestampLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05",
}

// parseTimestamp parses a TEXT timestamp column and normalises it to UTC, which
// keeps two exports of the same row byte-identical regardless of the offset the
// writer recorded. A value no layout matches is exported as the zero time and
// counted in Result.UnparsedTimestamps instead of failing the whole export: the
// row is still exported, and substituting one column's depth for the whole
// database's absence is never worth a silent lie — the count makes it visible.
func parseTimestamp(s string, res *Result) time.Time {
	if t, ok := parseAnyTimestamp(s); ok {
		return t
	}
	res.UnparsedTimestamps++
	return time.Time{}
}

// nullableTimestamp maps a NULL-able timestamp column onto the pointer field of
// card.Card. An empty string is treated as NULL: the schema makes the column
// NULL rather than empty, but a hand-written row can hold an empty value.
func nullableTimestamp(v sql.NullString, res *Result) *time.Time {
	if !v.Valid || strings.TrimSpace(v.String) == "" {
		return nil
	}
	t := parseTimestamp(v.String, res)
	return &t
}

// parseAnyTimestamp returns the parsed timestamp and whether any known layout
// matched.
func parseAnyTimestamp(s string) (time.Time, bool) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return time.Time{}, false
	}
	for _, layout := range timestampLayouts {
		if t, err := time.Parse(layout, trimmed); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// readOnlyDSN returns the DSN a card database is opened with, chosen from that
// store's own on-disk state.
//
// The `file:` prefix is load-bearing. modernc.org/sqlite opens with
// SQLITE_OPEN_URI and hands the whole DSN to SQLite only when it starts with
// `file:`; a DSN that does not is split at `?`, the query string is consumed by
// the Go driver (which knows `_pragma`, `_busy_timeout`, … but not `mode`), and
// `mode=ro` is silently dropped — the connection is then READ-WRITE. Verified
// against modernc.org/sqlite v1.58.0: with the prefix an INSERT and a
// CREATE TABLE both fail with `attempt to write a readonly database (8)`;
// without it both succeed.
//
// Two further measurements (same driver, SQLite 3.53.4) decide between the two
// read-only forms, because neither alone satisfies the read-only requirement:
//
//   - `mode=ro` against a store whose WAL was already checkpointed — the normal
//     shape after a clean close, i.e. `compact.db` on its own — is not a
//     passive read: SQLite creates `compact.db-shm` (32 KiB) and a zero-length
//     `compact.db-wal` and leaves them behind. An exporter must not add files to
//     the store it is reading.
//   - `immutable=1` against a store whose committed data is still in the WAL is
//     a SILENT STALE READ: with ~90 KiB of uncheckpointed WAL an immutable
//     connection answered `no such table: cards`, because immutable promises the
//     file can never change and SQLite therefore ignores the WAL completely.
//
// So the WAL is read when the store has one in use — a `-shm` (SQLite's live
// WAL index) or a `-wal` that holds frames — and in that case the sidecars
// already exist, so the connection adds nothing. Otherwise every committed byte
// is already in the main file and the export reads it immutably: no `-shm`, no
// `-wal`, no locks, nothing created. The one case that still creates a file is a
// leftover `-wal` holding frames with no `-shm` (an unclean exit): the WAL must
// be read, and SQLite rebuilds the index — a repair of SQLite's own bookkeeping,
// never a change to the store's data.
func readOnlyDSN(path string) string {
	if walInUse(path) {
		return "file:" + escapeURIPath(path) + "?mode=ro&_busy_timeout=5000"
	}
	return "file:" + escapeURIPath(path) + "?mode=ro&immutable=1&_busy_timeout=5000"
}

// walInUse reports whether SQLite must read this store through its write-ahead
// log: a `-wal` holding frames, or a `-shm` index, means committed data may live
// outside the main database file.
func walInUse(path string) bool {
	if info, err := os.Stat(path + "-wal"); err == nil && info.Size() > 0 {
		return true
	}
	if _, err := os.Stat(path + "-shm"); err == nil {
		return true
	}
	return false
}

// escapeURIPath percent-escapes the characters that would otherwise terminate or
// reinterpret a SQLite URI filename. A literal `/` is deliberately NOT escaped:
// SQLite reads `%2F` as a filename character, not a path separator, so escaping
// it would turn an absolute path into a single-component name.
func escapeURIPath(path string) string {
	if !strings.ContainsAny(path, "%?# ") {
		return path
	}
	var b strings.Builder
	b.Grow(len(path) + 8)
	for i := 0; i < len(path); i++ {
		switch path[i] {
		case '%':
			b.WriteString("%25")
		case '?':
			b.WriteString("%3F")
		case '#':
			b.WriteString("%23")
		case ' ':
			b.WriteString("%20")
		default:
			b.WriteByte(path[i])
		}
	}
	return b.String()
}

// countingWriter counts the bytes handed to the wrapped writer so Result can
// report the export size without a second pass.
type countingWriter struct {
	w io.Writer
	n int
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += n
	return n, err
}
