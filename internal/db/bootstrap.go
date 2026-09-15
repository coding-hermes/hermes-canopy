// Package db provides dev-mode bootstrap: provision the well-known dev JWT
// user (and, since GAP-071, the dev workspace + profile) after migration.
//
// GAP-064: on a fresh database the documented quick start (README.md
// §"Authentication (dev mode)") authenticates with a FIXED subject UUID
// signed by the default dev secret, but no users row existed for it.
// The first tree create then aborted on the tree_members FK
// (tree_members_user_id_fkey) inside the service transaction and the
// caller only saw a blanket 503 "database unavailable". Provisioning
// the row at startup — only when the server runs on the default dev
// secret — makes the documented path work with zero manual SQL
// (previously scripts/seed-demo-data.sql had to be run by hand).
//
// GAP-071 extends the same pattern to the file-viewer subsystem
// (SPEC-PL-02): the /api/v1/files + /api/v1/viewers routes resolve the JWT
// sub to a profiles row (so a fresh database 404'd PROFILE_NOT_FOUND on
// every call) and the documented workspace-profile endpoint needs a
// workspaces row for profile_route's FK. EnsureDevWorkspaceProfile
// provisions both, still only on the default dev secret.
package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// DevJWTSecretDefault is the JWT secret the documented dev mode runs
	// with (internal/config.Default().JWTSecret; env override JWT_SECRET).
	// Provisioning only happens when the configured secret equals this —
	// a production server must never silently mint users.
	DevJWTSecretDefault = "dev-secret-change-me"

	// DevJWTUserID is the dev JWT subject UUID (sub claim) documented in
	// README.md and docs/INTEGRATION.md, mirrored by the Vite dev proxy's
	// pre-generated token.
	DevJWTUserID = "00000000-0000-0000-0000-000000000001"

	// DevWorkspaceID is the fixed workspace id provisioned in dev mode
	// (GAP-071). profile_route.workspace_id has an FK to workspaces(id)
	// (migration 000018), so the documented
	// POST /api/v1/workspaces/{ws}/profiles walkthrough needs a real
	// workspaces row; this constant is that row's id and is cited by the
	// docs and tests.
	DevWorkspaceID = "00000000-0000-0000-0000-000000000010"

	// DevProfileName is the dev profile provisioned for DevJWTUserID. The
	// file-viewer API resolves the JWT sub to an ACTING profile
	// (profiles.id where id = sub OR owner_id = sub), so without this row
	// every /api/v1/files and /viewers/dispatch call 404s
	// PROFILE_NOT_FOUND on a fresh database.
	DevProfileName = "dev-hermes"

	// devWorkspaceSlug is the workspaces.slug of the dev workspace. slug is
	// UNIQUE (migration 000018) and "dev" cannot collide with a
	// user-created workspace slug, which are generated from titles.
	devWorkspaceSlug = "dev"
)

// IsDevJWTSecret reports whether secret is the well-known dev default,
// i.e. whether dev-mode bootstrap conveniences should apply.
func IsDevJWTSecret(secret string) bool {
	return secret == DevJWTSecretDefault
}

// EnsureDevJWTUser provisions the dev JWT user (id = DevJWTUserID) so
// the fixed sub claim in the documented dev token satisfies the
// tree_members FK. It is idempotent: an existing row is left untouched
// (ON CONFLICT (id) DO NOTHING) and inserted reports false.
//
// Callers should fail startup on error — a dev server that cannot write
// its own dev user is broken.
func EnsureDevJWTUser(ctx context.Context, pool *pgxpool.Pool) (inserted bool, err error) {
	if pool == nil {
		return false, fmt.Errorf("db: EnsureDevJWTUser: nil pool")
	}
	tag, err := pool.Exec(ctx, `
        INSERT INTO users (id, hermes_user_id, email, display_name, is_active)
        VALUES ($1, $2, 'dev@canopy.dev', 'Dev User', true)
        ON CONFLICT (id) DO NOTHING`,
		DevJWTUserID, DevJWTUserID)
	if err != nil {
		return false, fmt.Errorf("db: provision dev JWT user: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// EnsureDevWorkspaceProfile provisions the dev-mode workspace, the dev
// profile owned by DevJWTUserID, and the active profile_route mapping for
// that workspace (GAP-071). It is idempotent — every insert is
// ON CONFLICT DO NOTHING against the row's natural key — and reports
// whether this call actually created anything (inserted=false means all
// three rows were already present).
//
// Why all three:
//   - workspaces: profile_route.workspace_id carries the FK
//     fk_profile_route_workspace (migration 000018), so
//     POST /api/v1/workspaces/{ws}/profiles used to fail with a raw 500
//     on a fresh database because no workspaces row ever existed.
//   - profiles: the file-viewer handlers resolve the JWT `sub` to an
//     ACTING profile with
//     `SELECT id FROM profiles WHERE deleted_at IS NULL AND (id = $1 OR owner_id = $1)`
//     (internal/fileviewer/repo.go ResolveActorProfile). A fresh database
//     has a users row (EnsureDevJWTUser) but no profiles row, so every
//     /api/v1/files and /api/v1/viewers/dispatch call 404'd
//     PROFILE_NOT_FOUND.
//   - profile_route: the documented set-active-profile walkthrough reads
//     the mapping back with GET .../profiles/active; seeding the same
//     mapping the walkthrough creates keeps the dev JWT from starting in
//     an unmapped state.
//
// The three inserts share one transaction so a partially provisioned dev
// database cannot exist: either a retry sees the previous state and
// completes it, or nothing was written at all.
//
// EnsureDevJWTUser must have run first — profiles.owner_id references
// users(id). Callers should fail startup on error: a dev server that
// cannot provision its own dev workspace is broken.
func EnsureDevWorkspaceProfile(ctx context.Context, pool *pgxpool.Pool) (inserted bool, err error) {
	if pool == nil {
		return false, fmt.Errorf("db: EnsureDevWorkspaceProfile: nil pool")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("db: provision dev workspace/profile: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. Workspace. slug is UNIQUE — ON CONFLICT (id) DO NOTHING keeps a
	// pre-existing row (including one with a different slug) untouched.
	wsTag, err := tx.Exec(ctx, `
        INSERT INTO workspaces (id, name, slug, description)
        VALUES ($1, 'Dev Workspace', $2, 'Dev-mode workspace (GAP-071)')
        ON CONFLICT (id) DO NOTHING`,
		DevWorkspaceID, devWorkspaceSlug)
	if err != nil {
		return false, fmt.Errorf("db: provision dev workspace: %w", err)
	}

	// 2. Profile owned by the dev JWT user. uq_profiles_owner_name is the
	// conflict target; name/display_name stay inside the CHECK limits
	// (64/200 chars) and config_json is a valid jsonb literal.
	profileTag, err := tx.Exec(ctx, `
        INSERT INTO profiles (owner_id, profile_type, name, display_name, description, config_json)
        VALUES ($1, 'hermes-profile', $2, 'Dev Hermes', 'Dev-mode profile (GAP-071)', '{}')
        ON CONFLICT (owner_id, name) DO NOTHING`,
		DevJWTUserID, DevProfileName)
	if err != nil {
		return false, fmt.Errorf("db: provision dev profile: %w", err)
	}

	// 3. Active profile mapping for the dev workspace.
	routeTag, err := tx.Exec(ctx, `
        INSERT INTO profile_route (workspace_id, profile_name, display_name, is_active)
        VALUES ($1, $2, 'Dev Hermes', true)
        ON CONFLICT (workspace_id, profile_name) DO NOTHING`,
		DevWorkspaceID, DevProfileName)
	if err != nil {
		return false, fmt.Errorf("db: provision dev profile route: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("db: provision dev workspace/profile: commit: %w", err)
	}
	return wsTag.RowsAffected() > 0 || profileTag.RowsAffected() > 0 || routeTag.RowsAffected() > 0, nil
}
