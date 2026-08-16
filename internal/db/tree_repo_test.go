// Tree repository integration tests. Uses a real PostgreSQL test
// database via testutil.NewIntegrationPool (unique DB per test).
package db_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

// helper to build a minimal valid Tree for testing.
func testTree(ownerID uuid.UUID) *db.Tree {
	return &db.Tree{
		OwnerID:     ownerID,
		Title:       "Test Tree",
		Description: "A tree for integration tests",
	}
}

// ---------------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------------

func TestPGTreeRepo_Create(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTreeRepo(pool)

	ownerID := uuid.New()
	in := testTree(ownerID)
	out, err := repo.Create(ctx, in)
	require.NoError(t, err)
	require.NotNil(t, out)

	assert.NotEqual(t, uuid.Nil, out.ID)
	assert.Equal(t, ownerID, out.OwnerID)
	assert.Equal(t, "Test Tree", out.Title)
	assert.Equal(t, "A tree for integration tests", out.Description)
	assert.False(t, out.CreatedAt.IsZero())
	assert.Nil(t, out.DeletedAt)
}

func TestPGTreeRepo_Create_Nil(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTreeRepo(pool)

	out, err := repo.Create(ctx, nil)
	assert.Error(t, err)
	assert.Nil(t, out)
	assert.Contains(t, err.Error(), "tree is nil")
}

// ---------------------------------------------------------------------------
// GetByID
// ---------------------------------------------------------------------------

func TestPGTreeRepo_GetByID(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTreeRepo(pool)

	// Create a tree to retrieve.
	created, err := repo.Create(ctx, testTree(uuid.New()))
	require.NoError(t, err)

	// Retrieve by ID.
	got, err := repo.GetByID(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, created.ID, got.ID)
	assert.Equal(t, created.Title, got.Title)
}

func TestPGTreeRepo_GetByID_NotFound(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTreeRepo(pool)

	got, err := repo.GetByID(ctx, uuid.New())
	assert.Nil(t, got)
	assert.ErrorIs(t, err, db.ErrNotFound)
}

func TestPGTreeRepo_GetByID_AfterSoftDelete(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTreeRepo(pool)

	created, err := repo.Create(ctx, testTree(uuid.New()))
	require.NoError(t, err)

	// Soft-delete the tree.
	err = repo.SoftDelete(ctx, created.ID)
	require.NoError(t, err)

	// Should no longer be findable.
	got, err := repo.GetByID(ctx, created.ID)
	assert.Nil(t, got)
	assert.ErrorIs(t, err, db.ErrNotFound)
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

func TestPGTreeRepo_List(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTreeRepo(pool)

	owner := uuid.New()
	// Create 3 trees.
	for i := 0; i < 3; i++ {
		tr := testTree(owner)
		if i == 1 {
			tr.Title = "Second Tree"
		}
		_, err := repo.Create(ctx, tr)
		require.NoError(t, err)
	}

	// List with default limit (clamped to 50).
	trees, err := repo.List(ctx, 50, 0)
	require.NoError(t, err)
	assert.Len(t, trees, 3)

	// List with offset.
	trees, err = repo.List(ctx, 50, 1)
	require.NoError(t, err)
	assert.Len(t, trees, 2)

	// List with limit.
	trees, err = repo.List(ctx, 1, 0)
	require.NoError(t, err)
	assert.Len(t, trees, 1)
}

func TestPGTreeRepo_List_Clamped(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTreeRepo(pool)

	// Create a tree.
	_, err := repo.Create(ctx, testTree(uuid.New()))
	require.NoError(t, err)

	// limit <= 0 is clamped to 50.
	trees, err := repo.List(ctx, 0, 0)
	require.NoError(t, err)
	assert.NotEmpty(t, trees)

	// offset < 0 is clamped to 0.
	trees, err = repo.List(ctx, 50, -5)
	require.NoError(t, err)
	assert.NotEmpty(t, trees)
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func TestPGTreeRepo_Update(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTreeRepo(pool)

	created, err := repo.Create(ctx, testTree(uuid.New()))
	require.NoError(t, err)

	// Mutate and update.
	created.Title = "Updated Title"
	created.Description = "Updated Desc"
	updated, err := repo.Update(ctx, created)
	require.NoError(t, err)
	require.NotNil(t, updated)

	assert.Equal(t, "Updated Title", updated.Title)
	assert.Equal(t, "Updated Desc", updated.Description)
	assert.NotNil(t, updated.EditedAt, "edited_at should be set after update")
}

func TestPGTreeRepo_Update_Nil(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTreeRepo(pool)

	out, err := repo.Update(ctx, nil)
	assert.Error(t, err)
	assert.Nil(t, out)
	assert.Contains(t, err.Error(), "tree is nil")
}

func TestPGTreeRepo_Update_NotFound(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTreeRepo(pool)

	// Non-existent ID.
	tr := testTree(uuid.New())
	tr.ID = uuid.New()
	out, err := repo.Update(ctx, tr)
	assert.Nil(t, out)
	assert.ErrorIs(t, err, db.ErrNotFound)
}

// ---------------------------------------------------------------------------
// SoftDelete
// ---------------------------------------------------------------------------

func TestPGTreeRepo_SoftDelete(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTreeRepo(pool)

	created, err := repo.Create(ctx, testTree(uuid.New()))
	require.NoError(t, err)

	err = repo.SoftDelete(ctx, created.ID)
	require.NoError(t, err)

	// Verify it's gone.
	_, err = repo.GetByID(ctx, created.ID)
	assert.ErrorIs(t, err, db.ErrNotFound)
}

func TestPGTreeRepo_SoftDelete_NotFound(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTreeRepo(pool)

	err := repo.SoftDelete(ctx, uuid.New())
	assert.ErrorIs(t, err, db.ErrNotFound)
}

// ---------------------------------------------------------------------------
// GetByOwner
// ---------------------------------------------------------------------------

func TestPGTreeRepo_GetByOwner(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTreeRepo(pool)

	ownerID := uuid.New()
	otherID := uuid.New()

	// Create trees for ownerID.
	_, err := repo.Create(ctx, &db.Tree{
		OwnerID: ownerID, Title: "Owner Tree 1",
	})
	require.NoError(t, err)
	_, err = repo.Create(ctx, &db.Tree{
		OwnerID: ownerID, Title: "Owner Tree 2",
	})
	require.NoError(t, err)

	// Create a tree for otherID.
	_, err = repo.Create(ctx, &db.Tree{
		OwnerID: otherID, Title: "Other Tree",
	})
	require.NoError(t, err)

	trees, err := repo.GetByOwner(ctx, ownerID)
	require.NoError(t, err)
	assert.Len(t, trees, 2)

	for _, tr := range trees {
		assert.Equal(t, ownerID, tr.OwnerID)
	}
}

func TestPGTreeRepo_GetByOwner_Empty(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTreeRepo(pool)

	trees, err := repo.GetByOwner(ctx, uuid.New())
	require.NoError(t, err)
	assert.Empty(t, trees)
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

func TestPGTreeRepo_Search(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTreeRepo(pool)

	owner := uuid.New()
	_, err := repo.Create(ctx, &db.Tree{
		OwnerID: owner, Title: "Alpha Project",
	})
	require.NoError(t, err)
	_, err = repo.Create(ctx, &db.Tree{
		OwnerID: owner, Title: "Beta Discussion",
		Description: "Deep dive on alpha features",
	})
	require.NoError(t, err)

	// Search by title.
	results, err := repo.Search(ctx, "Alpha", 50, 0)
	require.NoError(t, err)
	assert.Len(t, results, 2) // Both match: title "Alpha..." and description contains "alpha"

	// Search by specific term.
	results, err = repo.Search(ctx, "Beta", 50, 0)
	require.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, "Beta Discussion", results[0].Title)
}

func TestPGTreeRepo_Search_EmptyQuery(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTreeRepo(pool)

	results, err := repo.Search(ctx, "", 50, 0)
	assert.Nil(t, results)
	assert.ErrorIs(t, err, db.ErrNotFound)
}

func TestPGTreeRepo_Search_NoMatch(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTreeRepo(pool)

	_, err := repo.Create(ctx, testTree(uuid.New()))
	require.NoError(t, err)

	results, err := repo.Search(ctx, "XYZ_NONEXISTENT", 50, 0)
	require.NoError(t, err)
	assert.Empty(t, results)
}

// ---------------------------------------------------------------------------
// Count (PAG-002)
// ---------------------------------------------------------------------------

func TestPGTreeRepo_Count(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTreeRepo(pool)

	// Seed 3 active trees.
	for i := 0; i < 3; i++ {
		_, err := repo.Create(ctx, testTree(uuid.New()))
		require.NoError(t, err)
	}

	n, err := repo.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, n, "Count should return the number of active trees")
}

func TestPGTreeRepo_Count_ExcludesDeleted(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTreeRepo(pool)

	// Create 2 active + 1 deleted.
	active1, err := repo.Create(ctx, testTree(uuid.New()))
	require.NoError(t, err)
	_, err = repo.Create(ctx, testTree(uuid.New()))
	require.NoError(t, err)
	deleted, err := repo.Create(ctx, testTree(uuid.New()))
	require.NoError(t, err)
	require.NoError(t, repo.SoftDelete(ctx, deleted.ID))

	n, err := repo.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, n, "Count should exclude soft-deleted trees")

	// active1 must still be findable.
	_, err = repo.GetByID(ctx, active1.ID)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// ListKeyset (PAG-002)
// ---------------------------------------------------------------------------

func TestPGTreeRepo_ListKeyset_FirstPage(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTreeRepo(pool)

	// Seed 5 trees.
	owner := uuid.New()
	for i := 0; i < 5; i++ {
		_, err := repo.Create(ctx, testTree(owner))
		require.NoError(t, err)
	}

	// First page (nil cursor), limit=2.
	page, err := repo.ListKeyset(ctx, nil, 2)
	require.NoError(t, err)
	assert.Len(t, page, 2)
}

func TestPGTreeRepo_ListKeyset_TwoPageWalk(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTreeRepo(pool)

	// Seed 5 trees.
	owner := uuid.New()
	for i := 0; i < 5; i++ {
		_, err := repo.Create(ctx, testTree(owner))
		require.NoError(t, err)
	}

	// Page 1.
	page1, err := repo.ListKeyset(ctx, nil, 2)
	require.NoError(t, err)
	assert.Len(t, page1, 2)

	// Page 2: cursor = last ID of page1.
	cursor := page1[len(page1)-1].ID
	page2, err := repo.ListKeyset(ctx, &cursor, 2)
	require.NoError(t, err)
	assert.Len(t, page2, 2)

	// No overlap.
	seen := make(map[uuid.UUID]bool)
	for _, tr := range append(page1, page2...) {
		assert.False(t, seen[tr.ID], "tree %s appeared twice", tr.ID)
		seen[tr.ID] = true
	}

	// Page 3: cursor = last ID of page2 → should have 1 tree.
	cursor = page2[len(page2)-1].ID
	page3, err := repo.ListKeyset(ctx, &cursor, 2)
	require.NoError(t, err)
	assert.Len(t, page3, 1)

	// Verify all 5 are covered.
	for _, tr := range page3 {
		seen[tr.ID] = true
	}
	assert.Len(t, seen, 5)
}

func TestPGTreeRepo_ListKeyset_EmptyCursor(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTreeRepo(pool)

	// Non-existent cursor → no rows match (subquery returns NULL → NULL < anything is false).
	phantom := uuid.New()
	page, err := repo.ListKeyset(ctx, &phantom, 10)
	require.NoError(t, err)
	assert.Empty(t, page)
}
