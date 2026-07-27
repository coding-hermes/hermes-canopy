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

// NewIntegrationPool creates a pgxpool connected to a FRESH, uniquely-named
// test database and runs all pending migrations. Each call creates an
// isolated database so concurrent test packages (handler, testutil, etc.)
// do not interfere via dropTestDB/pg_terminate_backend.
// Callers MUST close the pool when done: pool.Close().
func NewIntegrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	dbName := uniqueDBName()

	// Admin connection URL (to the default 'postgres' database).
	adminURL := "postgres://canopy:canopy@localhost:5437/postgres?sslmode=disable"
	if u := os.Getenv("CANOPY_ADMIN_DB_URL"); u != "" {
		adminURL = u
	}

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
