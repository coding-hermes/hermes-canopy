package plugin

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

// These are integration tests: they require PostgreSQL and are skipped when
// CANOPY_SKIP_INTEGRATION is set (testutil.SkipIfNoDB pattern).
// Each test gets a fresh, uniquely-named database via NewIntegrationPool,
// which runs all migrations (including 000022-000024) and drops the
// database on cleanup.

func newTestRepo(t *testing.T) (*PGPluginRepo, uuid.UUID, uuid.UUID) {
	t.Helper()
	if testing.Short() {
		t.Skip("short mode: plugin PG integration")
	}
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	repo := NewPGPluginRepo(pool)

	userID, profileID := insertTestProfile(t, pool)
	return repo, userID, profileID
}

// insertTestProfile creates a user + profile row (FK targets for
// plugin_registry.author_profile_id / plugin_instances.profile_id).
func insertTestProfile(t *testing.T, pool *pgxpool.Pool) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	var userID uuid.UUID
	err := pool.QueryRow(ctx, `
        INSERT INTO users (hermes_user_id, display_name)
        VALUES ('plugin-repo-test-' || gen_random_uuid()::text, 'Plugin Repo Test')
        RETURNING id`).Scan(&userID)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	var profileID uuid.UUID
	err = pool.QueryRow(ctx, `
        INSERT INTO profiles (owner_id, name, display_name, profile_type)
        VALUES ($1, 'plugin-repo-profile', 'Plugin Repo Profile', 'human')
        RETURNING id`, userID).Scan(&profileID)
	if err != nil {
		t.Fatalf("insert profile: %v", err)
	}
	return userID, profileID
}

func repoTestPlugin(name, version string) *Plugin {
	return &Plugin{
		Name:           name,
		Slug:           DeriveSlug(name),
		Version:        version,
		Description:    "test plugin",
		Permissions:    []Permission{PermissionDataRead},
		ManifestJSON:   []byte(`{"name":"` + name + `","version":"` + version + `","render_type":"card","entry_point":"main"}`),
		SourceJS:       `function main() { return 1; }`,
		SourceSHA256:   "abc123",
		SourceByteSize: 26,
		Status:         PluginStatusActive,
		IsRootVersion:  true,
	}
}

// 1: Register + GetByID roundtrip; duplicate register returns existing row.
func TestPGRepoRegisterGetByID(t *testing.T) {
	repo, _, profileID := newTestRepo(t)
	ctx := context.Background()
	p := repoTestPlugin("csv-viewer", "1.0.0")
	p.AuthorProfileID = profileID

	created, err := repo.Register(ctx, p)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if created.ID == uuid.Nil {
		t.Fatal("created plugin has nil id")
	}
	if created.Status != PluginStatusActive || !created.IsRootVersion {
		t.Errorf("status/is_root = %s/%v", created.Status, created.IsRootVersion)
	}

	got, err := repo.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Name != "csv-viewer" || got.Version != "1.0.0" || got.SourceSHA256 != "abc123" {
		t.Errorf("roundtrip mismatch: %+v", got)
	}
	if len(got.Permissions) != 1 || got.Permissions[0] != PermissionDataRead {
		t.Errorf("permissions = %v", got.Permissions)
	}

	// Duplicate (name, version) → existing row returned, no error.
	dup, err := repo.Register(ctx, p)
	if err != nil {
		t.Fatalf("duplicate Register: %v", err)
	}
	if dup.ID != created.ID {
		t.Errorf("duplicate id = %s, want %s", dup.ID, created.ID)
	}

	if _, err := repo.GetByID(ctx, uuid.New()); !errors.Is(err, ErrPluginNotFound) {
		t.Fatalf("GetByID unknown = %v, want ErrPluginNotFound", err)
	}
}

// 2: GetActiveByName returns only the active version of a name.
func TestPGRepoGetActiveByName(t *testing.T) {
	repo, _, profileID := newTestRepo(t)
	ctx := context.Background()

	v1 := repoTestPlugin("task-card", "1.0.0")
	v1.AuthorProfileID = profileID
	v1.IsRootVersion = true
	created1, err := repo.Register(ctx, v1)
	if err != nil {
		t.Fatalf("Register v1: %v", err)
	}

	// Archive v1, then register v2 (simulates the service update path).
	if err := repo.Archive(ctx, created1.ID); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	v2 := repoTestPlugin("task-card", "1.1.0")
	v2.AuthorProfileID = profileID
	v2.IsRootVersion = false
	v2.PreviousVersionID = &created1.ID
	created2, err := repo.Register(ctx, v2)
	if err != nil {
		t.Fatalf("Register v2: %v", err)
	}

	active, err := repo.GetActiveByName(ctx, "task-card")
	if err != nil {
		t.Fatalf("GetActiveByName: %v", err)
	}
	if active.ID != created2.ID {
		t.Errorf("active = %s, want %s", active.ID, created2.ID)
	}

	// Archived v1 keeps its history link.
	stored1, err := repo.GetByID(ctx, created1.ID)
	if err != nil {
		t.Fatalf("GetByID v1: %v", err)
	}
	if stored1.Status != PluginStatusArchived || stored1.ArchivedAt == nil {
		t.Errorf("v1 status/archived_at = %s/%v", stored1.Status, stored1.ArchivedAt)
	}

	if _, err := repo.GetActiveByName(ctx, "nope"); !errors.Is(err, ErrPluginNotFound) {
		t.Fatalf("GetActiveByName unknown = %v, want ErrPluginNotFound", err)
	}
}

// 3: List pagination with total count.
func TestPGRepoList(t *testing.T) {
	repo, _, profileID := newTestRepo(t)
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		p := repoTestPlugin("list-plugin-"+string(rune('a'+i-1)), "1.0.0")
		p.AuthorProfileID = profileID
		if _, err := repo.Register(ctx, p); err != nil {
			t.Fatalf("Register %d: %v", i, err)
		}
	}

	plugins, total, err := repo.List(ctx, 2, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(plugins) != 2 {
		t.Errorf("len = %d, want 2", len(plugins))
	}
	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}

	page2, total, err := repo.List(ctx, 2, 2)
	if err != nil {
		t.Fatalf("List page2: %v", err)
	}
	if len(page2) != 1 || total != 3 {
		t.Errorf("page2 len/total = %d/%d, want 1/3", len(page2), total)
	}
}

// 4: Install + duplicate install → ErrAlreadyInstalled; install_count bumps.
func TestPGRepoInstall(t *testing.T) {
	repo, _, profileID := newTestRepo(t)
	ctx := context.Background()
	p := repoTestPlugin("install-me", "1.0.0")
	p.AuthorProfileID = profileID
	created, err := repo.Register(ctx, p)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	treeID := uuid.New()
	inst, err := repo.Install(ctx, &PluginInstance{
		PluginID:           created.ID,
		TreeID:             &treeID,
		ProfileID:          profileID,
		Settings:           []byte("{}"),
		GrantedPermissions: []Permission{PermissionDataRead},
		Status:             InstanceStatusActive,
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if inst.ID == uuid.Nil || inst.Status != InstanceStatusActive {
		t.Fatalf("bad instance: %+v", inst)
	}
	if inst.TreeID == nil || *inst.TreeID != treeID {
		t.Errorf("tree_id = %v, want %s", inst.TreeID, treeID)
	}

	// install_count incremented on the plugin row.
	stored, err := repo.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored.InstallCount != 1 {
		t.Errorf("install_count = %d, want 1", stored.InstallCount)
	}

	// Duplicate install → ErrAlreadyInstalled (partial unique index).
	_, err = repo.Install(ctx, &PluginInstance{
		PluginID:           created.ID,
		TreeID:             &treeID,
		ProfileID:          profileID,
		Settings:           []byte("{}"),
		GrantedPermissions: []Permission{PermissionDataRead},
		Status:             InstanceStatusActive,
	})
	if !errors.Is(err, ErrAlreadyInstalled) {
		t.Fatalf("duplicate Install err = %v, want ErrAlreadyInstalled", err)
	}
}

// 5: ListInstances profile + tree scoping (global vs per-tree).
func TestPGRepoListInstances(t *testing.T) {
	repo, _, profileID := newTestRepo(t)
	ctx := context.Background()

	globalPlugin := repoTestPlugin("global-plugin", "1.0.0")
	globalPlugin.AuthorProfileID = profileID
	gp, err := repo.Register(ctx, globalPlugin)
	if err != nil {
		t.Fatalf("Register global: %v", err)
	}
	treePlugin := repoTestPlugin("tree-plugin", "1.0.0")
	treePlugin.AuthorProfileID = profileID
	tp, err := repo.Register(ctx, treePlugin)
	if err != nil {
		t.Fatalf("Register tree: %v", err)
	}

	// Global instance (tree_id NULL).
	if _, err := repo.Install(ctx, &PluginInstance{
		PluginID: gp.ID, ProfileID: profileID, Settings: []byte("{}"),
		GrantedPermissions: []Permission{PermissionDataRead}, Status: InstanceStatusActive,
	}); err != nil {
		t.Fatalf("Install global: %v", err)
	}
	// Per-tree instance.
	treeID := uuid.New()
	if _, err := repo.Install(ctx, &PluginInstance{
		PluginID: tp.ID, TreeID: &treeID, ProfileID: profileID, Settings: []byte("{}"),
		GrantedPermissions: []Permission{PermissionDataRead}, Status: InstanceStatusActive,
	}); err != nil {
		t.Fatalf("Install tree: %v", err)
	}

	// Unfiltered: both instances.
	all, err := repo.ListInstances(ctx, profileID, nil)
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("unfiltered len = %d, want 2", len(all))
	}

	// Tree-scoped: only the per-tree instance.
	scoped, err := repo.ListInstances(ctx, profileID, &treeID)
	if err != nil {
		t.Fatalf("ListInstances scoped: %v", err)
	}
	if len(scoped) != 1 || scoped[0].PluginID != tp.ID {
		t.Errorf("scoped = %+v, want only tree-plugin", scoped)
	}

	// Different profile: nothing.
	otherProfile := uuid.New()
	none, err := repo.ListInstances(ctx, otherProfile, nil)
	if err != nil {
		t.Fatalf("ListInstances other profile: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("other-profile len = %d, want 0", len(none))
	}
}

// 6: UpdateInstanceStatus transitions + IncrementInvokeCount.
func TestPGRepoInstanceStatusAndInvokeCount(t *testing.T) {
	repo, _, profileID := newTestRepo(t)
	ctx := context.Background()
	p := repoTestPlugin("lifecycle", "1.0.0")
	p.AuthorProfileID = profileID
	created, err := repo.Register(ctx, p)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	inst, err := repo.Install(ctx, &PluginInstance{
		PluginID: created.ID, ProfileID: profileID, Settings: []byte("{}"),
		GrantedPermissions: []Permission{PermissionDataRead}, Status: InstanceStatusActive,
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	if err := repo.UpdateInstanceStatus(ctx, inst.ID, InstanceStatusPaused); err != nil {
		t.Fatalf("UpdateInstanceStatus paused: %v", err)
	}
	paused, err := repo.GetInstance(ctx, inst.ID)
	if err != nil {
		t.Fatalf("GetInstance: %v", err)
	}
	if paused.Status != InstanceStatusPaused {
		t.Errorf("status = %s, want paused", paused.Status)
	}

	if err := repo.IncrementInvokeCount(ctx, inst.ID); err != nil {
		t.Fatalf("IncrementInvokeCount: %v", err)
	}
	invoked, err := repo.GetInstance(ctx, inst.ID)
	if err != nil {
		t.Fatalf("GetInstance: %v", err)
	}
	if invoked.InvokeCount != 1 {
		t.Errorf("invoke_count = %d, want 1", invoked.InvokeCount)
	}

	// Unknown ids → typed not-found errors.
	if err := repo.UpdateInstanceStatus(ctx, uuid.New(), InstanceStatusActive); !errors.Is(err, ErrInstanceNotFound) {
		t.Fatalf("UpdateInstanceStatus unknown = %v, want ErrInstanceNotFound", err)
	}
	if err := repo.IncrementInvokeCount(ctx, uuid.New()); !errors.Is(err, ErrInstanceNotFound) {
		t.Fatalf("IncrementInvokeCount unknown = %v, want ErrInstanceNotFound", err)
	}
}
