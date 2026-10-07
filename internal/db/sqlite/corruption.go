package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// errCorruptDB is the sentinel QuickCheck wraps when the database file is
// damaged; IsCorruptionRefusal matches it with errors.Is. Driver-level
// refusals raised before QuickCheck runs (Open/Ping on a garbage file) are
// classified by message instead.
var errCorruptDB = errors.New("corrupt database file")

// notADatabaseText is modernc.org/sqlite's text for driver error 26
// (SQLITE_NOTADB). Matching the message substring is deliberate: the pure-Go
// driver does not export the code as a comparable value.
const notADatabaseText = "file is not a database"

// QuickCheck runs `PRAGMA quick_check` on the store and fails closed when the
// database file is corrupt. Every corruption refusal is a wrapped
// errCorruptDB whose message names the store path.
//
// Corruption is refused in three shapes:
//
//   - the driver refuses the file outright ("file is not a database" — a
//     garbage file, a foreign format, or a truncated header),
//   - quick_check itself errors while scanning (e.g. "database disk image is
//     malformed" on a half-truncated file),
//   - quick_check completes but reports problems (e.g. "btreeInitPage()
//     returns error code 11" on a zeroed interior page — a file whose header
//     page is intact, so Open and ping alone succeed and boot would silently
//     continue against damaged data; QA-HERMES-CANOPY-42).
//
// An empty store (fresh file SQLite has not yet written, or a zero-byte file)
// passes: SQLite treats it as a valid empty database and the caller's schema
// application (ApplyCoreSchema) performs the clean rebuild. The check is
// read-only; quick_check scans the file once.
func (s *Store) QuickCheck(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("sqlite: QuickCheck: nil store")
	}
	rows, err := s.db.QueryContext(ctx, "PRAGMA quick_check")
	if err != nil {
		if strings.Contains(err.Error(), notADatabaseText) {
			return fmt.Errorf("sqlite: QuickCheck %s: %w: %v", s.path, errCorruptDB, err)
		}
		return fmt.Errorf("sqlite: QuickCheck %s: %w", s.path, err)
	}
	defer func() { _ = rows.Close() }()

	var problems []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return fmt.Errorf("sqlite: QuickCheck %s: %w", s.path, err)
		}
		if line != "ok" {
			problems = append(problems, line)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("sqlite: QuickCheck %s: %w", s.path, err)
	}
	if len(problems) != 0 {
		return fmt.Errorf("sqlite: QuickCheck %s: %w: integrity check failed: %s",
			s.path, errCorruptDB, strings.Join(problems, "; "))
	}
	return nil
}

// IsCorruptionRefusal reports whether err is a database-corruption refusal:
// a QuickCheck failure (wrapped errCorruptDB) or the driver refusing the file
// at open time ("file is not a database" / "database disk image is
// malformed"). OpenRuntime surfaces both as errors naming the database path.
func IsCorruptionRefusal(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errCorruptDB) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, notADatabaseText) ||
		strings.Contains(msg, "database disk image is malformed")
}
