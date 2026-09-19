// Core-graph repositories (trees, nodes, edges) — file-level scope note.
//
// The package comment lives in store.go. This file and its siblings add the repositories
// over that store; what follows is the boundary they implement, and the PostgreSQL
// mechanisms SQLite cannot reproduce.
//
// Board row GAP-076-W3 — wave 2, slice 2. Slice 1 (store.go, obligations.go) opened the
// SQLite core-graph store and landed the four Go obligations that docs/SQLITE-PIVOT.md §5
// assigns to the repo layer. This slice is the next one: concrete database/sql
// repositories over that store for the three core-graph interfaces declared in
// internal/db — db.TreeRepo, db.NodeRepo and db.EdgeRepo.
//
// # Boundary (what this slice is, and is not)
//
//   - *TreeRepo implements db.TreeRepo in full, *NodeRepo implements db.NodeRepo in full.
//     Both are asserted at compile time below, so a future drift in either interface is a
//     build failure rather than a runtime surprise.
//   - *EdgeRepo implements the eleven driver-agnostic methods of db.EdgeRepo. The remaining
//     three — CreateReferenceSet, GetActiveIncoming and ValidateIncomingInvariant, the
//     transactional half of the SPEC-PL-06 multi-message reference model — take a concrete
//     pgx.Tx, a PostgreSQL transaction handle. No SQLite implementation can satisfy those
//     signatures without editing the public interface, and this slice must not edit it (the
//     PostgreSQL repos are its other consumers). The implemented set is DERIVED from
//     db.EdgeRepo by TestSQLiteEdgeRepoBoundaryIsDerivedFromInterface instead of being
//     restated here, so the boundary cannot drift silently in either direction. Nothing in
//     this package imports pgx: it stays pure Go, as the pivot contract requires.
//   - The reference model additionally needs nodes.parent_mode (PostgreSQL migration
//     000047), which is not part of the wave-1 SQLite batch (000001..000010). Every node in
//     this store is therefore 'lineage' — exactly the branch of PGEdgeRepo.Create's §12.1
//     matrix that this port implements, with the multi_reference branches narrowed in the
//     method's own comment.
//   - Out of scope, deliberately untouched: boot wiring (cmd/canopyd still gates on
//     PostgreSQL — wave 3), returning PostgreSQL (wave 4), and every unrelated feature
//     repo (approvals, topics, MLS, transport, federation, files — their own waves).
//
// # What is preserved from the PostgreSQL repos
//
// Column lists, result ordering, sentinels (db.ErrNotFound, db.ErrSelfEdge,
// db.ErrMultipleParents, db.ErrReferenceTargetType, ...) and the nil-input errors match
// their PG counterparts, so a caller can move between the two implementations without new
// error handling. Where SQLite forced a different mechanism, the difference is documented
// at the method AND pinned by a test — the five worth knowing before reading the code:
//
//  1. id generation — the DDL has no `DEFAULT uuidv7()`, so every insert generates its id
//     with NewID (obligation 1). A caller-supplied id is not honored, exactly as in
//     PGTreeRepo/PGNodeRepo where the DDL default wins.
//  2. node sequence — no BEFORE INSERT trigger can fill `NEW.sequence_num`, so Create
//     allocates it with NextNodeSequence inside the same transaction as the INSERT
//     (obligation 2).
//  3. content hashing — no SQL hash function, so every node write supplies ContentHash
//     (obligation 3). PG recomputed the hash in a trigger on UPDATE OF content; here the
//     update path recomputes it explicitly, which TestSQLiteNodeRepoUpdateRecomputesContentHash
//     pins.
//  4. RETURNING does not see AFTER-trigger writes. SQLite evaluates a RETURNING list
//     before the AFTER triggers fire (measured on SQLite 3.53.4 / modernc v1.58.0: an
//     UPDATE ... RETURNING edited_at returns NULL while the row read back a moment later
//     carries the trigger's timestamp). The repos therefore use one statement per write
//     and read the row back with a SELECT; the `trg_node_edited_at` trigger still owns
//     edited_at.
//  5. JSON columns are bound as TEXT, never as []byte. A []byte argument is stored with
//     blob storage class, and although json_valid/json_extract still accept it, the
//     edited_at trigger compares with `metadata IS NOT NEW.metadata` — a TEXT value
//     against the same JSON as a BLOB compares as a CHANGE, so a no-op update would bump
//     edited_at every time. jsonText is the single conversion point.

package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

// ErrNoStore is returned by a repository method whose handle was built without an open
// store (a nil *Store, or a Store whose pool is closed). Failing loudly at the call site
// is preferable to a panic in a boot path that has not been wired yet.
var ErrNoStore = errors.New("sqlite: repository has no open store")

// Compile-time proof that the full-interface implementations still satisfy the public
// interfaces of internal/db. The edge repository is checked method-by-method in its test
// (its interface is only partly satisfiable — see the package comment).
var (
	_ db.TreeRepo = (*TreeRepo)(nil)
	_ db.NodeRepo = (*NodeRepo)(nil)
)

// sqlNow is the SQLite spelling of PostgreSQL's clock_timestamp(): the wall clock at
// statement time, in the canonical D1 shape. It is byte-identical to the DDL default and
// to the edited_at trigger, so a timestamp written by the repo, by the DDL and by a
// trigger are indistinguishable and still compare correctly as plain TEXT.
const sqlNow = `strftime('%Y-%m-%dT%H:%M:%fZ','now')`

// rowScanner is the Scan surface shared by *sql.Row and *sql.Rows, so one scan helper per
// table serves every read path (the role pgx.Row plays in the PostgreSQL repos).
type rowScanner interface {
	Scan(dest ...any) error
}

// jsonText renders a JSON column argument as TEXT. Pure nil/empty becomes '{}' — the same
// value PostgreSQL's `COALESCE($n, '{}'::jsonb)` supplies. See point 5 of the package
// comment: substituting []byte here would store a BLOB and make the edited_at trigger fire
// on no-op updates.
func jsonText(b []byte) string {
	if len(b) == 0 {
		return "{}"
	}
	return string(b)
}

// idArg renders a uuid for a TEXT column; uuid.Nil renders as the zero uuid string, which
// a NOT NULL column accepts and an FK rejects by name.
func idArg(id uuid.UUID) string { return id.String() }

// optionalIDArg renders a nullable uuid column: a nil pointer becomes SQL NULL (no id),
// which is what the DDL's nullable id columns mean.
func optionalIDArg(id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	return id.String()
}

// optionalID decodes a nullable TEXT id column.
func optionalID(column string, v sql.NullString) (*uuid.UUID, error) {
	if !v.Valid || v.String == "" {
		return nil, nil
	}
	id, err := uuid.Parse(v.String)
	if err != nil {
		return nil, fmt.Errorf("%s %q: %w", column, v.String, err)
	}
	return &id, nil
}

// requiredTime decodes a NOT NULL TEXT timestamp column through the strict canonical
// decoder, so a value written by a different client (a psql-style session, an import)
// fails loudly instead of being reinterpreted.
func requiredTime(column string, v sql.NullString) (time.Time, error) {
	if !v.Valid || v.String == "" {
		return time.Time{}, fmt.Errorf("%s is empty; the DDL declares it NOT NULL", column)
	}
	t, err := DecodeTime(v.String)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s: %w", column, err)
	}
	return t, nil
}

// optionalTime decodes a nullable TEXT timestamp column.
func optionalTime(column string, v sql.NullString) (*time.Time, error) {
	if !v.Valid || v.String == "" {
		return nil, nil
	}
	t, err := DecodeTime(v.String)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", column, err)
	}
	return &t, nil
}

// placeholders renders `?, ?, ?` for n values. SQLite has no array type, so PostgreSQL's
// `= ANY($1)` becomes an expanded IN list; n == 0 is reported as 0 so the caller can skip
// the query rather than build an empty IN ().
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	out := make([]byte, 0, 3*n-2)
	for i := 0; i < n; i++ {
		if i > 0 {
			out = append(out, ',', ' ')
		}
		out = append(out, '?')
	}
	return string(out)
}
