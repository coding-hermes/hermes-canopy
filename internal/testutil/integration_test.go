package testutil

import (
	"context"
	"testing"
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
		"trees", "nodes", "edges", "snapshots",
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
