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

// 7: UpdateVersionChain archives the old row and links both directions
// (superseded_by_id on old → new, previous_version_id on new → old).
func TestPGRepoUpdateVersionChain(t *testing.T) {
	repo, _, profileID := newTestRepo(t)
	ctx := context.Background()

	v1 := repoTestPlugin("chain-plugin", "1.0.0")
	v1.AuthorProfileID = profileID
	oldP, err := repo.Register(ctx, v1)
	if err != nil {
		t.Fatalf("Register v1: %v", err)
	}
	// Only one active row per name — archive v1 before inserting v2
	// (mirrors the service Update path).
	if err := repo.Archive(ctx, oldP.ID); err != nil {
		t.Fatalf("Archive v1: %v", err)
	}
	v2 := repoTestPlugin("chain-plugin", "1.1.0")
	v2.AuthorProfileID = profileID
	newP, err := repo.Register(ctx, v2)
	if err != nil {
		t.Fatalf("Register v2: %v", err)
	}

	if err := repo.UpdateVersionChain(ctx, oldP.ID, newP.ID); err != nil {
		t.Fatalf("UpdateVersionChain: %v", err)
	}

	storedOld, err := repo.GetByID(ctx, oldP.ID)
	if err != nil {
		t.Fatalf("GetByID old: %v", err)
	}
	if storedOld.Status != PluginStatusArchived || storedOld.ArchivedAt == nil {
		t.Errorf("old status/archived_at = %s/%v, want archived/set", storedOld.Status, storedOld.ArchivedAt)
	}
	if storedOld.SupersededByID == nil || *storedOld.SupersededByID != newP.ID {
		t.Errorf("old superseded_by_id = %v, want %s", storedOld.SupersededByID, newP.ID)
	}

	storedNew, err := repo.GetByID(ctx, newP.ID)
	if err != nil {
		t.Fatalf("GetByID new: %v", err)
	}
	if storedNew.PreviousVersionID == nil || *storedNew.PreviousVersionID != oldP.ID {
		t.Errorf("new previous_version_id = %v, want %s", storedNew.PreviousVersionID, oldP.ID)
	}

	// Unknown ids → ErrPluginNotFound.
	if err := repo.UpdateVersionChain(ctx, uuid.New(), newP.ID); !errors.Is(err, ErrPluginNotFound) {
		t.Fatalf("UpdateVersionChain unknown old = %v, want ErrPluginNotFound", err)
	}
	// Unknown new id → the FK constraint rejects the dangling
	// superseded_by_id (23503), not a clean not-found.
	if err := repo.UpdateVersionChain(ctx, oldP.ID, uuid.New()); err == nil {
		t.Fatal("UpdateVersionChain unknown new = nil, want error (FK violation)")
	}
}

// 8: ActivateVersion re-activates a historical row (status active,
// archived_at cleared) and records the row it replaced.
func TestPGRepoActivateVersion(t *testing.T) {
	repo, _, profileID := newTestRepo(t)
	ctx := context.Background()

	v1 := repoTestPlugin("activate-plugin", "1.0.0")
	v1.AuthorProfileID = profileID
	oldP, err := repo.Register(ctx, v1)
	if err != nil {
		t.Fatalf("Register v1: %v", err)
	}
	// Only one active version per name — archive v1 before v2 (service
	// Update path mirrors this).
	if err := repo.Archive(ctx, oldP.ID); err != nil {
		t.Fatalf("Archive v1: %v", err)
	}
	v2 := repoTestPlugin("activate-plugin", "1.1.0")
	v2.AuthorProfileID = profileID
	newP, err := repo.Register(ctx, v2)
	if err != nil {
		t.Fatalf("Register v2: %v", err)
	}
	// Simulate the rollback path: the ACTIVE row (v2) is archived and chained
	// to the target (v1) — this clears the partial unique index slot.
	if err := repo.UpdateVersionChain(ctx, newP.ID, oldP.ID); err != nil {
		t.Fatalf("UpdateVersionChain v2→v1: %v", err)
	}
	if err := repo.Archive(ctx, oldP.ID); err != nil {
		t.Fatalf("Archive v1: %v", err)
	}

	// Rollback direction: re-activate v1, record v2 as the row it replaced.
	if err := repo.ActivateVersion(ctx, oldP.ID, newP.ID); err != nil {
		t.Fatalf("ActivateVersion: %v", err)
	}

	storedOld, err := repo.GetByID(ctx, oldP.ID)
	if err != nil {
		t.Fatalf("GetByID old: %v", err)
	}
	if storedOld.Status != PluginStatusActive || storedOld.ArchivedAt != nil {
		t.Errorf("old status/archived_at = %s/%v, want active/nil", storedOld.Status, storedOld.ArchivedAt)
	}
	if storedOld.SupersededByID == nil || *storedOld.SupersededByID != newP.ID {
		t.Errorf("old superseded_by_id = %v, want %s", storedOld.SupersededByID, newP.ID)
	}

	// The re-activated row is now the active version for the name.
	active, err := repo.GetActiveByName(ctx, "activate-plugin")
	if err != nil {
		t.Fatalf("GetActiveByName: %v", err)
	}
	if active.ID != oldP.ID {
		t.Errorf("active = %s, want %s", active.ID, oldP.ID)
	}

	// Unknown id → ErrPluginNotFound.
	if err := repo.ActivateVersion(ctx, uuid.New(), newP.ID); !errors.Is(err, ErrPluginNotFound) {
		t.Fatalf("ActivateVersion unknown = %v, want ErrPluginNotFound", err)
	}
}

// 9: ListVersionsByName returns all rows for a name, newest first.
func TestPGRepoListVersionsByName(t *testing.T) {
	repo, _, profileID := newTestRepo(t)
	ctx := context.Background()

	var last *Plugin
	for _, ver := range []string{"1.0.0", "1.1.0", "1.2.0"} {
		p := repoTestPlugin("history-plugin", ver)
		p.AuthorProfileID = profileID
		if last != nil {
			// One active version per name: archive the previous before the
			// next register (as the service Update path does).
			if err := repo.Archive(ctx, last.ID); err != nil {
				t.Fatalf("Archive %s: %v", last.Version, err)
			}
		}
		created, err := repo.Register(ctx, p)
		if err != nil {
			t.Fatalf("Register %s: %v", ver, err)
		}
		last = created
	}
	// A different name must not leak in.
	other := repoTestPlugin("other-plugin", "1.0.0")
	other.AuthorProfileID = profileID
	if _, err := repo.Register(ctx, other); err != nil {
		t.Fatalf("Register other: %v", err)
	}

	rows, err := repo.ListVersionsByName(ctx, "history-plugin")
	if err != nil {
		t.Fatalf("ListVersionsByName: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("len = %d, want 3", len(rows))
	}
	if rows[0].Version != "1.2.0" || rows[1].Version != "1.1.0" || rows[2].Version != "1.0.0" {
		t.Errorf("order = [%s, %s, %s], want [1.2.0, 1.1.0, 1.0.0]",
			rows[0].Version, rows[1].Version, rows[2].Version)
	}
	for i := 1; i < len(rows); i++ {
		if rows[i-1].CreatedAt.Before(rows[i].CreatedAt) {
			t.Errorf("created_at not descending at %d", i)
		}
	}

	// Unknown name → empty result (nil slice at repo level; the service
	// layer always returns a non-nil slice).
	empty, err := repo.ListVersionsByName(ctx, "nope")
	if err != nil {
		t.Fatalf("ListVersionsByName unknown: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("unknown name len = %d, want 0", len(empty))
	}
}

// 10: GetVersionByName found (any status) + not-found.
func TestPGRepoGetVersionByName(t *testing.T) {
	repo, _, profileID := newTestRepo(t)
	ctx := context.Background()

	p := repoTestPlugin("getver-plugin", "1.0.0")
	p.AuthorProfileID = profileID
	created, err := repo.Register(ctx, p)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, err := repo.GetVersionByName(ctx, "getver-plugin", "1.0.0")
	if err != nil {
		t.Fatalf("GetVersionByName: %v", err)
	}
	if got.ID != created.ID || got.Version != "1.0.0" || got.Status != PluginStatusActive {
		t.Errorf("got = %+v, want active 1.0.0 row %s", got, created.ID)
	}

	// Archived rows are still findable (rollback history).
	if err := repo.Archive(ctx, created.ID); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	archived, err := repo.GetVersionByName(ctx, "getver-plugin", "1.0.0")
	if err != nil {
		t.Fatalf("GetVersionByName archived: %v", err)
	}
	if archived.Status != PluginStatusArchived {
		t.Errorf("status = %s, want archived", archived.Status)
	}

	// Unknown (name, version) → ErrPluginVersionNotFound.
	_, err = repo.GetVersionByName(ctx, "getver-plugin", "9.9.9")
	if !errors.Is(err, ErrPluginVersionNotFound) {
		t.Fatalf("unknown version err = %v, want ErrPluginVersionNotFound", err)
	}
	_, err = repo.GetVersionByName(ctx, "nope", "1.0.0")
	if !errors.Is(err, ErrPluginVersionNotFound) {
		t.Fatalf("unknown name err = %v, want ErrPluginVersionNotFound", err)
	}
}
