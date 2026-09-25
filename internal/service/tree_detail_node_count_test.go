package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

func TestGetTree_DetailNodeCountMatchesActiveNodes(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	if _, err := db.EnsureDevJWTUser(ctx, pool); err != nil {
		t.Fatalf("EnsureDevJWTUser: %v", err)
	}
	ownerID, err := uuid.Parse(db.DevJWTUserID)
	if err != nil {
		t.Fatalf("parse dev user id: %v", err)
	}

	svc := NewTreeService(
		db.NewPGTreeRepo(pool),
		db.NewPGNodeRepo(pool),
		db.NewPGEdgeRepo(pool),
		pool,
	)

	tree, err := svc.CreateTree(ctx, CreateTreeParams{
		OwnerID:       ownerID,
		Title:         "detail node count",
		RootContent:   "root",
		ContentFormat: FormatMarkdown,
		NodeType:      NodeTypeMessage,
	})
	if err != nil {
		t.Fatalf("CreateTree: %v", err)
	}

	for i := 0; i < 3; i++ {
		_, err := pool.Exec(ctx, `
			INSERT INTO nodes (id, tree_id, parent_id, author_id, content,
			                   content_format, node_type, metadata)
			VALUES ($1, $2, $3, $4, $5, 'markdown', 'message', '{}'::jsonb)`,
			uuid.New(), tree.ID, tree.RootNodeID, tree.OwnerID, "child")
		if err != nil {
			t.Fatalf("insert active child %d: %v", i, err)
		}
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO nodes (id, tree_id, parent_id, author_id, content,
		                   content_format, node_type, metadata, deleted_at)
		VALUES ($1, $2, $3, $4, 'deleted child', 'markdown', 'message', '{}'::jsonb, $5)`,
		uuid.New(), tree.ID, tree.RootNodeID, tree.OwnerID, time.Now())
	if err != nil {
		t.Fatalf("insert deleted child: %v", err)
	}

	activeNodes, err := svc.nodeRepo.GetByTree(ctx, tree.ID)
	if err != nil {
		t.Fatalf("GetByTree: %v", err)
	}
	if len(activeNodes) != 4 {
		t.Fatalf("active node list length = %d, want 4", len(activeNodes))
	}

	withoutStats, err := svc.GetTree(ctx, tree.ID, GetTreeOptions{IncludeStats: false})
	if err != nil {
		t.Fatalf("GetTree without stats: %v", err)
	}
	if withoutStats.NodeCount != len(activeNodes) {
		t.Errorf("detail node_count without stats = %d, want %d", withoutStats.NodeCount, len(activeNodes))
	}

	withStats, err := svc.GetTree(ctx, tree.ID, GetTreeOptions{IncludeStats: true})
	if err != nil {
		t.Fatalf("GetTree with stats: %v", err)
	}
	if withStats.Stats == nil {
		t.Fatal("GetTree with stats returned nil stats")
	}
	if withStats.NodeCount != len(activeNodes) {
		t.Errorf("detail node_count with stats = %d, want %d", withStats.NodeCount, len(activeNodes))
	}
	if withStats.NodeCount != withStats.Stats.NodeCount {
		t.Errorf("detail node_count = %d, graph stats node_count = %d", withStats.NodeCount, withStats.Stats.NodeCount)
	}
}
