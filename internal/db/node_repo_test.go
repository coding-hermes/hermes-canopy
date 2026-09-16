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

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
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
// GAP-073 — subtree result must not repeat a node per incoming path
// ---------------------------------------------------------------------------

// gap073Tree is the fixture for the GAP-073 regression: a root with three
// children, a multi_reference reply reachable through ALL three of them (three
// active incoming reference edges — the condition that made the recursive CTE
// emit the node once per path), and one descendant below the reply so a
// duplicated row duplicates its subtree too.
type gap073Tree struct {
	treeID   uuid.UUID
	root     uuid.UUID
	reply    uuid.UUID
	followup uuid.UUID
	sources  []uuid.UUID
}

// gap073BuildTree writes the GAP-073 fixture against a real database.
// It mirrors the live probe (tick 464): an 11-node tree holding one 3-source
// multi-reference reply returned 13 node rows for 11 unique nodes; here the
// shape is reduced so the expected counts are exact.
func gap073BuildTree(t *testing.T, pool *pgxpool.Pool) gap073Tree {
	t.Helper()
	ctx := context.Background()

	treeID := refTestTree(t, pool)
	root := refTestNode(t, pool, treeID, nil, db.NodeTypeMessage, db.ParentModeLineage, "root")
	sourceA := refTestNode(t, pool, treeID, &root, db.NodeTypeMessage, db.ParentModeLineage, "source A")
	sourceB := refTestNode(t, pool, treeID, &root, db.NodeTypeMessage, db.ParentModeLineage, "source B")
	sourceC := refTestNode(t, pool, treeID, &root, db.NodeTypeMessage, db.ParentModeLineage, "source C")
	reply := refTestNode(t, pool, treeID, &sourceA, db.NodeTypeMessage, db.ParentModeMultiReference, "multi-source reply")
	followup := refTestNode(t, pool, treeID, &reply, db.NodeTypeMessage, db.ParentModeLineage, "follow-up")

	// Display edges root → A/B/C, the way the node service writes them
	// (explicit sequence_num — edges.sequence_num is NOT NULL, no default).
	for i, src := range []uuid.UUID{sourceA, sourceB, sourceC} {
		_, err := pool.Exec(ctx, `
            INSERT INTO edges (tree_id, source_id, target_id, edge_type, sequence_num)
            VALUES ($1, $2, $3, $4, $5)`,
			treeID, root, src, db.EdgeTypeReply, int64(i+1))
		require.NoError(t, err, "insert display edge root -> source %d", i)
	}
	_, err := pool.Exec(ctx, `
        INSERT INTO edges (tree_id, source_id, target_id, edge_type, sequence_num)
        VALUES ($1, $2, $3, $4, $5)`,
		treeID, reply, followup, db.EdgeTypeReply, 4)
	require.NoError(t, err, "insert display edge reply -> follow-up")

	// The 3-source reference set: A, B and C all point at the reply, so the
	// reply has THREE active incoming edges.
	tx, err := pool.Begin(ctx)
	require.NoError(t, err, "begin reference-set tx")
	edges, err := db.NewPGEdgeRepo(pool).CreateReferenceSet(ctx, tx, db.CreateReferenceSetInput{
		TreeID:         treeID,
		TargetID:       reply,
		OrderedSources: []uuid.UUID{sourceA, sourceB, sourceC},
	})
	if err != nil {
		_ = tx.Rollback(ctx)
		require.NoError(t, err, "CreateReferenceSet")
	}
	require.NoError(t, tx.Commit(ctx), "commit reference set")
	require.Len(t, edges, 3, "fixture must carry exactly 3 reference edges")

	return gap073Tree{
		treeID:   treeID,
		root:     root,
		reply:    reply,
		followup: followup,
		sources:  []uuid.UUID{sourceA, sourceB, sourceC},
	}
}

// gap073CountRows tallies node rows per id.
func gap073CountRows(nodes []db.Node) map[uuid.UUID]int {
	counts := make(map[uuid.UUID]int, len(nodes))
	for _, n := range nodes {
		counts[n.ID]++
	}
	return counts
}

// TestGAP073_GetSubtree_MultiParentNodesAppearOnce is the GAP-073 regression:
// GetSubtree must return each node AT MOST ONCE, regardless of how many active
// incoming edges make it reachable. Needs a real PostgreSQL — the duplication
// lives in the recursive CTE, so a stub or an empty database proves nothing.
//
// Pre-fix row counts for this fixture (5 paths over 6 nodes): 10 rows.
func TestGAP073_GetSubtree_MultiParentNodesAppearOnce(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	nodeRepo := db.NewPGNodeRepo(pool)
	edgeRepo := db.NewPGEdgeRepo(pool)

	fx := gap073BuildTree(t, pool)

	active, err := nodeRepo.GetByTree(ctx, fx.treeID)
	require.NoError(t, err)
	require.Len(t, active, 6, "fixture must really hold 6 active nodes")

	nodes, err := nodeRepo.GetSubtree(ctx, fx.root, 0)
	require.NoError(t, err)

	// (a) every returned node id is unique.
	counts := gap073CountRows(nodes)
	if len(nodes) != len(counts) {
		t.Errorf("GAP-073: GetSubtree returned %d node rows for %d unique ids (want one row per node)",
			len(nodes), len(counts))
	}
	for id, count := range counts {
		if count != 1 {
			t.Errorf("GAP-073: node %s returned %d times, want exactly 1", id, count)
		}
	}

	// (b) the unique node count equals the tree's active node count.
	require.Equal(t, len(active), len(counts),
		"subtree must reach every active node of the tree exactly once")
	for _, want := range active {
		if _, ok := counts[want.ID]; !ok {
			t.Errorf("GAP-073: subtree is missing active node %s (%q)", want.ID, want.Content)
		}
	}
	// The multi-parent node itself is present exactly once.
	if got := counts[fx.reply]; got != 1 {
		t.Errorf("GAP-073: multi-parent reply %s returned %d times, want 1", fx.reply, got)
	}

	// Ordering contract is preserved (ORDER BY sequence_num ASC).
	for i := 1; i < len(nodes); i++ {
		assert.LessOrEqual(t, nodes[i-1].SequenceNum, nodes[i].SequenceNum,
			"subtree rows must stay ordered by sequence_num ASC")
	}

	// (c) a soft-deleted edge must not resurrect a duplicate: with one of the
	// three reference edges gone the reply still has two active incoming
	// edges, so a per-path emission would return it twice.
	var edgeID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `
        SELECT id FROM edges
        WHERE tree_id = $1 AND source_id = $2 AND target_id = $3 AND deleted_at IS NULL`,
		fx.treeID, fx.sources[2], fx.reply).Scan(&edgeID), "locate the third reference edge")
	require.NoError(t, edgeRepo.SoftDelete(ctx, edgeID), "soft-delete the third reference edge")

	after, err := nodeRepo.GetSubtree(ctx, fx.root, 0)
	require.NoError(t, err)
	afterCounts := gap073CountRows(after)
	if len(after) != len(nodes) {
		t.Errorf("GAP-073: after soft-deleting one incoming edge GetSubtree returned %d rows, want %d",
			len(after), len(nodes))
	}
	if len(after) != len(afterCounts) {
		t.Errorf("GAP-073: after soft-deleting one incoming edge GetSubtree returned %d rows for %d unique ids",
			len(after), len(afterCounts))
	}
	if got := afterCounts[fx.reply]; got != 1 {
		t.Errorf("GAP-073: reply %s returned %d times after the edge soft-delete, want 1", fx.reply, got)
	}
	if got := afterCounts[fx.followup]; got != 1 {
		t.Errorf("GAP-073: follow-up %s returned %d times after the edge soft-delete, want 1", fx.followup, got)
	}
}

// TestGAP073_GetSubtree_DepthSemanticsPreserved pins the depth contract while
// the dedupe is in place: maxDepth == 0 is unbounded, maxDepth == -1 is
// normalized to 0, and maxDepth == n limits the walk to n levels below the
// root.
func TestGAP073_GetSubtree_DepthSemanticsPreserved(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	nodeRepo := db.NewPGNodeRepo(pool)

	fx := gap073BuildTree(t, pool)

	unbounded, err := nodeRepo.GetSubtree(ctx, fx.root, 0)
	require.NoError(t, err)
	require.Len(t, unbounded, 6, "maxDepth 0 reaches the whole tree")

	// depth 1 = root + its three direct children (the reply sits at depth 2).
	shallow, err := nodeRepo.GetSubtree(ctx, fx.root, 1)
	require.NoError(t, err)
	require.Len(t, shallow, 4, "maxDepth 1 must stop one level below the root")
	require.Len(t, gap073CountRows(shallow), 4, "maxDepth 1 rows must be unique")

	// depth 2 adds the reply but not the follow-up below it.
	medium, err := nodeRepo.GetSubtree(ctx, fx.root, 2)
	require.NoError(t, err)
	require.Len(t, medium, 5, "maxDepth 2 must include the reply but not its child")
	require.Len(t, gap073CountRows(medium), 5, "maxDepth 2 rows must be unique")
	if _, ok := gap073CountRows(medium)[fx.followup]; ok {
		t.Errorf("GAP-073: maxDepth 2 leaked the depth-3 node %s", fx.followup)
	}

	// -1 is normalized to 0 (unbounded) — same set, same ordering.
	normalized, err := nodeRepo.GetSubtree(ctx, fx.root, -1)
	require.NoError(t, err)
	require.Len(t, normalized, len(unbounded), "maxDepth -1 must behave exactly like 0")
	for i := range unbounded {
		assert.Equal(t, unbounded[i].ID, normalized[i].ID,
			"maxDepth -1 must preserve the unbounded ordering")
	}
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
