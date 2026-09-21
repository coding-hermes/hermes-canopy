// Dirty-migration recovery (QA-HERMES-CANOPY-30).
//
// A hard kill (SIGKILL) mid-migration leaves the schema_migrations row
// with dirty=true; before this helper every canopyd restart failed with
// "db: migrate up: Dirty database version N" and the docker compose
// restart policy looped forever with no recovery path. The recovery
// here is shared by MigrateWith (db.go, the canopyd boot path) and
// MigrateUp (migrations.go, the testutil integration path).
//
// golang-migrate v4 mechanics this relies on (verified against v4.20.1):
//   - Up() returns ErrDirty{Version} BEFORE touching SQL when the
//     bookkeeping row is dirty at entry.
//   - runMigrations sets the target version dirty BEFORE executing the
//     migration body, so a migration whose SQL fails mid-run leaves the
//     row (N, dirty=true) and Up() returns the raw failure (a
//     database.Error embedding the PostgreSQL message). That row is
//     readable again via m.Version().
//   - Force(v) rewrites the single bookkeeping row (v, dirty=false) in
//     one transaction; the migrate instance stays usable afterwards
//     (each Up() re-reads migration sources — there is no in-memory
//     applied-set cache).
//   - Force(database.NilVersion) empties the bookkeeping table so the
//     next Up() applies from the first migration. (Force(0) would NOT
//     work: Up() requires a migration to exist at the forced version.)
//   - A migration set fully marked applied makes Up() return
//     ErrNoChange without touching SQL. The bookkeeping row must never
//     be forced beyond the embedded max version: main()'s stale-build
//     guard treats a schema version greater than EmbeddedMaxVersion()
//     as "binary predates schema" and refuses to start.
package db

import (
	"errors"
	"fmt"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
)

// maxRepairAttempts bounds the accept-and-advance walk so recovery can
// never loop forever. The embedded set has EmbeddedMaxVersion() members
// (48 today); the cap comfortably exceeds any legitimate walk.
const maxRepairAttempts = 64

// runMigrateWithRepair applies pending migrations from m, repairing a
// dirty database state instead of failing the process. ErrNoChange is
// absorbed (no pending migrations is success); every other failure
// carries the "db: migrate" prefix.
func runMigrateWithRepair(m *migrate.Migrate) error {
	err := m.Up()
	if err == nil || errors.Is(err, migrate.ErrNoChange) {
		return nil
	}
	var dirty migrate.ErrDirty
	if !errors.As(err, &dirty) {
		return fmt.Errorf("db: migrate up: %w", err)
	}
	return repairDirty(m, dirty.Version)
}

// repairDirty recovers from dirty=true at dirtyVersion:
//
//   - Version out of range (<=0 or beyond this binary's embedded set):
//     the dirty version cannot exist here — force NilVersion and start
//     over from the first migration, accepting already-applied
//     migrations as the walk forward hits them.
//   - Otherwise: force the same version N and re-run. A duplicate-object
//     failure proves the migration actually completed before the kill:
//     accept it, force the next version, and walk forward one migration
//     at a time until Up() reports ErrNoChange (schema current).
//   - Any other failure is wrapped with the dirty version and a manual
//     repair recipe naming the bookkeeping row to fix — fail loud and
//     actionable, never loop silently.
func repairDirty(m *migrate.Migrate, dirtyVersion int) error {
	maxV64, err := EmbeddedMaxVersion()
	if err != nil {
		return repairFailed(dirtyVersion, dirtyVersion, err)
	}
	maxV := int(maxV64)

	// The reported version is outside this binary's migration set:
	// start over from scratch. Otherwise re-run the dirty migration
	// itself (Force clears the dirty flag so Up() will proceed).
	startOver := dirtyVersion <= 0 || dirtyVersion > maxV
	if startOver {
		if err := m.Force(database.NilVersion); err != nil {
			return repairFailed(dirtyVersion, dirtyVersion, err)
		}
	} else {
		if err := m.Force(dirtyVersion); err != nil {
			return repairFailed(dirtyVersion, dirtyVersion, err)
		}
	}

	// Walk position: the bookkeeping version the next Up() will start
	// from. Unknown (0) after a scratch restart until the first failure
	// pins it via m.Version().
	version := 0
	if !startOver {
		version = dirtyVersion
	}

	for steps := 0; steps < maxRepairAttempts; steps++ {
		err := m.Up()
		if err == nil || errors.Is(err, migrate.ErrNoChange) {
			return nil // recovered: schema now current
		}
		var dirtyAgain migrate.ErrDirty
		if errors.As(err, &dirtyAgain) {
			// The database went dirty again mid-recovery at a version
			// Up() refuses to start from. Continue the walk from the
			// newly reported version when it is walkable.
			v := dirtyAgain.Version
			if v <= 0 || v > maxV {
				return repairFailed(dirtyVersion, version, err)
			}
			if err := m.Force(v); err != nil {
				return repairFailed(dirtyVersion, version, err)
			}
			version = v
			continue
		}
		if isAlreadyAppliedError(err) {
			// The migration at the failed version actually completed
			// before the kill. runMigrations marks the target version
			// dirty BEFORE running the body, so the row now names the
			// failed migration's own version — trust it over the tracked
			// position (which is still 0 right after a scratch restart).
			cur, _, verr := m.Version()
			if verr != nil || int(cur) <= version || int(cur) > maxV {
				return repairFailed(dirtyVersion, version, err)
			}
			version = int(cur)
			// Schema current? (the last migration was accepted as
			// already applied). Keep the row AT max: forcing max+1 would
			// leave the schema version above the embedded max, which
			// main()'s stale-build guard treats as a binary older than
			// the schema.
			if version >= maxV {
				if err := m.Force(version); err != nil {
					return repairFailed(dirtyVersion, version, err)
				}
				return nil
			}
			if err := m.Force(version + 1); err != nil {
				return repairFailed(dirtyVersion, version, err)
			}
			version++
			continue
		}
		// A migration failed with a non-duplicate error mid-recovery:
		// runMigrations marked the target version dirty before the body
		// ran, so the row names the version that actually failed — read
		// it back (best effort) and fail loud with the manual recipe
		// targeting that row.
		if cur, _, verr := m.Version(); verr == nil && int(cur) > 0 && int(cur) <= maxV {
			version = int(cur)
		}
		return repairFailed(dirtyVersion, version, err)
	}
	return fmt.Errorf("db: migrate: dirty version %d could not be auto-repaired after %d repair steps", dirtyVersion, maxRepairAttempts)
}

// repairFailed wraps a recovery failure with the dirty version and the
// actionable manual repair recipe targeting the row currently in
// schema_migrations. Never called with err == nil.
func repairFailed(dirtyVersion, rowVersion int, err error) error {
	if rowVersion < 1 {
		rowVersion = 1
	}
	return fmt.Errorf(
		"db: migrate: dirty version %d could not be auto-repaired: %v; repair manually with: UPDATE schema_migrations SET dirty=false WHERE version=%d; then re-run canopyd",
		dirtyVersion, err, rowVersion)
}

// isAlreadyAppliedError reports whether err's text carries a duplicate-
// object signature (table/type/index/constraint/column already exists,
// duplicate key, duplicate PK) — i.e. the migration's DDL already ran,
// so the migration is treated as completed rather than failed.
func isAlreadyAppliedError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, sig := range []string{
		"already exists",
		"duplicate key value violates unique constraint",
		"multiple primary keys for table",
		"duplicate constraint",
	} {
		if strings.Contains(msg, sig) {
			return true
		}
	}
	return false
}
