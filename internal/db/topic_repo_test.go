// Topic repository integration tests. Uses a real PostgreSQL test
// database via testutil.NewIntegrationPool (unique DB per test).
package db_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/testutil"
)

// ---------------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------------

func TestPGTopicRepo_Create(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTopicRepo(pool)

	tree, node := createTreeAndNode(t, pool)

	out, err := repo.Create(ctx, db.TopicCreateInput{
		TreeID:      tree.ID,
		RootNodeID:  node.ID,
		Title:       "Test Topic",
		Description: "A topic for integration tests",
		TopicTags:   []string{},
	})
	require.NoError(t, err)
	require.NotNil(t, out)

	assert.NotEqual(t, uuid.Nil, out.ID)
	assert.Equal(t, tree.ID, out.TreeID)
	assert.Equal(t, node.ID, out.RootNodeID)
	assert.Equal(t, "Test Topic", out.Title)
	assert.Equal(t, "test-topic", out.Slug)
	assert.Equal(t, "active", out.Status)
	assert.False(t, out.CreatedAt.IsZero())
	assert.Nil(t, out.ArchivedAt)
	assert.Nil(t, out.DeletedAt)
}

func TestPGTopicRepo_Create_WithTags(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTopicRepo(pool)

	tree, node := createTreeAndNode(t, pool)

	tags := []string{"design", "architecture", "v2"}
	out, err := repo.Create(ctx, db.TopicCreateInput{
		TreeID:      tree.ID,
		RootNodeID:  node.ID,
		Title:       "Tagged Topic",
		TopicTags:   tags,
	})
	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, tags, out.TopicTags)
}

func TestPGTopicRepo_Create_WithParentTopic(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTopicRepo(pool)

	tree, node := createTreeAndNode(t, pool)

	parent, err := repo.Create(ctx, db.TopicCreateInput{
		TreeID:     tree.ID,
		RootNodeID: node.ID,
		Title:      "Parent Topic",
		TopicTags:   []string{},
	})
	require.NoError(t, err)

	tree2, node2 := createTreeAndNode(t, pool)
	child, err := repo.Create(ctx, db.TopicCreateInput{
		TreeID:        tree2.ID,
		RootNodeID:    node2.ID,
		Title:         "Child Topic",
		ParentTopicID: &parent.ID,
		TopicTags:     []string{},
	})
	require.NoError(t, err)
	require.NotNil(t, child)
	assert.NotNil(t, child.ParentTopicID)
	assert.Equal(t, parent.ID, *child.ParentTopicID)
}

// ---------------------------------------------------------------------------
// GetByID
// ---------------------------------------------------------------------------

func TestPGTopicRepo_GetByID(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTopicRepo(pool)

	tree, node := createTreeAndNode(t, pool)
	created, err := repo.Create(ctx, db.TopicCreateInput{
		TreeID:     tree.ID,
		RootNodeID: node.ID,
		Title:      "GetByID Topic",
		TopicTags:  []string{},
	})
	require.NoError(t, err)

	got, err := repo.GetByID(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, created.ID, got.ID)
	assert.Equal(t, created.Title, got.Title)
}

func TestPGTopicRepo_GetByID_NotFound(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTopicRepo(pool)

	got, err := repo.GetByID(ctx, uuid.New())
	assert.ErrorIs(t, err, db.ErrNotFound)
	assert.Nil(t, got)
}

func TestPGTopicRepo_GetByID_SoftDeleted(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTopicRepo(pool)

	tree, node := createTreeAndNode(t, pool)
	created, err := repo.Create(ctx, db.TopicCreateInput{
		TreeID:     tree.ID,
		RootNodeID: node.ID,
		Title:      "To Delete",
		TopicTags:  []string{},
	})
	require.NoError(t, err)

	err = repo.SoftDelete(ctx, created.ID)
	require.NoError(t, err)

	// Soft-deleted topics should not be returned by GetByID.
	got, err := repo.GetByID(ctx, created.ID)
	assert.ErrorIs(t, err, db.ErrNotFound)
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// GetByTree
// ---------------------------------------------------------------------------

func TestPGTopicRepo_GetByTree(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTopicRepo(pool)

	tree, node := createTreeAndNode(t, pool)

	_, err := repo.Create(ctx, db.TopicCreateInput{
		TreeID:     tree.ID,
		RootNodeID: node.ID,
		Title:      "Topic 1",
		TopicTags:  []string{},
	})
	require.NoError(t, err)

	topics, err := repo.GetByTree(ctx, tree.ID, "")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(topics), 1)
}

// ---------------------------------------------------------------------------
// GetBySlug
// ---------------------------------------------------------------------------

func TestPGTopicRepo_GetBySlug(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTopicRepo(pool)

	tree, node := createTreeAndNode(t, pool)
	created, err := repo.Create(ctx, db.TopicCreateInput{
		TreeID:     tree.ID,
		RootNodeID: node.ID,
		Title:      "Slug Test Topic",
		TopicTags:  []string{},
	})
	require.NoError(t, err)

	got, err := repo.GetBySlug(ctx, tree.ID, created.Slug)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, created.ID, got.ID)
}

func TestPGTopicRepo_GetBySlug_NotFound(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTopicRepo(pool)

	got, err := repo.GetBySlug(ctx, uuid.New(), "non-existent")
	assert.ErrorIs(t, err, db.ErrNotFound)
	assert.Nil(t, got)
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func TestPGTopicRepo_Update(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTopicRepo(pool)

	tree, node := createTreeAndNode(t, pool)
	created, err := repo.Create(ctx, db.TopicCreateInput{
		TreeID:      tree.ID,
		RootNodeID:  node.ID,
		Title:       "Original Title",
		Description: "Original description",
		TopicTags:   []string{},
	})
	require.NoError(t, err)

	newTitle := "Updated Title"
	newDesc := "Updated description"
	updated, err := repo.Update(ctx, created.ID, db.TopicUpdateInput{
		Title:       &newTitle,
		Description: &newDesc,
	})
	require.NoError(t, err)
	require.NotNil(t, updated)
	assert.Equal(t, newTitle, updated.Title)
	assert.Equal(t, newDesc, updated.Description)
}

// ---------------------------------------------------------------------------
// Archive / Restore
// ---------------------------------------------------------------------------

func TestPGTopicRepo_ArchiveAndRestore(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTopicRepo(pool)

	tree, node := createTreeAndNode(t, pool)
	created, err := repo.Create(ctx, db.TopicCreateInput{
		TreeID:     tree.ID,
		RootNodeID: node.ID,
		Title:      "Archive Test",
		TopicTags:  []string{},
	})
	require.NoError(t, err)

	// Archive.
	err = repo.Archive(ctx, created.ID)
	require.NoError(t, err)

	got, err := repo.GetByID(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "archived", got.Status)
	assert.NotNil(t, got.ArchivedAt)

	// Restore.
	err = repo.Restore(ctx, created.ID)
	require.NoError(t, err)

	got, err = repo.GetByID(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "active", got.Status)
}

// ---------------------------------------------------------------------------
// SoftDelete
// ---------------------------------------------------------------------------

func TestPGTopicRepo_SoftDelete(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTopicRepo(pool)

	tree, node := createTreeAndNode(t, pool)
	created, err := repo.Create(ctx, db.TopicCreateInput{
		TreeID:     tree.ID,
		RootNodeID: node.ID,
		Title:      "Soft Delete Test",
		TopicTags:  []string{},
	})
	require.NoError(t, err)

	err = repo.SoftDelete(ctx, created.ID)
	require.NoError(t, err)

	// Should not be findable.
	_, err = repo.GetByID(ctx, created.ID)
	assert.ErrorIs(t, err, db.ErrNotFound)
}

// ---------------------------------------------------------------------------
// HardDelete
// ---------------------------------------------------------------------------

func TestPGTopicRepo_HardDelete(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTopicRepo(pool)

	tree, node := createTreeAndNode(t, pool)
	created, err := repo.Create(ctx, db.TopicCreateInput{
		TreeID:     tree.ID,
		RootNodeID: node.ID,
		Title:      "Hard Delete Test",
		TopicTags:  []string{},
	})
	require.NoError(t, err)

	err = repo.HardDelete(ctx, created.ID)
	require.NoError(t, err)

	_, err = repo.GetByID(ctx, created.ID)
	assert.ErrorIs(t, err, db.ErrNotFound)
}

// ---------------------------------------------------------------------------
// GetParentTopics / GetChildTopics
// ---------------------------------------------------------------------------

func TestPGTopicRepo_ParentChildTopics(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTopicRepo(pool)

	tree, node := createTreeAndNode(t, pool)

	parent, err := repo.Create(ctx, db.TopicCreateInput{
		TreeID:     tree.ID,
		RootNodeID: node.ID,
		Title:      "Parent",
		TopicTags:  []string{},
	})
	require.NoError(t, err)

	tree2, node2 := createTreeAndNode(t, pool)
	_, err = repo.Create(ctx, db.TopicCreateInput{
		TreeID:        tree2.ID,
		RootNodeID:    node2.ID,
		Title:         "Child",
		ParentTopicID: &parent.ID,
		TopicTags:     []string{},
	})
	require.NoError(t, err)

	// Get child topics of parent.
	children, err := repo.GetChildTopics(ctx, parent.ID)
	require.NoError(t, err)
	assert.Len(t, children, 1)

	// Get parent topics of child.
	parents, err := repo.GetParentTopics(ctx, children[0].ID)
	require.NoError(t, err)
	assert.Len(t, parents, 1)
	assert.Equal(t, parent.ID, parents[0].ID)
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

func TestPGTopicRepo_Search(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTopicRepo(pool)

	tree, node := createTreeAndNode(t, pool)

	_, err := repo.Create(ctx, db.TopicCreateInput{
		TreeID:      tree.ID,
		RootNodeID:  node.ID,
		Title:       "Database Schema Design",
		Description: "Designing the PostgreSQL schema for topics",
		TopicTags:   []string{},
	})
	require.NoError(t, err)

	results, total, err := repo.Search(ctx, tree.ID, "schema", 10, 0)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, total, 1)
	assert.GreaterOrEqual(t, len(results), 1)
}

// ---------------------------------------------------------------------------
// RefreshNodeCount
// ---------------------------------------------------------------------------

func TestPGTopicRepo_RefreshNodeCount(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTopicRepo(pool)

	tree, node := createTreeAndNode(t, pool)
	created, err := repo.Create(ctx, db.TopicCreateInput{
		TreeID:     tree.ID,
		RootNodeID: node.ID,
		Title:      "Node Count Test",
		TopicTags:  []string{},
	})
	require.NoError(t, err)

	count, err := repo.RefreshNodeCount(ctx, created.ID)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, count, int32(1))
}

// ---------------------------------------------------------------------------
// GetTopicsForNode
// ---------------------------------------------------------------------------

func TestPGTopicRepo_GetTopicsForNode(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := db.NewPGTopicRepo(pool)

	tree, node := createTreeAndNode(t, pool)
	created, err := repo.Create(ctx, db.TopicCreateInput{
		TreeID:     tree.ID,
		RootNodeID: node.ID,
		Title:      "Node Topic",
		TopicTags:  []string{},
	})
	require.NoError(t, err)

	topics, err := repo.GetTopicsForNode(ctx, node.ID)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(topics), 1)

	// Should include the topic rooted at this node.
	found := false
	for _, t := range topics {
		if t.ID == created.ID {
			found = true
			break
		}
	}
	assert.True(t, found, "expected topic to appear in GetTopicsForNode")
}
