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
	"fmt"
	"os"
	"testing"

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

// dropTestDB drops and recreates the test database so each run starts
// with a clean slate. Connects to the default postgres database first.
func dropTestDB(ctx context.Context, url string) error {
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return fmt.Errorf("connect for drop: %w", err)
	}
	defer conn.Close(ctx)

	// Terminate existing connections to the target database.
	if _, err := conn.Exec(ctx, `
		SELECT pg_terminate_backend(pg_stat_activity.pid)
		FROM pg_stat_activity
		WHERE pg_stat_activity.datname = 'canopy'
		  AND pid <> pg_backend_pid()
	`); err != nil {
		// Ignore errors — the test DB may not exist yet.
		_ = err
	}

	_, _ = conn.Exec(ctx, "DROP DATABASE IF EXISTS canopy WITH (FORCE)")
	_, err = conn.Exec(ctx, "CREATE DATABASE canopy")
	return err
}

// DefaultTestDBURL is the default connection string for the
// docker-compose PostgreSQL instance (port 5437, user/pass canopy).
const DefaultTestDBURL = "postgres://canopy:canopy@localhost:5437/canopy?sslmode=disable"

// TestDBURL returns the database URL for integration tests, preferring
// the CANOPY_TEST_DB_URL environment variable over the default.
func TestDBURL() string {
	if u := os.Getenv("CANOPY_TEST_DB_URL"); u != "" {
		return u
	}
	return DefaultTestDBURL
}

// SkipIfNoDB skips the test if integration tests are disabled.
// Set CANOPY_SKIP_INTEGRATION=1 or CANOPY_TEST_DB_URL to an
// unreachable URL to skip.
func SkipIfNoDB(t *testing.T) {
	t.Helper()
	if os.Getenv("CANOPY_SKIP_INTEGRATION") != "" {
		t.Skip("CANOPY_SKIP_INTEGRATION is set")
	}
}

// NewIntegrationPool creates a pgxpool connected to the test database
// and runs all pending migrations. It first drops and recreates the
// database from a fresh state, creates the canopy_app role (needed by
// migration 000009), then runs all migrations.
// Callers MUST close the pool when done: pool.Close().
func NewIntegrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	url := TestDBURL()

	// Drop and recreate the database to ensure a clean slate.
	// Connect to the default 'postgres' database for admin operations.
	adminURL := "postgres://canopy:canopy@localhost:5437/postgres?sslmode=disable"
	if os.Getenv("CANOPY_ADMIN_DB_URL") != "" {
		adminURL = os.Getenv("CANOPY_ADMIN_DB_URL")
	}
	if err := dropTestDB(ctx, adminURL); err != nil {
		t.Fatalf("NewIntegrationPool: dropTestDB: %v", err)
	}

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("NewIntegrationPool: pgxpool.New(%s): %v", url, err)
	}
	t.Cleanup(pool.Close)

	// Ping to verify connectivity.
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("NewIntegrationPool: ping(%s): %v", url, err)
	}

	// Create the canopy_app role before running migrations (migration
	// 000009 requires it, but 000019 creates it — ordering issue).
	if err := ensureCanopyRole(ctx, pool); err != nil {
		t.Fatalf("NewIntegrationPool: ensureCanopyRole: %v", err)
	}

	// Run migrations.
	if err := db.MigrateUp(url); err != nil {
		t.Fatalf("NewIntegrationPool: MigrateUp(%s): %v", url, err)
	}

	return pool
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
		"approvals",
		"profile_route",
		"profiles",
		"users",
		"tree_snapshots",
		"tree_events",
		"edges",
		"nodes",
		"topic_members",
		"topics",
		"trees",
		"workspaces",
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("TruncateAll: begin tx: %v", err)
	}
	defer tx.Rollback(ctx)

	for _, name := range tables {
		if _, err := tx.Exec(ctx, fmt.Sprintf("TRUNCATE TABLE %s CASCADE", name)); err != nil {
			t.Fatalf("TruncateAll: truncate %s: %v", name, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("TruncateAll: commit: %v", err)
	}
}
