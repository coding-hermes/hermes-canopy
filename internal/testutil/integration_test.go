package testutil

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestIntegration_Migration verifies that migrations apply cleanly
// against a real PostgreSQL instance. This is the canary test for
// the BE-12a integration test framework.
func TestIntegration_Migration(t *testing.T) {
	SkipIfNoDB(t)

	pool := NewIntegrationPool(t)
	ctx := context.Background()

	// Verify essential tables exist by selecting from them.
	var count int
	rows := []string{
		"trees", "nodes", "edges", "tree_snapshots",
		"tree_events", "users", "profiles",
		"approvals", "approval_audit_log",
		"transport_connections", "transport_configs", "transport_events",
		"mls_groups", "mls_group_members", "mls_key_packages", "mls_pending_proposals",
		"topics", "topic_members",
		"workspaces",
	}
	for _, table := range rows {
		if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatalf("table %s unreachable: %v", table, err)
		}
	}

	// Verify functions exist.
	var fnExists bool
	if err := pool.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM pg_proc WHERE proname = 'uuidv7')",
	).Scan(&fnExists); err != nil {
		t.Fatalf("uuidv7 function check: %v", err)
	}
	if !fnExists {
		t.Fatal("uuidv7 function not found after migration")
	}
}

// TestIntegration_Truncate verifies TruncateAll works. Creates a tree,
// truncates, and confirms the table is empty.
func TestIntegration_Truncate(t *testing.T) {
	SkipIfNoDB(t)

	pool := NewIntegrationPool(t)
	ctx := context.Background()

	// Insert a row into trees.
	if _, err := pool.Exec(ctx,
		`INSERT INTO trees (owner_id, title) VALUES ('00000000-0000-0000-0000-000000000001', 'test-truncate')`,
	); err != nil {
		t.Fatalf("insert test row: %v", err)
	}

	TruncateAll(t, pool)

	var count int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM trees").Scan(&count); err != nil {
		t.Fatalf("count trees: %v", err)
	}
	if count != 0 {
		t.Fatalf("trees table not empty after TruncateAll: got %d rows", count)
	}
}

// TestStaleTestDBs exercises the stale-database decision logic of the
// pre-run sweep. Pure unit test — no PostgreSQL required, runs in the
// non-PG gate too.
func TestStaleTestDBs(t *testing.T) {
	now := time.Now()
	old := now.Add(-2 * time.Hour)
	fresh := now.Add(-30 * time.Minute)

	tests := []struct {
		name  string
		cands []testDBInfo
		want  []string
	}{
		{
			name: "old and idle is dropped",
			cands: []testDBInfo{
				{Name: "canopy_aaaaaaaa", Created: old, HasCreated: true},
			},
			want: []string{"canopy_aaaaaaaa"},
		},
		{
			name: "old but active connections is kept",
			cands: []testDBInfo{
				{Name: "canopy_aaaaaaaa", Created: old, HasCreated: true, Active: 1},
			},
			want: nil,
		},
		{
			name: "fresh and idle is kept",
			cands: []testDBInfo{
				{Name: "canopy_aaaaaaaa", Created: fresh, HasCreated: true},
			},
			want: nil,
		},
		{
			name: "exactly one hour old is kept (strictly older required)",
			cands: []testDBInfo{
				{Name: "canopy_aaaaaaaa", Created: now.Add(-time.Hour), HasCreated: true},
			},
			want: nil,
		},
		{
			name: "unknown creation time is never dropped",
			cands: []testDBInfo{
				{Name: "canopy_aaaaaaaa", Created: old, HasCreated: false},
			},
			want: nil,
		},
		{
			name: "mixed candidates",
			cands: []testDBInfo{
				{Name: "canopy_aaaaaaaa", Created: old, HasCreated: true},
				{Name: "canopy_bbbbbbbb", Created: old, HasCreated: true, Active: 2},
				{Name: "canopy_cccccccc", Created: fresh, HasCreated: true},
				{Name: "canopy_dddddddd", Created: old, HasCreated: true},
			},
			want: []string{"canopy_aaaaaaaa", "canopy_dddddddd"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := staleTestDBs(tt.cands, now)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("staleTestDBs() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestSweepKeepsFreshDB verifies the live sweep never drops a database that
// belongs to a running test: a freshly-created database must survive
// sweepStaleTestDBs untouched (the 1-hour age gate protects live tests).
// Requires a live PostgreSQL — skips when CANOPY_SKIP_INTEGRATION is set.
func TestSweepKeepsFreshDB(t *testing.T) {
	SkipIfNoDB(t)

	ctx := context.Background()
	adminURL := "postgres://canopy:canopy@localhost:5437/postgres?sslmode=disable"
	if u := os.Getenv("CANOPY_ADMIN_DB_URL"); u != "" {
		adminURL = u
	}

	// Create a fresh uniquely-named database (drop+recreate, same as
	// NewIntegrationPool does for its own test database).
	name := uniqueDBName()
	if err := dropTestDBByName(ctx, adminURL, name); err != nil {
		t.Fatalf("create fresh DB %s: %v", name, err)
	}
	t.Cleanup(func() {
		dropCtx := context.Background()
		conn, err := pgx.Connect(dropCtx, adminURL)
		if err != nil {
			return
		}
		defer conn.Close(dropCtx)
		_, _ = conn.Exec(dropCtx,
			fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", name))
	})

	sweepStaleTestDBs(ctx, adminURL)

	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)
	var exists bool
	if err := conn.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = $1)",
		name).Scan(&exists); err != nil {
		t.Fatalf("check %s exists: %v", name, err)
	}
	if !exists {
		t.Fatalf("sweep dropped freshly-created database %s — age gate broken", name)
	}
}
