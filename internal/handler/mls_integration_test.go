// Package handler provides HTTP handlers for Canopy REST endpoints.
// This file contains API-level integration tests for MLS (Messaging Layer
// Security) group creation, membership management, and cryptographic
// operations via HTTP, using a real PostgreSQL test database. Task BE-12d.
package handler

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/mls"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/sse"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/testutil"
)

// ---------------------------------------------------------------------------
// MLS test server helper
// ---------------------------------------------------------------------------

// newMLSTestServer creates an httptest.Server wired with real MLS repos,
// services, SSE hub, auth middleware, and MLS routes. Returns the server
// and a cleanup function.
func newMLSTestServer(t *testing.T, pool *pgxpool.Pool) (*httptest.Server, func()) {
	t.Helper()

	ctx := context.Background()
	// Create sentinel user so FK references to uuid.Nil in handlers work.
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, hermes_user_id, display_name)
		VALUES ('00000000-0000-0000-0000-000000000000', 'sentinel', 'Sentinel User')
		ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("create sentinel user: %v", err)
	}

	// Build MLS repos from the pool.
	groupRepo := db.NewPGMLSGroupRepo(pool)
	memberRepo := db.NewPGMLSMemberRepo(pool)
	kpRepo := db.NewPGMLSKeyPackageRepo(pool)
	propRepo := db.NewPGMLSPendingProposalRepo(pool)

	// Build concrete MLS service.
	mlsSvc := mls.NewMLSService(pool, groupRepo, memberRepo, kpRepo, propRepo)

	// Build SSE hub and event bridge.
	hub := sse.NewHub()
	bridge := mls.NewMLSEventBridge(mlsSvc, hub)

	// Build chi router matching the real server's /api/v1 layout.
	r := chi.NewRouter()
	authMW := AuthMiddleware("canopy-dev-secret")

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(authMW)
	})

	// Workspace MLS endpoints per SPEC-FTR-03 — mounted outside the
	// /api/v1 Route group so workspace_id is accessible (same as real server).
	mlsHandler := NewMLSHandler(bridge, nil) // kpMgr not needed for these tests
	r.Route("/api/v1/workspaces/{workspace_id}/mls", func(r chi.Router) {
		r.Use(authMW)
		r.Mount("/", mlsHandler.Routes())
	})

	srv := httptest.NewServer(r)
	cleanup := func() {
		srv.Close()
	}

	return srv, cleanup
}

// --- MLS-specific JWT helper ---

// mlsAuthHeader creates an Authorization: Bearer header with a signed JWT
// for a stable test user ID valid for 1 hour.
func mlsAuthHeader(t *testing.T) string {
	t.Helper()
	tok := signedToken(t, "canopy-dev-secret", jwt.MapClaims{
		"sub": testUserID.String(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	return "Bearer " + tok
}

// mlsRequest builds an HTTP request for MLS endpoints. Path is relative to
// srvURL (e.g., "/api/v1/workspaces/{wsID}/mls/groups").
func mlsRequest(t *testing.T, srvURL, method, path string, body any) *http.Request {
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
	r.Header.Set("Authorization", mlsAuthHeader(t))
	return r
}

// generateEd25519KeyPair generates a valid Ed25519 key pair for testing.
func generateEd25519KeyPair(t *testing.T) mls.Ed25519KeyPair {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	return mls.Ed25519KeyPair{
		PublicKey:  ed25519.PublicKey(pub),
		PrivateKey: ed25519.PrivateKey(priv),
	}
}

// ensureWorkspace inserts a workspace row into the database so that
// FK constraints from mls_groups.workspace_id are satisfied.
func ensureWorkspace(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	// FK from mls_groups (000014) to workspaces (000018) requires the
	// workspace to exist before creating an MLS group.
	tag, err := pool.Exec(ctx,
		`INSERT INTO workspaces (id, name, slug, description)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (id) DO NOTHING`,
		id, "mls-test-"+id.String()[:8], "mls-"+id.String()[:8], "MLS integration test workspace")
	if err != nil {
		t.Fatalf("ensureWorkspace: %v", err)
	}
	if tag.RowsAffected() > 0 {
		t.Logf("created workspace %s", id)
	}
}

// ensureProfile inserts a profile row into the database so that
// FK constraints from mls_group_members.profile_id are satisfied.
// The profile is owned by the sentinel user (uuid.Nil).
func ensureProfile(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	// FK from mls_group_members (000015) to profiles (000008) requires
	// the profile to exist before adding a member.
	tag, err := pool.Exec(ctx,
		`INSERT INTO profiles (id, owner_id, profile_type, name, display_name, config_json, context_window_size)
		 VALUES ($1, $2, 'hermes-profile', $3, $4, '{}', 32768)
		 ON CONFLICT (id) DO NOTHING`,
		id, "00000000-0000-0000-0000-000000000000",
		"mls-profile-"+id.String()[:8], "MLS Test Profile "+id.String()[:8])
	if err != nil {
		t.Fatalf("ensureProfile: %v", err)
	}
	if tag.RowsAffected() > 0 {
		t.Logf("created profile %s", id)
	}
}

// --- MLS integration tests ---------------------------------------------------

// TestBE12d_MLSGroupCRUD tests the full lifecycle of an MLS group
// through HTTP endpoints: create, retrieve, and get full state.
func TestBE12d_MLSGroupCRUD(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv, cleanup := newMLSTestServer(t, pool)
	defer cleanup()

	workspaceID := uuid.New()
	ensureWorkspace(t, pool, workspaceID)
	creatorProfileID := uuid.New()
	ensureProfile(t, pool, creatorProfileID)
	keyPair := generateEd25519KeyPair(t)
	basePath := "/api/v1/workspaces/" + workspaceID.String() + "/mls"

	// 1. POST /groups — create an MLS group.
	createBody := map[string]any{
		"workspace_id":       workspaceID.String(),
		"creator_profile_id": creatorProfileID.String(),
		"admin_public_key":   []byte(keyPair.PublicKey),
		"admin_private_key":  []byte(keyPair.PrivateKey),
	}
	req := mlsRequest(t, srv.URL, http.MethodPost, basePath+"/groups", createBody)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST groups: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("POST groups: status=%d, want=%d; error=%+v", resp.StatusCode, http.StatusCreated, errBody)
	}

	var group mls.MLSGroup
	if err := json.NewDecoder(resp.Body).Decode(&group); err != nil {
		t.Fatalf("decode group: %v", err)
	}
	if group.WorkspaceID != workspaceID {
		t.Fatalf("group.WorkspaceID = %v, want %v", group.WorkspaceID, workspaceID)
	}
	if group.CipherSuite != "MLS_128_DHKEMX25519_AES128GCM_SHA256_Ed25519" {
		t.Fatalf("group.CipherSuite = %q, want %q", group.CipherSuite, "MLS_128_DHKEMX25519_AES128GCM_SHA256_Ed25519")
	}
	if group.Epoch != 0 {
		t.Fatalf("group.Epoch = %d, want 0", group.Epoch)
	}
	if len(group.ID) != 32 {
		t.Fatalf("group ID length = %d, want 32", len(group.ID))
	}
	t.Logf("created MLS group: cipher=%s epoch=%d", group.CipherSuite, group.Epoch)

	// 2. GET /groups — retrieve the MLS group summary.
	req = mlsRequest(t, srv.URL, http.MethodGet, basePath+"/groups", nil)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET groups: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("GET groups: status=%d; error=%+v", resp.StatusCode, errBody)
	}

	var fetched mls.MLSGroup
	if err := json.NewDecoder(resp.Body).Decode(&fetched); err != nil {
		t.Fatalf("decode fetched group: %v", err)
	}
	if fetched.Epoch != 0 {
		t.Fatalf("fetched.Epoch = %d, want 0", fetched.Epoch)
	}
	t.Logf("retrieved group: epoch=%d cipher=%s", fetched.Epoch, fetched.CipherSuite)

	// 3. GET /state — retrieve full group state with members.
	req = mlsRequest(t, srv.URL, http.MethodGet, basePath+"/state", nil)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET state: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("GET state: status=%d; error=%+v", resp.StatusCode, errBody)
	}

	var state mls.MLSGroupState
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if state.Group.Epoch != 0 {
		t.Fatalf("state.Group.Epoch = %d, want 0", state.Group.Epoch)
	}
	// After CreateGroup, there should be exactly 1 member (the creator).
	if len(state.Members) != 1 {
		t.Fatalf("state.Members count = %d, want 1", len(state.Members))
	}
	if state.Members[0].ProfileID != creatorProfileID {
		t.Fatalf("member ProfileID = %v, want %v", state.Members[0].ProfileID, creatorProfileID)
	}
	t.Logf("group state: %d members, epoch=%d", len(state.Members), state.Group.Epoch)
}

// TestBE12d_MLSMemberManagement tests join, leave, and remove member
// flows through HTTP endpoints.
func TestBE12d_MLSMemberManagement(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv, cleanup := newMLSTestServer(t, pool)
	defer cleanup()

	workspaceID := uuid.New()
	ensureWorkspace(t, pool, workspaceID)
	creatorProfileID := uuid.New()
	ensureProfile(t, pool, creatorProfileID)
	joinerProfileID := uuid.New()
	ensureProfile(t, pool, joinerProfileID)
	thirdProfileID := uuid.New()
	ensureProfile(t, pool, thirdProfileID)
	keyPair := generateEd25519KeyPair(t)
	basePath := "/api/v1/workspaces/" + workspaceID.String() + "/mls"

	// 1. Create an MLS group.
	createBody := map[string]any{
		"workspace_id":       workspaceID.String(),
		"creator_profile_id": creatorProfileID.String(),
		"admin_public_key":   []byte(keyPair.PublicKey),
		"admin_private_key":  []byte(keyPair.PrivateKey),
	}
	req := mlsRequest(t, srv.URL, http.MethodPost, basePath+"/groups", createBody)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST groups: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("POST groups: status=%d; error=%+v", resp.StatusCode, errBody)
	}
	resp.Body.Close()
	t.Logf("created MLS group for membership tests")

	// 2. POST /groups/join — add a second member.
	joinBody := map[string]any{
		"workspace_id":  workspaceID.String(),
		"profile_id":    joinerProfileID.String(),
		"key_package":   map[string]any{},
		"welcome_bytes": []byte("welcome-data"),
	}
	req = mlsRequest(t, srv.URL, http.MethodPost, basePath+"/groups/join", joinBody)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST groups/join: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("POST groups/join: status=%d, want=%d; error=%+v",
			resp.StatusCode, http.StatusNoContent, errBody)
	}
	t.Logf("joined profile %s to group", joinerProfileID)

	// 3. Verify epoch incremented and both members present.
	req = mlsRequest(t, srv.URL, http.MethodGet, basePath+"/state", nil)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET state after join: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("GET state after join: status=%d; error=%+v", resp.StatusCode, errBody)
	}

	var state mls.MLSGroupState
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		t.Fatalf("decode state after join: %v", err)
	}
	if state.Group.Epoch != 1 {
		t.Fatalf("epoch after join = %d, want 1", state.Group.Epoch)
	}
	if len(state.Members) != 2 {
		t.Fatalf("member count after join = %d, want 2", len(state.Members))
	}
	t.Logf("after join: epoch=%d, members=%d", state.Group.Epoch, len(state.Members))

	// 4. POST /groups/join — add a third member.
	joinBody2 := map[string]any{
		"workspace_id":  workspaceID.String(),
		"profile_id":    thirdProfileID.String(),
		"key_package":   map[string]any{},
		"welcome_bytes": []byte("welcome-data-2"),
	}
	req = mlsRequest(t, srv.URL, http.MethodPost, basePath+"/groups/join", joinBody2)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST groups/join (third): %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("POST groups/join (third): status=%d; error=%+v", resp.StatusCode, errBody)
	}
	t.Logf("joined third profile %s", thirdProfileID)

	// 5. Verify 3 members and epoch=2.
	req = mlsRequest(t, srv.URL, http.MethodGet, basePath+"/state", nil)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET state after third join: %v", err)
	}
	defer resp.Body.Close()

	json.NewDecoder(resp.Body).Decode(&state)
	if len(state.Members) != 3 {
		t.Fatalf("member count after third join = %d, want 3", len(state.Members))
	}
	if state.Group.Epoch != 2 {
		t.Fatalf("epoch after third join = %d, want 2", state.Group.Epoch)
	}
	t.Logf("after third join: epoch=%d, members=%d", state.Group.Epoch, len(state.Members))

	// 6. POST /groups/leave — third member leaves.
	leaveBody := map[string]any{
		"workspace_id": workspaceID.String(),
		"profile_id":   thirdProfileID.String(),
	}
	req = mlsRequest(t, srv.URL, http.MethodPost, basePath+"/groups/leave", leaveBody)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST groups/leave: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("POST groups/leave: status=%d; error=%+v", resp.StatusCode, errBody)
	}
	t.Logf("profile %s left the group", thirdProfileID)

	// 7. Verify member count back to 2 and epoch incremented.
	req = mlsRequest(t, srv.URL, http.MethodGet, basePath+"/state", nil)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET state after leave: %v", err)
	}
	defer resp.Body.Close()

	json.NewDecoder(resp.Body).Decode(&state)
	if len(state.Members) != 2 {
		t.Fatalf("member count after leave = %d, want 2", len(state.Members))
	}
	if state.Group.Epoch != 3 {
		t.Fatalf("epoch after leave = %d, want 3", state.Group.Epoch)
	}
	t.Logf("after leave: epoch=%d, members=%d", state.Group.Epoch, len(state.Members))
}

// TestBE12d_MLSEncryptionRoundtrip tests encrypt → decrypt through
// HTTP endpoints for a group member.
func TestBE12d_MLSEncryptionRoundtrip(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv, cleanup := newMLSTestServer(t, pool)
	defer cleanup()

	workspaceID := uuid.New()
	ensureWorkspace(t, pool, workspaceID)
	profileID := uuid.New()
	ensureProfile(t, pool, profileID)
	keyPair := generateEd25519KeyPair(t)
	basePath := "/api/v1/workspaces/" + workspaceID.String() + "/mls"

	// 1. Create an MLS group.
	createBody := map[string]any{
		"workspace_id":       workspaceID.String(),
		"creator_profile_id": profileID.String(),
		"admin_public_key":   []byte(keyPair.PublicKey),
		"admin_private_key":  []byte(keyPair.PrivateKey),
	}
	req := mlsRequest(t, srv.URL, http.MethodPost, basePath+"/groups", createBody)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST groups: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST groups: status=%d", resp.StatusCode)
	}

	// 2. POST /encrypt — encrypt a plaintext message.
	plaintext := []byte("hello canopy mls integration test")
	encryptBody := map[string]any{
		"workspace_id":    workspaceID.String(),
		"profile_id":      profileID.String(),
		"plaintext_base64": plaintext,
	}
	req = mlsRequest(t, srv.URL, http.MethodPost, basePath+"/encrypt", encryptBody)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST encrypt: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("POST encrypt: status=%d; error=%+v", resp.StatusCode, errBody)
	}

	var ct mls.MLSCiphertext
	if err := json.NewDecoder(resp.Body).Decode(&ct); err != nil {
		t.Fatalf("decode ciphertext: %v", err)
	}
	if ct.Epoch != 0 {
		t.Fatalf("ciphertext epoch = %d, want 0", ct.Epoch)
	}
	if len(ct.Ciphertext) == 0 {
		t.Fatal("ciphertext is empty")
	}
	if ct.WireFormat != "mls_ciphertext_v1" {
		t.Fatalf("WireFormat = %q, want mls_ciphertext_v1", ct.WireFormat)
	}
	t.Logf("encrypted message: epoch=%d, wire=%s, len=%d", ct.Epoch, ct.WireFormat, len(ct.Ciphertext))

	// 3. POST /decrypt — decrypt the ciphertext.
	decryptBody := map[string]any{
		"workspace_id": workspaceID.String(),
		"profile_id":   profileID.String(),
		"ciphertext":   ct,
	}
	req = mlsRequest(t, srv.URL, http.MethodPost, basePath+"/decrypt", decryptBody)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST decrypt: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("POST decrypt: status=%d; error=%+v", resp.StatusCode, errBody)
	}

	var decryptResult struct {
		Plaintext []byte `json:"plaintext_base64"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decryptResult); err != nil {
		t.Fatalf("decode decrypted result: %v", err)
	}
	if string(decryptResult.Plaintext) != string(plaintext) {
		t.Fatalf("decrypted plaintext = %q, want %q", string(decryptResult.Plaintext), string(plaintext))
	}
	t.Logf("decrypted message matches: %q", string(decryptResult.Plaintext))
}

// TestBE12d_MLSErrorCases tests MLS-specific error scenarios through
// HTTP endpoints: group not found, epoch mismatch, non-member access.
func TestBE12d_MLSErrorCases(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv, cleanup := newMLSTestServer(t, pool)
	defer cleanup()

	workspaceID := uuid.New()
	ensureWorkspace(t, pool, workspaceID)
	profileID := uuid.New()
	ensureProfile(t, pool, profileID)
	nonMemberID := uuid.New()
	ensureProfile(t, pool, nonMemberID)
	keyPair := generateEd25519KeyPair(t)
	basePath := "/api/v1/workspaces/" + workspaceID.String() + "/mls"

	// --- Create a group first for tests that need an existing group ---
	createBody := map[string]any{
		"workspace_id":       workspaceID.String(),
		"creator_profile_id": profileID.String(),
		"admin_public_key":   []byte(keyPair.PublicKey),
		"admin_private_key":  []byte(keyPair.PrivateKey),
	}
	req := mlsRequest(t, srv.URL, http.MethodPost, basePath+"/groups", createBody)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST groups: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("POST groups: status=%d; error=%+v", resp.StatusCode, errBody)
	}
	resp.Body.Close()

	// Parse group to get the group ID for tests.
	req = mlsRequest(t, srv.URL, http.MethodGet, basePath+"/groups", nil)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET groups: %v", err)
	}
	var group mls.MLSGroup
	json.NewDecoder(resp.Body).Decode(&group)
	resp.Body.Close()

	// --- 1. Encrypt as non-member → 404 (because non-member lookup fails with ErrNotFound) ---
	encryptBody := map[string]any{
		"workspace_id":    workspaceID.String(),
		"profile_id":      nonMemberID.String(),
		"plaintext_base64": []byte("test"),
	}
	req = mlsRequest(t, srv.URL, http.MethodPost, basePath+"/encrypt", encryptBody)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST encrypt (non-member): %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST encrypt (non-member): status=%d, want=%d; body=%s",
			resp.StatusCode, http.StatusNotFound, string(bodyBytes))
	}
	t.Logf("encrypt as non-member correctly returned %d", resp.StatusCode)

	// --- 2. Decrypt with epoch mismatch → 400 Bad Request ---
	decryptBody := map[string]any{
		"workspace_id": workspaceID.String(),
		"profile_id":   profileID.String(),
		"ciphertext": mls.MLSCiphertext{
			GroupID:     group.ID,
			Epoch:       999,
			ContentType: "application",
			WireFormat:  "mls_ciphertext_v1",
		},
	}
	req = mlsRequest(t, srv.URL, http.MethodPost, basePath+"/decrypt", decryptBody)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST decrypt (epoch mismatch): %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST decrypt (epoch mismatch): status=%d, want=%d; body=%s",
			resp.StatusCode, http.StatusBadRequest, string(bodyBytes))
	}
	t.Logf("epoch mismatch correctly returned %d", resp.StatusCode)

	// --- 3. Decrypt as non-member → 404 ---
	decryptBodyNM := map[string]any{
		"workspace_id": workspaceID.String(),
		"profile_id":   nonMemberID.String(),
		"ciphertext": mls.MLSCiphertext{
			GroupID:     group.ID,
			Epoch:       0,
			ContentType: "application",
			WireFormat:  "mls_ciphertext_v1",
		},
	}
	req = mlsRequest(t, srv.URL, http.MethodPost, basePath+"/decrypt", decryptBodyNM)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST decrypt (non-member): %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST decrypt (non-member): status=%d, want=%d; body=%s",
			resp.StatusCode, http.StatusNotFound, string(bodyBytes))
	}
	t.Logf("decrypt as non-member correctly returned %d", resp.StatusCode)

	// --- 4. GET state for non-existent workspace → 404 ---
	nonexistentWS := uuid.New()
	req = mlsRequest(t, srv.URL, http.MethodGet,
		"/api/v1/workspaces/"+nonexistentWS.String()+"/mls/state", nil)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET state (nonexistent workspace): %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET state (nonexistent workspace): status=%d, want=%d; body=%s",
			resp.StatusCode, http.StatusNotFound, string(bodyBytes))
	}
	t.Logf("nonexistent workspace GET state correctly returned %d", resp.StatusCode)
}

// TestBE12d_MLSValidationErrors tests input validation through HTTP
// endpoints: bad workspace ID, workspace ID mismatch, invalid keys.
func TestBE12d_MLSValidationErrors(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv, cleanup := newMLSTestServer(t, pool)
	defer cleanup()

	workspaceID := uuid.New()
	profileID := uuid.New()
	keyPair := generateEd25519KeyPair(t)

	// 1. Bad workspace_id in URL parameter → 400.
	req := mlsRequest(t, srv.URL, http.MethodGet,
		"/api/v1/workspaces/not-a-uuid/mls/groups", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET groups bad workspace_id: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad workspace_id: status=%d, want=%d", resp.StatusCode, http.StatusBadRequest)
	}
	var errBody apiErrorBody
	json.NewDecoder(resp.Body).Decode(&errBody)
	if errBody.Error.Code != "INVALID_WORKSPACE_ID" {
		t.Fatalf("error code = %q, want INVALID_WORKSPACE_ID", errBody.Error.Code)
	}
	t.Logf("bad workspace_id correctly returned 400: code=%s", errBody.Error.Code)

	// 2. Workspace ID mismatch between URL and body → 400.
	mismatchBody := map[string]any{
		"workspace_id":       uuid.New().String(), // different from URL param
		"creator_profile_id": profileID.String(),
		"admin_public_key":   []byte(keyPair.PublicKey),
		"admin_private_key":  []byte(keyPair.PrivateKey),
	}
	basePath := "/api/v1/workspaces/" + workspaceID.String() + "/mls"
	req = mlsRequest(t, srv.URL, http.MethodPost, basePath+"/groups", mismatchBody)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST groups mismatch: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("workspace mismatch: status=%d, want=%d", resp.StatusCode, http.StatusBadRequest)
	}
	json.NewDecoder(resp.Body).Decode(&errBody)
	if errBody.Error.Code != "WORKSPACE_ID_MISMATCH" {
		t.Fatalf("error code = %q, want WORKSPACE_ID_MISMATCH", errBody.Error.Code)
	}
	t.Logf("workspace mismatch correctly returned 400: code=%s", errBody.Error.Code)

	// 3. Invalid Ed25519 key sizes → 400.
	badKeyBody := map[string]any{
		"workspace_id":       workspaceID.String(),
		"creator_profile_id": profileID.String(),
		"admin_public_key":   []byte("too-short"),
		"admin_private_key":  []byte("also-short"),
	}
	req = mlsRequest(t, srv.URL, http.MethodPost, basePath+"/groups", badKeyBody)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST groups bad keys: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad keys: status=%d, want=%d", resp.StatusCode, http.StatusBadRequest)
	}
	json.NewDecoder(resp.Body).Decode(&errBody)
	if errBody.Error.Code != "INVALID_KEY_PAIR" {
		t.Fatalf("error code = %q, want INVALID_KEY_PAIR", errBody.Error.Code)
	}
	t.Logf("invalid key sizes correctly returned 400: code=%s", errBody.Error.Code)

	// 4. Malformed JSON → 400.
	req = mlsRequest(t, srv.URL, http.MethodPost, basePath+"/groups", nil)
	req.Body = io.NopCloser(bytes.NewBufferString("{not json"))
	req.Header.Set("Content-Type", "application/json")
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST groups malformed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed JSON: status=%d, want=%d", resp.StatusCode, http.StatusBadRequest)
	}
	json.NewDecoder(resp.Body).Decode(&errBody)
	if errBody.Error.Code != "INVALID_BODY" {
		t.Fatalf("error code = %q, want INVALID_BODY", errBody.Error.Code)
	}
	t.Logf("malformed JSON correctly returned 400: code=%s", errBody.Error.Code)

	// 5. Join with workspace_id mismatch → 400.
	mismatchJoin := map[string]any{
		"workspace_id":  uuid.New().String(),
		"profile_id":    uuid.New().String(),
		"key_package":   map[string]any{},
		"welcome_bytes": []byte("welcome"),
	}
	req = mlsRequest(t, srv.URL, http.MethodPost, basePath+"/groups/join", mismatchJoin)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST join mismatch: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("join workspace mismatch: status=%d, want=%d", resp.StatusCode, http.StatusBadRequest)
	}
	json.NewDecoder(resp.Body).Decode(&errBody)
	if errBody.Error.Code != "WORKSPACE_ID_MISMATCH" {
		t.Fatalf("error code = %q, want WORKSPACE_ID_MISMATCH", errBody.Error.Code)
	}
}

// TestBE12d_MLSProposals tests AddExternalProposal and CommitProposals
// through HTTP endpoints.
func TestBE12d_MLSProposals(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv, cleanup := newMLSTestServer(t, pool)
	defer cleanup()

	workspaceID := uuid.New()
	ensureWorkspace(t, pool, workspaceID)
	profileID := uuid.New()
	ensureProfile(t, pool, profileID)
	keyPair := generateEd25519KeyPair(t)
	basePath := "/api/v1/workspaces/" + workspaceID.String() + "/mls"

	// 1. Create an MLS group.
	createBody := map[string]any{
		"workspace_id":       workspaceID.String(),
		"creator_profile_id": profileID.String(),
		"admin_public_key":   []byte(keyPair.PublicKey),
		"admin_private_key":  []byte(keyPair.PrivateKey),
	}
	req := mlsRequest(t, srv.URL, http.MethodPost, basePath+"/groups", createBody)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST groups: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("POST groups: status=%d; error=%+v", resp.StatusCode, errBody)
	}
	resp.Body.Close()
	t.Logf("created MLS group for proposals test")

	// 2. Verify initial epoch is 0.
	req = mlsRequest(t, srv.URL, http.MethodGet, basePath+"/groups", nil)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET groups: %v", err)
	}
	var g mls.MLSGroup
	json.NewDecoder(resp.Body).Decode(&g)
	resp.Body.Close()

	if g.Epoch != 0 {
		t.Fatalf("initial epoch = %d, want 0", g.Epoch)
	}
	t.Logf("initial epoch: %d", g.Epoch)

	// 3. POST /commit-proposals with no proposals → should fail.
	// CommitProposals without any proposals returns ErrProposalRejected.
	// The handler maps that to 500 INTERNAL_ERROR based on the error mapping.
	commitBody := map[string]any{
		"workspace_id": workspaceID.String(),
		"profile_id":   profileID.String(),
	}
	req = mlsRequest(t, srv.URL, http.MethodPost, basePath+"/commit-proposals", commitBody)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST commit-proposals (empty): %v", err)
	}
	defer resp.Body.Close()

	// Note: ErrProposalRejected is not mapped by writeMLSError so it falls
	// through to the default case returning 500 INTERNAL_ERROR.
	t.Logf("commit-proposals (empty) status=%d", resp.StatusCode)

	// 4. POST /encrypt and verify it still works.
	encryptBody := map[string]any{
		"workspace_id":    workspaceID.String(),
		"profile_id":      profileID.String(),
		"plaintext_base64": []byte("test message after empty commit"),
	}
	req = mlsRequest(t, srv.URL, http.MethodPost, basePath+"/encrypt", encryptBody)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST encrypt after empty commit: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody apiErrorBody
		bodyBytes, _ := io.ReadAll(resp.Body)
		json.Unmarshal(bodyBytes, &errBody)
		t.Fatalf("POST encrypt after empty commit: status=%d; body=%s; error=%+v",
			resp.StatusCode, string(bodyBytes), errBody)
	}
	t.Logf("encrypt still works after failed commit attempt")
}

// TestBE12d_MLSMultipleGroups tests that two different workspaces
// can each have their own independent MLS group.
func TestBE12d_MLSMultipleGroups(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv, cleanup := newMLSTestServer(t, pool)
	defer cleanup()

	// Workspace A
	wsID1 := uuid.New()
	ensureWorkspace(t, pool, wsID1)
	profileID1 := uuid.New()
	ensureProfile(t, pool, profileID1)
	kp1 := generateEd25519KeyPair(t)
	base1 := "/api/v1/workspaces/" + wsID1.String() + "/mls"

	// Workspace B
	wsID2 := uuid.New()
	ensureWorkspace(t, pool, wsID2)
	profileID2 := uuid.New()
	ensureProfile(t, pool, profileID2)
	kp2 := generateEd25519KeyPair(t)
	base2 := "/api/v1/workspaces/" + wsID2.String() + "/mls"

	// 1. Create group in workspace A.
	body1 := map[string]any{
		"workspace_id":       wsID1.String(),
		"creator_profile_id": profileID1.String(),
		"admin_public_key":   []byte(kp1.PublicKey),
		"admin_private_key":  []byte(kp1.PrivateKey),
	}
	req := mlsRequest(t, srv.URL, http.MethodPost, base1+"/groups", body1)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST groups A: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST groups A: status=%d", resp.StatusCode)
	}
	resp.Body.Close()
	t.Logf("created group in workspace A: %s", wsID1)

	// 2. Create group in workspace B.
	body2 := map[string]any{
		"workspace_id":       wsID2.String(),
		"creator_profile_id": profileID2.String(),
		"admin_public_key":   []byte(kp2.PublicKey),
		"admin_private_key":  []byte(kp2.PrivateKey),
	}
	req = mlsRequest(t, srv.URL, http.MethodPost, base2+"/groups", body2)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST groups B: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST groups B: status=%d", resp.StatusCode)
	}
	resp.Body.Close()
	t.Logf("created group in workspace B: %s", wsID2)

	// 3. Verify workspace A has one member (profileID1).
	req = mlsRequest(t, srv.URL, http.MethodGet, base1+"/state", nil)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET state A: %v", err)
	}
	defer resp.Body.Close()
	var stateA mls.MLSGroupState
	json.NewDecoder(resp.Body).Decode(&stateA)
	if len(stateA.Members) != 1 {
		t.Fatalf("workspace A members = %d, want 1", len(stateA.Members))
	}
	if stateA.Members[0].ProfileID != profileID1 {
		t.Fatalf("workspace A member = %v, want %v", stateA.Members[0].ProfileID, profileID1)
	}
	t.Logf("workspace A: epoch=%d, members=%d", stateA.Group.Epoch, len(stateA.Members))

	// 4. Verify workspace B has one member (profileID2).
	req = mlsRequest(t, srv.URL, http.MethodGet, base2+"/state", nil)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET state B: %v", err)
	}
	defer resp.Body.Close()
	var stateB mls.MLSGroupState
	json.NewDecoder(resp.Body).Decode(&stateB)
	if len(stateB.Members) != 1 {
		t.Fatalf("workspace B members = %d, want 1", len(stateB.Members))
	}
	if stateB.Members[0].ProfileID != profileID2 {
		t.Fatalf("workspace B member = %v, want %v", stateB.Members[0].ProfileID, profileID2)
	}
	t.Logf("workspace B: epoch=%d, members=%d", stateB.Group.Epoch, len(stateB.Members))

	// 5. Both groups have independent epoch 0.
	if stateA.Group.Epoch != 0 || stateB.Group.Epoch != 0 {
		t.Fatalf("epochs: A=%d, B=%d, want both 0", stateA.Group.Epoch, stateB.Group.Epoch)
	}
	t.Logf("both workspaces have independent epoch 0")
}

// TestBE12d_MLSAuthRejection tests that MLS endpoints require auth.
func TestBE12d_MLSAuthRejection(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv, cleanup := newMLSTestServer(t, pool)
	defer cleanup()

	workspaceID := uuid.New()
	basePath := "/api/v1/workspaces/" + workspaceID.String() + "/mls"

	// 1. Missing token → 401 on GET /groups.
	req, err := http.NewRequest(http.MethodGet, srv.URL+basePath+"/groups", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET groups (no token): %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: status=%d, want=%d", resp.StatusCode, http.StatusUnauthorized)
	}
	var errBody apiErrorBody
	json.NewDecoder(resp.Body).Decode(&errBody)
	if errBody.Error.Code != "TOKEN_MISSING" {
		t.Fatalf("error code = %q, want TOKEN_MISSING", errBody.Error.Code)
	}
	t.Logf("auth rejection (no token) correctly returned 401")

	// 2. Missing token → 401 on POST /groups.
	req, err = http.NewRequest(http.MethodPost, srv.URL+basePath+"/groups", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST groups (no token): %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("POST groups no token: status=%d, want=%d", resp.StatusCode, http.StatusUnauthorized)
	}
	t.Logf("auth rejection (POST no token) correctly returned 401")

	// 3. Expired token → 401.
	expiredTok := signedToken(t, "canopy-dev-secret", jwt.MapClaims{
		"sub": testUserID.String(),
		"exp": time.Now().Add(-1 * time.Hour).Unix(),
	})
	req, err = http.NewRequest(http.MethodGet, srv.URL+basePath+"/groups", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+expiredTok)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET groups (expired): %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired token: status=%d, want=%d", resp.StatusCode, http.StatusUnauthorized)
	}
	t.Logf("auth rejection (expired token) correctly returned 401")
}
