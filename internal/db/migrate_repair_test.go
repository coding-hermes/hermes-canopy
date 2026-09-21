// QA-HERMES-CANOPY-30 regression tests: dirty-migration recovery.
//
// A canopyd SIGKILL mid-migration leaves the schema_migrations row with
// dirty=true; before the recovery helper every restart failed with
// "db: migrate up: Dirty database version N" and the compose restart
// policy looped forever. These tests poison a PRIVATE (per-test)
// database's bookkeeping row exactly like a hard kill would and assert
// both migration entry points self-heal to the embedded max version.
//
// schema_migrations holds a single row (version, dirty) — poisoning is
// an UPDATE of that row, not a WHERE version=N predicate (which would
// match nothing).
package db_test

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

// TestDirtyMigrationRecovery covers the three recovery shapes approved
// for QA-HERMES-CANOPY-30:
//
//   - mid:     a dirty version that exists in this binary's set and whose
//     objects already exist (migration completed before the kill) — the
//     accept-and-advance walk must reach the latest version.
//   - latest-1: same walk starting one below the max, including accepting
//     the max migration itself as already applied.
//   - outofrange: a dirty version that cannot exist in this binary's set
//     (corrupt row / schema from a newer binary) — recovery restarts from
//     scratch, accepts every already-applied migration as it walks
//     forward, and completes.
func TestDirtyMigrationRecovery(t *testing.T) {
	testutil.SkipIfNoDB(t)
	// Private per-test database (created, migrated, dropped at cleanup):
	// never the shared pool — poisoning must not leak between tests.
	pool := testutil.NewIntegrationPool(t)

	maxV, err := db.EmbeddedMaxVersion()
	if err != nil {
		t.Fatalf("EmbeddedMaxVersion: %v", err)
	}
	if maxV < 2 {
		t.Fatalf("EmbeddedMaxVersion = %d, need >= 2 for the mid-version case", maxV)
	}

	cases := []struct {
		name    string
		version int64
	}{
		{"mid", 4},
		{"latest-1", maxV - 1},
		{"outofrange", 99999},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()

			// A hard kill mid-migration leaves the single bookkeeping
			// row dirty at the version being applied.
			if _, err := pool.Exec(ctx,
				"UPDATE schema_migrations SET version=$1, dirty=true", tc.version); err != nil {
				t.Fatalf("poison schema_migrations (version=%d): %v", tc.version, err)
			}

			repo, err := db.New(ctx, db.PoolConfig{DSN: pool.Config().ConnConfig.ConnString()})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			defer repo.Close()

			if err := repo.Migrate(ctx); err != nil {
				t.Fatalf("Migrate did not recover from dirty version %d: %v", tc.version, err)
			}
			got, err := repo.SchemaVersion(ctx)
			if err != nil {
				t.Fatalf("SchemaVersion after recovery: %v", err)
			}
			if got != maxV {
				t.Fatalf("schema version after recovery = %d, want %d", got, maxV)
			}
		})
	}
}

// TestMigrateUpRecoversFromDirtyState proves the shared recovery also
// covers the package-level MigrateUp entry point (the one testutil uses
// when creating integration databases — a test run killed mid-migration
// leaves the same dirty row behind for the next run).
func TestMigrateUpRecoversFromDirtyState(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if _, err := pool.Exec(ctx,
		"UPDATE schema_migrations SET version=$1, dirty=true", 4); err != nil {
		t.Fatalf("poison schema_migrations: %v", err)
	}

	if err := db.MigrateUp(pool.Config().ConnConfig.ConnString()); err != nil {
		t.Fatalf("MigrateUp did not recover from dirty state: %v", err)
	}

	maxV, err := db.EmbeddedMaxVersion()
	if err != nil {
		t.Fatalf("EmbeddedMaxVersion: %v", err)
	}
	var got int64
	if err := pool.QueryRow(ctx,
		"SELECT COALESCE((SELECT max(version) FROM schema_migrations), 0)").Scan(&got); err != nil {
		t.Fatalf("schema version after recovery: %v", err)
	}
	if got != maxV {
		t.Fatalf("schema version after MigrateUp recovery = %d, want %d", got, maxV)
	}
}

// TestDirtyRepairFailsLoudWithManualRecipe pins the fail-loud contract:
// a repair attempt that still fails with a NON already-applied error must
// surface an error that names the dirty version and carries the manual
// repair recipe — actionable for a human, never a silent loop — and the
// bookkeeping row must stay dirty at the version the recipe targets.
//
// The failure is injected through MigrateWith's custom source FS (its
// documented test seam): the test role `canopy` is a superuser on the
// test instance, so privilege-revocation injection is a no-op there.
// A `SELECT 1/0` migration body fails with "division by zero", which
// carries no duplicate-object signature, so recovery must NOT accept it.
func TestDirtyRepairFailsLoudWithManualRecipe(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	const dirtyVersion = 4
	if _, err := pool.Exec(ctx,
		"UPDATE schema_migrations SET version=$1, dirty=true", dirtyVersion); err != nil {
		t.Fatalf("poison schema_migrations: %v", err)
	}

	// Minimal 3-version source set whose next pending migration (5)
	// fails with a clean non-duplicate error. golang-migrate's Up()
	// never re-runs the migration AT the current bookkeeping version —
	// Force(4) + Up() applies version 5 onward — so version 5 is the
	// body that must fail. Only the up side is needed for MigrateWith.
	src := fstest.MapFS{
		"000003_init.up.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
		"000004_init.up.sql": &fstest.MapFile{Data: []byte("SELECT 1;")},
		"000005_boom.up.sql": &fstest.MapFile{Data: []byte("SELECT 1/0;")},
	}

	repo, err := db.New(ctx, db.PoolConfig{DSN: pool.Config().ConnConfig.ConnString()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer repo.Close()

	err = repo.MigrateWith(ctx, src, ".")
	if err == nil {
		t.Fatalf("Migrate succeeded despite an unrepairable dirty version %d; want actionable failure", dirtyVersion)
	}
	for _, want := range []string{
		"dirty version 4",
		"could not be auto-repaired",
		// The recipe must target the ACTUALLY stuck row (the migration
		// that failed mid-recovery), not the originally reported one.
		"UPDATE schema_migrations SET dirty=false WHERE version=5",
		"re-run canopyd",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("recovery error missing %q:\n%v", want, err)
		}
	}

	// The manual recipe names a version — it must name the one the row
	// is actually sitting on, or the recipe itself would be wrong.
	var rowVersion int64
	var rowDirty bool
	if err := pool.QueryRow(ctx,
		"SELECT version, dirty FROM schema_migrations").Scan(&rowVersion, &rowDirty); err != nil {
		t.Fatalf("read schema_migrations after failed repair: %v", err)
	}
	if rowVersion != 5 || !rowDirty {
		t.Fatalf("schema_migrations = (%d, dirty=%t), want (5, dirty=true) — manual recipe would target the wrong row",
			rowVersion, rowDirty)
	}
}
