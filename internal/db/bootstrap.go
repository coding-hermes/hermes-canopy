// Package db provides dev-mode bootstrap: provision the well-known dev JWT
// user after migration.
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
