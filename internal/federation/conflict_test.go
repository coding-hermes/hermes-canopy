package federation

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

func TestConcurrentWriteLWWAndReplayIdempotency(t *testing.T) {
	t.Setenv("CANOPY_REQUIRE_DB", "1")
	pool := testutil.NewIntegrationPool(t)
	ctx := context.Background()
	ownerID, profileID, treeID, nodeID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,hermes_user_id,display_name) VALUES($1,$2,'Conflict Owner')`, ownerID, "conflict-"+ownerID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO profiles(id,owner_id,profile_type,name,display_name) VALUES($1,$2,'hermes-profile',$3,'Conflict Profile')`, profileID, ownerID, "conflict-"+profileID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO trees(id,owner_id,title) VALUES($1,$2,'Conflict Tree')`, treeID, profileID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO nodes(id,tree_id,author_id,content) VALUES($1,$2,$3,'base')`, nodeID, treeID, profileID); err != nil {
		t.Fatal(err)
	}
	store := newConflictStore(pool)
	leftPeer := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	rightPeer := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	left := &mutationSnapshot{nodeID, json.RawMessage(`{"node_id":"` + nodeID.String() + `","content":"left"}`), VectorClock{leftPeer.String(): 1}, 7, leftPeer}
	right := &mutationSnapshot{nodeID, json.RawMessage(`{"node_id":"` + nodeID.String() + `","content":"right"}`), VectorClock{rightPeer.String(): 1}, 7, rightPeer}
	if err := store.apply(ctx, treeID, left); err != nil {
		t.Fatal(err)
	}
	if err := store.apply(ctx, treeID, right); err != nil {
		t.Fatal(err)
	}
	if err := store.apply(ctx, treeID, right); err != nil {
		t.Fatal(err)
	}
	var content string
	var count int
	if err := pool.QueryRow(ctx, `SELECT content FROM nodes WHERE id=$1`, nodeID).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM federation_conflicts WHERE node_id=$1`, nodeID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if content != "right" || count != 1 {
		t.Fatalf("winner content = %q, conflicts = %d; want right, 1", content, count)
	}
}
