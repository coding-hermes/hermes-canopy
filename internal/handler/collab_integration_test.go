//go:build integration

// Package handler provides HTTP handlers for Canopy REST endpoints.
// This file contains SPEC-FTR-01 Phase P1 integration tests: workspace
// CRUD, membership, and invitations against a real PostgreSQL instance.
package handler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/coding-hermes/hermes-canopy/internal/collaboration"
	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/service"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

// collabTestServer is an httptest.Server with the real auth middleware and
// the collaboration handler mounted at /api/v1/collab.
type collabTestServer struct {
	Server *httptest.Server
	Pool   *pgxpool.Pool
}

// newCollabTestServer builds a chi router with auth + collab routes.
func newCollabTestServer(t *testing.T, pool *pgxpool.Pool) *collabTestServer {
	t.Helper()

	// Build the collaboration service on the real repo.
	collabSvc := service.NewCollaborationService(db.NewPGWorkspaceRepo(pool))

	r := chi.NewRouter()
	authMW := AuthMiddleware("canopy-dev-secret")

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(authMW)
		r.Mount("/collab", NewCollabHandler(collabSvc).Routes())
	})

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return &collabTestServer{Server: srv, Pool: pool}
}

// collabUser inserts a user row with the exact UUID used as the JWT sub.
// NOTE: this uses a raw INSERT (not userRepo.Create) because Create
// ignores the ID field and lets the DB assign a random uuidv7 — the row
// would then never match the JWT sub, breaking every FK (the same reason
// TestINT02_ConcurrentEdits currently 503s at HEAD).
func collabUser(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID, displayName string) {
	t.Helper()
	ctx := context.Background()
	email := fmt.Sprintf("%s@canopy.dev", displayName)
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, hermes_user_id, email, display_name)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (id) DO NOTHING`,
		userID, userID.String(), email, displayName); err != nil {
		t.Fatalf("collabUser(%s): %v", userID, err)
	}
}

// sha256Sum returns the hex SHA-256 digest of s (matches the service's
// token hashing for direct invitation inserts).
func sha256Sum(s string) string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", sum[:])
}

// collabRequest builds an authenticated request against the collab server.
func collabRequest(t *testing.T, srv *collabTestServer, method, path string, userID uuid.UUID, body any) *http.Request {
	t.Helper()
	return multiUserRequest(t, srv.Server.URL, method, path, userID, body)
}

// doCollab performs a request and returns the response.
func doCollab(t *testing.T, srv *collabTestServer, method, path string, userID uuid.UUID, body any) *http.Response {
	t.Helper()
	req := collabRequest(t, srv, method, path, userID, body)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// decodeCollab decodes a JSON response body into v.
func decodeCollab(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

// createCollabWorkspace creates a workspace via HTTP and returns its ID.
func createCollabWorkspace(t *testing.T, srv *collabTestServer, userID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	resp := doCollab(t, srv, http.MethodPost, "/api/v1/collab/", userID, map[string]any{"name": name})
	if resp.StatusCode != http.StatusCreated {
		body := readBody(t, resp)
		t.Fatalf("POST /collab: status=%d, body=%s", resp.StatusCode, body)
	}
	var out struct {
		WorkspaceID uuid.UUID              `json:"workspace_id"`
		Name        string                 `json:"name"`
		Role        string                 `json:"role"`
		Members     []collaboration.Member `json:"members"`
	}
	decodeCollab(t, resp, &out)
	if out.WorkspaceID == uuid.Nil {
		t.Fatal("create workspace: nil workspace_id")
	}
	if out.Role != "admin" {
		t.Errorf("create workspace: role=%q, want admin", out.Role)
	}
	if len(out.Members) != 1 || out.Members[0].UserID != userID || out.Members[0].Role != collaboration.RoleAdmin {
		t.Errorf("create workspace: members=%+v, want single admin member", out.Members)
	}
	return out.WorkspaceID
}

// inviteCollab generates an invite token via HTTP and returns it.
func inviteCollab(t *testing.T, srv *collabTestServer, workspaceID, userID uuid.UUID) string {
	t.Helper()
	resp := doCollab(t, srv, http.MethodPost, "/api/v1/collab/"+workspaceID.String()+"/invite", userID, nil)
	if resp.StatusCode != http.StatusCreated {
		body := readBody(t, resp)
		t.Fatalf("POST invite: status=%d, body=%s", resp.StatusCode, body)
	}
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	decodeCollab(t, resp, &out)
	if out.Token == "" {
		t.Fatal("invite: empty token")
	}
	if !out.ExpiresAt.After(time.Now()) {
		t.Errorf("invite: expires_at %v not in the future", out.ExpiresAt)
	}
	return out.Token
}

// readBody reads and returns a response body as a string.
func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	buf := make([]byte, 0, 512)
	tmp := make([]byte, 512)
	for {
		n, err := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return string(buf)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestCollabWorkspaceLifecycle(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	alice := uuid.MustParse("d0000000-0000-0000-0000-000000000001")
	bob := uuid.MustParse("d0000000-0000-0000-0000-000000000002")
	collabUser(t, pool, alice, "Alice")
	collabUser(t, pool, bob, "Bob")

	srv := newCollabTestServer(t, pool)

	// 1. Create workspace → 201, creator is admin member, slug generated.
	wsID := createCollabWorkspace(t, srv, alice, "My Project Tree")

	// 2. List workspaces → contains created; Get → 200 with members.
	resp := doCollab(t, srv, http.MethodGet, "/api/v1/collab/", alice, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /collab: status=%d", resp.StatusCode)
	}
	var list struct {
		Workspaces []*collaboration.Workspace `json:"workspaces"`
	}
	decodeCollab(t, resp, &list)
	found := false
	for _, ws := range list.Workspaces {
		if ws.ID == wsID {
			found = true
			if ws.Name != "My Project Tree" {
				t.Errorf("list: name=%q, want My Project Tree", ws.Name)
			}
		}
	}
	if !found {
		t.Fatalf("list: workspace %s not in %+v", wsID, list.Workspaces)
	}

	resp = doCollab(t, srv, http.MethodGet, "/api/v1/collab/"+wsID.String(), alice, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET workspace: status=%d", resp.StatusCode)
	}
	var ws collaboration.Workspace
	decodeCollab(t, resp, &ws)
	if ws.ID != wsID || len(ws.Members) != 1 || ws.Members[0].Role != collaboration.RoleAdmin {
		t.Errorf("GET workspace: %+v", ws)
	}

	// Non-member cannot read the workspace → 403.
	resp = doCollab(t, srv, http.MethodGet, "/api/v1/collab/"+wsID.String(), bob, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("GET workspace as non-member: status=%d, want 403", resp.StatusCode)
	}
}

func TestCollabInviteAndJoin(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	alice := uuid.MustParse("d0000000-0000-0000-0000-000000000011")
	bob := uuid.MustParse("d0000000-0000-0000-0000-000000000012")
	collabUser(t, pool, alice, "Alice")
	collabUser(t, pool, bob, "Bob")

	srv := newCollabTestServer(t, pool)
	wsID := createCollabWorkspace(t, srv, alice, "Invite Tree")

	// 3. Second user joins via invite token → 200; then can GET.
	token := inviteCollab(t, srv, wsID, alice)
	resp := doCollab(t, srv, http.MethodPost, "/api/v1/collab/"+wsID.String()+"/join?token="+token, bob, nil)
	if resp.StatusCode != http.StatusOK {
		body := readBody(t, resp)
		t.Fatalf("join: status=%d, body=%s", resp.StatusCode, body)
	}

	resp = doCollab(t, srv, http.MethodGet, "/api/v1/collab/"+wsID.String(), bob, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET workspace after join: status=%d", resp.StatusCode)
	}
	var ws collaboration.Workspace
	decodeCollab(t, resp, &ws)
	if len(ws.Members) != 2 {
		t.Errorf("members after join = %d, want 2", len(ws.Members))
	}

	// 4. Join with bad token → 400.
	resp = doCollab(t, srv, http.MethodPost, "/api/v1/collab/"+wsID.String()+"/join?token=not-a-real-token", bob, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("join bad token: status=%d, want 400", resp.StatusCode)
	}

	// Used token → 400 (single-use).
	resp = doCollab(t, srv, http.MethodPost, "/api/v1/collab/"+wsID.String()+"/join?token="+token, bob, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("join used token: status=%d, want 400", resp.StatusCode)
	}

	// Expired token → 400. Insert an invitation directly with a past
	// expiry and join with its raw token (hash round-trip via the repo).
	ctx := context.Background()
	rawToken := "expired-token-abc123"
	hash := fmt.Sprintf("%x", sha256Sum(rawToken))
	if _, err := pool.Exec(ctx, `INSERT INTO invitations (id, workspace_id, created_by, token_hash, expires_at)
		VALUES ($1, $2, $3, $4, now() - interval '1 hour')`,
		uuid.New(), wsID, alice, hash); err != nil {
		t.Fatalf("insert expired invitation: %v", err)
	}
	resp = doCollab(t, srv, http.MethodPost, "/api/v1/collab/"+wsID.String()+"/join?token="+rawToken, bob, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("join expired token: status=%d, want 400", resp.StatusCode)
	}
}

func TestCollabAdminPermissions(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	alice := uuid.MustParse("d0000000-0000-0000-0000-000000000021")
	bob := uuid.MustParse("d0000000-0000-0000-0000-000000000022")
	carol := uuid.MustParse("d0000000-0000-0000-0000-000000000023")
	collabUser(t, pool, alice, "Alice")
	collabUser(t, pool, bob, "Bob")
	collabUser(t, pool, carol, "Carol")

	srv := newCollabTestServer(t, pool)
	wsID := createCollabWorkspace(t, srv, alice, "Admin Tree")

	// Bob joins via invite.
	token := inviteCollab(t, srv, wsID, alice)
	resp := doCollab(t, srv, http.MethodPost, "/api/v1/collab/"+wsID.String()+"/join?token="+token, bob, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bob join: status=%d", resp.StatusCode)
	}

	// 5. Non-admin PATCH workspace → 403; admin PATCH → 200.
	resp = doCollab(t, srv, http.MethodPatch, "/api/v1/collab/"+wsID.String(), bob, map[string]any{"name": "Hijacked"})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("non-admin PATCH: status=%d, want 403", resp.StatusCode)
	}
	resp = doCollab(t, srv, http.MethodPatch, "/api/v1/collab/"+wsID.String(), alice, map[string]any{"name": "Renamed Tree"})
	if resp.StatusCode != http.StatusOK {
		body := readBody(t, resp)
		t.Fatalf("admin PATCH: status=%d, body=%s", resp.StatusCode, body)
	}
	var updated collaboration.Workspace
	decodeCollab(t, resp, &updated)
	if updated.Name != "Renamed Tree" {
		t.Errorf("PATCH: name=%q, want Renamed Tree", updated.Name)
	}

	// 6. Admin changes member role → 200; non-admin tries → 403.
	resp = doCollab(t, srv, http.MethodPatch, "/api/v1/collab/"+wsID.String()+"/members/"+bob.String(), alice, map[string]any{"role": 2})
	if resp.StatusCode != http.StatusOK {
		body := readBody(t, resp)
		t.Fatalf("admin role change: status=%d, body=%s", resp.StatusCode, body)
	}
	resp = doCollab(t, srv, http.MethodPatch, "/api/v1/collab/"+wsID.String()+"/members/"+carol.String(), bob, map[string]any{"role": 2})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("non-admin role change: status=%d, want 403", resp.StatusCode)
	}

	// Owner cannot be demoted → 403.
	resp = doCollab(t, srv, http.MethodPatch, "/api/v1/collab/"+wsID.String()+"/members/"+alice.String(), alice, map[string]any{"role": 0})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("demote owner: status=%d, want 403", resp.StatusCode)
	}

	// 9. Invite by non-admin → 403. Carol joins as editor (default role),
	// then attempts to invite.
	tokenCarol := inviteCollab(t, srv, wsID, alice)
	resp = doCollab(t, srv, http.MethodPost, "/api/v1/collab/"+wsID.String()+"/join?token="+tokenCarol, carol, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("carol join: status=%d", resp.StatusCode)
	}
	resp = doCollab(t, srv, http.MethodPost, "/api/v1/collab/"+wsID.String()+"/invite", carol, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("non-admin invite: status=%d, want 403", resp.StatusCode)
	}
}

func TestCollabMemberRemovalAndLeave(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	alice := uuid.MustParse("d0000000-0000-0000-0000-000000000031")
	bob := uuid.MustParse("d0000000-0000-0000-0000-000000000032")
	collabUser(t, pool, alice, "Alice")
	collabUser(t, pool, bob, "Bob")

	srv := newCollabTestServer(t, pool)
	wsID := createCollabWorkspace(t, srv, alice, "Removal Tree")

	token := inviteCollab(t, srv, wsID, alice)
	resp := doCollab(t, srv, http.MethodPost, "/api/v1/collab/"+wsID.String()+"/join?token="+token, bob, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bob join: status=%d", resp.StatusCode)
	}

	// 7. Admin removes member → 204; removed member GET → 403.
	resp = doCollab(t, srv, http.MethodDelete, "/api/v1/collab/"+wsID.String()+"/members/"+bob.String(), alice, nil)
	if resp.StatusCode != http.StatusNoContent {
		body := readBody(t, resp)
		t.Fatalf("remove member: status=%d, body=%s", resp.StatusCode, body)
	}
	resp = doCollab(t, srv, http.MethodGet, "/api/v1/collab/"+wsID.String(), bob, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("removed member GET: status=%d, want 403", resp.StatusCode)
	}

	// Owner cannot be removed → 403.
	resp = doCollab(t, srv, http.MethodDelete, "/api/v1/collab/"+wsID.String()+"/members/"+alice.String(), alice, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("remove owner: status=%d, want 403", resp.StatusCode)
	}

	// 8. Owner leave → 403; member leave → 204.
	resp = doCollab(t, srv, http.MethodPost, "/api/v1/collab/"+wsID.String()+"/leave", alice, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("owner leave: status=%d, want 403", resp.StatusCode)
	}

	// Re-join Bob so he can leave.
	token2 := inviteCollab(t, srv, wsID, alice)
	resp = doCollab(t, srv, http.MethodPost, "/api/v1/collab/"+wsID.String()+"/join?token="+token2, bob, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bob re-join: status=%d", resp.StatusCode)
	}
	resp = doCollab(t, srv, http.MethodPost, "/api/v1/collab/"+wsID.String()+"/leave", bob, nil)
	if resp.StatusCode != http.StatusNoContent {
		body := readBody(t, resp)
		t.Fatalf("member leave: status=%d, body=%s", resp.StatusCode, body)
	}
	resp = doCollab(t, srv, http.MethodGet, "/api/v1/collab/"+wsID.String(), bob, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("left member GET: status=%d, want 403", resp.StatusCode)
	}
}

func TestCollabDeleteWorkspace(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	alice := uuid.MustParse("d0000000-0000-0000-0000-000000000041")
	bob := uuid.MustParse("d0000000-0000-0000-0000-000000000042")
	collabUser(t, pool, alice, "Alice")
	collabUser(t, pool, bob, "Bob")

	srv := newCollabTestServer(t, pool)
	wsID := createCollabWorkspace(t, srv, alice, "Delete Tree")

	// Non-admin delete → 403.
	resp := doCollab(t, srv, http.MethodDelete, "/api/v1/collab/"+wsID.String(), bob, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("non-admin delete: status=%d, want 403", resp.StatusCode)
	}

	// Admin delete → 204; subsequent GET → 404.
	resp = doCollab(t, srv, http.MethodDelete, "/api/v1/collab/"+wsID.String(), alice, nil)
	if resp.StatusCode != http.StatusNoContent {
		body := readBody(t, resp)
		t.Fatalf("admin delete: status=%d, body=%s", resp.StatusCode, body)
	}
	resp = doCollab(t, srv, http.MethodGet, "/api/v1/collab/"+wsID.String(), alice, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET deleted workspace: status=%d, want 404", resp.StatusCode)
	}
}
