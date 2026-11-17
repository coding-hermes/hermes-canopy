// Tree member repository integration tests — BUG-043 coverage for
// IsTreeDeleted (the deleted-tree gate backing TreeMembershipMiddleware) and
// CAN-MEM-02 coverage for UpdateMember (the SPEC-API-06 §10 partial write).
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

// TestPGTreeMemberRepo_UpdateMember covers the CAN-MEM-02 partial update
// against real PostgreSQL: COALESCE semantics per column, both fields in one
// statement, ErrNotFound for an unknown id, and the enum cast rejecting an
// out-of-vocabulary role without leaving a partial write behind.
func TestPGTreeMemberRepo_UpdateMember(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	members := db.NewPGTreeMemberRepo(pool)
	trees := db.NewPGTreeRepo(pool)
	users := db.NewPGUserRepo(pool)

	tree, err := trees.Create(ctx, testTree(uuid.New()))
	require.NoError(t, err)
	require.NotNil(t, tree)

	email := "can-mem-02-" + uuid.NewString() + "@canopy.dev"
	user, err := users.Create(ctx, &db.User{
		HermesUserID: "can-mem-02-" + uuid.NewString(),
		Email:        &email,
		DisplayName:  "Partial Member",
	})
	require.NoError(t, err)
	require.NotNil(t, user)

	member, err := members.Add(ctx, &db.TreeMember{
		TreeID: tree.ID, UserID: &user.ID, Role: db.TreeRoleMember, IsVisible: true,
	})
	require.NoError(t, err)
	require.False(t, member.AutoApproved)

	// role only — auto_approved (false) must survive untouched.
	admin := db.TreeRoleAdmin
	updated, err := members.UpdateMember(ctx, member.ID, &admin, nil)
	require.NoError(t, err)
	require.Equal(t, db.TreeRoleAdmin, updated.Role)
	require.False(t, updated.AutoApproved, "a nil auto_approved must not write the column")
	require.NotNil(t, updated.UserID)
	require.Equal(t, user.ID, *updated.UserID)
	require.True(t, updated.JoinedAt.Equal(member.JoinedAt), "joined_at must be untouched")

	// auto_approved only — the role must survive untouched.
	approve := true
	updated, err = members.UpdateMember(ctx, member.ID, nil, &approve)
	require.NoError(t, err)
	require.True(t, updated.AutoApproved)
	require.Equal(t, db.TreeRoleAdmin, updated.Role, "a nil role must not write the column")

	// both fields, one statement.
	viewer := db.TreeRoleViewer
	off := false
	updated, err = members.UpdateMember(ctx, member.ID, &viewer, &off)
	require.NoError(t, err)
	require.Equal(t, db.TreeRoleViewer, updated.Role)
	require.False(t, updated.AutoApproved)

	// An unknown id is ErrNotFound.
	_, err = members.UpdateMember(ctx, uuid.New(), &admin, nil)
	require.ErrorIs(t, err, db.ErrNotFound)

	// An out-of-enum role is rejected by the tree_role cast and leaves the
	// row exactly as the last successful update left it.
	bad := "superuser"
	_, err = members.UpdateMember(ctx, member.ID, &bad, &approve)
	require.Error(t, err, "tree_role cast must reject an out-of-enum role")

	stored, err := members.GetByTree(ctx, tree.ID)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	require.Equal(t, db.TreeRoleViewer, stored[0].Role)
	require.False(t, stored[0].AutoApproved, "the rejected update must not leave a partial write")

	// UpdateRole and Remove keep working alongside UpdateMember.
	role, err := members.UpdateRole(ctx, member.ID, db.TreeRoleMember)
	require.NoError(t, err)
	require.Equal(t, db.TreeRoleMember, role.Role)
	require.NoError(t, members.Remove(ctx, member.ID))
	require.ErrorIs(t, members.Remove(ctx, member.ID), db.ErrNotFound)
}
