// Actor → profile resolution tests (rework defect: the API treated the JWT
// `sub` — a users.id — as a profiles.id). Covers the deterministic rule at
// the repo level, the service mapping (no rows → ErrProfileNotFound), and
// end-to-end through the HTTP handlers with the REAL dev-user shape
// (actor id ≠ profile id, actor owns the profile).
package fileviewer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

// seedPair inserts a users row + profiles row with explicit ids and
// created_at; returns the profile id. The explicit created_at makes
// "newest owned profile" deterministic.
func seedPair(t *testing.T, pool *pgxpool.Pool, userID, profileID uuid.UUID, createdAt time.Time) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, hermes_user_id, display_name) VALUES ($1, $2, 'fv-actor-user') ON CONFLICT (id) DO NOTHING`,
		userID, "fv-actor-"+userID.String()); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO profiles (id, owner_id, profile_type, name, display_name, created_at) VALUES ($1, $2, 'hermes-profile', $3, 'FV Actor', $4)`,
		profileID, userID, "fv-actor-"+profileID.String(), createdAt); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	return profileID
}

// TestRepo_ResolveActorProfile covers the deterministic rule on the pgx
// repo: exact-id match wins; else newest owned live profile; else no rows.
func TestRepo_ResolveActorProfile(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	repo := NewPGFileMetadataRepo(pool)
	ctx := context.Background()

	// (a) actor id EQUALS a profile id → that profile.
	userA := uuid.New()
	profileA := uuid.MustParse("00000000-0000-0000-0000-0000000000a1")
	// The user row must exist for the FK even though the profile id
	// equals the actor id (the actor here is the PROFILE id, so this
	// user is just the profile's owner).
	seedPair(t, pool, userA, profileA, time.Now().Add(-2*time.Hour))

	got, err := repo.ResolveActorProfile(ctx, profileA)
	if err != nil {
		t.Fatalf("exact-id resolve: %v", err)
	}
	if got != profileA {
		t.Fatalf("exact-id resolve = %s, want %s", got, profileA)
	}

	// (b) actor id is a USER id owning a profile under a different id
	// (the dev-user shape that was the bug). Owner with TWO profiles:
	// newest must win.
	owner := uuid.MustParse("00000000-0000-0000-0000-0000000000a2")
	older := uuid.MustParse("00000000-0000-0000-0000-0000000000a3")
	newer := uuid.MustParse("00000000-0000-0000-0000-0000000000a4")
	seedPair(t, pool, owner, older, time.Now().Add(-2*time.Hour))
	seedPair(t, pool, owner, newer, time.Now().Add(-1*time.Hour))

	got, err = repo.ResolveActorProfile(ctx, owner)
	if err != nil {
		t.Fatalf("owner resolve: %v", err)
	}
	if got != newer {
		t.Fatalf("owner resolve = %s, want newest owned %s", got, newer)
	}

	// Exact-id match beats a newer owned profile (rule ordering).
	got, err = repo.ResolveActorProfile(ctx, older)
	if err != nil {
		t.Fatalf("exact-id-beats-owner resolve: %v", err)
	}
	if got != older {
		t.Fatalf("exact-id-beats-owner = %s, want %s", got, older)
	}

	// Soft-deleted owned profiles do not resolve (deleted_at IS NULL).
	if _, err := pool.Exec(ctx, `UPDATE profiles SET deleted_at = now() WHERE id = $1`, newer); err != nil {
		t.Fatalf("soft-delete profile: %v", err)
	}
	got, err = repo.ResolveActorProfile(ctx, owner)
	if err != nil {
		t.Fatalf("post-soft-delete resolve: %v", err)
	}
	if got != older {
		t.Fatalf("soft-deleted profile resolved: got %s, want remaining live %s", got, older)
	}

	// (c) actor with no profile at all → pgx.ErrNoRows.
	stranger := uuid.New()
	_, err = repo.ResolveActorProfile(ctx, stranger)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("profile-less actor: want pgx.ErrNoRows, got %v", err)
	}
}

// TestService_ResolveActorProfile checks the service maps the no-row case
// to ErrProfileNotFound (the 404 the handlers emit), and that the pgx repo
// satisfies the optional resolver interface (a rename would silently
// degrade to pass-through).
func TestService_ResolveActorProfile(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	repo := NewPGFileMetadataRepo(pool)
	if _, ok := interface{}(repo).(resolveProfileID); !ok {
		t.Fatal("PGFileMetadataRepo must satisfy resolveProfileID")
	}

	svc := NewService(repo, NewPGViewerRegistryRepo(pool),
		NewPGFileAccessLogRepo(pool), NewFileStore(t.TempDir())).(*serviceImpl)

	stranger := uuid.New()
	if _, err := svc.ResolveActorProfile(context.Background(), stranger); !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("profile-less actor: want ErrProfileNotFound, got %v", err)
	}
}

// TestHandler_ActorOwnedProfileUpload is the end-to-end regression for the
// rework defect: the JWT sub is a USER id whose profile lives under a
// DIFFERENT id (the dev-user shape). Upload must return 201 (not the old
// PROFILE_NOT_FOUND), resolve-by-hash WITHOUT a profile_id must return 200,
// and list/dispatch must work through the resolved profile.
func TestHandler_ActorOwnedProfileUpload(t *testing.T) {
	srv, _ := newHandlerTestServer(t)

	// Fresh actor owning a profile under a different id.
	pool := testutil.NewSharedIntegrationPool(t)
	userID := uuid.MustParse("00000000-0000-0000-0000-00000000e101")
	profileID := uuid.MustParse("00000000-0000-0000-0000-00000000e102")
	seedPair(t, pool, userID, profileID, time.Now())

	// The production actor lookup returns the JWT sub (the USER id).
	prev := actorIDFn
	SetActorLookup(func(_ *http.Request) uuid.UUID { return userID })
	defer func() { SetActorLookup(prev) }()

	content := "owned-profile upload body"
	status, out := uploadMultipart(t, srv, "owned.md", content, "")
	if status != 201 {
		t.Fatalf("upload with owned-profile actor: status = %d (want 201)", status)
	}
	if out.FileMetadata.ProfileID != profileID {
		t.Fatalf("file landed under profile %s, want owned %s", out.FileMetadata.ProfileID, profileID)
	}

	// Dedup: same bytes again → same file id.
	status2, out2 := uploadMultipart(t, srv, "owned-again.md", content, "")
	if status2 != 201 || out2.FileMetadata.ID != out.FileMetadata.ID {
		t.Fatalf("dedup upload: status=%d id=%s (want 201 / %s)", status2, out2.FileMetadata.ID, out.FileMetadata.ID)
	}

	// Resolve-by-hash with profile_id OMITTED (zero) → acting profile.
	body, _ := json.Marshal(map[string]any{
		"hash_ref": map[string]any{"sha256": out.FileMetadata.SHA256},
	})
	resp, raw := do(t, srv, "POST", "/api/v1/files/resolve", body, map[string]string{"Content-Type": "application/json"})
	if resp.StatusCode != 200 {
		t.Fatalf("resolve by hash (no profile_id) = %d (%s), want 200", resp.StatusCode, raw)
	}
	var res ResolveFileOutput
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("decode resolve: %v", err)
	}
	if res.FileMetadata.ID != out.FileMetadata.ID {
		t.Fatalf("resolve mismatch: %s vs %s", res.FileMetadata.ID, out.FileMetadata.ID)
	}

	// List sees the file via the resolved profile.
	resp, raw = do(t, srv, "GET", "/api/v1/files", nil, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list = %d (%s)", resp.StatusCode, raw)
	}
	var page struct {
		Files []FileMetadataSlim `json:"files"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(page.Files) != 1 || page.Files[0].ID != out.FileMetadata.ID {
		t.Fatalf("list should show the actor's 1 file, got %+v", page)
	}

	// Dispatch for the file resolves without any profile in the body.
	body, _ = json.Marshal(map[string]any{"file_id": out.FileMetadata.ID.String()})
	resp, raw = do(t, srv, "POST", "/api/v1/viewers/dispatch", body, map[string]string{"Content-Type": "application/json"})
	if resp.StatusCode != 200 {
		t.Fatalf("dispatch = %d (%s), want 200", resp.StatusCode, raw)
	}

	// An actor with NO profile anywhere still gets PROFILE_NOT_FOUND.
	stranger := uuid.MustParse("00000000-0000-0000-0000-00000000e999")
	SetActorLookup(func(_ *http.Request) uuid.UUID { return stranger })
	resp, raw = do(t, srv, "GET", "/api/v1/files", nil, nil)
	if resp.StatusCode != 404 {
		t.Fatalf("profile-less actor list = %d, want 404", resp.StatusCode)
	}
	var errBody struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &errBody); err != nil || errBody.Error.Code != "PROFILE_NOT_FOUND" {
		t.Fatalf("profile-less actor body = %s (err %v), want PROFILE_NOT_FOUND", raw, err)
	}
}
