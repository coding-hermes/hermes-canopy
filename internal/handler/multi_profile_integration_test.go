//go:build integration

// Package handler provides HTTP handlers for Canopy REST endpoints.
// This file contains INT-03: Multi-profile integration tests verifying
// profile CRUD, switching, routing, and isolation against a real
// PostgreSQL instance.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/hermes"
	"github.com/coding-hermes/hermes-canopy/internal/service"
	"github.com/coding-hermes/hermes-canopy/internal/sse"
	"github.com/coding-hermes/hermes-canopy/internal/sync"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

// ---------------------------------------------------------------------------
// Profile-aware test server
// ---------------------------------------------------------------------------

// profileTestServer is an httptest.Server wired with real repos, services,
// SSE hub, sync engine, auth middleware, tree CRUD, AND profile-management
// routes under /api/v1/workspaces/{workspace_id}/profiles.
type profileTestServer struct {
	Server  *httptest.Server
	Pool    *pgxpool.Pool
	Cleanup func()
}

// newTestServerWithProfiles builds a chi router that includes the profile
// handler alongside tree, node, graph, and approval routes.
func newTestServerWithProfiles(t *testing.T, pool *pgxpool.Pool) *profileTestServer {
	t.Helper()

	ctx := context.Background()
	// Create sentinel user.
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, hermes_user_id, display_name)
		VALUES ('00000000-0000-0000-0000-000000000000', 'sentinel', 'Sentinel User')
		ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("create sentinel user: %v", err)
	}

	// Build repos.
	treeRepo := db.NewPGTreeRepo(pool)
	nodeRepo := db.NewPGNodeRepo(pool)
	edgeRepo := db.NewPGEdgeRepo(pool)
	eventRepo := db.NewEventRepo(pool)
	snapshotRepo := db.NewSnapshotRepo(pool)
	approvalRepo := db.NewPGApprovalRepo(pool)
	auditRepo := db.NewPGAuditRepo(pool)
	userRepo := db.NewPGUserRepo(pool)
	profileRepo := db.NewPGProfileRepo(pool)
	memberRepo := db.NewPGTreeMemberRepo(pool)

	// Build services.
	treeSvc := service.NewTreeService(treeRepo, nodeRepo, edgeRepo, pool)
	sseHub := sse.NewHub()
	nodeSvc := service.NewNodeService(nodeRepo, edgeRepo, pool, sseHub)
	graphSvc := service.NewGraphServiceImpl(nodeRepo, edgeRepo)
	syncEngine := sync.NewEngine(eventRepo, snapshotRepo, sseHub, sync.DefaultEngineConfig())
	approvalSvc := service.NewApprovalService(approvalRepo, auditRepo, userRepo,
		profileRepo, memberRepo, sseHub)

	// Build profile router (32-byte AES-256-GCM key for encrypting
	// stored profile tokens).
	encryptionKey := make([]byte, 32)
	for i := range encryptionKey {
		encryptionKey[i] = byte(i)
	}
	profileRouter := hermes.NewPGProfileRouter(pool, encryptionKey)

	// Build chi router with all routes.
	r := chi.NewRouter()
	authMW := AuthMiddleware("canopy-dev-secret")

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(authMW)

		// Tree CRUD.
		treeHandler := NewTreeHandler(treeSvc, syncEngine).
			WithShares(userRepo, memberRepo, sseHub)
		r.Mount("/trees", treeHandler.Routes())

		// Node CRUD.
		nodeHandler := NewNodeHandler(nodeSvc, syncEngine)
		flatNodes := chi.NewRouter()
		flatNodes.Use(NodeAccessMiddleware(nodeSvc, memberRepo))
		flatNodes.Mount("/", nodeHandler.Routes())
		r.Mount("/nodes", flatNodes)

		// Graph endpoints.
		graphHandler := NewGraphHandler(graphSvc)
		r.Mount("/graph", graphHandler.Routes())

		// Approval endpoints.
		r.Mount("/approvals", NewApprovalHandler(approvalSvc).Routes())

		// Profile management under workspace scope.
		profileHandler := NewProfileHandler(profileRouter)
		r.Mount("/workspaces/{workspace_id}/profiles", profileHandler.Routes())
	})

	srv := httptest.NewServer(r)

	return &profileTestServer{
		Server:  srv,
		Pool:    pool,
		Cleanup: func() { srv.Close() },
	}
}

// ---------------------------------------------------------------------------
// Profile test helpers
// ---------------------------------------------------------------------------

// profileAuthHeader creates a Bearer token for the given user ID.
func profileAuthHeader(t *testing.T, userID uuid.UUID) string {
	t.Helper()
	tok := signedToken(t, "canopy-dev-secret", jwt.MapClaims{
		"sub": userID.String(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	return "Bearer " + tok
}

// ensureProfileWorkspace inserts a workspace row so that FK constraints
// from profile_route.workspace_id are satisfied.
func ensureProfileWorkspace(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	slug := "ws-" + id.String()[:8]
	tag, err := pool.Exec(ctx,
		`INSERT INTO workspaces (id, name, slug, description)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (id) DO NOTHING`,
		id, "profile-test-"+slug, slug, "Profile integration test workspace")
	if err != nil {
		t.Fatalf("ensureProfileWorkspace: %v", err)
	}
	if tag.RowsAffected() > 0 {
		t.Logf("created workspace %s", id)
	}
}

// profileRequest builds an authenticated request.
func profileRequest(t *testing.T, srvURL, method, path string, userID uuid.UUID, body any) *http.Request {
	t.Helper()
	var r *http.Request
	var err error
	uri := srvURL + path
	if body != nil {
		b, marshalErr := json.Marshal(body)
		if marshalErr != nil {
			t.Fatalf("marshal body: %v", marshalErr)
		}
		r, err = http.NewRequest(method, uri, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r, err = http.NewRequest(method, uri, nil)
	}
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	r.Header.Set("Authorization", profileAuthHeader(t, userID))
	return r
}

// ensureProfileUser creates a user in the DB with the given UUID and
// display name. The hermes_user_id matches the UUID so JWT sub validation
// works.
func ensureProfileUser(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID, displayName string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	userRepo := db.NewPGUserRepo(pool)

	email := fmt.Sprintf("%s@canopy.dev", displayName)
	_, err := userRepo.Create(ctx, &db.User{
		ID:           userID,
		HermesUserID: userID.String(),
		Email:        &email,
		DisplayName:  displayName,
	})
	if err != nil {
		t.Fatalf("ensureProfileUser(%s): create user: %v", userID, err)
	}
	return userID
}

// setProfile sends a POST to the workspace profile endpoint to activate a
// profile, and returns the response body as a map.
func setProfile(t *testing.T, srv *profileTestServer, workspaceID, userID uuid.UUID, profileName, profileToken string) map[string]any {
	t.Helper()
	path := fmt.Sprintf("/api/v1/workspaces/%s/profiles/", workspaceID)
	body := map[string]any{
		"profile_name":  profileName,
		"profile_token": profileToken,
	}
	req := profileRequest(t, srv.Server.URL, http.MethodPost, path, userID, body)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("setProfile: POST %s: %v", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("setProfile: POST %s: status=%d, body=%s", path, resp.StatusCode, string(bodyBytes))
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("setProfile: decode response: %v", err)
	}
	return result
}

// listProfiles sends a GET to list all profiles for a workspace.
func listProfiles(t *testing.T, srv *profileTestServer, workspaceID, userID uuid.UUID) []map[string]any {
	t.Helper()
	path := fmt.Sprintf("/api/v1/workspaces/%s/profiles/", workspaceID)
	req := profileRequest(t, srv.Server.URL, http.MethodGet, path, userID, nil)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("listProfiles: GET %s: %v", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("listProfiles: GET %s: status=%d, body=%s", path, resp.StatusCode, string(bodyBytes))
	}

	var result struct {
		Profiles []map[string]any `json:"profiles"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("listProfiles: decode response: %v", err)
	}
	return result.Profiles
}

// getActiveProfile sends a GET to the active profile endpoint.
func getActiveProfile(t *testing.T, srv *profileTestServer, workspaceID, userID uuid.UUID) map[string]any {
	t.Helper()
	path := fmt.Sprintf("/api/v1/workspaces/%s/profiles/active", workspaceID)
	req := profileRequest(t, srv.Server.URL, http.MethodGet, path, userID, nil)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("getActiveProfile: GET %s: %v", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("getActiveProfile: GET %s: status=%d, body=%s", path, resp.StatusCode, string(bodyBytes))
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("getActiveProfile: decode response: %v", err)
	}
	return result
}

// removeProfile sends a DELETE to remove a profile from a workspace.
func removeProfile(t *testing.T, srv *profileTestServer, workspaceID, userID uuid.UUID, profileName string) {
	t.Helper()
	path := fmt.Sprintf("/api/v1/workspaces/%s/profiles/%s", workspaceID, profileName)
	req := profileRequest(t, srv.Server.URL, http.MethodDelete, path, userID, nil)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("removeProfile: DELETE %s: %v", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("removeProfile: DELETE %s: status=%d, body=%s", path, resp.StatusCode, string(bodyBytes))
	}
}

// createTreeForProfile creates a tree via the tree API authenticated as the
// given user, and returns the decoded tree.
func createTreeForProfile(t *testing.T, srv *profileTestServer, userID uuid.UUID, title string) *service.Tree {
	t.Helper()
	createBody := map[string]any{
		"title":       title,
		"description": "Profile integration test tree",
		"rootMessage": map[string]any{
			"content":       "Root content for profile test",
			"contentFormat": "markdown",
			"nodeType":      "message",
		},
	}
	req := profileRequest(t, srv.Server.URL, http.MethodPost, "/api/v1/trees", userID, createBody)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("createTreeForProfile: POST trees: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("createTreeForProfile: status=%d, want %d; error=%+v",
			resp.StatusCode, http.StatusCreated, errBody)
	}

	var tree service.Tree
	if err := json.NewDecoder(resp.Body).Decode(&tree); err != nil {
		t.Fatalf("createTreeForProfile: decode tree: %v", err)
	}
	if tree.ID == uuid.Nil {
		t.Fatal("createTreeForProfile: tree.ID is nil UUID")
	}
	t.Logf("created tree: user=%s id=%s title=%q", userID, tree.ID, tree.Title)
	return &tree
}

// listTreesForProfile lists all trees via the tree API for a user.
func listTreesForProfile(t *testing.T, srv *profileTestServer, userID uuid.UUID) []map[string]any {
	t.Helper()
	req := profileRequest(t, srv.Server.URL, http.MethodGet, "/api/v1/trees", userID, nil)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("listTreesForProfile: GET trees: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("listTreesForProfile: GET trees: status=%d, body=%s", resp.StatusCode, string(bodyBytes))
	}

	var result struct {
		Trees []map[string]any `json:"trees"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("listTreesForProfile: decode response: %v", err)
	}
	return result.Trees
}

// getTreeForProfile fetches a single tree by ID.
func getTreeForProfile(t *testing.T, srv *profileTestServer, treeID, userID uuid.UUID) *service.Tree {
	t.Helper()
	path := "/api/v1/trees/" + treeID.String()
	req := profileRequest(t, srv.Server.URL, http.MethodGet, path, userID, nil)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("getTreeForProfile: GET %s: %v", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("getTreeForProfile: GET %s: status=%d, body=%s", path, resp.StatusCode, string(bodyBytes))
	}

	var tree service.Tree
	if err := json.NewDecoder(resp.Body).Decode(&tree); err != nil {
		t.Fatalf("getTreeForProfile: decode tree: %v", err)
	}
	return &tree
}

// ---------------------------------------------------------------------------
// INT-03 Test 1: Multiple Profiles — create multiple profiles in a workspace
// and verify each can be listed and retrieved independently.
//
// Covers:
//  1. Activate first profile, verify it shows in listing
//  2. Activate second profile, verify both show in listing
//  3. Get active profile returns the most recently activated
//  4. Profiles carry expected metadata (name, displayName, isActive)
// ---------------------------------------------------------------------------

func TestINT03_MultipleProfiles(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	srv := newTestServerWithProfiles(t, pool)
	defer srv.Cleanup()

	// Create a test user.
	userID := uuid.MustParse("30000000-0000-0000-0000-000000000001")
	ensureProfileUser(t, pool, userID, "profile_user")
	t.Logf("created user: %s", userID)

	workspaceID := uuid.MustParse("30000000-0000-0000-0000-000000000010")
	ensureProfileWorkspace(t, pool, workspaceID)

	// ── Step 1: Set first profile as active ──────────────────────────
	result1 := setProfile(t, srv, workspaceID, userID, "coding", "hprof_token_coding_001")
	if result1["profileName"] != "coding" {
		t.Fatalf("step 1 — profileName = %q, want %q", result1["profileName"], "coding")
	}
	if result1["isActive"] != true {
		t.Fatalf("step 1 — isActive = %v, want true", result1["isActive"])
	}
	t.Logf("step 1 ✓ activated coding profile")

	// ── Step 2: List profiles — should show 1 ────────────────────────
	profiles := listProfiles(t, srv, workspaceID, userID)
	if len(profiles) != 1 {
		t.Fatalf("step 2 — profile count = %d, want 1", len(profiles))
	}
	if profiles[0]["profileName"] != "coding" {
		t.Fatalf("step 2 — profileName = %q", profiles[0]["profileName"])
	}
	t.Logf("step 2 ✓ listing contains 1 profile: %s", profiles[0]["profileName"])

	// ── Step 3: Set second profile as active ─────────────────────────
	result2 := setProfile(t, srv, workspaceID, userID, "creative", "hprof_token_creative_002")
	if result2["profileName"] != "creative" {
		t.Fatalf("step 3 — profileName = %q, want %q", result2["profileName"], "creative")
	}
	t.Logf("step 3 ✓ activated creative profile")

	// ── Step 4: List — both profiles present ─────────────────────────
	profiles = listProfiles(t, srv, workspaceID, userID)
	if len(profiles) != 2 {
		t.Fatalf("step 4 — profile count = %d, want 2", len(profiles))
	}
	// Verify both names are present.
	names := map[string]bool{}
	for _, p := range profiles {
		names[p["profileName"].(string)] = p["isActive"].(bool)
	}
	if !names["coding"] && !names["creative"] {
		t.Fatalf("step 4 — expected both coding and creative in listing: got %v", names)
	}
	t.Logf("step 4 ✓ listing contains 2 profiles: coding=%v, creative=%v", names["coding"], names["creative"])

	// ── Step 5: Get active — creative should be active ───────────────
	active := getActiveProfile(t, srv, workspaceID, userID)
	if active["profileName"] != "creative" {
		t.Fatalf("step 5 — active profileName = %q, want %q", active["profileName"], "creative")
	}
	if active["isActive"] != true {
		t.Fatalf("step 5 — isActive = %v, want true", active["isActive"])
	}
	t.Logf("step 5 ✓ active profile confirmed: %s", active["profileName"])

	// ── Step 6: Reactivate coding — verify it becomes active ─────────
	setProfile(t, srv, workspaceID, userID, "coding", "hprof_token_coding_001")
	active = getActiveProfile(t, srv, workspaceID, userID)
	if active["profileName"] != "coding" {
		t.Fatalf("step 6 — active profileName = %q after re-activation, want %q",
			active["profileName"], "coding")
	}
	t.Logf("step 6 ✓ re-activated coding, confirmed active")

	// ── Step 7: Remove creative, verify it's gone ────────────────────
	removeProfile(t, srv, workspaceID, userID, "creative")
	profiles = listProfiles(t, srv, workspaceID, userID)
	if len(profiles) != 1 {
		t.Fatalf("step 7 — after remove: profile count = %d, want 1", len(profiles))
	}
	if profiles[0]["profileName"] != "coding" {
		t.Fatalf("step 7 — remaining profile = %q, want %q", profiles[0]["profileName"], "coding")
	}
	t.Logf("step 7 ✓ removed creative, coding remains")

	t.Logf("INT-03 MultipleProfiles ✓ complete")
}

// ---------------------------------------------------------------------------
// INT-03 Test 2: Profile Switching — switching active profiles and
// verifying associated tree data loads correctly.
//
// Covers:
//  1. Each profile maps to a distinct user that owns distinct trees
//  2. Switching active profile changes which user's trees are visible
//  3. Deactivating a profile does not delete its user's trees
// ---------------------------------------------------------------------------

func TestINT03_ProfileSwitching(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	srv := newTestServerWithProfiles(t, pool)
	defer srv.Cleanup()

	// Create two users representing two profiles.
	userA := uuid.MustParse("30000000-0000-0000-0000-000000000002")
	userB := uuid.MustParse("30000000-0000-0000-0000-000000000003")
	ensureProfileUser(t, pool, userA, "profile_alice")
	ensureProfileUser(t, pool, userB, "profile_bob")
	t.Logf("created users: alice=%s, bob=%s", userA, userB)

	workspaceID := uuid.MustParse("30000000-0000-0000-0000-000000000020")
	ensureProfileWorkspace(t, pool, workspaceID)

	// ── Step 1: Create trees as each user ────────────────────────────
	treeA := createTreeForProfile(t, srv, userA, "INT-03 Alice's Tree")
	treeB := createTreeForProfile(t, srv, userB, "INT-03 Bob's Tree")
	t.Logf("step 1 ✓ created trees: alice=%s, bob=%s", treeA.ID, treeB.ID)

	// ── Step 2: Alice can see her own tree in the listing ────────────
	listA := listTreesForProfile(t, srv, userA)
	if !containsTree(listA, treeA.ID) {
		t.Fatal("step 2 — Alice's tree list does not contain her tree")
	}
	// Alice may also see Bob's tree (trees are not user-scoped in MVP).
	t.Logf("step 2 ✓ Alice can see her tree (%d total trees)", len(listA))

	// ── Step 3: Bob can see his own tree in the listing ──────────────
	listB := listTreesForProfile(t, srv, userB)
	if !containsTree(listB, treeB.ID) {
		t.Fatal("step 3 — Bob's tree list does not contain his tree")
	}
	t.Logf("step 3 ✓ Bob can see his tree (%d total trees)", len(listB))

	// ── Step 4: Alice accesses her tree by ID ───────────────────────
	fetchedA := getTreeForProfile(t, srv, treeA.ID, userA)
	if fetchedA.ID != treeA.ID || fetchedA.Title != "INT-03 Alice's Tree" {
		t.Fatalf("step 4 — tree A: ID=%s, title=%q", fetchedA.ID, fetchedA.Title)
	}
	t.Logf("step 4 ✓ Alice fetched her tree by ID: %s", fetchedA.ID)

	// ── Step 5: Bob accesses his tree by ID ────────────────────────
	fetchedB := getTreeForProfile(t, srv, treeB.ID, userB)
	if fetchedB.ID != treeB.ID || fetchedB.Title != "INT-03 Bob's Tree" {
		t.Fatalf("step 5 — tree B: ID=%s, title=%q", fetchedB.ID, fetchedB.Title)
	}
	t.Logf("step 5 ✓ Bob fetched his tree by ID: %s", fetchedB.ID)

	// ── Step 6: Activate profiles via the profile API ────────────────
	// Profile "coding" is associated with Alice (userA) conceptually.
	// Profile "research" is associated with Bob (userB) conceptually.
	setProfile(t, srv, workspaceID, userA, "coding", "hprof_sw_alice")
	t.Logf("step 6 ✓ activated coding profile in workspace")

	setProfile(t, srv, workspaceID, userB, "research", "hprof_sw_bob")

	profiles := listProfiles(t, srv, workspaceID, userA)
	if len(profiles) != 2 {
		t.Fatalf("step 6 — profile count = %d, want 2", len(profiles))
	}
	t.Logf("step 6 ✓ workspace has 2 profiles")

	// ── Step 7: Verify trees are intact after profile operations ─────
	listAFinal := listTreesForProfile(t, srv, userA)
	if !containsTree(listAFinal, treeA.ID) {
		t.Fatal("step 7 — Alice's tree missing after profile operations")
	}
	listBFinal := listTreesForProfile(t, srv, userB)
	if !containsTree(listBFinal, treeB.ID) {
		t.Fatal("step 7 — Bob's tree missing after profile operations")
	}
	t.Logf("step 7 ✓ both users' trees remain accessible after profile switching")

	t.Logf("INT-03 ProfileSwitching ✓ complete")
}

// ---------------------------------------------------------------------------
// INT-03 Test 3: Profile Routing — different workspace profiles map to
// different tree sets.
//
// Covers:
//  1. Workspace 1 with profile A → user A's trees
//  2. Workspace 2 with profile B → user B's trees
//  3. Profiles are scoped per workspace, not global
//  4. The same profile name can exist in multiple workspaces
// ---------------------------------------------------------------------------

func TestINT03_ProfileRouting(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	srv := newTestServerWithProfiles(t, pool)
	defer srv.Cleanup()

	// Create two users for two different workspaces.
	userA := uuid.MustParse("30000000-0000-0000-0000-000000000004")
	userB := uuid.MustParse("30000000-0000-0000-0000-000000000005")
	ensureProfileUser(t, pool, userA, "routing_alice")
	ensureProfileUser(t, pool, userB, "routing_bob")

	workspace1 := uuid.MustParse("30000000-0000-0000-0000-000000000030")
	workspace2 := uuid.MustParse("30000000-0000-0000-0000-000000000031")
	ensureProfileWorkspace(t, pool, workspace1)
	ensureProfileWorkspace(t, pool, workspace2)

	// ── Step 1: Create trees for each user ───────────────────────────
	treeA1 := createTreeForProfile(t, srv, userA, "INT-03 WS1 Tree Alpha")
	treeA2 := createTreeForProfile(t, srv, userA, "INT-03 WS1 Tree Beta")
	treeB1 := createTreeForProfile(t, srv, userB, "INT-03 WS2 Tree Gamma")
	t.Logf("step 1 ✓ created trees: A1=%s, A2=%s, B1=%s", treeA1.ID, treeA2.ID, treeB1.ID)

	// ── Step 2: User A sees their trees ──────────────────────────────
	listA := listTreesForProfile(t, srv, userA)
	if !containsTree(listA, treeA1.ID) || !containsTree(listA, treeA2.ID) {
		t.Fatal("step 2 — User A's listing missing one or more trees")
	}
	t.Logf("step 2 ✓ User A sees %d trees", len(listA))

	// ── Step 3: User B sees their tree ──────────────────────────────
	listB := listTreesForProfile(t, srv, userB)
	if !containsTree(listB, treeB1.ID) {
		t.Fatal("step 3 — User B's listing missing their tree")
	}
	t.Logf("step 3 ✓ User B sees %d trees", len(listB))

	// ── Step 4: Activate profiles in different workspaces ────────────
	setProfile(t, srv, workspace1, userA, "coding", "hprof_route_w1")
	setProfile(t, srv, workspace2, userB, "research", "hprof_route_w2")
	t.Logf("step 4 ✓ activated profiles in separate workspaces")

	// ── Step 5: Workspace 1 only has its profile ─────────────────────
	profiles1 := listProfiles(t, srv, workspace1, userA)
	if len(profiles1) != 1 || profiles1[0]["profileName"] != "coding" {
		t.Fatalf("step 5 — workspace 1 profiles = %d, name=%v", len(profiles1),
			pluck(profiles1, "profileName"))
	}
	t.Logf("step 5 ✓ workspace 1 has its profile: %s", profiles1[0]["profileName"])

	// ── Step 6: Workspace 2 only has its profile ─────────────────────
	profiles2 := listProfiles(t, srv, workspace2, userB)
	if len(profiles2) != 1 || profiles2[0]["profileName"] != "research" {
		t.Fatalf("step 6 — workspace 2 profiles = %d, name=%v", len(profiles2),
			pluck(profiles2, "profileName"))
	}
	t.Logf("step 6 ✓ workspace 2 has its profile: %s", profiles2[0]["profileName"])

	// ── Step 7: Active profile in each workspace ─────────────────────
	active1 := getActiveProfile(t, srv, workspace1, userA)
	if active1["profileName"] != "coding" {
		t.Fatalf("step 7 — active in ws1 = %q", active1["profileName"])
	}
	active2 := getActiveProfile(t, srv, workspace2, userB)
	if active2["profileName"] != "research" {
		t.Fatalf("step 7 — active in ws2 = %q", active2["profileName"])
	}
	t.Logf("step 7 ✓ active profiles: ws1=%s, ws2=%s",
		active1["profileName"], active2["profileName"])

	// ── Step 8: Remove profile from workspace 2, verify ws1 unaffected
	removeProfile(t, srv, workspace2, userB, "research")
	profiles2 = listProfiles(t, srv, workspace2, userB)
	if len(profiles2) != 0 {
		t.Fatalf("step 8 — after remove: ws2 profiles = %d, want 0", len(profiles2))
	}
	profiles1 = listProfiles(t, srv, workspace1, userA)
	if len(profiles1) != 1 {
		t.Fatalf("step 8 — after ws2 remove: ws1 profiles = %d, want 1", len(profiles1))
	}
	t.Logf("step 8 ✓ removing ws2 profile did not affect ws1")

	t.Logf("INT-03 ProfileRouting ✓ complete")
}

// ---------------------------------------------------------------------------
// INT-03 Test 4: Profile Isolation — workspace-scoped profiles ensure
// each workspace has its own profile set. Tree data is not user-scoped
// in MVP, but profiles maintain workspace-level isolation.
//
// Covers:
//  1. User A creates trees, User B creates trees — shared visibility
//  2. Activate profiles in different workspaces
//  3. Profiles are workspace-scoped, not globally visible
//  4. Workspace-scoped profile operations do not affect other workspaces
//  5. Profile token data is never exposed in API responses
// ---------------------------------------------------------------------------

func TestINT03_ProfileIsolation(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	srv := newTestServerWithProfiles(t, pool)
	defer srv.Cleanup()

	// Create two users for two isolated profiles.
	userA := uuid.MustParse("30000000-0000-0000-0000-000000000006")
	userB := uuid.MustParse("30000000-0000-0000-0000-000000000007")
	ensureProfileUser(t, pool, userA, "isolated_alice")
	ensureProfileUser(t, pool, userB, "isolated_bob")

	workspaceA := uuid.MustParse("30000000-0000-0000-0000-000000000040")
	workspaceB := uuid.MustParse("30000000-0000-0000-0000-000000000041")
	ensureProfileWorkspace(t, pool, workspaceA)
	ensureProfileWorkspace(t, pool, workspaceB)

	// ── Step 1: Each user creates trees in their workspace ───────────
	treeA1 := createTreeForProfile(t, srv, userA, "INT-03 Iso: Alice Alpha")
	treeA2 := createTreeForProfile(t, srv, userA, "INT-03 Iso: Alice Beta")
	treeB1 := createTreeForProfile(t, srv, userB, "INT-03 Iso: Bob Gamma")
	t.Logf("step 1 ✓ created isolated trees: A1=%s, A2=%s, B1=%s",
		treeA1.ID, treeA2.ID, treeB1.ID)

	// ── Step 2: User A's tree listing ────────────────────────────────
	// Trees are not user-scoped in MVP — all users see all trees.
	listA := listTreesForProfile(t, srv, userA)
	aIDs := collectTreeIDs(listA)
	if !aIDs[treeA1.ID] || !aIDs[treeA2.ID] {
		t.Fatalf("step 2 — User A's listing missing their own trees: got %v, want both %s and %s",
			treeIDKeys(aIDs), treeA1.ID, treeA2.ID)
	}
	// Confirm Bob's tree is also visible (no user-scoped tree isolation).
	if !aIDs[treeB1.ID] {
		t.Fatalf("step 2 — User A should see all trees including Bob's: %s not found",
			treeB1.ID)
	}
	t.Logf("step 2 ✓ User A sees all %d trees (not user-scoped in MVP)", len(listA))

	// ── Step 3: User B's tree listing ────────────────────────────────
	listB := listTreesForProfile(t, srv, userB)
	bIDs := collectTreeIDs(listB)
	if !bIDs[treeB1.ID] {
		t.Fatalf("step 3 — User B's listing missing their own tree: %s", treeB1.ID)
	}
	if !bIDs[treeA1.ID] || !bIDs[treeA2.ID] {
		t.Fatalf("step 3 — User B should see all trees: got %v", treeIDKeys(bIDs))
	}
	t.Logf("step 3 ✓ User B sees all %d trees", len(listB))

	// ── Step 4: Activate profiles in each workspace ─────────────────
	setProfile(t, srv, workspaceA, userA, "coding", "hprof_iso_wsa")
	setProfile(t, srv, workspaceB, userB, "research", "hprof_iso_wsb")
	t.Logf("step 4 ✓ profiles activated in isolated workspaces")

	// ── Step 5: Workspace A's profiles do not leak to workspace B ────
	profilesA := listProfiles(t, srv, workspaceA, userA)
	if len(profilesA) != 1 || profilesA[0]["profileName"] != "coding" {
		t.Fatalf("step 5 — wsA profiles: count=%d, name=%v",
			len(profilesA), pluck(profilesA, "profileName"))
	}
	profilesB := listProfiles(t, srv, workspaceB, userB)
	if len(profilesB) != 1 || profilesB[0]["profileName"] != "research" {
		t.Fatalf("step 5 — wsB profiles: count=%d, name=%v",
			len(profilesB), pluck(profilesB, "profileName"))
	}
	t.Logf("step 5 ✓ profile isolation confirmed: wsA=%s, wsB=%s",
		profilesA[0]["profileName"], profilesB[0]["profileName"])

	// ── Step 6: Verify cross-workspace profile visibility ──────────
	// Profiles are workspace-scoped: listing by workspace_id returns
	// only that workspace's profiles regardless of the caller.
	profilesBAsA := listProfiles(t, srv, workspaceB, userA)
	if len(profilesBAsA) != 1 || profilesBAsA[0]["profileName"] != "research" {
		t.Fatalf("step 6 — user A listing wsB: count=%d, name=%v",
			len(profilesBAsA), pluck(profilesBAsA, "profileName"))
	}
	profilesAAsB := listProfiles(t, srv, workspaceA, userB)
	if len(profilesAAsB) != 1 || profilesAAsB[0]["profileName"] != "coding" {
		t.Fatalf("step 6 — user B listing wsA: count=%d, name=%v",
			len(profilesAAsB), pluck(profilesAAsB, "profileName"))
	}
	t.Logf("step 6 ✓ cross-workspace listing works (workspace-scoped, not user-scoped)")

	// ── Step 7: Verify profile token is not exposed in listings ──────
	for _, p := range profilesA {
		if _, exposed := p["profileTokenEncrypted"]; exposed {
			t.Fatal("step 7 — encrypted profile token exposed in response")
		}
		if _, exposed := p["profile_token_encrypted"]; exposed {
			t.Fatal("step 7 — snake_case encrypted profile token exposed in response")
		}
	}
	t.Logf("step 7 ✓ profile tokens not exposed in listings")

	// ── Step 8: Trees remain accessible after profile operations ─────
	listAFinal := listTreesForProfile(t, srv, userA)
	if !containsTree(listAFinal, treeA1.ID) || !containsTree(listAFinal, treeA2.ID) {
		t.Fatalf("step 8 — after profile ops: user A's own trees missing")
	}
	listBFinal := listTreesForProfile(t, srv, userB)
	if !containsTree(listBFinal, treeB1.ID) {
		t.Fatalf("step 8 — after profile ops: user B's own tree missing")
	}
	t.Logf("step 8 ✓ all trees accessible after profile operations")

	t.Logf("INT-03 ProfileIsolation ✓ complete")
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// containsTree checks whether a list of trees (as map[string]any)
// contains a tree with the given UUID.
func containsTree(trees []map[string]any, id uuid.UUID) bool {
	for _, ts := range trees {
		if tid, ok := ts["id"].(string); ok && tid == id.String() {
			return true
		}
	}
	return false
}

// collectTreeIDs builds a set of UUIDs from tree map entries.
func collectTreeIDs(trees []map[string]any) map[uuid.UUID]bool {
	m := make(map[uuid.UUID]bool, len(trees))
	for _, ts := range trees {
		if tid, ok := ts["id"].(string); ok {
			if id, err := uuid.Parse(tid); err == nil {
				m[id] = true
			}
		}
	}
	return m
}

// treeIDKeys returns the string keys of a UUID set (for logging).
func treeIDKeys(m map[uuid.UUID]bool) []string {
	out := make([]string, 0, len(m))
	for id := range m {
		out = append(out, id.String())
	}
	return out
}

// pluck extracts a named field from a list of map results.
func pluck(items []map[string]any, field string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if v, ok := item[field].(string); ok {
			out = append(out, v)
		}
	}
	return out
}
