package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

// ── shared helpers for the identity repository tests (users, profiles, tree_members) ──
//
// Every test opens its own store under t.TempDir() (newTestStoreWithSchema) and reaches the
// repositories only through their public constructors, so the tests exercise real DDL, real
// SQL, real triggers and real foreign-key enforcement. No mocks, no skips.
//
// Where a test needs a row in a state the repos cannot produce — a soft-deleted user, a
// backdated created_at, a profile membership whose joined_at predates another — it writes
// that state with plain SQL (execSQL). That is deliberate: the PG interfaces declare no
// delete and no timestamp setter, so an externally written row is also the only way to prove
// what the read paths filter on.

// newIdentityRepos opens a schema-applied store plus the identity repositories (and the tree
// repo, which the membership tests need for a real trees row).
func newIdentityRepos(t *testing.T) (*Store, *TreeRepo, *UserRepo, *ProfileRepo, *TreeMemberRepo) {
	t.Helper()
	s := newTestStoreWithSchema(t)
	return s, NewTreeRepo(s), NewUserRepo(s), NewProfileRepo(s), NewTreeMemberRepo(s)
}

// mustUser creates an active user with a unique Hermes subject.
func mustUser(t *testing.T, ctx context.Context, r *UserRepo, hermesUserID, displayName string) *db.User {
	t.Helper()
	u, err := r.Create(ctx, &db.User{
		HermesUserID: hermesUserID,
		DisplayName:  displayName,
		IsActive:     true,
	})
	if err != nil {
		t.Fatalf("UserRepo.Create(%q): %v", hermesUserID, err)
	}
	return u
}

// mustProfile creates a profile owned by ownerID, with the repo defaults in play.
func mustProfile(t *testing.T, ctx context.Context, r *ProfileRepo, ownerID uuid.UUID, name string) *db.Profile {
	t.Helper()
	p, err := r.Create(ctx, &db.Profile{OwnerID: ownerID, Name: name, DisplayName: name})
	if err != nil {
		t.Fatalf("ProfileRepo.Create(%q): %v", name, err)
	}
	return p
}

// execSQL runs one raw statement. t.Fatalf keeps a seeding failure from being mistaken for a
// repository failure later in the test.
func execSQL(t *testing.T, ctx context.Context, s *Store, query string, args ...any) {
	t.Helper()
	if _, err := s.DB().ExecContext(ctx, query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// ── interface satisfaction ───────────────────────────────────────────────────────────

// TestSQLiteIdentityReposSatisfyTheirInterfaces is the reflection-side companion to the
// compile-time assertions in user_repo.go: all three identity interfaces are satisfiable and
// satisfied in full (unlike db.EdgeRepo, whose reference model is pgx-scoped). It fails
// loudly if an interface gains a method the implementations do not carry, which is the drift
// the assertions would turn into a build failure.
func TestSQLiteIdentityReposSatisfyTheirInterfaces(t *testing.T) {
	cases := []struct {
		name  string
		iface reflect.Type
		impl  reflect.Type
	}{
		{"UserRepo", reflect.TypeOf((*db.UserRepo)(nil)).Elem(), reflect.TypeOf(&UserRepo{})},
		{"ProfileRepo", reflect.TypeOf((*db.ProfileRepo)(nil)).Elem(), reflect.TypeOf(&ProfileRepo{})},
		{"TreeMemberRepo", reflect.TypeOf((*db.TreeMemberRepo)(nil)).Elem(), reflect.TypeOf(&TreeMemberRepo{})},
	}
	for _, tc := range cases {
		if tc.iface.NumMethod() == 0 {
			t.Errorf("db.%s has no methods; the derivation is broken", tc.name)
			continue
		}
		if !tc.impl.Implements(tc.iface) {
			t.Errorf("*%s does not satisfy db.%s", tc.name, tc.name)
			continue
		}
		for i := 0; i < tc.iface.NumMethod(); i++ {
			if _, ok := tc.impl.MethodByName(tc.iface.Method(i).Name); !ok {
				t.Errorf("*%s is missing db.%s.%s", tc.name, tc.name, tc.iface.Method(i).Name)
			}
		}
		t.Logf("*%s implements all %d db.%s methods", tc.name, tc.iface.NumMethod(), tc.name)
	}
}

// TestSQLiteIdentityReposWithoutStoreReportErrNoStore pins the nil-store contract on the new
// repositories: a wiring mistake must fail at the call site, not panic.
func TestSQLiteIdentityReposWithoutStoreReportErrNoStore(t *testing.T) {
	ctx := context.Background()

	if _, err := NewUserRepo(nil).GetByID(ctx, uuid.New()); !errors.Is(err, ErrNoStore) {
		t.Errorf("UserRepo(nil).GetByID error = %v, want ErrNoStore", err)
	}
	if _, err := NewUserRepo(nil).Create(ctx, &db.User{}); !errors.Is(err, ErrNoStore) {
		t.Errorf("UserRepo(nil).Create error = %v, want ErrNoStore", err)
	}
	if _, err := NewProfileRepo(nil).GetByOwner(ctx, uuid.New()); !errors.Is(err, ErrNoStore) {
		t.Errorf("ProfileRepo(nil).GetByOwner error = %v, want ErrNoStore", err)
	}
	if _, err := NewProfileRepo(nil).Create(ctx, &db.Profile{}); !errors.Is(err, ErrNoStore) {
		t.Errorf("ProfileRepo(nil).Create error = %v, want ErrNoStore", err)
	}
	if _, err := NewTreeMemberRepo(nil).GetByTree(ctx, uuid.New()); !errors.Is(err, ErrNoStore) {
		t.Errorf("TreeMemberRepo(nil).GetByTree error = %v, want ErrNoStore", err)
	}
	if err := NewTreeMemberRepo(nil).Remove(ctx, uuid.New()); !errors.Is(err, ErrNoStore) {
		t.Errorf("TreeMemberRepo(nil).Remove error = %v, want ErrNoStore", err)
	}
	// A zero-value Store is the same condition: no pool.
	if _, err := NewUserRepo(&Store{}).GetByEmail(ctx, "a@b.com"); !errors.Is(err, ErrNoStore) {
		t.Errorf("UserRepo(empty Store).GetByEmail error = %v, want ErrNoStore", err)
	}
	if _, err := NewTreeMemberRepo(&Store{}).IsMember(ctx, uuid.New(), uuid.New()); !errors.Is(err, ErrNoStore) {
		t.Errorf("TreeMemberRepo(empty Store).IsMember error = %v, want ErrNoStore", err)
	}
}

// ── UserRepo ─────────────────────────────────────────────────────────────────────────

func TestSQLiteUserRepoCRUD(t *testing.T) {
	s, _, users, _, _ := newIdentityRepos(t)
	ctx := context.Background()

	email := "alice@example.com"
	avatar := "https://example.com/alice.png"
	seen := time.Date(2026, 3, 1, 9, 30, 0, 0, time.UTC)
	callerSupplied := uuid.New()

	created, err := users.Create(ctx, &db.User{
		ID:           callerSupplied,
		HermesUserID: "hermes-alice",
		Email:        &email,
		DisplayName:  "Alice",
		AvatarURL:    &avatar,
		LastSeenAt:   &seen,
		IsActive:     true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == uuid.Nil {
		t.Fatal("Create returned a nil id")
	}
	// The DDL carries no DEFAULT uuidv7() for users.id, so the store generates the id —
	// the same behaviour as PGUserRepo, where the DDL default wins over the caller's value.
	if created.ID == callerSupplied {
		t.Errorf("Create honored the caller-supplied id %s; the store generates ids (PG parity)", callerSupplied)
	}
	if created.HermesUserID != "hermes-alice" || created.DisplayName != "Alice" {
		t.Errorf("hermes_user_id/display_name = %q/%q", created.HermesUserID, created.DisplayName)
	}
	if created.Email == nil || *created.Email != email {
		t.Errorf("email = %v, want %q", created.Email, email)
	}
	if created.AvatarURL == nil || *created.AvatarURL != avatar {
		t.Errorf("avatar_url = %v, want %q", created.AvatarURL, avatar)
	}
	if created.LastSeenAt == nil || !created.LastSeenAt.Equal(seen) {
		t.Errorf("last_seen_at = %v, want %v", created.LastSeenAt, seen)
	}
	if !created.IsActive {
		t.Error("is_active = false, want the explicit true that was passed")
	}
	if created.DeletedAt != nil {
		t.Errorf("deleted_at = %v, want nil", created.DeletedAt)
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() ||
		created.CreatedAt.Location() != time.UTC || created.UpdatedAt.Location() != time.UTC {
		t.Errorf("created_at/updated_at = %v/%v, want non-zero UTC instants", created.CreatedAt, created.UpdatedAt)
	}

	// Every stored timestamp is in the canonical D1 shape, including the one Go wrote.
	for _, col := range []string{"created_at", "updated_at", "last_seen_at"} {
		stored := storedText(t, ctx, s, `SELECT `+col+` FROM users WHERE id = ?`, created.ID.String())
		if _, err := DecodeTime(stored); err != nil {
			t.Errorf("stored %s %q does not decode as the canonical shape: %v", col, stored, err)
		}
	}
	// Boolean columns store the INTEGER 0/1 domain the DDL's CHECK declares.
	if got := storedText(t, ctx, s, `SELECT is_active FROM users WHERE id = ?`, created.ID.String()); got != "1" {
		t.Errorf("stored is_active = %q, want 1", got)
	}

	// The three single-row readers, including the case-insensitive email lookup.
	got, err := users.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ID != created.ID || got.HermesUserID != created.HermesUserID {
		t.Errorf("GetByID = %+v, want the created row", got)
	}
	if got, err = users.GetByHermesUserID(ctx, "hermes-alice"); err != nil {
		t.Fatalf("GetByHermesUserID: %v", err)
	} else if got.ID != created.ID {
		t.Errorf("GetByHermesUserID returned %s, want %s", got.ID, created.ID)
	}
	// PG's ILIKE is translated to LIKE (ASCII case-insensitive), so either case matches.
	if got, err = users.GetByEmail(ctx, "ALICE@EXAMPLE.COM"); err != nil {
		t.Fatalf("GetByEmail (upper case): %v", err)
	} else if got.ID != created.ID {
		t.Errorf("GetByEmail (upper case) returned %s, want %s", got.ID, created.ID)
	}

	// Update changes the two mutable fields and leaves everything else alone.
	updated, err := users.Update(ctx, created.ID, "Alice Renamed", nil)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.DisplayName != "Alice Renamed" {
		t.Errorf("display_name = %q, want the updated value", updated.DisplayName)
	}
	if updated.AvatarURL != nil {
		t.Errorf("avatar_url = %v, want nil after passing nil", updated.AvatarURL)
	}
	if n := countRows(t, ctx, s, `SELECT COUNT(*) FROM users WHERE id = ? AND avatar_url IS NULL`, created.ID.String()); n != 1 {
		t.Error("avatar_url is not NULL in the row after Update(nil)")
	}
	if !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Errorf("created_at changed on update: %v -> %v", created.CreatedAt, updated.CreatedAt)
	}
	if updated.UpdatedAt.Before(created.UpdatedAt) {
		t.Errorf("updated_at moved backwards: %v -> %v", created.UpdatedAt, updated.UpdatedAt)
	}
	if updated.LastSeenAt == nil || !updated.LastSeenAt.Equal(seen) {
		t.Errorf("last_seen_at = %v, want it untouched by Update", updated.LastSeenAt)
	}
	// The read-back value is the one stored, so the trigger's write is what the caller sees.
	if stored := storedText(t, ctx, s, `SELECT updated_at FROM users WHERE id = ?`, created.ID.String()); stored != EncodeTime(updated.UpdatedAt) {
		t.Errorf("stored updated_at = %q, returned %q", stored, EncodeTime(updated.UpdatedAt))
	}
}

func TestSQLiteUserRepoDefaultsAndSoftDeleteFiltering(t *testing.T) {
	s, _, users, _, _ := newIdentityRepos(t)
	ctx := context.Background()

	// Only the two NOT NULL columns are supplied: is_active and the nullable columns must
	// come from the DDL, and the stored booleans must be the 0/1 domain.
	created, err := users.Create(ctx, &db.User{HermesUserID: "hermes-dave", DisplayName: "Dave"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.IsActive {
		t.Error("is_active = true, want the zero value passed through (PG's COALESCE($6, true) is inert for a non-nullable Go bool)")
	}
	if created.Email != nil || created.AvatarURL != nil || created.LastSeenAt != nil {
		t.Errorf("email/avatar_url/last_seen_at = %v/%v/%v, want nil", created.Email, created.AvatarURL, created.LastSeenAt)
	}
	if got := storedText(t, ctx, s, `SELECT is_active FROM users WHERE id = ?`, created.ID.String()); got != "0" {
		t.Errorf("stored is_active = %q, want 0", got)
	}
	if n := countRows(t, ctx, s, `SELECT COUNT(*) FROM users WHERE id = ? AND email IS NULL AND avatar_url IS NULL AND last_seen_at IS NULL`, created.ID.String()); n != 1 {
		t.Error("the nullable columns are not stored as NULL")
	}

	// Both literal addresses the DDL accepts (chk_users_email) round-trip; a bare local part
	// is rejected, which is the translated CHECK doing the job of PG's POSIX regex.
	bad := "not-an-email"
	if _, err := users.Create(ctx, &db.User{HermesUserID: "hermes-bad", DisplayName: "Bad", Email: &bad}); err == nil {
		t.Error("Create with a malformed email succeeded, want the chk_users_email CHECK to reject it")
	}

	// A soft-deleted user is invisible to every reader and to Update. The row itself
	// survives: neither db.UserRepo nor this port exposes a delete.
	execSQL(t, ctx, s, `UPDATE users SET deleted_at = ? WHERE id = ?`, EncodeTime(time.Now()), created.ID.String())
	if _, err := users.GetByID(ctx, created.ID); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("GetByID after soft delete error = %v, want db.ErrNotFound", err)
	}
	if _, err := users.GetByHermesUserID(ctx, "hermes-dave"); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("GetByHermesUserID after soft delete error = %v, want db.ErrNotFound", err)
	}
	if _, err := users.Update(ctx, created.ID, "Dave Renamed", nil); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("Update after soft delete error = %v, want db.ErrNotFound", err)
	}
	if n := countRows(t, ctx, s, `SELECT COUNT(*) FROM users WHERE id = ?`, created.ID.String()); n != 1 {
		t.Errorf("soft-deleted user row count = %d, want the row to survive with deleted_at set", n)
	}
	if got := storedText(t, ctx, s, `SELECT display_name FROM users WHERE id = ?`, created.ID.String()); got != "Dave" {
		t.Errorf("display_name = %q after a rejected Update, want the original value", got)
	}
}

// TestSQLiteUserRepoUpdateBumpsUpdatedAtViaTrigger proves the one mechanism this table
// cannot express with RETURNING: users.updated_at is written by the translated
// set_users_updated_at trigger, which fires AFTER the statement, so RETURNING would report
// the pre-update value. Update therefore reads the row back, and the observable proof is that
// a row backdated to 2020 comes back with a current updated_at while created_at is untouched.
func TestSQLiteUserRepoUpdateBumpsUpdatedAtViaTrigger(t *testing.T) {
	s, _, users, _, _ := newIdentityRepos(t)
	ctx := context.Background()

	// Raw INSERT: users has no AFTER INSERT trigger, so an explicit backdated pair survives.
	vintage := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	id := uuid.New()
	execSQL(t, ctx, s, `
        INSERT INTO users (id, hermes_user_id, display_name, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?)`,
		id.String(), "hermes-vintage", "Vintage", EncodeTime(vintage), EncodeTime(vintage))

	// The premise the read-back exists for: RETURNING runs before the AFTER trigger, so it
	// hands back the value the statement's own UPDATE saw — still the 2020 instant.
	var returnedByReturning string
	if err := s.DB().QueryRowContext(ctx,
		`UPDATE users SET display_name = ? WHERE id = ? RETURNING updated_at`,
		"Returning Probe", id.String()).Scan(&returnedByReturning); err != nil {
		t.Fatalf("UPDATE ... RETURNING probe: %v", err)
	}
	if returnedByReturning != EncodeTime(vintage) {
		t.Errorf("UPDATE ... RETURNING updated_at = %q, want the pre-trigger %q "+
			"(if this changed, SQLite now evaluates RETURNING after AFTER triggers and the read-back in Update can be revisited)",
			returnedByReturning, EncodeTime(vintage))
	}
	if stored := storedText(t, ctx, s, `SELECT updated_at FROM users WHERE id = ?`, id.String()); stored == EncodeTime(vintage) {
		t.Error("updated_at is still the 2020 instant after the probe UPDATE, want the trigger to have bumped it")
	}

	updated, err := users.Update(ctx, id, "Vintage Renamed", nil)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.DisplayName != "Vintage Renamed" {
		t.Errorf("display_name = %q", updated.DisplayName)
	}
	if !updated.CreatedAt.Equal(vintage) {
		t.Errorf("created_at = %v, want the untouched %v", updated.CreatedAt, vintage)
	}
	if !updated.UpdatedAt.After(vintage) {
		t.Errorf("updated_at = %v, want a value after the backdated %v (the trigger must bump it)", updated.UpdatedAt, vintage)
	}
	if stored := storedText(t, ctx, s, `SELECT updated_at FROM users WHERE id = ?`, id.String()); stored != EncodeTime(updated.UpdatedAt) {
		t.Errorf("stored updated_at = %q, returned %q", stored, EncodeTime(updated.UpdatedAt))
	}
}

func TestSQLiteUserRepoErrorSemantics(t *testing.T) {
	s, _, users, _, _ := newIdentityRepos(t)
	ctx := context.Background()

	// Nil input keeps PGUserRepo's message verbatim.
	if _, err := users.Create(ctx, nil); err == nil || err.Error() != "db: user is nil" {
		t.Errorf("Create(nil) error = %v, want %q", err, "db: user is nil")
	}
	// Unknown ids are ErrNotFound on every reader and on Update.
	if _, err := users.GetByID(ctx, uuid.New()); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("GetByID(unknown) error = %v, want db.ErrNotFound", err)
	}
	if _, err := users.GetByHermesUserID(ctx, "hermes-nobody"); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("GetByHermesUserID(unknown) error = %v, want db.ErrNotFound", err)
	}
	if _, err := users.GetByEmail(ctx, "nobody@example.com"); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("GetByEmail(unknown) error = %v, want db.ErrNotFound", err)
	}
	if _, err := users.Update(ctx, uuid.New(), "Ghost", nil); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("Update(unknown) error = %v, want db.ErrNotFound", err)
	}

	// hermes_user_id is UNIQUE. The violation is wrapped in the same shape PGUserRepo
	// produces (neither implementation maps it onto a sentinel), and it leaves no row behind.
	mustUser(t, ctx, users, "hermes-dup", "First")
	_, err := users.Create(ctx, &db.User{HermesUserID: "hermes-dup", DisplayName: "Second"})
	if err == nil {
		t.Fatal("Create with a duplicate hermes_user_id succeeded, want the UNIQUE constraint to reject it")
	}
	if !strings.Contains(err.Error(), "db: insert user") {
		t.Errorf("duplicate error = %v, want the wrapped %q shape", err, "db: insert user: ...")
	}
	if !strings.Contains(err.Error(), "UNIQUE") {
		t.Errorf("duplicate error = %v, want it to name the UNIQUE constraint (the schema must be what rejects it)", err)
	}
	if n := countRows(t, ctx, s, `SELECT COUNT(*) FROM users`); n != 1 {
		t.Errorf("users row count = %d, want 1 (the rejected insert must not leave a row)", n)
	}
}

// ── ProfileRepo ──────────────────────────────────────────────────────────────────────

func TestSQLiteProfileRepoCRUD(t *testing.T) {
	s, _, users, profiles, _ := newIdentityRepos(t)
	ctx := context.Background()
	owner := mustUser(t, ctx, users, "hermes-owner", "Owner")

	// Defaults: PGProfileRepo supplies profile_type='hermes-profile' and
	// context_window_size=32768 in Go, and COALESCEs a nil config to '{}' in SQL.
	callerSupplied := uuid.New()
	def, err := profiles.Create(ctx, &db.Profile{
		ID: callerSupplied, OwnerID: owner.ID, Name: "default", DisplayName: "Default",
	})
	if err != nil {
		t.Fatalf("Create with defaults: %v", err)
	}
	if def.ID == callerSupplied {
		t.Error("Create honored the caller-supplied id; the store generates ids (PG parity)")
	}
	if def.ProfileType != db.ProfileTypeHermesProfile {
		t.Errorf("profile_type = %q, want %q", def.ProfileType, db.ProfileTypeHermesProfile)
	}
	if def.ContextWindowSize != 32768 {
		t.Errorf("context_window_size = %d, want the 32768 default", def.ContextWindowSize)
	}
	if string(def.ConfigJSON) != "{}" {
		t.Errorf("config_json = %q, want the '{}' default", def.ConfigJSON)
	}
	if def.CanAutoRespond || def.IsPublic {
		t.Errorf("can_auto_respond/is_public = %v/%v, want the zero values passed through", def.CanAutoRespond, def.IsPublic)
	}
	if def.Description != nil || def.DeletedAt != nil {
		t.Errorf("description/deleted_at = %v/%v, want nil", def.Description, def.DeletedAt)
	}
	if def.CreatedAt.IsZero() || def.UpdatedAt.IsZero() || def.CreatedAt.Location() != time.UTC {
		t.Errorf("created_at/updated_at = %v/%v, want non-zero UTC instants", def.CreatedAt, def.UpdatedAt)
	}
	// config_json is TEXT (never blob) and valid JSON, which is what the DDL's CHECK and the
	// updated_at trigger rely on.
	if got := storedText(t, ctx, s, `SELECT typeof(config_json) FROM profiles WHERE id = ?`, def.ID.String()); got != "text" {
		t.Errorf("typeof(config_json) = %q, want \"text\"", got)
	}
	if got := storedText(t, ctx, s, `SELECT json_valid(config_json) FROM profiles WHERE id = ?`, def.ID.String()); got != "1" {
		t.Errorf("json_valid(config_json) = %q, want 1", got)
	}
	if got := storedText(t, ctx, s, `SELECT context_window_size FROM profiles WHERE id = ?`, def.ID.String()); got != "32768" {
		t.Errorf("stored context_window_size = %q, want 32768", got)
	}

	// An explicit row, including both boolean columns on and a real config document.
	desc := "Autonomous coding agent"
	cfg := `{"provider":"deepseek","model":"v4"}`
	full, err := profiles.Create(ctx, &db.Profile{
		OwnerID:           owner.ID,
		ProfileType:       db.ProfileTypeHuman,
		Name:              "coder",
		DisplayName:       "Coding Hermes",
		Description:       &desc,
		ConfigJSON:        []byte(cfg),
		CanAutoRespond:    true,
		ContextWindowSize: 4096,
		IsPublic:          true,
	})
	if err != nil {
		t.Fatalf("Create with explicit fields: %v", err)
	}
	if full.ProfileType != db.ProfileTypeHuman || full.ContextWindowSize != 4096 {
		t.Errorf("profile_type/context_window_size = %q/%d", full.ProfileType, full.ContextWindowSize)
	}
	if full.Description == nil || *full.Description != desc {
		t.Errorf("description = %v, want %q", full.Description, desc)
	}
	if string(full.ConfigJSON) != cfg {
		t.Errorf("config_json = %q, want %q", full.ConfigJSON, cfg)
	}
	if !full.CanAutoRespond || !full.IsPublic {
		t.Errorf("can_auto_respond/is_public = %v/%v, want true/true", full.CanAutoRespond, full.IsPublic)
	}
	for _, col := range []string{"can_auto_respond", "is_public"} {
		if got := storedText(t, ctx, s, `SELECT `+col+` FROM profiles WHERE id = ?`, full.ID.String()); got != "1" {
			t.Errorf("stored %s = %q, want 1", col, got)
		}
	}

	got, err := profiles.GetByID(ctx, full.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ID != full.ID || got.Name != "coder" || got.OwnerID != owner.ID {
		t.Errorf("GetByID = %+v, want the created row", got)
	}
	if _, err := profiles.GetByID(ctx, uuid.New()); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("GetByID(unknown) error = %v, want db.ErrNotFound", err)
	}
	if _, err := profiles.Create(ctx, nil); err == nil || err.Error() != "db: profile is nil" {
		t.Errorf("Create(nil) error = %v, want %q", err, "db: profile is nil")
	}
}

func TestSQLiteProfileRepoGetByOwnerOrdersAndFilters(t *testing.T) {
	s, _, users, profiles, _ := newIdentityRepos(t)
	ctx := context.Background()
	owner := mustUser(t, ctx, users, "hermes-owner", "Owner")
	other := mustUser(t, ctx, users, "hermes-other", "Other")

	base := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	mine := make([]*db.Profile, 0, 3)
	for i := 0; i < 3; i++ {
		p := mustProfile(t, ctx, profiles, owner.ID, "profile-"+string(rune('a'+i)))
		// created_at is seeded so the DESC ordering is deterministic instead of depending on
		// millisecond-frequency inserts.
		execSQL(t, ctx, s, `UPDATE profiles SET created_at = ? WHERE id = ?`,
			EncodeTime(base.Add(time.Duration(i)*time.Minute)), p.ID.String())
		mine = append(mine, p)
	}
	theirs := mustProfile(t, ctx, profiles, other.ID, "someone-else")

	// A soft-deleted profile must not appear, on either owner's list.
	execSQL(t, ctx, s, `UPDATE profiles SET deleted_at = ? WHERE id = ?`,
		EncodeTime(time.Now()), mine[1].ID.String())

	got, err := profiles.GetByOwner(ctx, owner.ID)
	if err != nil {
		t.Fatalf("GetByOwner: %v", err)
	}
	if len(got) != 2 || got[0].ID != mine[2].ID || got[1].ID != mine[0].ID {
		t.Errorf("GetByOwner = %v, want [%s %s] (newest first, soft-deleted row excluded)",
			profileIDs(got), mine[2].ID, mine[0].ID)
	}
	otherList, err := profiles.GetByOwner(ctx, other.ID)
	if err != nil {
		t.Fatalf("GetByOwner(other): %v", err)
	}
	if len(otherList) != 1 || otherList[0].ID != theirs.ID {
		t.Errorf("GetByOwner(other) = %v, want only %s", profileIDs(otherList), theirs.ID)
	}
	// An empty result is an empty slice, not nil: PGProfileRepo builds make([]Profile, 0).
	empty, err := profiles.GetByOwner(ctx, uuid.New())
	if err != nil {
		t.Fatalf("GetByOwner(unknown): %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Errorf("GetByOwner(unknown) = %v (nil=%v), want an empty non-nil slice", empty, empty == nil)
	}
}

func TestSQLiteProfileRepoListByTree(t *testing.T) {
	s, trees, users, profiles, members := newIdentityRepos(t)
	ctx := context.Background()
	owner := mustUser(t, ctx, users, "hermes-owner", "Owner")
	tree := mustTree(t, ctx, trees, "membership")
	otherTree := mustTree(t, ctx, trees, "unrelated")

	p1 := mustProfile(t, ctx, profiles, owner.ID, "p1")
	p2 := mustProfile(t, ctx, profiles, owner.ID, "p2")
	nonMember := mustProfile(t, ctx, profiles, owner.ID, "p3")
	deleted := mustProfile(t, ctx, profiles, owner.ID, "p4")

	// p1 joined first, then p2; the user member is not a profile and must not appear.
	joinedP1 := addMember(t, ctx, members, &db.TreeMember{TreeID: tree.ID, ProfileID: &p1.ID, IsVisible: true})
	joinedP2 := addMember(t, ctx, members, &db.TreeMember{TreeID: tree.ID, ProfileID: &p2.ID, IsVisible: true})
	base := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	execSQL(t, ctx, s, `UPDATE tree_members SET joined_at = ? WHERE id = ?`, EncodeTime(base), joinedP1.ID.String())
	execSQL(t, ctx, s, `UPDATE tree_members SET joined_at = ? WHERE id = ?`, EncodeTime(base.Add(time.Hour)), joinedP2.ID.String())
	addMember(t, ctx, members, &db.TreeMember{TreeID: tree.ID, UserID: &owner.ID, IsVisible: true})
	addMember(t, ctx, members, &db.TreeMember{TreeID: otherTree.ID, ProfileID: &nonMember.ID, IsVisible: true})
	addMember(t, ctx, members, &db.TreeMember{TreeID: tree.ID, ProfileID: &deleted.ID, IsVisible: true})
	execSQL(t, ctx, s, `UPDATE profiles SET deleted_at = ? WHERE id = ?`, EncodeTime(time.Now()), deleted.ID.String())

	got, err := profiles.ListByTree(ctx, tree.ID)
	if err != nil {
		t.Fatalf("ListByTree: %v", err)
	}
	// joined_at DESC puts the most recent membership first; the user membership, the profile
	// from another tree and the soft-deleted profile are all excluded.
	if len(got) != 2 || got[0].ID != p2.ID || got[1].ID != p1.ID {
		t.Errorf("ListByTree = %v, want [%s %s]", profileIDs(got), p2.ID, p1.ID)
	}
	none, err := profiles.ListByTree(ctx, uuid.New())
	if err != nil {
		t.Fatalf("ListByTree(unknown tree): %v", err)
	}
	if none == nil || len(none) != 0 {
		t.Errorf("ListByTree(unknown tree) = %v (nil=%v), want an empty non-nil slice", none, none == nil)
	}
}

func TestSQLiteProfileRepoSchemaConstraints(t *testing.T) {
	s, _, users, profiles, _ := newIdentityRepos(t)
	ctx := context.Background()
	owner := mustUser(t, ctx, users, "hermes-owner", "Owner")
	mustProfile(t, ctx, profiles, owner.ID, "taken")

	// owner_id references users(id): an unknown owner is a foreign-key failure, not a row.
	if _, err := profiles.Create(ctx, &db.Profile{OwnerID: uuid.New(), Name: "orphan", DisplayName: "Orphan"}); err == nil {
		t.Error("Create with an unknown owner succeeded, want the owner_id foreign key to reject it")
	}
	// uq_profiles_owner_name: the same name twice under one owner is rejected.
	if _, err := profiles.Create(ctx, &db.Profile{OwnerID: owner.ID, Name: "taken", DisplayName: "Again"}); err == nil {
		t.Error("Create with a duplicate (owner_id, name) succeeded, want the UNIQUE constraint to reject it")
	}
	// chk_profiles_name / chk_profiles_display_name / chk_profiles_context_window.
	if _, err := profiles.Create(ctx, &db.Profile{OwnerID: owner.ID, Name: "", DisplayName: "Empty name"}); err == nil {
		t.Error("Create with an empty name succeeded, want chk_profiles_name to reject it")
	}
	if _, err := profiles.Create(ctx, &db.Profile{OwnerID: owner.ID, Name: "short-name", DisplayName: ""}); err == nil {
		t.Error("Create with an empty display_name succeeded, want chk_profiles_display_name to reject it")
	}
	if _, err := profiles.Create(ctx, &db.Profile{OwnerID: owner.ID, Name: "tiny-window", DisplayName: "Tiny", ContextWindowSize: 10}); err == nil {
		t.Error("Create with context_window_size below 1024 succeeded, want chk_profiles_context_window to reject it")
	}
	// An explicit 1024 is the boundary and must be accepted (it is not defaulted away).
	edge, err := profiles.Create(ctx, &db.Profile{OwnerID: owner.ID, Name: "edge-window", DisplayName: "Edge", ContextWindowSize: 1024})
	if err != nil {
		t.Fatalf("Create with context_window_size 1024: %v", err)
	}
	if edge.ContextWindowSize != 1024 {
		t.Errorf("context_window_size = %d, want the explicit 1024", edge.ContextWindowSize)
	}
	// config_json is validated on write, the guarantee jsonb gave PostgreSQL.
	if _, err := profiles.Create(ctx, &db.Profile{OwnerID: owner.ID, Name: "bad-json", DisplayName: "Bad", ConfigJSON: []byte("{")}); err == nil {
		t.Error("Create with malformed config_json succeeded, want the json_valid CHECK to reject it")
	}
	// The rejected inserts left exactly the two accepted rows behind.
	if n := countRows(t, ctx, s, `SELECT COUNT(*) FROM profiles`); n != 2 {
		t.Errorf("profiles row count = %d, want 2 (rejected inserts must not leave rows)", n)
	}
}

// ── TreeMemberRepo ───────────────────────────────────────────────────────────────────

func TestSQLiteTreeMemberRepoCRUD(t *testing.T) {
	s, trees, users, profiles, members := newIdentityRepos(t)
	ctx := context.Background()
	user := mustUser(t, ctx, users, "hermes-member", "Member")
	owner := mustUser(t, ctx, users, "hermes-owner", "Owner")
	profile := mustProfile(t, ctx, profiles, owner.ID, "agent")
	tree := mustTree(t, ctx, trees, "shared")

	// A user membership: Role empty default to 'member' (the PG Go-side default).
	um, err := members.Add(ctx, &db.TreeMember{TreeID: tree.ID, UserID: &user.ID, IsVisible: true, InvitedBy: &owner.ID})
	if err != nil {
		t.Fatalf("Add(user member): %v", err)
	}
	if um.ID == uuid.Nil || um.Role != db.TreeRoleMember {
		t.Errorf("id/role = %s/%q, want a generated id and %q", um.ID, um.Role, db.TreeRoleMember)
	}
	if um.UserID == nil || *um.UserID != user.ID || um.ProfileID != nil {
		t.Errorf("user_id/profile_id = %v/%v, want the user participant only", um.UserID, um.ProfileID)
	}
	if !um.IsVisible || um.AutoApproved {
		t.Errorf("is_visible/auto_approved = %v/%v, want the passed true and zero false", um.IsVisible, um.AutoApproved)
	}
	if um.InvitedBy == nil || *um.InvitedBy != owner.ID {
		t.Errorf("invited_by = %v, want %s", um.InvitedBy, owner.ID)
	}
	if um.JoinedAt.IsZero() || um.JoinedAt.Location() != time.UTC {
		t.Errorf("joined_at = %v, want a non-zero UTC instant", um.JoinedAt)
	}
	if got := storedText(t, ctx, s, `SELECT is_visible FROM tree_members WHERE id = ?`, um.ID.String()); got != "1" {
		t.Errorf("stored is_visible = %q, want 1", got)
	}
	if got := storedText(t, ctx, s, `SELECT auto_approved FROM tree_members WHERE id = ?`, um.ID.String()); got != "0" {
		t.Errorf("stored auto_approved = %q, want 0", got)
	}
	if stored := storedText(t, ctx, s, `SELECT joined_at FROM tree_members WHERE id = ?`, um.ID.String()); stored != EncodeTime(um.JoinedAt) {
		t.Errorf("stored joined_at = %q, want %q (canonical D1 shape)", stored, EncodeTime(um.JoinedAt))
	}

	// A profile membership with an explicit role and both flags set.
	pm, err := members.Add(ctx, &db.TreeMember{
		TreeID: tree.ID, ProfileID: &profile.ID, Role: db.TreeRoleAdmin,
		IsVisible: false, AutoApproved: true,
	})
	if err != nil {
		t.Fatalf("Add(profile member): %v", err)
	}
	if pm.ProfileID == nil || *pm.ProfileID != profile.ID || pm.UserID != nil {
		t.Errorf("profile_id/user_id = %v/%v, want the profile participant only", pm.ProfileID, pm.UserID)
	}
	if pm.Role != db.TreeRoleAdmin || pm.IsVisible || !pm.AutoApproved {
		t.Errorf("role/is_visible/auto_approved = %q/%v/%v, want admin/false/true", pm.Role, pm.IsVisible, pm.AutoApproved)
	}
	if got := storedText(t, ctx, s, `SELECT auto_approved FROM tree_members WHERE id = ?`, pm.ID.String()); got != "1" {
		t.Errorf("stored auto_approved = %q, want 1", got)
	}

	// GetByTree: oldest membership first. joined_at is seeded so the ordering does not depend
	// on millisecond-frequency inserts.
	base := time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	execSQL(t, ctx, s, `UPDATE tree_members SET joined_at = ? WHERE id = ?`, EncodeTime(base), um.ID.String())
	execSQL(t, ctx, s, `UPDATE tree_members SET joined_at = ? WHERE id = ?`, EncodeTime(base.Add(time.Hour)), pm.ID.String())
	byTree, err := members.GetByTree(ctx, tree.ID)
	if err != nil {
		t.Fatalf("GetByTree: %v", err)
	}
	if len(byTree) != 2 || byTree[0].ID != um.ID || byTree[1].ID != pm.ID {
		t.Errorf("GetByTree = %v, want [%s %s] (joined_at ASC)", memberIDs(byTree), um.ID, pm.ID)
	}

	// GetByUser: every tree the user belongs to, newest first; the profile membership belongs
	// to a profile, so it is not in this list.
	second := mustTree(t, ctx, trees, "second")
	um2, err := members.Add(ctx, &db.TreeMember{TreeID: second.ID, UserID: &user.ID, IsVisible: true})
	if err != nil {
		t.Fatalf("Add(user member, second tree): %v", err)
	}
	execSQL(t, ctx, s, `UPDATE tree_members SET joined_at = ? WHERE id = ?`, EncodeTime(base.Add(2*time.Hour)), um2.ID.String())
	byUser, err := members.GetByUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetByUser: %v", err)
	}
	if len(byUser) != 2 || byUser[0].ID != um2.ID || byUser[1].ID != um.ID {
		t.Errorf("GetByUser = %v, want [%s %s] (joined_at DESC)", memberIDs(byUser), um2.ID, um.ID)
	}
	if other, err := members.GetByUser(ctx, profile.ID); err != nil {
		t.Fatalf("GetByUser(profile id): %v", err)
	} else if other == nil || len(other) != 0 {
		t.Errorf("GetByUser(profile id) = %v (nil=%v), want an empty non-nil slice", other, other == nil)
	}

	// IsMember / IsTreeDeleted.
	if ok, err := members.IsMember(ctx, tree.ID, user.ID); err != nil || !ok {
		t.Errorf("IsMember = %v (err %v), want true", ok, err)
	}
	if ok, err := members.IsMember(ctx, second.ID, owner.ID); err != nil || ok {
		t.Errorf("IsMember(non-member) = %v (err %v), want false", ok, err)
	}
	if del, err := members.IsTreeDeleted(ctx, tree.ID); err != nil || del {
		t.Errorf("IsTreeDeleted(live tree) = %v (err %v), want false", del, err)
	}
	if err := trees.SoftDelete(ctx, tree.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if del, err := members.IsTreeDeleted(ctx, tree.ID); err != nil || !del {
		t.Errorf("IsTreeDeleted(soft-deleted tree) = %v (err %v), want true", del, err)
	}
	if del, err := members.IsTreeDeleted(ctx, uuid.New()); err != nil || del {
		t.Errorf("IsTreeDeleted(unknown tree) = %v (err %v), want false", del, err)
	}

	// UpdateRole returns the stored row; an unknown id is ErrNotFound and an unknown role is
	// rejected by the translated CHECK (PG's enum cast does the same).
	promoted, err := members.UpdateRole(ctx, um.ID, db.TreeRoleOwner)
	if err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}
	if promoted.ID != um.ID || promoted.Role != db.TreeRoleOwner {
		t.Errorf("UpdateRole = %s/%q, want %s/%q", promoted.ID, promoted.Role, um.ID, db.TreeRoleOwner)
	}
	if promoted.UserID == nil || *promoted.UserID != user.ID || promoted.ProfileID != nil {
		t.Errorf("UpdateRole changed the participant: user_id/profile_id = %v/%v", promoted.UserID, promoted.ProfileID)
	}
	if !promoted.IsVisible || promoted.AutoApproved {
		t.Errorf("UpdateRole changed the flags: is_visible/auto_approved = %v/%v", promoted.IsVisible, promoted.AutoApproved)
	}
	// joined_at still carries the seeded instant, i.e. the UPDATE touched the role only.
	if !promoted.JoinedAt.Equal(base) {
		t.Errorf("UpdateRole changed joined_at: %v, want the untouched %v", promoted.JoinedAt, base)
	}
	if _, err := members.UpdateRole(ctx, um.ID, "superuser"); err == nil {
		t.Error("UpdateRole with an unknown role succeeded, want the role CHECK to reject it")
	}
	if _, err := members.UpdateRole(ctx, uuid.New(), db.TreeRoleViewer); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("UpdateRole(unknown id) error = %v, want db.ErrNotFound", err)
	}

	// Remove hard-deletes and reports the second attempt as not found.
	if err := members.Remove(ctx, pm.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := members.Remove(ctx, pm.ID); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("second Remove error = %v, want db.ErrNotFound", err)
	}
	if n := countRows(t, ctx, s, `SELECT COUNT(*) FROM tree_members WHERE id = ?`, pm.ID.String()); n != 0 {
		t.Errorf("removed membership row count = %d, want the row gone (hard delete)", n)
	}
	if ok, err := members.IsMember(ctx, tree.ID, user.ID); err != nil || !ok {
		t.Errorf("IsMember after removing the profile membership = %v (err %v), want true", ok, err)
	}
	if remaining, err := members.GetByTree(ctx, tree.ID); err != nil {
		t.Fatalf("GetByTree after Remove: %v", err)
	} else if len(remaining) != 1 || remaining[0].ID != um.ID {
		t.Errorf("GetByTree after Remove = %v, want only %s", memberIDs(remaining), um.ID)
	}
}

func TestSQLiteTreeMemberRepoParticipantValidation(t *testing.T) {
	s, trees, users, profiles, members := newIdentityRepos(t)
	ctx := context.Background()
	user := mustUser(t, ctx, users, "hermes-member", "Member")
	owner := mustUser(t, ctx, users, "hermes-owner", "Owner")
	profile := mustProfile(t, ctx, profiles, owner.ID, "agent")
	tree := mustTree(t, ctx, trees, "shared")

	// Both participant checks keep PGTreeMemberRepo's message verbatim, and neither writes.
	cases := []struct {
		name string
		in   *db.TreeMember
		want string
	}{
		{"nil", nil, "db: tree member is nil"},
		{"both", &db.TreeMember{TreeID: tree.ID, UserID: &user.ID, ProfileID: &profile.ID},
			"db: tree member must reference exactly one of user_id, profile_id"},
		{"neither", &db.TreeMember{TreeID: tree.ID},
			"db: tree member must reference exactly one of user_id, profile_id"},
	}
	for _, tc := range cases {
		_, err := members.Add(ctx, tc.in)
		if err == nil || err.Error() != tc.want {
			t.Errorf("Add(%s) error = %v, want %q", tc.name, err, tc.want)
		}
	}
	if n := countRows(t, ctx, s, `SELECT COUNT(*) FROM tree_members`); n != 0 {
		t.Errorf("tree_members row count = %d, want 0 (a rejected insert must not leave a row)", n)
	}
}

func TestSQLiteTreeMemberRepoSchemaConstraints(t *testing.T) {
	s, trees, users, profiles, members := newIdentityRepos(t)
	ctx := context.Background()
	user := mustUser(t, ctx, users, "hermes-member", "Member")
	owner := mustUser(t, ctx, users, "hermes-owner", "Owner")
	profile := mustProfile(t, ctx, profiles, owner.ID, "agent")
	tree := mustTree(t, ctx, trees, "shared")

	// tree_id / user_id / profile_id all carry foreign keys (foreign_keys=ON, D6).
	if _, err := members.Add(ctx, &db.TreeMember{TreeID: uuid.New(), UserID: &user.ID}); err == nil {
		t.Error("Add with an unknown tree succeeded, want the tree_id foreign key to reject it")
	}
	unknown := uuid.New()
	if _, err := members.Add(ctx, &db.TreeMember{TreeID: tree.ID, UserID: &unknown}); err == nil {
		t.Error("Add with an unknown user succeeded, want the user_id foreign key to reject it")
	}
	if _, err := members.Add(ctx, &db.TreeMember{TreeID: tree.ID, ProfileID: &unknown}); err == nil {
		t.Error("Add with an unknown profile succeeded, want the profile_id foreign key to reject it")
	}
	if _, err := members.Add(ctx, &db.TreeMember{TreeID: tree.ID, UserID: &user.ID, InvitedBy: &unknown}); err == nil {
		t.Error("Add with an unknown invited_by succeeded, want the invited_by foreign key to reject it")
	}
	if n := countRows(t, ctx, s, `SELECT COUNT(*) FROM tree_members`); n != 0 {
		t.Errorf("tree_members row count = %d, want 0 (every insert above was rejected)", n)
	}

	// uq_tree_members_tree_user: one row per (tree, user).
	addMember(t, ctx, members, &db.TreeMember{TreeID: tree.ID, UserID: &user.ID})
	_, err := members.Add(ctx, &db.TreeMember{TreeID: tree.ID, UserID: &user.ID})
	if err == nil {
		t.Fatal("Add of a duplicate (tree_id, user_id) succeeded, want the UNIQUE constraint to reject it")
	}
	if !strings.Contains(err.Error(), "db: insert tree member") || !strings.Contains(err.Error(), "UNIQUE") {
		t.Errorf("duplicate error = %v, want the wrapped db: insert tree member … UNIQUE shape", err)
	}
	// The second UNIQUE constraint is on the profile participant: the same profile twice in
	// one tree is rejected even though user_id is NULL in both rows.
	addMember(t, ctx, members, &db.TreeMember{TreeID: tree.ID, ProfileID: &profile.ID})
	if _, err := members.Add(ctx, &db.TreeMember{TreeID: tree.ID, ProfileID: &profile.ID}); err == nil {
		t.Error("Add of a duplicate (tree_id, profile_id) succeeded, want the UNIQUE constraint to reject it")
	}
	if n := countRows(t, ctx, s, `SELECT COUNT(*) FROM tree_members`); n != 2 {
		t.Errorf("tree_members row count = %d, want 2 (the two accepted rows)", n)
	}
}

// ── helper-level unit tests ──────────────────────────────────────────────────────────

// TestSQLiteIdentityRepoHelpers pins the small conversions the three repositories share, at
// the boundaries the DDL allows and one value it forbids.
func TestSQLiteIdentityRepoHelpers(t *testing.T) {
	if boolArg(true) != 1 || boolArg(false) != 0 {
		t.Errorf("boolArg = %d/%d, want 1/0", boolArg(true), boolArg(false))
	}
	for _, tc := range []struct {
		name string
		in   sql.NullInt64
		want bool
		err  bool
	}{
		{"zero", sql.NullInt64{Valid: true, Int64: 0}, false, false},
		{"one", sql.NullInt64{Valid: true, Int64: 1}, true, false},
		{"null", sql.NullInt64{Valid: false}, false, true},
		{"out of domain", sql.NullInt64{Valid: true, Int64: 2}, false, true},
	} {
		got, err := boolColumn("col", tc.in)
		if (err != nil) != tc.err {
			t.Errorf("boolColumn(%s) error = %v, want error=%v", tc.name, err, tc.err)
		}
		if !tc.err && got != tc.want {
			t.Errorf("boolColumn(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}

	// qualifyColumns must survive the multi-line column constants in this package: one
	// qualified name per input column, single-spaced, no newlines.
	qualified := qualifyColumns("p", profileColumns)
	if !strings.HasPrefix(qualified, "p.id, p.owner_id, p.profile_type") {
		t.Errorf("qualifyColumns = %q, want it to start with p.id, p.owner_id, p.profile_type", qualified)
	}
	if strings.Contains(qualified, "  ") || strings.Contains(qualified, "\n") {
		t.Errorf("qualifyColumns = %q, want single spaces and no newlines", qualified)
	}
	if n, want := strings.Count(qualified, ", "), strings.Count(profileColumns, ","); n != want {
		t.Errorf("qualifyColumns separator count = %d, want %d (no column dropped or duplicated)", n, want)
	}
	for _, col := range strings.Split(qualified, ", ") {
		if !strings.HasPrefix(col, "p.") || strings.TrimSpace(col) != col {
			t.Errorf("qualifyColumns produced %q, want every column prefixed with p. and trimmed", col)
		}
	}

	// optionalString: only SQL NULL is nil, so an empty string round-trips as "".
	if s, err := optionalString("col", sql.NullString{Valid: false}); err != nil || s != nil {
		t.Errorf("optionalString(NULL) = %v (err %v), want nil", s, err)
	}
	if s, err := optionalString("col", sql.NullString{Valid: true, String: ""}); err != nil || s == nil || *s != "" {
		t.Errorf("optionalString(\"\") = %v (err %v), want a pointer to the empty string", s, err)
	}

	// nullableTimeArg renders through EncodeTime, so the value the store decodes is the value
	// the caller supplied, truncated to the canonical millisecond precision.
	at := time.Date(2026, 5, 4, 3, 2, 1, 987654321, time.UTC)
	arg := nullableTimeArg(&at)
	s, ok := arg.(string)
	if !ok {
		t.Fatalf("nullableTimeArg = %T, want the canonical string shape", arg)
	}
	decoded, err := DecodeTime(s)
	if err != nil {
		t.Fatalf("nullableTimeArg produced %q, which DecodeTime rejects: %v", s, err)
	}
	if !decoded.Equal(at.Truncate(time.Millisecond)) {
		t.Errorf("nullableTimeArg round-trip = %v, want %v", decoded, at.Truncate(time.Millisecond))
	}
	if got := nullableTimeArg(nil); got != nil {
		t.Errorf("nullableTimeArg(nil) = %v, want SQL NULL", got)
	}
}

// ── test-local helpers ───────────────────────────────────────────────────────────────

// addMember creates a membership through the repository and fails the test if the stored row
// is missing a participant.
func addMember(t *testing.T, ctx context.Context, r *TreeMemberRepo, m *db.TreeMember) *db.TreeMember {
	t.Helper()
	out, err := r.Add(ctx, m)
	if err != nil {
		t.Fatalf("TreeMemberRepo.Add(%v): %v", m, err)
	}
	return out
}

// profileIDs and memberIDs project slices onto their ids, for readable failure messages.
func profileIDs(profiles []db.Profile) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(profiles))
	for _, p := range profiles {
		out = append(out, p.ID)
	}
	return out
}

func memberIDs(members []db.TreeMember) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(members))
	for _, m := range members {
		out = append(out, m.ID)
	}
	return out
}
