// Tree member repository integration tests — BUG-043 coverage for
// IsTreeDeleted, the deleted-tree gate backing TreeMembershipMiddleware.
// Uses a real PostgreSQL test database via testutil.NewSharedIntegrationPool.
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

func TestPGTreeMemberRepo_IsTreeDeleted(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	members := db.NewPGTreeMemberRepo(pool)
	trees := db.NewPGTreeRepo(pool)

	// Live tree: not deleted.
	tree, err := trees.Create(ctx, testTree(uuid.New()))
	require.NoError(t, err)
	require.NotNil(t, tree)

	deleted, err := members.IsTreeDeleted(ctx, tree.ID)
	require.NoError(t, err)
	assert.False(t, deleted, "live tree must not be reported as deleted")

	// Soft-delete it, then the flag flips.
	require.NoError(t, trees.SoftDelete(ctx, tree.ID))

	deleted, err = members.IsTreeDeleted(ctx, tree.ID)
	require.NoError(t, err)
	assert.True(t, deleted, "soft-deleted tree must be reported as deleted")

	// A tree that never existed is NOT "deleted" — callers needing the
	// not-found distinction use the tree repo directly (documented contract).
	deleted, err = members.IsTreeDeleted(ctx, uuid.New())
	require.NoError(t, err)
	assert.False(t, deleted, "nonexistent tree must not be reported as deleted")
}
