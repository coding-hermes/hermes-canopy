// Package testutil provides shared test helpers for Canopy integration
// tests. The canonical test database is provided by docker-compose:
//
//	docker compose up -d
//
// The test helpers expect the environment variable CANOPY_TEST_DB_URL
// (default: postgres://canopy:canopy@localhost:5437/canopy?sslmode=disable).
package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
)

// ensureCanopyRole creates the canopy_app role if it doesn't exist.
// Migration 000009 revokes privileges from canopy_app, but the role
// isn't created until migration 000019. We create it upfront to
// work around this ordering issue.
func ensureCanopyRole(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `DO $$
	BEGIN
		IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'canopy_app') THEN
			CREATE ROLE canopy_app;
		END IF;
	END
	$$;`)
	return err
}

// dropTestDBByName drops (if exists) and recreates the named test database
// so each run starts with a clean slate and concurrent packages don't
// interfere. Connects to the admin (postgres) database first.
// Uses WITH (FORCE) to terminate any lingering connections on retries.
func dropTestDBByName(ctx context.Context, adminURL, dbName string) error {
	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return fmt.Errorf("connect for drop: %w", err)
	}
	defer conn.Close(ctx)

	_, _ = conn.Exec(ctx, fmt.Sprintf(
		"DROP DATABASE IF EXISTS %s WITH (FORCE)", dbName))
	_, err = conn.Exec(ctx, fmt.Sprintf(
		"CREATE DATABASE %s", dbName))
	return err
}

// SkipIfNoDB skips the test if integration tests are disabled.
// Set CANOPY_SKIP_INTEGRATION=1 to skip.
func SkipIfNoDB(t *testing.T) {
	t.Helper()
	if os.Getenv("CANOPY_SKIP_INTEGRATION") != "" {
		t.Skip("CANOPY_SKIP_INTEGRATION is set")
	}
}

// uniqueDBName generates a unique database name per call so concurrent
// integration test packages (handler, testutil, etc.) do not interfere.
// Each test call to NewIntegrationPool creates an isolated database,
// preventing cross-package dropTestDB() from killing another test's
// connections via pg_terminate_backend().
func uniqueDBName() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "canopy_test"
	}
	return "canopy_" + hex.EncodeToString(b)
}

// testDBInfo describes one candidate test database for the stale sweep.
type testDBInfo struct {
	Name       string
	Created    time.Time
	HasCreated bool // false when the creation time could not be determined
	Active     int  // number of active backends on the database
}

// staleTestDBAge is the minimum age before a leaked test database is
// considered safe to drop. Live tests always run against databases created
// seconds earlier by this same run, so a canopy_* database older than an
// hour with zero connections cannot belong to a running test.
const staleTestDBAge = time.Hour

// sweepStaleTimeout bounds the best-effort stale-DB sweep. DROP DATABASE
// WITH (FORCE) can block indefinitely waiting for a stuck backend from a
// prior timed-out run to terminate; without a deadline the sweep hangs
// NewIntegrationPool and every test behind it (BUG-031). 5s is generous
// for the scan + per-DB checks; a hung DROP is abandoned and retried next
// run (the sweep is idempotent).
const sweepStaleTimeout = 5 * time.Second

// staleTestDBs returns the subset of candidate databases that are safe to
// drop: older than staleTestDBAge AND holding zero active connections.
// Databases whose creation time is unknown are never returned — an
// undeterminable age means skip, never drop.
func staleTestDBs(cands []testDBInfo, now time.Time) []string {
	var stale []string
	for _, c := range cands {
		if !c.HasCreated {
			continue
		}
		if now.Sub(c.Created) <= staleTestDBAge {
			continue
		}
		if c.Active > 0 {
			continue
		}
		stale = append(stale, c.Name)
	}
	return stale
}

// sweepStaleTestDBs drops leaked test databases left behind by timed-out or
// panicked test runs whose t.Cleanup never executed (BUG-012: 420+ DBs,
// ~25 GB accumulated). Each leaked DB is ~10MB, so this self-healing sweep
// is the long-term fix. It is best-effort and NON-FATAL: every error is
// logged and ignored so a locked or unreadable database can never block
// test setup.
//
// Safety: only databases matching the uniqueDBName pattern (canopy_ + 8 hex
// chars) are candidates, and a database is dropped ONLY if BOTH
//   - it has zero active connections (pg_stat_activity), AND
//   - it was created more than staleTestDBAge ago. Creation time is the
//     per-database directory mtime under $PGDATA/base/<oid> (via
//     pg_stat_file) — PostgreSQL sets it at CREATE DATABASE and never
//     touches it afterwards.
//
// The 'canopy' base database, the 'canopy_test' fallback name, and anything
// not matching the hex pattern are never touched. Age detection runs
// per-database so one unreadable directory only skips that database instead
// of aborting the whole sweep.
func sweepStaleTestDBs(ctx context.Context, adminURL string) {
	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		log.Printf("testutil: sweepStaleTestDBs: connect: %v", err)
		return
	}
	defer conn.Close(ctx)

	// Candidate leaked test databases. The escaped LIKE prefix plus the
	// hex-pattern regex confines the sweep to uniqueDBName() output only.
	rows, err := conn.Query(ctx, `
		SELECT datname, oid::text, dattablespace::int
		FROM pg_database
		WHERE datname LIKE 'canopy\_%'
		  AND datname ~ '^canopy_[0-9a-f]{8}$'`)
	if err != nil {
		log.Printf("testutil: sweepStaleTestDBs: list candidates: %v", err)
		return
	}
	type candidate struct {
		name string
		oid  string
		spc  int
	}
	var cands []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.name, &c.oid, &c.spc); err != nil {
			rows.Close()
			log.Printf("testutil: sweepStaleTestDBs: scan candidate: %v", err)
			return
		}
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		log.Printf("testutil: sweepStaleTestDBs: iterate candidates: %v", err)
		return
	}
	if len(cands) == 0 {
		return
	}

	// Per-database age + connection check. Age detection is isolated
	// per-database so one failure only skips that database (conservative).
	infos := make([]testDBInfo, 0, len(cands))
	for _, c := range cands {
		if c.spc != 1663 { // pg_default tablespace
			log.Printf("testutil: sweepStaleTestDBs: %s: non-default tablespace, skipping", c.name)
			continue
		}
		var created *time.Time
		if err := conn.QueryRow(ctx, `
			SELECT (pg_stat_file(format('%s/base/%s',
			         (SELECT setting FROM pg_settings WHERE name='data_directory'),
			         $1::text))).modification`,
			c.oid).Scan(&created); err != nil {
			log.Printf("testutil: sweepStaleTestDBs: age(%s): %v — skipping", c.name, err)
			continue
		}
		var active int
		if err := conn.QueryRow(ctx,
			`SELECT count(*) FROM pg_stat_activity WHERE datname = $1`,
			c.name).Scan(&active); err != nil {
			log.Printf("testutil: sweepStaleTestDBs: connections(%s): %v — skipping", c.name, err)
			continue
		}
		infos = append(infos, testDBInfo{
			Name:       c.name,
			Created:    *created,
			HasCreated: true,
			Active:     active,
		})
	}

	for _, name := range staleTestDBs(infos, time.Now()) {
		// WITH (FORCE) terminates any connection that raced in between the
		// zero-connection check and the drop. Safe here because the age gate
		// guarantees the database cannot belong to a live test.
		if _, err := conn.Exec(ctx,
			fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", name)); err != nil {
			log.Printf("testutil: sweepStaleTestDBs: drop %s: %v", name, err)
			continue
		}
		log.Printf("testutil: sweepStaleTestDBs: dropped stale leaked test DB %s", name)
	}
}

// NewIntegrationPool creates a pgxpool connected to a FRESH, uniquely-named
// test database and runs all pending migrations. Each call creates an
// isolated database so concurrent test packages (handler, testutil, etc.)
// do not interfere via dropTestDB/pg_terminate_backend.
// Callers MUST close the pool when done: pool.Close().
func NewIntegrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	// Admin connection URL (to the default 'postgres' database).
	adminURL := "postgres://canopy:canopy@localhost:5437/postgres?sslmode=disable"
	if u := os.Getenv("CANOPY_ADMIN_DB_URL"); u != "" {
		adminURL = u
	}

	// Best-effort sweep of test databases leaked by timed-out or panicked
	// runs (their t.Cleanup never fired). NON-FATAL: any error is logged and
	// ignored so a locked database can never block test setup. One sweep per
	// pool creation is enough — it is idempotent.
	//
	// BUG-031: the sweep uses context.Background() internally (its queries
	// have no deadline). A DROP DATABASE WITH (FORCE) that blocks waiting
	// for a stuck backend from a previous timed-out run to terminate will
	// hang the sweep forever — hanging NewIntegrationPool and the whole
	// subtest. Bound it with a generous deadline: 5s is plenty for the
	// candidate scan + age/connection checks, and if a DROP does hang we
	// abandon it and let the next run retry (idempotent).
	sweepCtx, sweepCancel := context.WithTimeout(ctx, sweepStaleTimeout)
	defer sweepCancel()
	sweepStaleTestDBs(sweepCtx, adminURL)

	dbName := uniqueDBName()

	// Drop (if re-running) and recreate the unique test database.
	dropTestDBByName(ctx, adminURL, dbName)

	// Connect to the newly-created database.
	targetURL := fmt.Sprintf("postgres://canopy:canopy@localhost:5437/%s?sslmode=disable", dbName)
	if u := os.Getenv("CANOPY_TEST_DB_URL"); u != "" {
		// If a custom URL is set, use it as-is (user wants a specific DB).
		targetURL = u
	}

	cfg, err := pgxpool.ParseConfig(targetURL)
	if err != nil {
		t.Fatalf("NewIntegrationPool: pgxpool.ParseConfig(%s): %v", targetURL, err)
	}
	cfg.MaxConns = 10
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("NewIntegrationPool: pgxpool.NewWithConfig(%s): %v", targetURL, err)
	}
	t.Cleanup(func() {
		pool.Close()
		// Drop the uniquely-named test database so it doesn't accumulate.
		// Without this, every test run leaks a database (~10MB each) —
		// 420+ databases per full run, 25 GB over 13 ticks (BUG-012).
		dropCtx := context.Background()
		dropConn, dropErr := pgx.Connect(dropCtx, adminURL)
		if dropErr != nil {
			return
		}
		defer dropConn.Close(dropCtx)
		_, _ = dropConn.Exec(dropCtx,
			fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", dbName))
	})

	// Ping to verify connectivity.
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("NewIntegrationPool: ping(%s): %v", targetURL, err)
	}

	// Create the canopy_app role before running migrations (migration
	// 000009 requires it, but 000019 creates it — ordering issue).
	if err := ensureCanopyRole(ctx, pool); err != nil {
		t.Fatalf("NewIntegrationPool: ensureCanopyRole: %v", err)
	}

	// Run migrations.
	if err := db.MigrateUp(targetURL); err != nil {
		t.Fatalf("NewIntegrationPool: MigrateUp(%s): %v", targetURL, err)
	}

	return pool
}

// ---------------------------------------------------------------------------
// Shared integration pool (TEST-004): one migrated database per test binary,
// truncated between tests. Cuts PG suite time from ~15s/test to ~0.1s/test.
// ---------------------------------------------------------------------------

var (
	sharedPoolOnce  sync.Once
	sharedPool      *pgxpool.Pool
	sharedPoolErr   error
	sharedPoolName  string
	sharedPoolAdmin string
)

// NewSharedIntegrationPool returns a pool to a package-level test database
// that is created and migrated ONCE per test binary, then reused by every
// test in the package. Each call truncates all tables first, so every test
// still starts from an empty schema — the same isolation contract as
// NewIntegrationPool, without paying CREATE DATABASE + 21 migrations per
// test (~10-15s each). With 224 PG tests across db+handler, this turns a
// ~35-minute suite into ~3 minutes.
//
// The shared database is intentionally NOT dropped per test (it must
// outlive the first test that created it). It is reclaimed by the
// stale-DB sweep (sweepStaleTestDBs, TEST-002) once the run ends: the DB
// name matches the canopy_[0-9a-f]{8} pattern, and after the process exits
// it is >1h old with zero connections — exactly the sweep's drop criteria.
//
// Callers MUST NOT close the returned pool; it is owned by the package.
func NewSharedIntegrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	SkipIfNoDB(t)
	ctx := context.Background()

	sharedPoolOnce.Do(func() {
		sharedPoolName = uniqueDBName()
		sharedPoolAdmin = "postgres://canopy:canopy@localhost:5437/postgres?sslmode=disable"
		if u := os.Getenv("CANOPY_ADMIN_DB_URL"); u != "" {
			sharedPoolAdmin = u
		}

		// Same best-effort stale-DB sweep as NewIntegrationPool — the
		// shared pool is used by most handler tests, so crashed runs
		// leak DBs through this path too (BUG-027: 12 leaked DBs,
		// ~120MB, slowing every CREATE DATABASE and turning fast tests
		// into parallel-suite timeouts).
		//
		// BUG-031: bounded with sweepStaleTimeout — see NewIntegrationPool.
		sweepCtx, sweepCancel := context.WithTimeout(ctx, sweepStaleTimeout)
		defer sweepCancel()
		sweepStaleTestDBs(sweepCtx, sharedPoolAdmin)

		// Drop any prior run's shared DB for this binary, then recreate.
		if err := dropTestDBByName(ctx, sharedPoolAdmin, sharedPoolName); err != nil {
			sharedPoolErr = fmt.Errorf("NewSharedIntegrationPool: drop/recreate %s: %w", sharedPoolName, err)
			return
		}

		targetURL := fmt.Sprintf("postgres://canopy:canopy@localhost:5437/%s?sslmode=disable", sharedPoolName)
		if u := os.Getenv("CANOPY_TEST_DB_URL"); u != "" {
			targetURL = u
		}

		cfg, err := pgxpool.ParseConfig(targetURL)
		if err != nil {
			sharedPoolErr = fmt.Errorf("NewSharedIntegrationPool: parse %s: %w", targetURL, err)
			return
		}
		cfg.MaxConns = 10
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			sharedPoolErr = fmt.Errorf("NewSharedIntegrationPool: connect: %w", err)
			return
		}
		if err := pool.Ping(ctx); err != nil {
			sharedPoolErr = fmt.Errorf("NewSharedIntegrationPool: ping: %w", err)
			return
		}
		if err := ensureCanopyRole(ctx, pool); err != nil {
			sharedPoolErr = fmt.Errorf("NewSharedIntegrationPool: ensureCanopyRole: %w", err)
			return
		}
		if err := db.MigrateUp(targetURL); err != nil {
			sharedPoolErr = fmt.Errorf("NewSharedIntegrationPool: MigrateUp(%s): %w", targetURL, err)
			return
		}
		sharedPool = pool
	})

	if sharedPoolErr != nil {
		t.Fatalf("%v", sharedPoolErr)
	}

	// Fresh empty schema for this test, like NewIntegrationPool provides.
	TruncateAll(t, sharedPool)
	return sharedPool
}

// TruncateAll drops all rows from every table (in dependency-safe
// order) so each test starts with a clean database. Runs in a single
// transaction. MUST be called after NewIntegrationPool.
func TruncateAll(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	tables := []string{
		"mls_pending_proposals",
		"mls_key_packages",
		"mls_group_members",
		"mls_groups",
		"transport_events",
		"transport_configs",
		"transport_connections",
		"approval_audit_log",
		"approval_rules",
		"approvals",
		"profile_invites",
		"profile_route",
		"profiles",
		"users",
		"tree_snapshots",
		"tree_events",
		"tree_event_seq",
		"tree_members",
		"edges",
		"nodes",
		"node_resolved_refs",
		"topic_members",
		"topics",
		"trees",
		"workspaces",
		"plugin_registry",
		"plugin_instances",
		"plugin_audit_log",
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("TruncateAll: begin tx: %v", err)
	}
	defer tx.Rollback(ctx)

	// Single TRUNCATE statement listing ALL tables: PostgreSQL resolves
	// the FK dependency graph ONCE instead of per-statement. Per-table
	// TRUNCATE ... CASCADE costs ~0.3-0.9s each (each cascade re-scans
	// pg_constraint/pg_trigger for the full dependency closure), so 20
	// separate cascades ≈ 7s; one combined statement ≈ 3ms (TEST-004).
	quoted := make([]string, len(tables))
	for i, name := range tables {
		quoted[i] = fmt.Sprintf("%q", name)
	}
	stmt := "TRUNCATE TABLE " + strings.Join(quoted, ", ") + " CASCADE"
	if _, err := tx.Exec(ctx, stmt); err != nil {
		t.Fatalf("TruncateAll: %s: %v", stmt, err)
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("TruncateAll: commit: %v", err)
	}
}
