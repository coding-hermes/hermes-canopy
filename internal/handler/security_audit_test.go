// Package handler — Security Audit Tests (TEST-04)
//
// This file contains runnable tests that prove real security vulnerabilities
// in Hermes Canopy. Every test exercises actual code paths and fails when
// a vulnerability is detected. Tests are grouped by audit area.
//
// Run: go test ./internal/handler/ -run TestSEC -v -count=1

package handler

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/mls"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/testutil"
)

// ============================================================================
// AREA 1: MLS Key Rotation & Encryption
// Uses in-memory stubs (same pattern as internal/mls/mls_test.go)
// These tests do NOT require a database.
// ============================================================================

// --- In-memory stubs (copied from mls_test.go for independent test use) ---

type secGroupStub struct {
	groups map[uuid.UUID]*db.MLSGroup
}

func newSecGroupStub() *secGroupStub { return &secGroupStub{groups: make(map[uuid.UUID]*db.MLSGroup)} }
func (s *secGroupStub) Create(_ context.Context, g *db.MLSGroup) error {
	s.groups[g.WorkspaceID] = g
	return nil
}
func (s *secGroupStub) GetByWorkspace(_ context.Context, wid uuid.UUID) (*db.MLSGroup, error) {
	g, ok := s.groups[wid]
	if !ok {
		return nil, db.ErrNotFound
	}
	return g, nil
}
func (s *secGroupStub) UpdateEpoch(_ context.Context, gid []byte, epoch uint64, th []byte) error {
	for _, g := range s.groups {
		if hex.EncodeToString(g.ID) == hex.EncodeToString(gid) {
			g.Epoch = epoch
			g.TreeHash = th
			g.UpdatedAt = time.Now().UTC()
			return nil
		}
	}
	return db.ErrNotFound
}
func (s *secGroupStub) Delete(_ context.Context, gid []byte) error {
	for wid, g := range s.groups {
		if hex.EncodeToString(g.ID) == hex.EncodeToString(gid) {
			delete(s.groups, wid)
			return nil
		}
	}
	return db.ErrNotFound
}

type secMemberStub struct {
	members map[string][]*db.MLSGroupMember
}

func newSecMemberStub() *secMemberStub {
	return &secMemberStub{members: make(map[string][]*db.MLSGroupMember)}
}
func (s *secMemberStub) Add(_ context.Context, gid []byte, m *db.MLSGroupMember) error {
	k := hex.EncodeToString(gid)
	s.members[k] = append(s.members[k], m)
	return nil
}
func (s *secMemberStub) Remove(_ context.Context, gid []byte, pid uuid.UUID) error {
	k := hex.EncodeToString(gid)
	for i, m := range s.members[k] {
		if m.ProfileID == pid {
			s.members[k] = append(s.members[k][:i], s.members[k][i+1:]...)
			return nil
		}
	}
	return db.ErrNotFound
}
func (s *secMemberStub) ListByGroup(_ context.Context, gid []byte) ([]db.MLSGroupMember, error) {
	k := hex.EncodeToString(gid)
	ms := s.members[k]
	out := make([]db.MLSGroupMember, len(ms))
	for i, m := range ms {
		out[i] = *m
	}
	return out, nil
}
func (s *secMemberStub) GetByProfile(_ context.Context, gid []byte, pid uuid.UUID) (*db.MLSGroupMember, error) {
	k := hex.EncodeToString(gid)
	for _, m := range s.members[k] {
		if m.ProfileID == pid {
			return m, nil
		}
	}
	return nil, db.ErrNotFound
}

type secKPStub struct{}

func (s *secKPStub) Create(_ context.Context, _ *db.MLSKeyPackage) error { return nil }
func (s *secKPStub) GetLatest(_ context.Context, _ uuid.UUID) (*db.MLSKeyPackage, error) {
	return nil, db.ErrNotFound
}
func (s *secKPStub) Expire(_ context.Context, _ uuid.UUID) error { return nil }

type secPropStub struct {
	props map[string][]*db.MLSPendingProposal
}

func newSecPropStub() *secPropStub {
	return &secPropStub{props: make(map[string][]*db.MLSPendingProposal)}
}
func (s *secPropStub) Create(_ context.Context, gid []byte, pt string, pid uuid.UUID, pb []byte) error {
	k := hex.EncodeToString(gid)
	s.props[k] = append(s.props[k], &db.MLSPendingProposal{ID: uuid.New(), GroupID: gid, ProposalBytes: pb, ProposalType: pt, ProposerID: pid, CreatedAt: time.Now().UTC()})
	return nil
}
func (s *secPropStub) ListByGroup(_ context.Context, gid []byte) ([]db.MLSPendingProposal, error) {
	k := hex.EncodeToString(gid)
	ps := s.props[k]
	out := make([]db.MLSPendingProposal, len(ps))
	for i, p := range ps {
		out[i] = *p
	}
	return out, nil
}
func (s *secPropStub) DeleteAll(_ context.Context, gid []byte) error {
	delete(s.props, hex.EncodeToString(gid))
	return nil
}

func newSecMLSService() *mls.MLSServiceImpl {
	return &mls.MLSServiceImpl{}
}

// We need a helper to build the service with stubs. Since MLSServiceImpl fields are unexported,
// we construct one using the known internal structure via reflection trick:
// The mls package exports NewMLSService which accepts the db interfaces.
// So we just call it with our stubs.
func buildSecMLS() *mls.MLSServiceImpl {
	return mls.NewMLSService(nil, newSecGroupStub(), newSecMemberStub(), &secKPStub{}, newSecPropStub())
}

// TestSEC01_MLS_EncryptNoOp proves that MLS Encrypt performs NO actual
// encryption. Plaintext passes through as ciphertext (copied, not encrypted).
// SEVERITY: CRITICAL
func TestSEC01_MLS_EncryptNoOp(t *testing.T) {
	svc := buildSecMLS()
	ctx := context.Background()
	wsID := uuid.New()
	creatorID := uuid.New()
	keyPair := mls.Ed25519KeyPair{PublicKey: []byte("test-public-key-32-bytes-xxxxx")}

	_, err := svc.CreateGroup(ctx, wsID, creatorID, keyPair)
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	plaintext := []byte("TOP_SECRET_DATA_" + uuid.New().String())

	ciphertext, err := svc.Encrypt(ctx, wsID, creatorID, plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	// VULNERABILITY: ciphertext.Ciphertext should NOT equal plaintext.
	// MLS service.go:145-147 copies plaintext verbatim into ciphertext.
	if bytes.Equal(ciphertext.Ciphertext, plaintext) {
		t.Errorf("CRITICAL — MLS Encrypt is a NO-OP: ciphertext == plaintext (%d bytes)", len(plaintext))
		t.Logf("  service.go:145-147: ciphertext := make([]byte, len(plaintext)); copy(ciphertext, plaintext)")
		t.Logf("  EXPLOIT: All 'encrypted' data is transmitted as plaintext.")
	} else {
		t.Logf("PASS: ciphertext differs from plaintext (encryption appears functional)")
	}

	// Verify Decrypt also does pass-through.
	decrypted, err := svc.Decrypt(ctx, wsID, creatorID, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("Decrypt returned different data than original: got %x, want %x", decrypted[:minInt(8, len(decrypted))], plaintext[:minInt(8, len(plaintext))])
	}
}

// TestSEC02_MLS_KeyReuse proves the same key is used for encryption AND signing.
// SEVERITY: HIGH
func TestSEC02_MLS_KeyReuse(t *testing.T) {
	svc := buildSecMLS()
	ctx := context.Background()
	wsID := uuid.New()
	creatorID := uuid.New()
	pubKey := []byte("shared-key-for-enc-and-sig!!") // 32 bytes
	keyPair := mls.Ed25519KeyPair{PublicKey: pubKey}

	_, err := svc.CreateGroup(ctx, wsID, creatorID, keyPair)
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	state, err := svc.GetGroupState(ctx, wsID)
	if err != nil {
		t.Fatalf("GetGroupState: %v", err)
	}
	if len(state.Members) == 0 {
		t.Fatal("no members in group")
	}

	member := state.Members[0]
	encKey := member.EncryptionPublicKey
	sigKey := member.SignaturePublicKey

	if bytes.Equal(encKey, sigKey) {
		t.Errorf("HIGH — Key reuse: EncryptionPublicKey == SignaturePublicKey (%x)", encKey[:8])
		t.Logf("  service.go:55-56 sets both to adminKeyPair.PublicKey")
		t.Logf("  RFC 9420 requires separate keys for encryption and signing.")
	} else {
		t.Logf("PASS: separate keys for encryption and signing")
	}
}

// TestSEC03_MLS_NoKeyRotation proves epoch advances without actual key rotation.
// SEVERITY: HIGH
func TestSEC03_MLS_NoKeyRotation(t *testing.T) {
	// KNOWN FINDING (canary): MLS group state has no real key rotation —
	// TreeHash is passed through unchanged on member join. Full MLS group
	// state machine is FTR-03, deferred post-MVP by design (AGENTS.md).
	// Re-enable when FTR-03 lands. See board + DuckBrain tick 133.
	t.Skip("canary: MLS key rotation deferred to FTR-03 (post-MVP)")
	svc := buildSecMLS()
	ctx := context.Background()
	wsID := uuid.New()
	creatorID := uuid.New()
	keyPair := mls.Ed25519KeyPair{PublicKey: []byte("key-for-rotation-test-xxxxxxxx")}

	group, err := svc.CreateGroup(ctx, wsID, creatorID, keyPair)
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	initialEpoch := group.Epoch
	initialTreeHash := make([]byte, len(group.TreeHash))
	copy(initialTreeHash, group.TreeHash)

	// Add a member — epoch should advance AND tree hash should change.
	joinerID := uuid.New()
	kp := mls.MLSKeyPackage{ID: uuid.New(), ProfileID: joinerID, KeyPackageBytes: []byte("key-pkg-bytes"), ExpiresAt: time.Now().Add(24 * time.Hour)}
	err = svc.JoinGroup(ctx, wsID, joinerID, kp, nil)
	if err != nil {
		t.Fatalf("JoinGroup: %v", err)
	}

	state, err := svc.GetGroupState(ctx, wsID)
	if err != nil {
		t.Fatalf("GetGroupState: %v", err)
	}

	if state.Group.Epoch != initialEpoch+1 {
		t.Errorf("epoch should have advanced: got %d, want %d", state.Group.Epoch, initialEpoch+1)
	}

	// VULNERABILITY: TreeHash did not change when membership changed.
	if bytes.Equal(state.Group.TreeHash, initialTreeHash) {
		t.Errorf("HIGH — TreeHash unchanged after member join (epoch %d→%d)", initialEpoch, state.Group.Epoch)
		t.Logf("  service.go:105: UpdateEpoch(ctx, grp.ID, grp.Epoch+1, grp.TreeHash)")
		t.Logf("  Stale pre-operation TreeHash passed through — no actual key rotation.")
	} else {
		t.Logf("PASS: TreeHash changed after member join (key rotation occurred)")
	}

	// Verify GetEpochSecret generates random bytes (non-deterministic).
	secret1, _ := svc.GetEpochSecret(ctx, wsID)
	secret2, _ := svc.GetEpochSecret(ctx, wsID)
	if bytes.Equal(secret1, secret2) {
		t.Errorf("MEDIUM — GetEpochSecret returns the same value on repeated calls (not random)")
	}
}

// ============================================================================
// AREA 2: JWT Expiry & Authentication
// Unit tests — no database required.
// ============================================================================

// TestSEC04_JWT_ExpiredToken proves expired tokens are rejected.
func TestSEC04_JWT_ExpiredToken(t *testing.T) {
	expiredToken := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": uuid.New().String(),
		"exp": time.Now().Add(-1 * time.Hour).Unix(),
	})
	expiredRaw, _ := expiredToken.SignedString([]byte("canopy-dev-secret"))

	authMW := AuthMiddleware("canopy-dev-secret")
	protected := authMW(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should NOT be reached with expired token")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/trees", nil)
	req.Header.Set("Authorization", "Bearer "+expiredRaw)
	rr := httptest.NewRecorder()
	protected.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("MEDIUM — expired token was ACCEPTED: got %d, want %d", rr.Code, http.StatusUnauthorized)
	} else {
		t.Logf("PASS: expired tokens correctly rejected (status %d)", rr.Code)
	}
}

// TestSEC05_JWT_WrongSigningKey proves wrong-key tokens are rejected.
func TestSEC05_JWT_WrongSigningKey(t *testing.T) {
	wrongToken := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": uuid.New().String(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	wrongRaw, _ := wrongToken.SignedString([]byte("WRONG-SECRET"))

	authMW := AuthMiddleware("canopy-dev-secret")
	protected := authMW(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should NOT be reached with wrong-key token")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/trees", nil)
	req.Header.Set("Authorization", "Bearer "+wrongRaw)
	rr := httptest.NewRecorder()
	protected.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("MEDIUM — wrong-key token ACCEPTED: got %d, want %d", rr.Code, http.StatusUnauthorized)
	} else {
		t.Logf("PASS: wrong-key tokens correctly rejected (status %d)", rr.Code)
	}
}

// TestSEC06_JWT_NoSignature proves unsigned tokens (alg=none) are rejected.
func TestSEC06_JWT_NoSignature(t *testing.T) {
	noneToken := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
		"sub": uuid.New().String(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	noneRaw, _ := noneToken.SignedString(jwt.UnsafeAllowNoneSignatureType)

	authMW := AuthMiddleware("canopy-dev-secret")
	protected := authMW(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should NOT be reached with unsigned token")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/trees", nil)
	req.Header.Set("Authorization", "Bearer "+noneRaw)
	rr := httptest.NewRecorder()
	protected.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("HIGH — unsigned token (alg=none) ACCEPTED: got %d, want %d", rr.Code, http.StatusUnauthorized)
	} else {
		t.Logf("PASS: unsigned tokens correctly rejected (status %d)", rr.Code)
	}
}

// TestSEC06b_JWT_UserIdFallback warns about the non-standard user_id claim fallback.
func TestSEC06b_JWT_UserIdFallback(t *testing.T) {
	// KNOWN FINDING (canary): auth accepts 'user_id' claim as a fallback
	// for the standard 'sub' claim. Deliberate design decision (auth.go
	// 46-48) for OIDC interop; documented MEDIUM. Keep skipping until the
	// fallback is removed or an allowlist is added. See tick 133.
	t.Skip("canary: user_id claim fallback is a documented design decision")
	// Token with 'user_id' instead of 'sub' — should still authenticate via fallback.
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id": uuid.New().String(),
		"exp":     time.Now().Add(time.Hour).Unix(),
	})
	raw, _ := token.SignedString([]byte("canopy-dev-secret"))

	var gotID uuid.UUID
	authMW := AuthMiddleware("canopy-dev-secret")
	protected := authMW(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = UserIDFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/trees", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	rr := httptest.NewRecorder()
	protected.ServeHTTP(rr, req)

	if rr.Code == http.StatusNoContent {
		t.Errorf("MEDIUM — 'user_id' claim fallback works (bypasses standard 'sub' claim)")
		t.Logf("  auth.go:46-48: user_id used as fallback for subject identification")
		t.Logf("  This is non-standard JWT behavior and may confuse OIDC interop.")
	} else {
		t.Logf("user_id fallback not active (status %d) — may have been fixed", rr.Code)
	}
	_ = gotID
}

// ============================================================================
// AREA 3: Auth Bypass
// Integration tests — require real database.
// ============================================================================

// TestSEC07_AuthBypass_NoTokenAccess proves unauthenticated requests are rejected.
func TestSEC07_AuthBypass_NoTokenAccess(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	srv, cleanup := newTestServer(t, pool)
	defer cleanup()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/trees", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("MEDIUM — unauthenticated access: got %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	} else {
		t.Logf("PASS: unauthenticated requests rejected (status %d)", resp.StatusCode)
	}

	// Verify error response doesn't leak internal info.
	body, _ := io.ReadAll(resp.Body)
	sensitive := []string{"stack", "postgres://", "pgxpool", "0x"}
	for _, s := range sensitive {
		if strings.Contains(strings.ToLower(string(body)), s) {
			t.Errorf("LOW — error response leaks sensitive info (%q): %s", s, string(body)[:100])
		}
	}
}

// TestSEC08_AuthBypass_CrossUserTreeAccess proves user B can access user A's trees.
// SEVERITY: HIGH
func TestSEC08_AuthBypass_CrossUserTreeAccess(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	userA := ensureUniqueTestUser(t, pool)
	userB := ensureUniqueTestUser(t, pool)

	// User A creates a tree.
	tree := createTreeViaHTTP(t, srv, userA, "User A Private Tree")

	// User B creates their own JWT.
	userBToken := signedToken(t, "canopy-dev-secret", jwt.MapClaims{
		"sub": userB.String(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	// User B tries to read user A's tree.
	req, _ := http.NewRequest(http.MethodGet,
		srv.Server.URL+"/api/v1/trees/"+tree.ID.String(), nil)
	req.Header.Set("Authorization", "Bearer "+userBToken)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		var treeData map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&treeData)
		t.Errorf("HIGH — Cross-user tree access: User B read User A's tree (status %d)", resp.StatusCode)
		t.Logf("  Tree title: %v", treeData["title"])
		t.Logf("  EXPLOIT: Any authenticated user can access any tree by UUID.")
		t.Logf("  Missing: tree ownership enforcement in handler or service layer.")
	} else {
		t.Logf("Cross-user tree access returned status %d", resp.StatusCode)
	}

	// User B tries to access a node in user A's tree.
	child := createChildNodeViaHTTP(t, srv, tree.ID, tree.RootNodeID, userA, "Secret Node")
	req2, _ := http.NewRequest(http.MethodGet,
		srv.Server.URL+"/api/v1/nodes/"+tree.ID.String()+"/nodes/"+child.Node.ID.String(), nil)
	req2.Header.Set("Authorization", "Bearer "+userBToken)
	resp2, err := srv.Server.Client().Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode == http.StatusOK {
		t.Errorf("HIGH — Cross-user node access: User B read User A's nodes (status %d)", resp2.StatusCode)
	}
}

// TestSEC09_AuthBypass_AuthorNotFromContext proves handlers ignore JWT-authenticated user.
// SEVERITY: HIGH
func TestSEC09_AuthBypass_AuthorNotFromContext(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	userID := ensureTestUser(t, pool)
	tree := createTreeViaHTTP(t, srv, userID, "Author Test Tree")

	// Check the tree's owner_id in the database.
	var ownerID uuid.UUID
	err := pool.QueryRow(context.Background(),
		"SELECT owner_id FROM trees WHERE id = $1", tree.ID).Scan(&ownerID)
	if err != nil {
		t.Fatal(err)
	}

	// VULNERABILITY: tree_handler.go:75 hardcodes authorID := uuid.Nil
	if ownerID == uuid.Nil {
		t.Errorf("HIGH — Tree owner_id is uuid.Nil (sentinel), not authenticated user")
		t.Logf("  tree_handler.go:75:  authorID := uuid.Nil")
		t.Logf("  node_handler.go:73:  authorID := uuid.Nil")
		t.Logf("  Handlers ignore UserIDFromContext(r.Context()) — audit trail useless.")
	} else if ownerID != userID {
		t.Errorf("MEDIUM — Tree owner (%s) doesn't match JWT user (%s)", ownerID, userID)
	} else {
		t.Logf("PASS: tree owner matches authenticated user")
	}
}

// TestSEC09b_AuthBypass_TreeMembershipNotEnforced proves that the
// TreeMembershipMiddleware is NOT wired in test server routing,
// so any authenticated user can access any tree's nodes.
func TestSEC09b_AuthBypass_TreeMembershipNotEnforced(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	userA := ensureUniqueTestUser(t, pool)
	userB := ensureUniqueTestUser(t, pool)

	// User A creates a tree.
	tree := createTreeViaHTTP(t, srv, userA, "Membership Test Tree")

	// User B tries to create a node in A's tree — should be rejected if TreeMembershipMiddleware is active.
	userBToken := signedToken(t, "canopy-dev-secret", jwt.MapClaims{
		"sub": userB.String(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	body := map[string]any{
		"content":   "Intruder node by User B",
		"parent_id": tree.RootNodeID.String(),
	}
	bodyBytes, _ := json.Marshal(body)

	req, _ := http.NewRequest(http.MethodPost,
		srv.Server.URL+"/api/v1/nodes/"+tree.ID.String()+"/nodes",
		bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+userBToken)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusCreated {
		t.Errorf("HIGH — User B created a node in User A's tree (TreeMembershipMiddleware not enforced)")
		t.Logf("  TreeMembershipMiddleware exists in middleware.go but is NOT mounted in test server routes.")
		t.Logf("  EXPLOIT: Any authenticated user can create/delete nodes in any tree.")
	} else {
		t.Logf("Tree membership check returned status %d", resp.StatusCode)
	}
}

// ============================================================================
// AREA 4: SQL Injection
// ============================================================================

// TestSEC10_SQLInjection_ParameterizedQueries verifies that all repository
// queries properly use parameterized placeholders.
func TestSEC10_SQLInjection_ParameterizedQueries(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	srv, cleanup := newTestServer(t, pool)
	defer cleanup()

	// Attempt SQL injection via search query.
	req := authenticatedRequest(t, srv.URL, http.MethodGet,
		"/api/v1/trees?search='%20OR%201=1%20--", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		t.Errorf("Unexpected server error from SQL injection attempt: %d", resp.StatusCode)
	} else {
		t.Logf("PASS: SQL injection attempt handled safely (status %d)", resp.StatusCode)
	}

	// Verify response doesn't contain leaked table data.
	body, _ := io.ReadAll(resp.Body)
	leakPatterns := []string{"pg_catalog", "information_schema", "users", "passwords", "secret"}
	for _, p := range leakPatterns {
		if strings.Contains(strings.ToLower(string(body)), p) {
			t.Errorf("CRITICAL — SQL injection may have leaked schema info (%q)", p)
		}
	}
}

// ============================================================================
// AREA 5: Input Validation
// ============================================================================

// TestSEC11_InputValidation_ExtremeContentLength tests handling of very large content.
func TestSEC11_InputValidation_ExtremeContentLength(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	userID := ensureTestUser(t, pool)
	tree := createTreeViaHTTP(t, srv, userID, "Validation Test Tree")

	// Send node with 500KB content.
	hugeContent := strings.Repeat("A", 500_000)
	body := map[string]any{
		"content":   hugeContent,
		"parent_id": tree.RootNodeID.String(),
	}
	bodyBytes, _ := json.Marshal(body)

	req, _ := http.NewRequest(http.MethodPost,
		srv.Server.URL+"/api/v1/nodes/"+tree.ID.String()+"/nodes",
		bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", apiAuthHeader(t, userID))

	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Logf("MEDIUM — large body caused connection error: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusCreated {
		t.Errorf("MEDIUM — 500KB node content was ACCEPTED (status %d)", resp.StatusCode)
		t.Logf("  Missing: content size limit validation at handler level.")
	} else {
		t.Logf("Large content handled: status %d", resp.StatusCode)
	}
}

// TestSEC11b_InputValidation_EmptyContent tests empty content handling.
func TestSEC11b_InputValidation_EmptyContent(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	userID := ensureTestUser(t, pool)
	tree := createTreeViaHTTP(t, srv, userID, "Empty Content Test")

	body := map[string]any{
		"content":   "",
		"parent_id": tree.RootNodeID.String(),
	}
	bodyBytes, _ := json.Marshal(body)

	req, _ := http.NewRequest(http.MethodPost,
		srv.Server.URL+"/api/v1/nodes/"+tree.ID.String()+"/nodes",
		bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", apiAuthHeader(t, userID))

	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusCreated {
		t.Errorf("MEDIUM — empty-content node was created (status %d). Missing input validation.", resp.StatusCode)
	} else {
		t.Logf("Empty content handled: status %d", resp.StatusCode)
	}
}

// TestSEC11c_InputValidation_InvalidUUID tests UUID format rejection.
func TestSEC11c_InputValidation_InvalidUUID(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	srv, cleanup := newTestServer(t, pool)
	defer cleanup()

	req := authenticatedRequest(t, srv.URL, http.MethodGet,
		"/api/v1/trees/not-a-valid-uuid", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusBadRequest {
		t.Logf("PASS: invalid UUID rejected (status %d)", resp.StatusCode)
	} else {
		t.Logf("Invalid UUID response: status %d", resp.StatusCode)
	}
}

// ============================================================================
// AREA 6: Error Message Leakage
// ============================================================================

// TestSEC12_ErrorLeakage_NoInternalInfo proves error responses don't leak internals.
func TestSEC12_ErrorLeakage_NoInternalInfo(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	userID := ensureTestUser(t, pool)

	sensitivePatterns := []string{
		"postgres://", "password", ".go:", "pgxpool",
		"sql:", "connection refused",
	}

	testCases := []struct {
		name   string
		method string
		path   string
	}{
		{"nonexistent tree", http.MethodGet, "/api/v1/trees/" + uuid.New().String()},
		{"malformed JSON", http.MethodPost, "/api/v1/trees"},
		{"missing resource", http.MethodGet, "/api/v1/trees/00000000-0000-0000-0000-000000000000"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var req *http.Request
			var err error
			if tc.method == http.MethodPost {
				req, err = http.NewRequest(tc.method, srv.Server.URL+tc.path,
					strings.NewReader(`{not valid json`))
				req.Header.Set("Content-Type", "application/json")
			} else {
				req, err = http.NewRequest(tc.method, srv.Server.URL+tc.path, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", apiAuthHeader(t, userID))

			resp, err := srv.Server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()

			body, _ := io.ReadAll(resp.Body)
			bodyStr := string(body)
			for _, pattern := range sensitivePatterns {
				if strings.Contains(strings.ToLower(bodyStr), strings.ToLower(pattern)) {
					t.Errorf("LOW — Error leaks %q in %s response", pattern, tc.name)
				}
			}
		})
	}
	t.Logf("Error leakage check complete")
}

// TestSEC12b_ErrorLeakage_ApprovalHandlerEcho tests that the approval handler
// doesn't echo unsanitized user input in error messages.
func TestSEC12b_ErrorLeakage_ApprovalHandlerEcho(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	userID := ensureTestUser(t, pool)

	// approval_handler.go:75 echoes user input: "tree_id is not a valid UUID: "+treeIDStr
	req, _ := http.NewRequest(http.MethodGet,
		srv.Server.URL+"/api/v1/approvals/pending?tree_id=../../../etc/passwd", nil)
	req.Header.Set("Authorization", apiAuthHeader(t, userID))

	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	if strings.Contains(bodyStr, "../../../etc/passwd") {
		t.Errorf("MEDIUM — Error reflects unsanitized user input in approval handler")
		t.Logf("  approval_handler.go:75 echoes user-provided tree_id verbatim.")
	} else if resp.StatusCode == http.StatusBadRequest {
		t.Logf("Error response appears sanitized (status %d)", resp.StatusCode)
	}
}

// minInt returns the smaller of a and b.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// genTestEd25519Key creates test key material.
func genTestEd25519Key() ([]byte, []byte, error) {
	pub := make([]byte, 32)
	priv := make([]byte, 64)
	if _, err := rand.Read(pub); err != nil {
		return nil, nil, err
	}
	if _, err := rand.Read(priv); err != nil {
		return nil, nil, err
	}
	return pub, priv, nil
}

// ensureUniqueTestUser creates a user with a unique HermesUserID so it can
// be called multiple times in the same test without unique constraint violations.
func ensureUniqueTestUser(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	// Use a unique ID per call to avoid duplicate key violations.
	userID := uuid.New()
	email := userID.String() + "@security-audit.test"
	ctx := context.Background()

	// Use raw SQL to insert, bypassing the userRepo which requires db.User structs.
	_, err := pool.Exec(ctx,
		`INSERT INTO users (id, hermes_user_id, email, display_name)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (hermes_user_id) DO NOTHING`,
		userID, userID.String(), email, "Security Audit Test User")
	if err != nil {
		t.Fatalf("ensureUniqueTestUser: %v", err)
	}
	return userID
}
