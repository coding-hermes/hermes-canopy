// Package handler — WIRE-006 integration tests.
//
// Verifies that GET /trees/{id} returns the session-lineage Related
// object when the tree's metadata contains session association fields,
// and that the backfill path (UpdateTreeMetadata) is idempotent.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/service"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

// insertTreeWithMeta inserts a tree row directly via SQL with the given
// metadata JSON. Used to simulate imported session trees for the Related
// extraction tests.
func insertTreeWithMeta(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, title, metadataJSON string) {
	t.Helper()
	owner := testUserID
	_, err := pool.Exec(ctx, `
		INSERT INTO trees (id, owner_id, title, description, metadata)
		VALUES ($1, $2, $3, $4, $5::jsonb)`,
		id, owner, title, "test tree", metadataJSON)
	if err != nil {
		t.Fatalf("insert tree %s: %v", id, err)
	}
}

// getTreeRepo creates a PGTreeRepo from the pool — needed for the
// backfill test's service construction.
func getTreeRepo(t *testing.T, pool *pgxpool.Pool) db.TreeRepo {
	t.Helper()
	return db.NewPGTreeRepo(pool)
}

// TestWIRE006_GetTreeRelated verifies that GET /trees/{id} returns the
// Related object with project, board_task, commit_hash, and delegation
// goals parsed from tree metadata JSON.
func TestWIRE006_GetTreeRelated(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	srv, cleanup := newTestServer(t, pool)
	defer cleanup()

	ctx := context.Background()

	// Insert two trees with session metadata: a parent and a child.
	// The child references the parent's session_id.
	parentID := uuid.New()
	childID := uuid.New()

	parentMeta := `{"session_id": "parent_sess_001", "project": "hermes-canopy", "child_session_ids": ["child_sess_001"]}`
	childMeta := `{"session_id": "child_sess_001", "parent_session_id": "parent_sess_001", "project": "hermes-canopy", "board_task": "BUG-034", "commit_hash": "a1b2c3d", "delegation_goals": [{"delegation_id": "deleg_1", "goal": "Fix the crash"}]}`

	insertTreeWithMeta(t, ctx, pool, parentID, "Parent Session Tree", parentMeta)
	insertTreeWithMeta(t, ctx, pool, childID, "Child Session Tree", childMeta)

	// GET the child tree — should return Related with parent ref,
	// scalar fields, and delegation goals.
	req := authenticatedRequest(t, srv.URL, http.MethodGet, "/api/v1/trees/"+childID.String(), nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET tree: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET tree: status=%d, want %d", resp.StatusCode, http.StatusOK)
	}

	var detail service.TreeDetail
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		t.Fatalf("decode tree detail: %v", err)
	}

	if detail.Related == nil {
		t.Fatal("Related is nil, expected populated related object")
	}

	// Parent should be resolved to the parent tree.
	if detail.Related.Parent == nil {
		t.Fatal("Related.Parent is nil")
	}
	if detail.Related.Parent.ID != parentID {
		t.Errorf("Related.Parent.ID = %s, want %s", detail.Related.Parent.ID, parentID)
	}
	if detail.Related.Parent.Title != "Parent Session Tree" {
		t.Errorf("Related.Parent.Title = %q", detail.Related.Parent.Title)
	}

	// Scalar fields.
	if detail.Related.Project == nil || *detail.Related.Project != "hermes-canopy" {
		t.Errorf("Related.Project = %v, want hermes-canopy", detail.Related.Project)
	}
	if detail.Related.BoardTask == nil || *detail.Related.BoardTask != "BUG-034" {
		t.Errorf("Related.BoardTask = %v, want BUG-034", detail.Related.BoardTask)
	}
	if detail.Related.CommitHash == nil || *detail.Related.CommitHash != "a1b2c3d" {
		t.Errorf("Related.CommitHash = %v, want a1b2c3d", detail.Related.CommitHash)
	}

	// Delegation goals.
	if len(detail.Related.DelegationGoals) != 1 {
		t.Fatalf("Related.DelegationGoals = %d, want 1", len(detail.Related.DelegationGoals))
	}
	if detail.Related.DelegationGoals[0].Goal != "Fix the crash" {
		t.Errorf("DelegationGoals[0].Goal = %q", detail.Related.DelegationGoals[0].Goal)
	}
}

// TestWIRE006_GetTreeRelated_NilWhenNoSessionID verifies that trees
// without session metadata do NOT return a Related object.
func TestWIRE006_GetTreeRelated_NilWhenNoSessionID(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	srv, cleanup := newTestServer(t, pool)
	defer cleanup()

	// Create a normal tree (no session metadata) via HTTP.
	createBody := map[string]any{
		"title": "Regular Tree",
		"rootMessage": map[string]any{
			"content":       "root",
			"contentFormat": "markdown",
			"nodeType":      "message",
		},
	}
	req := authenticatedRequest(t, srv.URL, http.MethodPost, "/api/v1/trees", createBody)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST tree: %v", err)
	}
	var tree service.Tree
	json.NewDecoder(resp.Body).Decode(&tree)
	resp.Body.Close()
	if tree.ID == uuid.Nil {
		t.Fatal("failed to create tree")
	}

	// GET the tree — Related should be nil/omitted.
	req = authenticatedRequest(t, srv.URL, http.MethodGet, "/api/v1/trees/"+tree.ID.String(), nil)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET tree: %v", err)
	}
	defer resp.Body.Close()

	var detail service.TreeDetail
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if detail.Related != nil {
		t.Errorf("Related = %+v, want nil (no session metadata)", detail.Related)
	}
}

// TestWIRE006_BackfillIdempotent verifies that UpdateTreeMetadata is
// idempotent: running it twice produces the same metadata.
func TestWIRE006_BackfillIdempotent(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()

	treeSvc := service.NewTreeService(
		getTreeRepo(t, pool), nil, nil, pool,
	)

	// Insert a tree with old-style metadata.
	treeID := uuid.New()
	insertTreeWithMeta(t, ctx, pool, treeID, "Backfill Test", `{"session_id": "backfill_sess"}`)

	// Compute new metadata with associations.
	newMeta := json.RawMessage(`{"session_id": "backfill_sess", "project": "hermes-canopy", "board_task": "WIRE-006"}`)

	// First backfill.
	if err := treeSvc.UpdateTreeMetadata(ctx, treeID, newMeta); err != nil {
		t.Fatalf("backfill 1: %v", err)
	}

	// Second backfill — identical metadata (idempotent).
	if err := treeSvc.UpdateTreeMetadata(ctx, treeID, newMeta); err != nil {
		t.Fatalf("backfill 2: %v", err)
	}

	// Verify the metadata is correct.
	var gotMeta []byte
	err := pool.QueryRow(ctx, `SELECT metadata FROM trees WHERE id = $1`, treeID).Scan(&gotMeta)
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(gotMeta, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["session_id"] != "backfill_sess" {
		t.Errorf("session_id = %v", m["session_id"])
	}
	if m["project"] != "hermes-canopy" {
		t.Errorf("project = %v", m["project"])
	}
	if m["board_task"] != "WIRE-006" {
		t.Errorf("board_task = %v", m["board_task"])
	}
}

