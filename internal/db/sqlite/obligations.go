package sqlite

// The four Go obligations — the PostgreSQL objects that wave 1 could not turn into SQLite
// DDL (docs/SQLITE-PIVOT.md §5), as pure, testable functions. Each one is checked against
// the PostgreSQL behaviour it replaces, not just against itself:
//
//	PG object                                    → this file
//	uuidv7() / DEFAULT uuidv7()                  → NewID
//	set_node_sequence() / trg_node_sequence      → NextNodeSequence
//	set_content_hash() / trg_node_content_hash   → ContentHash
//	timestamptz column + clock_timestamp()       → EncodeTime / DecodeTime
//
// There is no SQLite equivalent for any of them: no extension catalogue, no stored
// functions, no assignable NEW.* in triggers (docs/SQLITE-PIVOT.md §3), and no hash
// function at all (`SELECT sha3('a',256)` → "no such function: sha3"). Keeping them as
// pure Go functions means the wave-2 repo layer has exactly one implementation to call —
// no SQL expression can re-introduce a second, divergent one.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// TimeLayout is the canonical timestamp shape of the SQLite store (docs/SQLITE-PIVOT.md
// §6, decision D1 (a)): RFC 3339, UTC, millisecond precision, `Z` suffix. It is the exact
// Go equivalent of the SQLite DDL's
// `strftime('%Y-%m-%dT%H:%M:%fZ', ...)` default, so a value written by Go and a value
// written by a DDL default are indistinguishable and compare correctly as plain TEXT.
//
// Layout reference: `05.000` is literal millisecond digits (not nines), and the trailing
// `Z` is a literal (a `Z07:00` marker would only be recognised before a `-0700`-style
// offset).
const TimeLayout = "2006-01-02T15:04:05.000Z"

// NewID returns a time-ordered RFC 9562 UUIDv7, the wave-2 replacement for PostgreSQL's
// `DEFAULT uuidv7()` (000001_extensions.up.sql:21, and every `DEFAULT gen_random_uuid()`).
// The SQLite DDL declares no default for any id column, so a forgotten id fails loudly as
// a NOT NULL violation instead of silently receiving a random value.
//
// Time ordering matters twice over: it gives the graph's inserts locality, and it is what
// orders `tree_events` rows written in the same transaction. github.com/google/uuid's v7
// constructor guarantees the (milliseconds, sequence) pair strictly increases between
// calls, so ids generated back-to-back in the same millisecond still sort in generation
// order.
func NewID() (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("sqlite: NewID: %w", err)
	}
	return id, nil
}

// EncodeTime renders t in the canonical store shape (TimeLayout): converted to UTC,
// truncated to milliseconds, with a `Z` suffix. Truncation is explicit rather than left to
// the layout so the result is unambiguous.
func EncodeTime(t time.Time) string {
	return t.UTC().Truncate(time.Millisecond).Format(TimeLayout)
}

// DecodeTime parses a value written by EncodeTime (or by an SQLite `strftime(...,'%f')`
// default). It is strict on purpose: only the canonical shape is accepted, so a value with
// a different precision, a numeric offset or a space separator fails loudly instead of
// being silently reinterpreted. The returned time is in UTC.
func DecodeTime(s string) (time.Time, error) {
	t, err := time.Parse(TimeLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("sqlite: DecodeTime %q: %w", s, err)
	}
	return t, nil
}

// ContentHash returns the lowercase hex SHA-256 of content's UTF-8 bytes — the wave-2
// replacement for `set_content_hash()` / `trg_node_content_hash`, and the acceptance
// property of docs/SQLITE-PIVOT.md §7: for the same content it is byte-identical to
// PostgreSQL's `encode(sha256(convert_to(content,'UTF8')),'hex')` (the corrected form from
// 000025_node_content_hash_utf8.up.sql), i.e. sha256 over the UTF-8 encoding, not over the
// database or client's native encoding.
//
// The `nodes.content_hash` column is TEXT NOT NULL with no default, so every write path
// must supply this value — a missing hash cannot pass as a real one.
func ContentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// NextNodeSequence returns the next `sequence_num` for a node in treeID:
// `COALESCE(MAX(sequence_num), 0) + 1` over the tree — the wave-2 replacement for
// `set_node_sequence()` / `trg_node_sequence` (000003_nodes.up.sql:38-52), which filled
// NEW.sequence_num inside a BEFORE INSERT trigger. SQLite cannot assign `NEW.<col>` in a
// trigger body and `sequence_num` is NOT NULL, so no trigger can produce this value and
// the caller must.
//
// The tx parameter is mandatory (not a *sql.DB) because the allocation is only meaningful
// inside the transaction that performs the INSERT: PG computed it in the same statement,
// and splitting the two would let a concurrent writer allocate the same number.
//
// PG filled the column only `WHEN (NEW.sequence_num IS NULL)`, i.e. a caller could supply
// an explicit sequence; this helper implements exactly that default path. A caller that has
// an explicit sequence does not call it.
//
// # Concurrency contract
//
// Single writer per transaction. SQLite permits one write transaction at a time and the
// store's busy_timeout (5000ms, D6) makes a contending writer wait for it rather than fail
// immediately. This helper takes no lock of its own — it reads inside the transaction it
// is given — so a caller that runs concurrent write transactions may observe SQLITE_BUSY
// as an ordinary error. Retry/serialization policy belongs to the wave-2 repo layer, not
// here.
func NextNodeSequence(ctx context.Context, tx *sql.Tx, treeID string) (int64, error) {
	if tx == nil {
		return 0, errors.New("sqlite: NextNodeSequence: nil transaction")
	}
	if strings.TrimSpace(treeID) == "" {
		return 0, errors.New("sqlite: NextNodeSequence: empty tree id")
	}

	// COALESCE cannot yield NULL here, so the result is scanned into a plain int64.
	var next int64
	err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(sequence_num), 0) + 1 FROM nodes WHERE tree_id = ?`,
		treeID).Scan(&next)
	if err != nil {
		return 0, fmt.Errorf("sqlite: NextNodeSequence: tree %s: %w", treeID, err)
	}
	return next, nil
}
