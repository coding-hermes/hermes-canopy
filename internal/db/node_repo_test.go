// Node repository integration tests. Uses a real PostgreSQL test
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

// createTestTree creates a minimal tree for node/edge tests.
func createTestTree(t *testing.T, pool *pgxpool.Pool) *db.Tree {
	t.Helper()
	ctx := context.Background()
	repo := db.NewPGTreeRepo(pool)
	tree, err := repo.Create(ctx, &db.Tree{
		OwnerID: uuid.New(),
		Title:   "Node Test Tree",
	})
	require.NoError(t, err)
	require.NotNil(t, tree)
	return tree
}

// testNode returns a minimal valid Node for the given tree.
func testNode(treeID, authorID uuid.UUID) *db.Node {
	return &db.Node{
		TreeID:        treeID,
		AuthorID:      authorID,
		Content:       "Test node content",
		ContentFormat: db.ContentFormatMarkdown,
		NodeType:      db.NodeTypeMessage,
	}
}

// ---------------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------------

func TestPGNodeRepo_Create(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGNodeRepo(pool)

	tree := createTestTree(t, pool)
	authorID := uuid.New()

	in := testNode(tree.ID, authorID)
	out, err := repo.Create(ctx, in)
	require.NoError(t, err)
	require.NotNil(t, out)

	assert.NotEqual(t, uuid.Nil, out.ID)
	assert.Equal(t, tree.ID, out.TreeID)
	assert.Equal(t, authorID, out.AuthorID)
	assert.Equal(t, "Test node content", out.Content)
	assert.Equal(t, db.NodeTypeMessage, out.NodeType)
	assert.Equal(t, db.ContentFormatMarkdown, out.ContentFormat) // default
	assert.False(t, out.CreatedAt.IsZero())
	assert.Nil(t, out.DeletedAt)
	assert.Greater(t, out.SequenceNum, int64(0))
}

func TestPGNodeRepo_Create_Nil(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGNodeRepo(pool)

	out, err := repo.Create(ctx, nil)
	assert.Error(t, err)
	assert.Nil(t, out)
	assert.Contains(t, err.Error(), "node is nil")
}

func TestPGNodeRepo_Create_WithDefaults(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGNodeRepo(pool)

	tree := createTestTree(t, pool)

	in := &db.Node{
		TreeID:   tree.ID,
		AuthorID: uuid.New(),
		Content:  "Minimal node",
	}
	out, err := repo.Create(ctx, in)
	require.NoError(t, err)
	require.NotNil(t, out)

	assert.Equal(t, db.ContentFormatMarkdown, out.ContentFormat)
	assert.Equal(t, db.NodeTypeMessage, out.NodeType)
}

// ---------------------------------------------------------------------------
// GetByID
// ---------------------------------------------------------------------------

func TestPGNodeRepo_GetByID(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGNodeRepo(pool)

	tree := createTestTree(t, pool)
	created, err := repo.Create(ctx, testNode(tree.ID, uuid.New()))
	require.NoError(t, err)

	got, err := repo.GetByID(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, created.ID, got.ID)
	assert.Equal(t, created.Content, got.Content)
}

func TestPGNodeRepo_GetByID_NotFound(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGNodeRepo(pool)

	got, err := repo.GetByID(ctx, uuid.New())
	assert.Nil(t, got)
	assert.ErrorIs(t, err, db.ErrNotFound)
}

func TestPGNodeRepo_GetByID_AfterSoftDelete(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGNodeRepo(pool)

	tree := createTestTree(t, pool)
	created, err := repo.Create(ctx, testNode(tree.ID, uuid.New()))
	require.NoError(t, err)

	err = repo.SoftDelete(ctx, created.ID)
	require.NoError(t, err)

	got, err := repo.GetByID(ctx, created.ID)
	assert.Nil(t, got)
	assert.ErrorIs(t, err, db.ErrNotFound)
}

// ---------------------------------------------------------------------------
// GetByTree
// ---------------------------------------------------------------------------

func TestPGNodeRepo_GetByTree(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGNodeRepo(pool)

	tree := createTestTree(t, pool)
	authorID := uuid.New()

	for i := 0; i < 3; i++ {
		_, err := repo.Create(ctx, &db.Node{
			TreeID:   tree.ID,
			AuthorID: authorID,
			Content:  "Node content",
		})
		require.NoError(t, err)
	}

	nodes, err := repo.GetByTree(ctx, tree.ID)
	require.NoError(t, err)
	assert.Len(t, nodes, 3)
}

func TestPGNodeRepo_GetByTree_Empty(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGNodeRepo(pool)

	nodes, err := repo.GetByTree(ctx, uuid.New())
	require.NoError(t, err)
	assert.Empty(t, nodes)
}

// ---------------------------------------------------------------------------
// GetChildren
// ---------------------------------------------------------------------------

func TestPGNodeRepo_GetChildren(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	nodeRepo := db.NewPGNodeRepo(pool)
	edgeRepo := db.NewPGEdgeRepo(pool)

	tree := createTestTree(t, pool)
	authorID := uuid.New()

	parent, err := nodeRepo.Create(ctx, testNode(tree.ID, authorID))
	require.NoError(t, err)

	child1, err := nodeRepo.Create(ctx, &db.Node{
		TreeID:   tree.ID,
		AuthorID: authorID,
		Content:  "Child 1",
	})
	require.NoError(t, err)
	child2, err := nodeRepo.Create(ctx, &db.Node{
		TreeID:   tree.ID,
		AuthorID: authorID,
		Content:  "Child 2",
	})
	require.NoError(t, err)

	_, err = edgeRepo.Create(ctx, &db.Edge{TreeID: tree.ID, SourceID: parent.ID, TargetID: child1.ID, EdgeType: db.EdgeTypeReply, SequenceNum: 1})
	require.NoError(t, err)
	_, err = edgeRepo.Create(ctx, &db.Edge{TreeID: tree.ID, SourceID: parent.ID, TargetID: child2.ID, EdgeType: db.EdgeTypeReply, SequenceNum: 2})
	require.NoError(t, err)

	children, err := nodeRepo.GetChildren(ctx, parent.ID)
	require.NoError(t, err)
	assert.Len(t, children, 2)
}

func TestPGNodeRepo_GetChildren_Empty(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGNodeRepo(pool)

	children, err := repo.GetChildren(ctx, uuid.New())
	require.NoError(t, err)
	assert.Empty(t, children)
}

// ---------------------------------------------------------------------------
// GetAncestors
// ---------------------------------------------------------------------------

func TestPGNodeRepo_GetAncestors(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	nodeRepo := db.NewPGNodeRepo(pool)

	tree := createTestTree(t, pool)
	authorID := uuid.New()

	root, err := nodeRepo.Create(ctx, &db.Node{
		TreeID:   tree.ID,
		AuthorID: authorID,
		Content:  "Root",
	})
	require.NoError(t, err)

	child, err := nodeRepo.Create(ctx, &db.Node{
		TreeID:   tree.ID,
		AuthorID: authorID,
		ParentID: &root.ID,
		Content:  "Child",
	})
	require.NoError(t, err)

	ancestors, err := nodeRepo.GetAncestors(ctx, child.ID)
	require.NoError(t, err)
	assert.Len(t, ancestors, 2)
	// Query orders by sequence_num ASC (root first, then descendants)
	assert.Equal(t, root.ID, ancestors[0].ID)
	assert.Equal(t, child.ID, ancestors[1].ID)
}

func TestPGNodeRepo_GetAncestors_Root(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGNodeRepo(pool)

	tree := createTestTree(t, pool)
	root, err := repo.Create(ctx, &db.Node{
		TreeID:   tree.ID,
		AuthorID: uuid.New(),
		Content:  "Root",
	})
	require.NoError(t, err)

	ancestors, err := repo.GetAncestors(ctx, root.ID)
	require.NoError(t, err)
	assert.Len(t, ancestors, 1)
	assert.Equal(t, root.ID, ancestors[0].ID)
}

// ---------------------------------------------------------------------------
// GetSubtree
// ---------------------------------------------------------------------------

func TestPGNodeRepo_GetSubtree(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	nodeRepo := db.NewPGNodeRepo(pool)
	edgeRepo := db.NewPGEdgeRepo(pool)

	tree := createTestTree(t, pool)
	authorID := uuid.New()

	root, err := nodeRepo.Create(ctx, &db.Node{
		TreeID:        tree.ID,
		AuthorID:      authorID,
		Content:       "Root",
		ContentFormat: db.ContentFormatMarkdown,
	})
	require.NoError(t, err)

	child, err := nodeRepo.Create(ctx, &db.Node{
		TreeID:        tree.ID,
		AuthorID:      authorID,
		Content:       "Child",
		ContentFormat: db.ContentFormatMarkdown,
	})
	require.NoError(t, err)

	// GetSubtree walks via edges, so create one.
	_, err = edgeRepo.Create(ctx, &db.Edge{
		TreeID:      tree.ID,
		SourceID:    root.ID,
		TargetID:    child.ID,
		EdgeType:    db.EdgeTypeReply,
		SequenceNum: 1,
	})
	require.NoError(t, err)

	subtree, err := nodeRepo.GetSubtree(ctx, root.ID, 0)
	require.NoError(t, err)
	assert.Len(t, subtree, 2)
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func TestPGNodeRepo_Update(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGNodeRepo(pool)

	tree := createTestTree(t, pool)
	created, err := repo.Create(ctx, testNode(tree.ID, uuid.New()))
	require.NoError(t, err)

	updated, err := repo.Update(ctx, created.ID, "Updated content", nil)
	require.NoError(t, err)
	require.NotNil(t, updated)

	assert.Equal(t, "Updated content", updated.Content)
	assert.NotNil(t, updated.EditedAt)
}

func TestPGNodeRepo_Update_NotFound(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGNodeRepo(pool)

	got, err := repo.Update(ctx, uuid.New(), "content", nil)
	assert.Nil(t, got)
	assert.ErrorIs(t, err, db.ErrNotFound)
}

// ---------------------------------------------------------------------------
// SoftDelete / HardDelete
// ---------------------------------------------------------------------------

func TestPGNodeRepo_SoftDelete(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGNodeRepo(pool)

	tree := createTestTree(t, pool)
	created, err := repo.Create(ctx, testNode(tree.ID, uuid.New()))
	require.NoError(t, err)

	err = repo.SoftDelete(ctx, created.ID)
	require.NoError(t, err)

	_, err = repo.GetByID(ctx, created.ID)
	assert.ErrorIs(t, err, db.ErrNotFound)
}

func TestPGNodeRepo_SoftDelete_NotFound(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGNodeRepo(pool)

	err := repo.SoftDelete(ctx, uuid.New())
	assert.ErrorIs(t, err, db.ErrNotFound)
}

func TestPGNodeRepo_HardDelete(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGNodeRepo(pool)

	tree := createTestTree(t, pool)
	created, err := repo.Create(ctx, testNode(tree.ID, uuid.New()))
	require.NoError(t, err)

	err = repo.HardDelete(ctx, created.ID)
	require.NoError(t, err)

	_, err = repo.GetByID(ctx, created.ID)
	assert.ErrorIs(t, err, db.ErrNotFound)
}

func TestPGNodeRepo_HardDelete_NotFound(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGNodeRepo(pool)

	err := repo.HardDelete(ctx, uuid.New())
	assert.ErrorIs(t, err, db.ErrNotFound)
}

// ---------------------------------------------------------------------------
// GetCounts
// ---------------------------------------------------------------------------

func TestPGNodeRepo_GetCounts(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	nodeRepo := db.NewPGNodeRepo(pool)

	tree := createTestTree(t, pool)

	for i := 0; i < 3; i++ {
		_, err := nodeRepo.Create(ctx, &db.Node{
			TreeID:   tree.ID,
			AuthorID: uuid.New(),
			Content:  "Node",
		})
		require.NoError(t, err)
	}

	counts, err := nodeRepo.GetCounts(ctx, tree.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(3), counts.TotalNodes)
	assert.Equal(t, int64(3), counts.ActiveNodes)
	assert.Equal(t, tree.ID, counts.TreeID)
}

func TestPGNodeRepo_GetCounts_EmptyTree(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGNodeRepo(pool)

	counts, err := repo.GetCounts(ctx, uuid.New())
	require.NoError(t, err)
	assert.Equal(t, int64(0), counts.TotalNodes)
	assert.Equal(t, int64(0), counts.ActiveNodes)
}
