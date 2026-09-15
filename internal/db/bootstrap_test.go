// GAP-071 tests: dev workspace + profile provisioning (idempotency, row
// shape, and — the acceptance criterion — the seeded rows satisfy the
// file-viewer actor-profile resolution query).
//
// DB-backed tests use the shared integration pool (one migrated DB per
// test binary, truncated per test — same isolation contract as
// NewIntegrationPool). They SKIP without PostgreSQL, exactly like the
// GAP-064 sibling tests in internal/service.
package db_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/fileviewer"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

// devWorkspaceUUID parses the exported constant so the tests fail loudly
// (not silently) if it ever stops being a valid UUID — docs cite it.
func devWorkspaceUUID(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(db.DevWorkspaceID)
	if err != nil {
		t.Fatalf("db.DevWorkspaceID = %q is not a valid UUID: %v", db.DevWorkspaceID, err)
	}
	return id
}

// TestGAP071_EnsureDevWorkspaceProfileIsIdempotent asserts the first call
// provisions all three rows, the second call inserts nothing, and the rows
// have the documented dev shape.
func TestGAP071_EnsureDevWorkspaceProfileIsIdempotent(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()

	if inserted, err := db.EnsureDevJWTUser(ctx, pool); err != nil {
		t.Fatalf("EnsureDevJWTUser: %v", err)
	} else if !inserted {
		t.Fatal("EnsureDevJWTUser inserted = false on a fresh database, want true")
	}

	inserted, err := db.EnsureDevWorkspaceProfile(ctx, pool)
	if err != nil {
		t.Fatalf("EnsureDevWorkspaceProfile (first run): %v", err)
	}
	if !inserted {
		t.Fatal("EnsureDevWorkspaceProfile inserted = false on a fresh database, want true")
	}

	wsID := devWorkspaceUUID(t)
	userID := uuid.MustParse(db.DevJWTUserID)

	// Workspace row shape.
	var name, slug, description string
	if err := pool.QueryRow(ctx,
		`SELECT name, slug, description FROM workspaces WHERE id = $1`, wsID).
		Scan(&name, &slug, &description); err != nil {
		t.Fatalf("select workspaces row: %v", err)
	}
	if name != "Dev Workspace" || slug != "dev" {
		t.Fatalf("workspaces row = (%q, %q), want (\"Dev Workspace\", \"dev\")", name, slug)
	}
	if description == "" {
		t.Fatal("workspaces.description is empty, want the GAP-071 marker text")
	}

	// Profile row: owned by the dev JWT user, live, name/display_name
	// inside the migration's CHECK limits.
	var profileID uuid.UUID
	var ownerID uuid.UUID
	var profileName, displayName, profileType string
	var contextWindow int
	if err := pool.QueryRow(ctx, `
		SELECT id, owner_id, name, display_name, profile_type, context_window_size
		FROM profiles
		WHERE owner_id = $1 AND name = $2 AND deleted_at IS NULL`,
		userID, db.DevProfileName).
		Scan(&profileID, &ownerID, &profileName, &displayName, &profileType, &contextWindow); err != nil {
		t.Fatalf("select profiles row: %v", err)
	}
	if ownerID != userID {
		t.Fatalf("profiles.owner_id = %s, want %s (the dev JWT user)", ownerID, userID)
	}
	if profileType != "hermes-profile" {
		t.Fatalf("profiles.profile_type = %q, want \"hermes-profile\"", profileType)
	}
	if displayName != "Dev Hermes" || contextWindow < 1024 {
		t.Fatalf("profiles row = (display_name %q, context_window_size %d)", displayName, contextWindow)
	}

	// Profile route: active mapping for the dev workspace.
	var routeName, routeDisplay string
	var routeActive bool
	if err := pool.QueryRow(ctx, `
		SELECT profile_name, display_name, is_active
		FROM profile_route WHERE workspace_id = $1`, wsID).
		Scan(&routeName, &routeDisplay, &routeActive); err != nil {
		t.Fatalf("select profile_route row: %v", err)
	}
	if routeName != db.DevProfileName || !routeActive {
		t.Fatalf("profile_route = (%q, is_active=%v), want (%q, true)", routeName, routeActive, db.DevProfileName)
	}

	// Idempotency: the second run must insert nothing and leave the row
	// count unchanged.
	counts := func() (int, int, int) {
		t.Helper()
		var ws, prof, route int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM workspaces`).Scan(&ws); err != nil {
			t.Fatalf("count workspaces: %v", err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM profiles`).Scan(&prof); err != nil {
			t.Fatalf("count profiles: %v", err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM profile_route`).Scan(&route); err != nil {
			t.Fatalf("count profile_route: %v", err)
		}
		return ws, prof, route
	}
	wsBefore, profBefore, routeBefore := counts()

	again, err := db.EnsureDevWorkspaceProfile(ctx, pool)
	if err != nil {
		t.Fatalf("EnsureDevWorkspaceProfile (second run): %v", err)
	}
	if again {
		t.Fatal("EnsureDevWorkspaceProfile inserted = true on the second run, want false (idempotent)")
	}
	wsAfter, profAfter, routeAfter := counts()
	if wsBefore != wsAfter || profBefore != profAfter || routeBefore != routeAfter {
		t.Fatalf("row counts changed on the second run: workspaces %d→%d, profiles %d→%d, profile_route %d→%d",
			wsBefore, wsAfter, profBefore, profAfter, routeBefore, routeAfter)
	}

	// The profile id must be stable across the two runs (the no-op path
	// must not mint a replacement row).
	var profileIDAfter uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT id FROM profiles WHERE owner_id = $1 AND name = $2`, userID, db.DevProfileName).
		Scan(&profileIDAfter); err != nil {
		t.Fatalf("re-select profiles row: %v", err)
	}
	if profileIDAfter != profileID {
		t.Fatalf("profiles.id changed across runs: %s → %s", profileID, profileIDAfter)
	}
}

// TestGAP071_SeededRowsSatisfyActorResolution is the acceptance criterion
// for the file-viewer half of GAP-071: after provisioning, the query the
// /api/v1/files + /api/v1/viewers/dispatch handlers run per request
// (internal/fileviewer ResolveActorProfile) resolves the documented dev
// JWT subject (a users.id) to the seeded dev profile — no 404
// PROFILE_NOT_FOUND.
func TestGAP071_SeededRowsSatisfyActorResolution(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()

	userID := uuid.MustParse(db.DevJWTUserID)

	// Precondition (RED-proof): with only the GAP-064 user provisioned, a
	// fresh database has NO resolvable profile — this is the bug.
	if _, err := db.EnsureDevJWTUser(ctx, pool); err != nil {
		t.Fatalf("EnsureDevJWTUser: %v", err)
	}
	repo := fileviewer.NewPGFileMetadataRepo(pool)
	if _, err := repo.ResolveActorProfile(ctx, userID); err == nil {
		t.Fatal("ResolveActorProfile succeeded before provisioning; the GAP-071 premise is not reproducible on this schema")
	}

	if _, err := db.EnsureDevWorkspaceProfile(ctx, pool); err != nil {
		t.Fatalf("EnsureDevWorkspaceProfile: %v", err)
	}

	got, err := repo.ResolveActorProfile(ctx, userID)
	if err != nil {
		t.Fatalf("ResolveActorProfile after provisioning: %v", err)
	}
	if got == uuid.Nil {
		t.Fatal("ResolveActorProfile returned the zero UUID")
	}
	if got == userID {
		t.Fatalf("ResolveActorProfile returned the actor id %s — that means no profiles row was seeded", got)
	}

	// The resolved id must be the seeded dev profile, live and owned by
	// the dev user.
	var owner uuid.UUID
	var name string
	if err := pool.QueryRow(ctx,
		`SELECT owner_id, name FROM profiles WHERE id = $1 AND deleted_at IS NULL`, got).
		Scan(&owner, &name); err != nil {
		t.Fatalf("resolved profile is not a live profiles row: %v", err)
	}
	if owner != userID || name != db.DevProfileName {
		t.Fatalf("resolved profile = (owner %s, name %q), want (%s, %q)", owner, name, userID, db.DevProfileName)
	}
}

// TestGAP071_NilPool guards the fail-loud contract callers rely on.
func TestGAP071_NilPool(t *testing.T) {
	if _, err := db.EnsureDevWorkspaceProfile(context.Background(), nil); err == nil {
		t.Fatal("EnsureDevWorkspaceProfile(nil pool) = nil error, want an error")
	}
}
