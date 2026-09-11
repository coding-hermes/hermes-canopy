// GAP-064 tests: dev JWT user provisioning (idempotency + DB row shape)
// and the CreateTree error chain carrying the underlying pg error text.
//
// DB-backed tests use the shared integration pool (one migrated DB per
// test binary, truncated per test — same isolation contract as
// NewIntegrationPool). The service package normally tests against
// repo stubs; the error-chain assertions here need a REAL PostgreSQL
// because they provoke an actual FK violation (tree_members_user_id_fkey).
package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/config"
	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

// TestGAP064_IsDevJWTSecret pins the dev-secret detection used by
// cmd/canopyd main: the config DEFAULT must be recognized as dev, and
// any custom secret (production) must not be.
func TestGAP064_IsDevJWTSecret(t *testing.T) {
	if got := config.Default().JWTSecret; got != "dev-secret-change-me" {
		t.Fatalf("config.Default().JWTSecret = %q, want the documented dev default", got)
	}
	cases := []struct {
		secret string
		want   bool
	}{
		{"dev-secret-change-me", true},
		{config.Default().JWTSecret, true},
		{"production-secret", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := db.IsDevJWTSecret(tc.secret); got != tc.want {
			t.Errorf("IsDevJWTSecret(%q) = %v, want %v", tc.secret, got, tc.want)
		}
	}
}

// TestGAP064_DevJWTUserProvisioning runs the startup provisioning twice
// against a fresh (truncated) database and asserts:
//  1. the first call inserts the row (inserted=true),
//  2. the row matches the exact shape of scripts/seed-demo-data.sql
//     (the dev sub UUID, hermes_user_id, email, display_name, active),
//  3. the second call is a no-op (inserted=false, row unchanged).
func TestGAP064_DevJWTUserProvisioning(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()

	inserted, err := db.EnsureDevJWTUser(ctx, pool)
	if err != nil {
		t.Fatalf("EnsureDevJWTUser (first run): %v", err)
	}
	if !inserted {
		t.Fatal("first EnsureDevJWTUser: inserted = false, want true (fresh DB)")
	}

	var (
		id, hermesID, email, displayName string
		isActive                         bool
	)
	err = pool.QueryRow(ctx,
		`SELECT id, hermes_user_id, email, display_name, is_active
         FROM users WHERE id = $1`,
		db.DevJWTUserID,
	).Scan(&id, &hermesID, &email, &displayName, &isActive)
	if err != nil {
		t.Fatalf("dev user row missing after provisioning: %v", err)
	}
	if id != db.DevJWTUserID || hermesID != db.DevJWTUserID {
		t.Errorf("dev user id/hermes_user_id = %s/%s, want both %s", id, hermesID, db.DevJWTUserID)
	}
	if email != "dev@canopy.dev" || displayName != "Dev User" || !isActive {
		t.Errorf("dev user row shape = (%s, %s, %v), want (dev@canopy.dev, Dev User, true)",
			email, displayName, isActive)
	}

	inserted, err = db.EnsureDevJWTUser(ctx, pool)
	if err != nil {
		t.Fatalf("EnsureDevJWTUser (second run): %v", err)
	}
	if inserted {
		t.Fatal("second EnsureDevJWTUser: inserted = true, want false (idempotent no-op)")
	}

	// The row must be untouched by the second run.
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM users WHERE id = $1`, db.DevJWTUserID,
	).Scan(&count); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if count != 1 {
		t.Fatalf("users rows with dev id = %d, want 1", count)
	}
}

// TestGAP064_CreateTree_MissingUser_FKErrorInChain is the 503-side
// regression: on a fresh DB without the dev user, CreateTree must still
// wrap ErrDatabaseUnavailable (the handler maps that to 503 — unchanged)
// but the error CHAIN must carry the underlying pg error text
// (tree_members FK violation) so the now-wired request logger surfaces it.
func TestGAP064_CreateTree_MissingUser_FKErrorInChain(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t) // truncated: users empty

	svc := NewTreeService(
		db.NewPGTreeRepo(pool),
		db.NewPGNodeRepo(pool),
		db.NewPGEdgeRepo(pool),
		pool,
	)

	_, err := svc.CreateTree(context.Background(), CreateTreeParams{
		OwnerID:       uuid.New(), // NO users row for this owner
		Title:         "GAP-064 missing user",
		RootContent:   "root",
		ContentFormat: FormatMarkdown,
		NodeType:      NodeTypeMessage,
	})
	if err == nil {
		t.Fatal("CreateTree with missing owner user: err = nil, want FK failure")
	}
	if !errors.Is(err, ErrDatabaseUnavailable) {
		t.Fatalf("CreateTree error = %v, want wrapped ErrDatabaseUnavailable", err)
	}
	// The service wraps with %w: step: %v — the pg text must be reachable
	// via err.Error() because that is all a handler log line ever shows.
	msg := err.Error()
	if !strings.Contains(msg, "violates foreign key constraint") {
		t.Fatalf("error chain missing pg FK text: %q", msg)
	}
	if !strings.Contains(msg, "tree_members") {
		t.Fatalf("error chain missing tree_members table name: %q", msg)
	}
	if !strings.Contains(msg, "insert tree_members") {
		t.Fatalf("error chain missing failing step label: %q", msg)
	}
}

// TestGAP064_CreateTree_AfterProvision_Succeeds is the fix-side proof at
// the service layer: after EnsureDevJWTUser, the documented dev flow
// (owner = the dev sub UUID) creates a tree — no manual SQL required.
func TestGAP064_CreateTree_AfterProvision_Succeeds(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()

	if _, err := db.EnsureDevJWTUser(ctx, pool); err != nil {
		t.Fatalf("EnsureDevJWTUser: %v", err)
	}

	svc := NewTreeService(
		db.NewPGTreeRepo(pool),
		db.NewPGNodeRepo(pool),
		db.NewPGEdgeRepo(pool),
		pool,
	)
	devSub, err := uuid.Parse(db.DevJWTUserID)
	if err != nil {
		t.Fatalf("parse dev sub: %v", err)
	}
	tree, err := svc.CreateTree(ctx, CreateTreeParams{
		OwnerID:       devSub,
		Title:         "GAP-064 after provisioning",
		RootContent:   "root",
		ContentFormat: FormatMarkdown,
		NodeType:      NodeTypeMessage,
	})
	if err != nil {
		t.Fatalf("CreateTree as provisioned dev user: %v", err)
	}
	if tree.ID == uuid.Nil || tree.RootNodeID == uuid.Nil {
		t.Fatalf("CreateTree returned unset ids: %+v", tree)
	}
}
