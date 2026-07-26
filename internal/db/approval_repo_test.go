// Approval repository integration tests. Uses a real PostgreSQL test
// database via testutil.NewIntegrationPool (unique DB per test).
package db_test

import (
	"context"
	"testing"
	"time"

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

// testApproval returns a minimal valid Approval for the given tree and node.
func testApproval(treeID, nodeID, ownerID uuid.UUID) *db.Approval {
	return &db.Approval{
		TreeID:      treeID,
		NodeID:      nodeID,
		OwnerID:     ownerID,
		RequestedBy: uuid.New(),
		Status:      db.ApprovalStatusPending,
	}
}

// createTreeAndNode creates a tree and a single root node for approval tests.
func createTreeAndNode(t *testing.T, pool *pgxpool.Pool) (tree *db.Tree, node *db.Node) {
	t.Helper()
	ctx := context.Background()
	treeRepo := db.NewPGTreeRepo(pool)
	nodeRepo := db.NewPGNodeRepo(pool)

	tree, err := treeRepo.Create(ctx, &db.Tree{
		OwnerID: uuid.New(),
		Title:   "Approval Test Tree",
	})
	require.NoError(t, err)

	node, err = nodeRepo.Create(ctx, &db.Node{
		TreeID:        tree.ID,
		AuthorID:      uuid.New(),
		Content:       "Approval Test Node",
		ContentFormat: db.ContentFormatMarkdown,
	})
	require.NoError(t, err)

	return tree, node
}

// ---------------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------------

func TestPGApprovalRepo_Create(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGApprovalRepo(pool)

	tree, node := createTreeAndNode(t, pool)
	ownerID := uuid.New()
	in := testApproval(tree.ID, node.ID, ownerID)

	out, err := repo.Create(ctx, in)
	require.NoError(t, err)
	require.NotNil(t, out)

	assert.NotEqual(t, uuid.Nil, out.ID)
	assert.Equal(t, tree.ID, out.TreeID)
	assert.Equal(t, node.ID, out.NodeID)
	assert.Equal(t, ownerID, out.OwnerID)
	assert.Equal(t, db.ApprovalStatusPending, out.Status)
	assert.False(t, out.CreatedAt.IsZero())
	assert.False(t, out.ExpiresAt.IsZero())
	assert.Nil(t, out.DecidedAt)
	assert.Nil(t, out.DecidedBy)
}

func TestPGApprovalRepo_Create_Nil(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGApprovalRepo(pool)

	out, err := repo.Create(ctx, nil)
	assert.Error(t, err)
	assert.Nil(t, out)
	assert.Contains(t, err.Error(), "approval is nil")
}

func TestPGApprovalRepo_Create_DuplicateNodeID(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGApprovalRepo(pool)

	tree, node := createTreeAndNode(t, pool)
	ownerID := uuid.New()
	in := testApproval(tree.ID, node.ID, ownerID)

	_, err := repo.Create(ctx, in)
	require.NoError(t, err)

	// Second create for the same node should fail (unique constraint).
	out, err := repo.Create(ctx, in)
	assert.Error(t, err)
	assert.Nil(t, out)
}

// ---------------------------------------------------------------------------
// GetByID
// ---------------------------------------------------------------------------

func TestPGApprovalRepo_GetByID(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGApprovalRepo(pool)

	tree, node := createTreeAndNode(t, pool)
	created, err := repo.Create(ctx, testApproval(tree.ID, node.ID, uuid.New()))
	require.NoError(t, err)

	got, err := repo.GetByID(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, created.ID, got.ID)
	assert.Equal(t, created.Status, got.Status)
}

func TestPGApprovalRepo_GetByID_NotFound(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGApprovalRepo(pool)

	got, err := repo.GetByID(ctx, uuid.New())
	assert.ErrorIs(t, err, db.ErrNotFound)
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// GetByNodeID
// ---------------------------------------------------------------------------

func TestPGApprovalRepo_GetByNodeID(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGApprovalRepo(pool)

	tree, node := createTreeAndNode(t, pool)
	created, err := repo.Create(ctx, testApproval(tree.ID, node.ID, uuid.New()))
	require.NoError(t, err)

	got, err := repo.GetByNodeID(ctx, node.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, created.ID, got.ID)
}

func TestPGApprovalRepo_GetByNodeID_NotFound(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGApprovalRepo(pool)

	got, err := repo.GetByNodeID(ctx, uuid.New())
	assert.ErrorIs(t, err, db.ErrNotFound)
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// ListPending
// ---------------------------------------------------------------------------

func TestPGApprovalRepo_ListPending(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGApprovalRepo(pool)

	tree, node := createTreeAndNode(t, pool)
	ownerID := uuid.New()

	// Create two pending approvals.
	a1, err := repo.Create(ctx, testApproval(tree.ID, node.ID, ownerID))
	require.NoError(t, err)

	// Second node for second approval.
	node2, err := db.NewPGNodeRepo(pool).Create(ctx, &db.Node{
		TreeID:        tree.ID,
		AuthorID:      uuid.New(),
		Content:       "Second Node",
		ContentFormat: db.ContentFormatMarkdown,
	})
	require.NoError(t, err)

	a2, err := repo.Create(ctx, testApproval(tree.ID, node2.ID, ownerID))
	require.NoError(t, err)

	approvals, total, err := repo.ListPending(ctx, ownerID, nil, 10, 0)
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Len(t, approvals, 2)
	assert.Equal(t, a1.ID, approvals[0].ID)
	assert.Equal(t, a2.ID, approvals[1].ID)
}

func TestPGApprovalRepo_ListPending_ByTree(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGApprovalRepo(pool)

	tree, node := createTreeAndNode(t, pool)
	ownerID := uuid.New()

	_, err := repo.Create(ctx, testApproval(tree.ID, node.ID, ownerID))
	require.NoError(t, err)

	// Filter by this tree.
	approvals, total, err := repo.ListPending(ctx, ownerID, &tree.ID, 10, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Len(t, approvals, 1)

	// Filter by non-matching tree.
	otherTreeID := uuid.New()
	approvals, total, err = repo.ListPending(ctx, ownerID, &otherTreeID, 10, 0)
	require.NoError(t, err)
	assert.Equal(t, 0, total)
	assert.Empty(t, approvals)
}

func TestPGApprovalRepo_ListPending_Empty(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGApprovalRepo(pool)

	approvals, total, err := repo.ListPending(ctx, uuid.New(), nil, 10, 0)
	require.NoError(t, err)
	assert.Equal(t, 0, total)
	assert.Empty(t, approvals)
}

// ---------------------------------------------------------------------------
// ListAll
// ---------------------------------------------------------------------------

func TestPGApprovalRepo_ListAll(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGApprovalRepo(pool)

	tree, node := createTreeAndNode(t, pool)
	ownerID := uuid.New()

	a1, err := repo.Create(ctx, testApproval(tree.ID, node.ID, ownerID))
	require.NoError(t, err)

	approvals, total, err := repo.ListAll(ctx, ownerID, 10, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Len(t, approvals, 1)
	assert.Equal(t, a1.ID, approvals[0].ID)
}

// ---------------------------------------------------------------------------
// ListByTree
// ---------------------------------------------------------------------------

func TestPGApprovalRepo_ListByTree(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGApprovalRepo(pool)

	tree, node := createTreeAndNode(t, pool)

	_, err := repo.Create(ctx, testApproval(tree.ID, node.ID, uuid.New()))
	require.NoError(t, err)

	approvals, err := repo.ListByTree(ctx, tree.ID, "", 10, 0)
	require.NoError(t, err)
	assert.Len(t, approvals, 1)
}

// ---------------------------------------------------------------------------
// Approve
// ---------------------------------------------------------------------------

func TestPGApprovalRepo_Approve(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGApprovalRepo(pool)

	tree, node := createTreeAndNode(t, pool)
	created, err := repo.Create(ctx, testApproval(tree.ID, node.ID, uuid.New()))
	require.NoError(t, err)

	actorID := uuid.New()
	approved, err := repo.Approve(ctx, created.ID, actorID, nil)
	require.NoError(t, err)
	require.NotNil(t, approved)
	assert.Equal(t, db.ApprovalStatusApproved, approved.Status)
	assert.NotNil(t, approved.DecidedBy)
	assert.Equal(t, actorID, *approved.DecidedBy)
	assert.NotNil(t, approved.DecidedAt)
}

func TestPGApprovalRepo_Approve_Twice(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGApprovalRepo(pool)

	tree, node := createTreeAndNode(t, pool)
	created, err := repo.Create(ctx, testApproval(tree.ID, node.ID, uuid.New()))
	require.NoError(t, err)

	actorID := uuid.New()
	_, err = repo.Approve(ctx, created.ID, actorID, nil)
	require.NoError(t, err)

	// Second approve should fail — already decided.
	_, err = repo.Approve(ctx, created.ID, actorID, nil)
	assert.ErrorIs(t, err, db.ErrNotFound)
}

func TestPGApprovalRepo_Approve_NotFound(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGApprovalRepo(pool)

	_, err := repo.Approve(ctx, uuid.New(), uuid.New(), nil)
	assert.ErrorIs(t, err, db.ErrNotFound)
}

// ---------------------------------------------------------------------------
// Deny
// ---------------------------------------------------------------------------

func TestPGApprovalRepo_Deny(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGApprovalRepo(pool)

	tree, node := createTreeAndNode(t, pool)
	created, err := repo.Create(ctx, testApproval(tree.ID, node.ID, uuid.New()))
	require.NoError(t, err)

	actorID := uuid.New()
	reason := "Not needed"
	denied, err := repo.Deny(ctx, created.ID, actorID, reason)
	require.NoError(t, err)
	require.NotNil(t, denied)
	assert.Equal(t, db.ApprovalStatusDenied, denied.Status)
	assert.NotNil(t, denied.DeniedReason)
	assert.Equal(t, reason, *denied.DeniedReason)
	assert.NotNil(t, denied.DecidedBy)
	assert.Equal(t, actorID, *denied.DecidedBy)
}

func TestPGApprovalRepo_Deny_EmptyReason(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGApprovalRepo(pool)

	tree, node := createTreeAndNode(t, pool)
	created, err := repo.Create(ctx, testApproval(tree.ID, node.ID, uuid.New()))
	require.NoError(t, err)

	_, err = repo.Deny(ctx, created.ID, uuid.New(), "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "deny reason required")
}

// ---------------------------------------------------------------------------
// ExpirePending
// ---------------------------------------------------------------------------

func TestPGApprovalRepo_ExpirePending(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGApprovalRepo(pool)

	tree, node := createTreeAndNode(t, pool)

	// Create an approval with an already-expired expires_at.
	expired := &db.Approval{
		TreeID:      tree.ID,
		NodeID:      node.ID,
		OwnerID:     uuid.New(),
		RequestedBy: uuid.New(),
		Status:      db.ApprovalStatusPending,
		ExpiresAt:   time.Now().UTC().Add(-1 * time.Hour),
	}
	created, err := repo.Create(ctx, expired)
	require.NoError(t, err)

	expiredIDs, err := repo.ExpirePending(ctx)
	require.NoError(t, err)
	assert.Contains(t, expiredIDs, created.ID)

	// Verify it's now expired.
	got, err := repo.GetByID(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, db.ApprovalStatusExpired, got.Status)
}

func TestPGApprovalRepo_ExpirePending_None(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGApprovalRepo(pool)

	tree, node := createTreeAndNode(t, pool)
	_, err := repo.Create(ctx, testApproval(tree.ID, node.ID, uuid.New()))
	require.NoError(t, err)

	expiredIDs, err := repo.ExpirePending(ctx)
	require.NoError(t, err)
	assert.Empty(t, expiredIDs)
}
