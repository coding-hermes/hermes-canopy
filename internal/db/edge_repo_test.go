// Edge repository integration tests. Uses a real PostgreSQL test
// database via testutil.NewIntegrationPool (unique DB per test).
package db_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/testutil"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// testEdge returns a minimal valid Edge connecting two nodes.
func testEdge(treeID, sourceID, targetID uuid.UUID) *db.Edge {
	return &db.Edge{
		TreeID:      treeID,
		SourceID:    sourceID,
		TargetID:    targetID,
		EdgeType:    db.EdgeTypeReply,
		SequenceNum: 1,
	}
}

// createTreeWithNodes creates a tree, a root node, and a child node —
// so edge tests can start with pre-existing nodes to connect.
func createTreeWithNodes(t *testing.T, pool *pgxpool.Pool) (tree *db.Tree, root, child *db.Node) {
	t.Helper()
	ctx := context.Background()
	treeRepo := db.NewPGTreeRepo(pool)
	nodeRepo := db.NewPGNodeRepo(pool)

	tree, err := treeRepo.Create(ctx, &db.Tree{
		OwnerID: uuid.New(),
		Title:   "Edge Test Tree",
	})
	require.NoError(t, err)

	root, err = nodeRepo.Create(ctx, &db.Node{
		TreeID:        tree.ID,
		AuthorID:      uuid.New(),
		Content:       "Root",
		ContentFormat: db.ContentFormatMarkdown,
	})
	require.NoError(t, err)

	child, err = nodeRepo.Create(ctx, &db.Node{
		TreeID:        tree.ID,
		AuthorID:      uuid.New(),
		Content:       "Child",
		ContentFormat: db.ContentFormatMarkdown,
	})
	require.NoError(t, err)

	return tree, root, child
}

// ---------------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------------

func TestPGEdgeRepo_Create(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	edgeRepo := db.NewPGEdgeRepo(pool)

	tree, root, child := createTreeWithNodes(t, pool)

	in := testEdge(tree.ID, root.ID, child.ID)
	out, err := edgeRepo.Create(ctx, in)
	require.NoError(t, err)
	require.NotNil(t, out)

	assert.NotEqual(t, uuid.Nil, out.ID)
	assert.Equal(t, tree.ID, out.TreeID)
	assert.Equal(t, root.ID, out.SourceID)
	assert.Equal(t, child.ID, out.TargetID)
	assert.Equal(t, db.EdgeTypeReply, out.EdgeType)
	assert.False(t, out.CreatedAt.IsZero())
	assert.Nil(t, out.DeletedAt)
}

func TestPGEdgeRepo_Create_Nil(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	edgeRepo := db.NewPGEdgeRepo(pool)

	out, err := edgeRepo.Create(ctx, nil)
	assert.Error(t, err)
	assert.Nil(t, out)
	assert.Contains(t, err.Error(), "edge is nil")
}

func TestPGEdgeRepo_Create_SelfEdge(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	edgeRepo := db.NewPGEdgeRepo(pool)

	tree, node, _ := createTreeWithNodes(t, pool)

	out, err := edgeRepo.Create(ctx, &db.Edge{
		TreeID:   tree.ID,
		SourceID: node.ID,
		TargetID: node.ID,
		EdgeType: db.EdgeTypeReply,
	})
	assert.Nil(t, out)
	assert.ErrorIs(t, err, db.ErrSelfEdge)
}

func TestPGEdgeRepo_Create_MultipleParents_Blocked(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	edgeRepo := db.NewPGEdgeRepo(pool)

	tree, root, child := createTreeWithNodes(t, pool)

	// Create one edge (root -> child).
	_, err := edgeRepo.Create(ctx, testEdge(tree.ID, root.ID, child.ID))
	require.NoError(t, err)

	// Create a second node and try to make it also a parent of child.
	secondParent, err := db.NewPGNodeRepo(pool).Create(ctx, &db.Node{
		TreeID:   tree.ID,
		AuthorID: uuid.New(),
		Content:  "Second Parent",
	})
	require.NoError(t, err)

	// Non-synthesis target — should be blocked.
	out, err := edgeRepo.Create(ctx, testEdge(tree.ID, secondParent.ID, child.ID))
	assert.Nil(t, out)
	assert.ErrorIs(t, err, db.ErrMultipleParents)
}

func TestPGEdgeRepo_Create_SynthesisAllowsMultipleParents(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	edgeRepo := db.NewPGEdgeRepo(pool)

	tree, _, _ := createTreeWithNodes(t, pool)

	// Create a synthesis node.
	synth, err := db.NewPGNodeRepo(pool).Create(ctx, &db.Node{
		TreeID:   tree.ID,
		AuthorID: uuid.New(),
		Content:  "Synthesis",
		NodeType: db.NodeTypeSynthesis,
	})
	require.NoError(t, err)

	// Create two source nodes.
	src1, err := db.NewPGNodeRepo(pool).Create(ctx, &db.Node{
		TreeID:   tree.ID,
		AuthorID: uuid.New(),
		Content:  "Source 1",
	})
	require.NoError(t, err)
	src2, err := db.NewPGNodeRepo(pool).Create(ctx, &db.Node{
		TreeID:   tree.ID,
		AuthorID: uuid.New(),
		Content:  "Source 2",
	})
	require.NoError(t, err)

	// Both edges to synthesis target should succeed.
	_, err = edgeRepo.Create(ctx, testEdge(tree.ID, src1.ID, synth.ID))
	require.NoError(t, err)
	_, err = edgeRepo.Create(ctx, testEdge(tree.ID, src2.ID, synth.ID))
	require.NoError(t, err)

	// Verify both exist.
	edges, err := edgeRepo.GetByTarget(ctx, synth.ID)
	require.NoError(t, err)
	assert.Len(t, edges, 2)
}

func TestPGEdgeRepo_Create_InvalidTarget(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	edgeRepo := db.NewPGEdgeRepo(pool)

	tree, _, _ := createTreeWithNodes(t, pool)

	out, err := edgeRepo.Create(ctx, &db.Edge{
		TreeID:   tree.ID,
		SourceID: uuid.New(),
		TargetID: uuid.New(),
		EdgeType: db.EdgeTypeReply,
	})
	assert.Nil(t, out)
	assert.ErrorIs(t, err, db.ErrNotFound)
}

// ---------------------------------------------------------------------------
// GetByID
// ---------------------------------------------------------------------------

func TestPGEdgeRepo_GetByID(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	edgeRepo := db.NewPGEdgeRepo(pool)

	tree, root, child := createTreeWithNodes(t, pool)
	created, err := edgeRepo.Create(ctx, testEdge(tree.ID, root.ID, child.ID))
	require.NoError(t, err)

	got, err := edgeRepo.GetByID(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, created.ID, got.ID)
}

func TestPGEdgeRepo_GetByID_NotFound(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	edgeRepo := db.NewPGEdgeRepo(pool)

	got, err := edgeRepo.GetByID(ctx, uuid.New())
	assert.Nil(t, got)
	assert.ErrorIs(t, err, db.ErrNotFound)
}

// ---------------------------------------------------------------------------
// GetBySource / GetByTarget / GetByTree
// ---------------------------------------------------------------------------

func TestPGEdgeRepo_GetBySource(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	edgeRepo := db.NewPGEdgeRepo(pool)

	tree, root, child := createTreeWithNodes(t, pool)

	_, err := edgeRepo.Create(ctx, testEdge(tree.ID, root.ID, child.ID))
	require.NoError(t, err)

	edges, err := edgeRepo.GetBySource(ctx, root.ID)
	require.NoError(t, err)
	assert.Len(t, edges, 1)
}

func TestPGEdgeRepo_GetByTarget(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	edgeRepo := db.NewPGEdgeRepo(pool)

	tree, root, child := createTreeWithNodes(t, pool)

	_, err := edgeRepo.Create(ctx, testEdge(tree.ID, root.ID, child.ID))
	require.NoError(t, err)

	edges, err := edgeRepo.GetByTarget(ctx, child.ID)
	require.NoError(t, err)
	assert.Len(t, edges, 1)
}

func TestPGEdgeRepo_GetByTree(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	edgeRepo := db.NewPGEdgeRepo(pool)
	nodeRepo := db.NewPGNodeRepo(pool)

	tree, root, child := createTreeWithNodes(t, pool)

	// Create another child.
	child2, err := nodeRepo.Create(ctx, &db.Node{
		TreeID:   tree.ID,
		AuthorID: uuid.New(),
		Content:  "Child 2",
	})
	require.NoError(t, err)

	_, err = edgeRepo.Create(ctx, testEdge(tree.ID, root.ID, child.ID))
	require.NoError(t, err)
	_, err = edgeRepo.Create(ctx, testEdge(tree.ID, root.ID, child2.ID))
	require.NoError(t, err)

	edges, err := edgeRepo.GetByTree(ctx, tree.ID)
	require.NoError(t, err)
	assert.Len(t, edges, 2)
}

// ---------------------------------------------------------------------------
// SoftDelete
// ---------------------------------------------------------------------------

func TestPGEdgeRepo_SoftDelete(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	edgeRepo := db.NewPGEdgeRepo(pool)

	tree, root, child := createTreeWithNodes(t, pool)
	created, err := edgeRepo.Create(ctx, testEdge(tree.ID, root.ID, child.ID))
	require.NoError(t, err)

	err = edgeRepo.SoftDelete(ctx, created.ID)
	require.NoError(t, err)

	_, err = edgeRepo.GetByID(ctx, created.ID)
	assert.ErrorIs(t, err, db.ErrNotFound)
}

func TestPGEdgeRepo_SoftDelete_NotFound(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	edgeRepo := db.NewPGEdgeRepo(pool)

	err := edgeRepo.SoftDelete(ctx, uuid.New())
	assert.ErrorIs(t, err, db.ErrNotFound)
}

// ---------------------------------------------------------------------------
// GetParents / GetSiblings
// ---------------------------------------------------------------------------

func TestPGEdgeRepo_GetParents(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	edgeRepo := db.NewPGEdgeRepo(pool)

	tree, root, child := createTreeWithNodes(t, pool)
	_, err := edgeRepo.Create(ctx, testEdge(tree.ID, root.ID, child.ID))
	require.NoError(t, err)

	parents, err := edgeRepo.GetParents(ctx, child.ID)
	require.NoError(t, err)
	assert.Len(t, parents, 1)
	assert.Equal(t, root.ID, parents[0].ID)
}

func TestPGEdgeRepo_GetSiblings(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	edgeRepo := db.NewPGEdgeRepo(pool)
	nodeRepo := db.NewPGNodeRepo(pool)

	tree, root, child := createTreeWithNodes(t, pool)

	// Create another child with the same edge type.
	child2, err := nodeRepo.Create(ctx, &db.Node{
		TreeID:   tree.ID,
		AuthorID: uuid.New(),
		Content:  "Sibling",
	})
	require.NoError(t, err)

	_, err = edgeRepo.Create(ctx, testEdge(tree.ID, root.ID, child.ID))
	require.NoError(t, err)
	_, err = edgeRepo.Create(ctx, testEdge(tree.ID, root.ID, child2.ID))
	require.NoError(t, err)

	siblings, err := edgeRepo.GetSiblings(ctx, root.ID, child.ID)
	require.NoError(t, err)
	assert.Len(t, siblings, 2) // child and child2 are siblings
}

// ---------------------------------------------------------------------------
// GetEdgeCounts
// ---------------------------------------------------------------------------

func TestPGEdgeRepo_GetEdgeCounts(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	edgeRepo := db.NewPGEdgeRepo(pool)

	tree, root, child := createTreeWithNodes(t, pool)

	_, err := edgeRepo.Create(ctx, testEdge(tree.ID, root.ID, child.ID))
	require.NoError(t, err)

	counts, err := edgeRepo.GetEdgeCounts(ctx, tree.ID)
	require.NoError(t, err)
	assert.Equal(t, tree.ID, counts.TreeID)
	assert.GreaterOrEqual(t, counts.Total, int64(1))
	assert.Equal(t, int64(1), counts.Active)
	assert.Equal(t, 1, counts.ByType[db.EdgeTypeReply])
}

// ---------------------------------------------------------------------------
// Move
// ---------------------------------------------------------------------------

func TestPGEdgeRepo_Move(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	edgeRepo := db.NewPGEdgeRepo(pool)
	nodeRepo := db.NewPGNodeRepo(pool)

	tree, root, child := createTreeWithNodes(t, pool)

	created, err := edgeRepo.Create(ctx, testEdge(tree.ID, root.ID, child.ID))
	require.NoError(t, err)

	// Create a new source node.
	newSource, err := nodeRepo.Create(ctx, &db.Node{
		TreeID:   tree.ID,
		AuthorID: uuid.New(),
		Content:  "New Source",
	})
	require.NoError(t, err)

	moved, err := edgeRepo.Move(ctx, created.ID, newSource.ID)
	require.NoError(t, err)
	require.NotNil(t, moved)
	assert.Equal(t, newSource.ID, moved.SourceID)
}

func TestPGEdgeRepo_Move_NotFound(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	edgeRepo := db.NewPGEdgeRepo(pool)

	out, err := edgeRepo.Move(ctx, uuid.New(), uuid.New())
	assert.Nil(t, out)
	assert.ErrorIs(t, err, db.ErrNotFound)
}
