package sqlite

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// EmbeddedCoreVersion is the highest SQLite migration included in the wave-1
// core-graph store. A database newer than this binary is refused before any
// migration runs, matching canopyd's PostgreSQL stale-build guard.
const EmbeddedCoreVersion int64 = 10

// OpenRuntime opens a SQLite canopy store, refuses a newer applied migration,
// applies the embedded core schema, and verifies the live table/column
// inventory. The caller owns the returned store and must close it.
func OpenRuntime(ctx context.Context, path string) (*Store, error) {
	store, err := Open(path)
	if err != nil {
		return nil, err
	}
	closeOnError := func(err error) (*Store, error) {
		_ = store.Close()
		return nil, err
	}

	before, err := store.AppliedMigrations(ctx)
	if err != nil {
		return closeOnError(err)
	}
	if version := maxMigrationVersion(before); version > EmbeddedCoreVersion {
		return closeOnError(fmt.Errorf("sqlite: STALE BUILD: database migration %d is newer than binary migration %d", version, EmbeddedCoreVersion))
	}
	if err := store.ApplyCoreSchema(ctx); err != nil {
		return closeOnError(err)
	}
	if err := CheckCoreSchema(ctx, store); err != nil {
		return closeOnError(err)
	}
	return store, nil
}

func maxMigrationVersion(names []string) int64 {
	var max int64
	for _, name := range names {
		prefix := name
		if i := strings.IndexByte(prefix, '_'); i >= 0 {
			prefix = prefix[:i]
		}
		version, err := strconv.ParseInt(prefix, 10, 64)
		if err == nil && version > max {
			max = version
		}
	}
	return max
}

// CoreSchemaVersion reports the greatest applied SQLite migration number.
func CoreSchemaVersion(ctx context.Context, store *Store) (int64, error) {
	if store == nil {
		return 0, fmt.Errorf("sqlite: CoreSchemaVersion: nil store")
	}
	names, err := store.AppliedMigrations(ctx)
	if err != nil {
		return 0, err
	}
	return maxMigrationVersion(names), nil
}
