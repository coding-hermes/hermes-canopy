// member_count truthfulness tests (DF-HERMES-CANOPY-88). The tree API
// responses used to hardcode MemberCount=1 ("owner only") even after
// POST /trees/{id}/share inserted real tree_members rows — the TreesPage
// "N members" badge therefore never moved. These tests pin the real
// count on the list, detail, and create paths.
//
// NewSharedIntegrationPool provisions a UNIQUE database per call, so
// every test here calls it exactly once and threads that one pool
// through the service, the member repo, and the user repo — rows must
// share one database for the FKs to hold.
package service

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

// mustDevOwnerID parses the dev JWT subject used as tree owner.
func mustDevOwnerID(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(db.DevJWTUserID)
	if err != nil {
		t.Fatalf("parse dev user id: %v", err)
	}
	return id
}

// shareTargets provisions n throwaway users and returns their ids —
// tree_members.user_id has an FK to users(id), so shares need real rows.
func shareTargets(t *testing.T, ctx context.Context, pool *pgxpool.Pool, n int) []uuid.UUID {
	t.Helper()
	users := db.NewPGUserRepo(pool)
	ids := make([]uuid.UUID, 0, n)
	for i := 0; i < n; i++ {
		u, err := users.Create(ctx, &db.User{
			HermesUserID: "df88-" + uuid.NewString(),
			DisplayName:  "DF-88 share target",
		})
		require.NoError(t, err)
		ids = append(ids, u.ID)
	}
	return ids
}

func TestListTrees_MemberCountReflectsTreeMembers(t *testing.T) {
	testutil.SkipIfNoDB(t)
	ctx := context.Background()
	pool := testutil.NewSharedIntegrationPool(t)
	if _, err := db.EnsureDevJWTUser(ctx, pool); err != nil {
		t.Fatalf("EnsureDevJWTUser: %v", err)
	}

	svc := NewTreeService(
		db.NewPGTreeRepo(pool),
		db.NewPGNodeRepo(pool),
		db.NewPGEdgeRepo(pool),
		pool,
	)

	tree, err := svc.CreateTree(ctx, CreateTreeParams{
		OwnerID:       mustDevOwnerID(t),
		Title:         "member count list",
		RootContent:   "root",
		ContentFormat: FormatMarkdown,
		NodeType:      NodeTypeMessage,
	})
	require.NoError(t, err)

	// Fresh tree: owner only.
	page, err := svc.ListTrees(ctx, ListTreesParams{})
	require.NoError(t, err)
	found := false
	for _, s := range page.Trees {
		if s.ID == tree.ID {
			found = true
			assert.Equal(t, 1, s.MemberCount, "fresh tree must report the owner-only count")
		}
	}
	require.True(t, found, "created tree missing from list")

	// Share with two more users → 3 members.
	members := db.NewPGTreeMemberRepo(pool)
	for _, uid := range shareTargets(t, ctx, pool, 2) {
		target := uid
		_, err := members.Add(ctx, &db.TreeMember{
			TreeID:    tree.ID,
			UserID:    &target,
			Role:      db.TreeRoleMember,
			IsVisible: true,
		})
		require.NoError(t, err)
	}

	page, err = svc.ListTrees(ctx, ListTreesParams{})
	require.NoError(t, err)
	found = false
	for _, s := range page.Trees {
		if s.ID == tree.ID {
			found = true
			assert.Equal(t, 3, s.MemberCount, "member_count must reflect tree_members after shares")
		}
	}
	require.True(t, found, "shared tree missing from refreshed list")
}

func TestGetTree_MemberCountReflectsTreeMembers(t *testing.T) {
	testutil.SkipIfNoDB(t)
	ctx := context.Background()
	pool := testutil.NewSharedIntegrationPool(t)
	if _, err := db.EnsureDevJWTUser(ctx, pool); err != nil {
		t.Fatalf("EnsureDevJWTUser: %v", err)
	}

	svc := NewTreeService(
		db.NewPGTreeRepo(pool),
		db.NewPGNodeRepo(pool),
		db.NewPGEdgeRepo(pool),
		pool,
	)

	tree, err := svc.CreateTree(ctx, CreateTreeParams{
		OwnerID:       mustDevOwnerID(t),
		Title:         "member count detail",
		RootContent:   "root",
		ContentFormat: FormatMarkdown,
		NodeType:      NodeTypeMessage,
	})
	require.NoError(t, err)

	withoutStats, err := svc.GetTree(ctx, tree.ID, GetTreeOptions{IncludeStats: false})
	require.NoError(t, err)
	assert.Equal(t, 1, withoutStats.MemberCount, "fresh tree detail must report the owner-only count")

	withStats, err := svc.GetTree(ctx, tree.ID, GetTreeOptions{IncludeStats: true})
	require.NoError(t, err)
	require.NotNil(t, withStats.Stats)
	assert.Equal(t, 1, withStats.MemberCount, "summary member_count with owner only")
	assert.Equal(t, 1, withStats.Stats.MemberCount, "stats member_count with owner only")

	members := db.NewPGTreeMemberRepo(pool)
	for _, uid := range shareTargets(t, ctx, pool, 1) {
		target := uid
		_, err := members.Add(ctx, &db.TreeMember{
			TreeID:    tree.ID,
			UserID:    &target,
			Role:      db.TreeRoleViewer,
			IsVisible: true,
		})
		require.NoError(t, err)
	}

	withStats, err = svc.GetTree(ctx, tree.ID, GetTreeOptions{IncludeStats: true})
	require.NoError(t, err)
	assert.Equal(t, 2, withStats.MemberCount, "summary member_count after share")
	assert.Equal(t, 2, withStats.Stats.MemberCount, "stats member_count after share")

	withoutStats, err = svc.GetTree(ctx, tree.ID, GetTreeOptions{IncludeStats: false})
	require.NoError(t, err)
	assert.Equal(t, 2, withoutStats.MemberCount, "detail member_count without stats after share")
}

func TestCreateTree_MemberCountIsOwnerOnly(t *testing.T) {
	testutil.SkipIfNoDB(t)
	ctx := context.Background()
	pool := testutil.NewSharedIntegrationPool(t)
	if _, err := db.EnsureDevJWTUser(ctx, pool); err != nil {
		t.Fatalf("EnsureDevJWTUser: %v", err)
	}

	svc := NewTreeService(
		db.NewPGTreeRepo(pool),
		db.NewPGNodeRepo(pool),
		db.NewPGEdgeRepo(pool),
		pool,
	)

	tree, err := svc.CreateTree(ctx, CreateTreeParams{
		OwnerID:       mustDevOwnerID(t),
		Title:         "member count create",
		RootContent:   "root",
		ContentFormat: FormatMarkdown,
		NodeType:      NodeTypeMessage,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, tree.MemberCount, "CreateTree response must report the owner-only count")
}

func TestCountMembersByTreeIDs_BatchesAndOmitsEmpty(t *testing.T) {
	testutil.SkipIfNoDB(t)
	ctx := context.Background()
	pool := testutil.NewSharedIntegrationPool(t)
	repo := db.NewPGTreeRepo(pool)

	a, err := repo.Create(ctx, &db.Tree{OwnerID: uuid.New(), Title: "members A"})
	require.NoError(t, err)
	b, err := repo.Create(ctx, &db.Tree{OwnerID: uuid.New(), Title: "members B"})
	require.NoError(t, err)

	// Two memberships on A (one extra member, one viewer), none on B.
	targets := shareTargets(t, ctx, pool, 2)
	members := db.NewPGTreeMemberRepo(pool)
	_, err = members.Add(ctx, &db.TreeMember{TreeID: a.ID, UserID: &targets[0], Role: db.TreeRoleMember, IsVisible: true})
	require.NoError(t, err)
	_, err = members.Add(ctx, &db.TreeMember{TreeID: a.ID, UserID: &targets[1], Role: db.TreeRoleViewer, IsVisible: true})
	require.NoError(t, err)

	counts, err := repo.CountMembersByTreeIDs(ctx, []uuid.UUID{a.ID, b.ID, uuid.New()})
	require.NoError(t, err)
	assert.Equal(t, 2, counts[a.ID], "tree A has two memberships")
	_, ok := counts[b.ID]
	assert.False(t, ok, "tree B has no memberships and must be absent from the map")

	empty, err := repo.CountMembersByTreeIDs(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, empty, "empty input returns an empty map without querying")
}
