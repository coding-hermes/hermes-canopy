package db_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

func TestCreateSnapshotIdempotentOnDuplicateHash(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()

	treeRepo := db.NewPGTreeRepo(pool)
	snapshotRepo := db.NewSnapshotRepo(pool)
	tree, err := treeRepo.Create(ctx, testTree(uuid.New()))
	require.NoError(t, err)

	first, err := snapshotRepo.CreateSnapshot(ctx, tree.ID)
	require.NoError(t, err)
	require.NotNil(t, first)

	second, err := snapshotRepo.CreateSnapshot(ctx, tree.ID)
	require.NoError(t, err)
	require.NotNil(t, second)
	require.Equal(t, first.ID, second.ID)
	require.Equal(t, first.Hash, second.Hash)
}
