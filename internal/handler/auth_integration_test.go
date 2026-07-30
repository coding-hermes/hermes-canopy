// Package handler provides HTTP handlers for Canopy REST endpoints.
// This file contains API-level integration tests for auth (JWT flow)
// and approval lifecycle. Task BE-12c.
package handler

import (
	"bytes"
	"context"
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
	"github.com/totalwindupflightsystems/hermes-canopy/internal/service"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/sse"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/sync"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/testutil"
)

// ---------------------------------------------------------------------------
// Test server helper with approval routes
// ---------------------------------------------------------------------------

// approvalTestServer is an httptest.Server wired with real repos, services,
// SSE hub, sync engine, auth middleware, and ALL CRUD + approval routes.
type approvalTestServer struct {
	Server  *httptest.Server
	Pool    *pgxpool.Pool
	UserID  uuid.UUID
	Cleanup func()
}

// newTestServerWithApprovals extends newTestServer by additionally mounting
// the approval handler. Returns the server plus the sentinel user ID used
// for JWT auth.
func newTestServerWithApprovals(t *testing.T, pool *pgxpool.Pool) *approvalTestServer {
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
	syncEngine := sync.NewEngine(eventRepo, snapshotRepo, sseHub,
		sync.DefaultEngineConfig())
	approvalSvc := service.NewApprovalService(approvalRepo, auditRepo, userRepo,
		profileRepo, memberRepo, sseHub)

	// Build chi router with all routes.
	r := chi.NewRouter()
	authMW := AuthMiddleware("canopy-dev-secret")

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(authMW)

		// Tree CRUD.
		treeHandler := NewTreeHandler(treeSvc, syncEngine)
		r.Mount("/trees", treeHandler.Routes())

		// Node CRUD.
		nodeHandler := NewNodeHandler(nodeSvc, syncEngine)
		r.Mount("/nodes", nodeHandler.Routes())

		// Graph endpoints.
		graphHandler := NewGraphHandler(graphSvc)
		r.Mount("/graph", graphHandler.Routes())

		// Approval endpoints (SPEC-API-05).
		r.Mount("/approvals", NewApprovalHandler(approvalSvc).Routes())
	})

	srv := httptest.NewServer(r)

	return &approvalTestServer{
		Server:  srv,
		Pool:    pool,
		UserID:  testUserID,
		Cleanup: func() { srv.Close() },
	}
}

// approvalAuthHeader creates a Bearer token for the given user ID.
func approvalAuthHeader(t *testing.T, userID uuid.UUID) string {
	t.Helper()
	tok := signedToken(t, "canopy-dev-secret", jwt.MapClaims{
		"sub": userID.String(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	return "Bearer " + tok
}

// approvalRequest builds an authenticated request with the given user's JWT.
func approvalRequest(t *testing.T, srvURL, method, path string, userID uuid.UUID, body any) *http.Request {
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
	r.Header.Set("Authorization", approvalAuthHeader(t, userID))
	return r
}

// ensureTestUser creates a user in the DB and returns its ID. The user's
// hermes_user_id is set to a UUID-derived string for JWT sub matching.
func ensureTestUser(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	userRepo := db.NewPGUserRepo(pool)

	userID := testUserID
	email := "test@canopy.dev"
	_, err := userRepo.Create(ctx, &db.User{
		ID:           userID,
		HermesUserID: userID.String(),
		Email:        &email,
		DisplayName:  "Test User",
	})
	if err != nil {
		t.Fatalf("ensureTestUser: create user: %v", err)
	}
	return userID
}

// createTestTree creates a tree via HTTP and returns the tree from the
// response body.
func createTestTree(t *testing.T, srv *approvalTestServer) *service.Tree {
	t.Helper()
	createBody := map[string]any{
		"title":       "BE-12c Approval Test Tree",
		"description": "Tree for approval integration tests",
		"rootMessage": map[string]any{
			"content":       "Root content for approval test",
			"contentFormat": "markdown",
			"nodeType":      "message",
		},
	}
	req := approvalRequest(t, srv.Server.URL, http.MethodPost, "/api/v1/trees", srv.UserID, createBody)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST tree: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("POST tree: status=%d, want=%d; error=%+v", resp.StatusCode, http.StatusCreated, errBody)
	}

	var tree service.Tree
	if err := json.NewDecoder(resp.Body).Decode(&tree); err != nil {
		t.Fatalf("decode tree: %v", err)
	}
	if tree.ID == uuid.Nil {
		t.Fatal("tree.ID is nil UUID")
	}
	t.Logf("created test tree: id=%s, root=%s", tree.ID, tree.RootNodeID)
	return &tree
}

// ---------------------------------------------------------------------------
// TestBE12c_UserRegistration
//
// Tests POST /api/v1/auth/register — endpoint does not exist yet.
// Skipped until auth endpoints are implemented (see SPEC-API-05 §9).
// ---------------------------------------------------------------------------

func TestBE12c_UserRegistration(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv := newTestServerWithApprovals(t, pool)
	defer srv.Cleanup()

	t.Skip("TODO(BE-12c): /api/v1/auth/register endpoint not yet implemented. " +
		"Auth handlers (registration, login, refresh) are not wired into the server. " +
		"See SPEC-API-05 §9 for the planned auth endpoint contracts.")

	// When implemented, the test should:
	//   POST /api/v1/auth/register with JSON body {"hermes_user_id": "...", "display_name": "..."}
	//   Expect 201 Created with a User JSON body and an Authorization header containing a JWT.
	_ = srv
}

// ---------------------------------------------------------------------------
// TestBE12c_UserLogin
//
// Tests POST /api/v1/auth/login — endpoint does not exist yet.
// Skipped until auth endpoints are implemented.
// ---------------------------------------------------------------------------

func TestBE12c_UserLogin(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv := newTestServerWithApprovals(t, pool)
	defer srv.Cleanup()

	t.Skip("TODO(BE-12c): /api/v1/auth/login endpoint not yet implemented. " +
		"Auth endpoints are not wired into the server. " +
		"See SPEC-API-05 §9 for the planned auth endpoint contracts.")

	// When implemented, the test should:
	//   1. Create a user in the DB (or via registration).
	//   2. POST /api/v1/auth/login with credentials or identity.
	//   3. Expect 200 OK with a JWT.
	//   4. Use the JWT to access a protected profile endpoint.
	_ = srv
}

// ---------------------------------------------------------------------------
// TestBE12c_TokenRefresh
//
// Tests POST /api/v1/auth/refresh — endpoint does not exist yet.
// Skipped until auth endpoints are implemented.
// ---------------------------------------------------------------------------

func TestBE12c_TokenRefresh(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv := newTestServerWithApprovals(t, pool)
	defer srv.Cleanup()

	t.Skip("TODO(BE-12c): /api/v1/auth/refresh endpoint not yet implemented. " +
		"Token refresh is not wired into the server. " +
		"See SPEC-API-05 §9 for the planned auth endpoint contracts.")

	// When implemented, the test should:
	//   1. Obtain a JWT (from login or registration).
	//   2. POST /api/v1/auth/refresh with the expiring token.
	//   3. Expect 200 OK with a new JWT.
	//   4. Verify the new token works on a protected endpoint.
	_ = srv
}

// ---------------------------------------------------------------------------
// TestBE12c_ApprovalCreate
//
// Creates an approval request via the service layer, then verifies it
// appears in the pending list via HTTP. There is no HTTP endpoint for
// creating approvals yet — RequestApproval is a service-level call.
// ---------------------------------------------------------------------------

func TestBE12c_ApprovalCreate(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv := newTestServerWithApprovals(t, pool)
	defer srv.Cleanup()

	// Create a test user (owner of the approval).
	ownerID := ensureTestUser(t, pool)
	t.Logf("owner user: %s", ownerID)

	// Create a tree.
	tree := createTestTree(t, srv)

	// Get the root node ID from the tree.
	if tree.RootNodeID == uuid.Nil {
		t.Fatal("tree has nil RootNodeID")
	}
	nodeID := tree.RootNodeID

	// Create a child node (so we have a distinct node to approve).
	nodeBody := map[string]any{
		"content":        "Child node for approval test",
		"content_format": "markdown",
		"node_type":      "message",
		"parent_id":      nodeID.String(),
	}
	req := approvalRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/nodes/"+tree.ID.String()+"/nodes", srv.UserID, nodeBody)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST child node: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST child node: status=%d, body=%s", resp.StatusCode, string(bodyBytes))
	}

	var createResult service.CreateNodeResult
	if err := json.NewDecoder(resp.Body).Decode(&createResult); err != nil {
		t.Fatalf("decode create node result: %v", err)
	}
	if createResult.Node == nil {
		t.Fatal("create node result has nil Node")
	}
	childNodeID := createResult.Node.ID
	t.Logf("created child node: %s", childNodeID)

	// Create an approval via the service layer (no HTTP endpoint for this).
	approvalRepo := db.NewPGApprovalRepo(pool)
	auditRepo := db.NewPGAuditRepo(pool)
	userRepo := db.NewPGUserRepo(pool)
	profileRepo := db.NewPGProfileRepo(pool)
	memberRepo := db.NewPGTreeMemberRepo(pool)
	sseHub := sse.NewHub()
	approvalSvc := service.NewApprovalService(approvalRepo, auditRepo, userRepo,
		profileRepo, memberRepo, sseHub)

	created, err := approvalSvc.RequestApproval(context.Background(),
		tree.ID, childNodeID, ownerID, srv.UserID)
	if err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}
	if created.ID == uuid.Nil {
		t.Fatal("created approval has nil ID")
	}
	if created.Status != "pending" {
		t.Fatalf("approval status = %q, want pending", created.Status)
	}
	t.Logf("created approval: id=%s, status=%s", created.ID, created.Status)

	// Verify the approval appears in the pending list via HTTP.
	// The owner's JWT matches the ownerID so pending approvals should be visible.
	req = approvalRequest(t, srv.Server.URL, http.MethodGet,
		"/api/v1/approvals/pending", ownerID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("GET pending approvals: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("GET pending approvals: status=%d; error=%+v", resp.StatusCode, errBody)
	}

	var pendingResp struct {
		Approvals []db.Approval `json:"approvals"`
		Total     int           `json:"total"`
		Limit     int           `json:"limit"`
		Offset    int           `json:"offset"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pendingResp); err != nil {
		t.Fatalf("decode pending: %v", err)
	}
	if pendingResp.Total < 1 {
		t.Fatal("expected at least 1 pending approval, got 0")
	}
	found := false
	for _, a := range pendingResp.Approvals {
		if a.ID == created.ID {
			found = true
			if a.Status != db.ApprovalStatusPending {
				t.Fatalf("approval %s status=%s, want pending", a.ID, a.Status)
			}
			break
		}
	}
	if !found {
		t.Fatalf("created approval %s not found in pending list", created.ID)
	}
	t.Logf("verified approval %s is pending (total=%d)", created.ID, pendingResp.Total)

	// Also verify we can GET the individual approval.
	req = approvalRequest(t, srv.Server.URL, http.MethodGet,
		"/api/v1/approvals/"+created.ID.String(), ownerID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("GET approval: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("GET approval: status=%d; error=%+v", resp.StatusCode, errBody)
	}

	var fetched db.Approval
	if err := json.NewDecoder(resp.Body).Decode(&fetched); err != nil {
		t.Fatalf("decode approval: %v", err)
	}
	if fetched.ID != created.ID {
		t.Fatalf("fetched approval ID = %s, want %s", fetched.ID, created.ID)
	}
}

// ---------------------------------------------------------------------------
// TestBE12c_ApprovalApproveDeny
//
// Creates an approval request, approves it via HTTP, then verifies the
// status change. Also tests deny flow on a second approval.
// ---------------------------------------------------------------------------

func TestBE12c_ApprovalApproveDeny(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv := newTestServerWithApprovals(t, pool)
	defer srv.Cleanup()

	// Create a test user (owner of the approval).
	ownerID := ensureTestUser(t, pool)

	// Create a tree.
	tree := createTestTree(t, srv)
	nodeID := tree.RootNodeID

	// Create a child node.
	nodeBody := map[string]any{
		"content":        "Child node for approve/deny test",
		"content_format": "markdown",
		"node_type":      "message",
		"parent_id":      nodeID.String(),
	}
	req := approvalRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/nodes/"+tree.ID.String()+"/nodes", srv.UserID, nodeBody)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST child node: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST child node: status=%d, body=%s", resp.StatusCode, string(bodyBytes))
	}

	var createResult service.CreateNodeResult
	json.NewDecoder(resp.Body).Decode(&createResult)
	childNodeID := createResult.Node.ID
	t.Logf("created child node: %s", childNodeID)

	// Create an approval via the service layer.
	approvalRepo := db.NewPGApprovalRepo(pool)
	auditRepo := db.NewPGAuditRepo(pool)
	userRepo := db.NewPGUserRepo(pool)
	profileRepo := db.NewPGProfileRepo(pool)
	memberRepo := db.NewPGTreeMemberRepo(pool)
	sseHub := sse.NewHub()
	approvalSvc := service.NewApprovalService(approvalRepo, auditRepo, userRepo,
		profileRepo, memberRepo, sseHub)

	created, err := approvalSvc.RequestApproval(context.Background(),
		tree.ID, childNodeID, ownerID, srv.UserID)
	if err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}
	t.Logf("created approval: id=%s, status=%s", created.ID, created.Status)

	// --- Approve via HTTP ---
	req = approvalRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/approvals/"+created.ID.String()+"/approve", ownerID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST approve: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("POST approve: status=%d, want=%d; error=%+v",
			resp.StatusCode, http.StatusOK, errBody)
	}

	var approved db.Approval
	if err := json.NewDecoder(resp.Body).Decode(&approved); err != nil {
		t.Fatalf("decode approved: %v", err)
	}
	if approved.Status != db.ApprovalStatusApproved {
		t.Fatalf("approved status = %q, want approved", approved.Status)
	}
	if approved.DecidedBy == nil || *approved.DecidedBy != ownerID {
		t.Fatalf("DecidedBy = %v, want %s", approved.DecidedBy, ownerID)
	}
	t.Logf("approved successfully: status=%s, decided_by=%v", approved.Status, approved.DecidedBy)

	// Approving again should return 409 Conflict (already decided).
	req = approvalRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/approvals/"+created.ID.String()+"/approve", ownerID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST approve (2nd): %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("POST approve (2nd): status=%d, want=%d", resp.StatusCode, http.StatusConflict)
	}
	t.Logf("correctly rejected duplicate approve with 409")

	// --- Deny test on a separate approval ---
	// Create a second child node.
	nodeBody2 := map[string]any{
		"content":        "Second child node for deny test",
		"content_format": "markdown",
		"node_type":      "message",
		"parent_id":      nodeID.String(),
	}
	req = approvalRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/nodes/"+tree.ID.String()+"/nodes", srv.UserID, nodeBody2)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST child node 2: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST child node 2: status=%d", resp.StatusCode)
	}
	var createResult2 service.CreateNodeResult
	json.NewDecoder(resp.Body).Decode(&createResult2)
	childNodeID2 := createResult2.Node.ID
	t.Logf("created second child node: %s", childNodeID2)

	created2, err := approvalSvc.RequestApproval(context.Background(),
		tree.ID, childNodeID2, ownerID, srv.UserID)
	if err != nil {
		t.Fatalf("RequestApproval 2: %v", err)
	}
	t.Logf("created second approval: id=%s", created2.ID)

	// Deny via HTTP.
	denyBody := map[string]string{"reason": "Not needed at this time"}
	req = approvalRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/approvals/"+created2.ID.String()+"/deny", ownerID, denyBody)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST deny: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("POST deny: status=%d, want=%d; error=%+v",
			resp.StatusCode, http.StatusOK, errBody)
	}

	var denied db.Approval
	if err := json.NewDecoder(resp.Body).Decode(&denied); err != nil {
		t.Fatalf("decode denied: %v", err)
	}
	if denied.Status != db.ApprovalStatusDenied {
		t.Fatalf("denied status = %q, want denied", denied.Status)
	}
	if denied.DeniedReason == nil || *denied.DeniedReason != "Not needed at this time" {
		t.Fatalf("DeniedReason = %v, want 'Not needed at this time'", denied.DeniedReason)
	}
	t.Logf("denied successfully: status=%s, reason=%v", denied.Status, denied.DeniedReason)

	// Pending list should now be empty (both decided).
	req = approvalRequest(t, srv.Server.URL, http.MethodGet,
		"/api/v1/approvals/pending", ownerID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("GET pending after decisions: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET pending after decisions: status=%d", resp.StatusCode)
	}
	var pendingResp struct {
		Approvals []db.Approval `json:"approvals"`
		Total     int           `json:"total"`
	}
	json.NewDecoder(resp.Body).Decode(&pendingResp)
	if pendingResp.Total != 0 {
		t.Fatalf("pending total = %d, want 0 after deciding both", pendingResp.Total)
	}
	t.Logf("pending list is empty after deciding both approvals")
}

// ---------------------------------------------------------------------------
// TestBE12c_ApprovalAuditTrail
//
// Creates an approval, approves it, and verifies that audit log entries
// were created for both the creation and the approval.
// ---------------------------------------------------------------------------

func TestBE12c_ApprovalAuditTrail(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv := newTestServerWithApprovals(t, pool)
	defer srv.Cleanup()

	ownerID := ensureTestUser(t, pool)
	tree := createTestTree(t, srv)
	nodeID := tree.RootNodeID

	// Create a child node.
	nodeBody := map[string]any{
		"content":        "Audit trail test node",
		"content_format": "markdown",
		"node_type":      "message",
		"parent_id":      nodeID.String(),
	}
	req := approvalRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/nodes/"+tree.ID.String()+"/nodes", srv.UserID, nodeBody)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST child node: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST child node: status=%d", resp.StatusCode)
	}
	var createResult service.CreateNodeResult
	json.NewDecoder(resp.Body).Decode(&createResult)
	childNodeID := createResult.Node.ID

	// Create and approve the approval via service.
	approvalRepo := db.NewPGApprovalRepo(pool)
	auditRepo := db.NewPGAuditRepo(pool)
	userRepo := db.NewPGUserRepo(pool)
	profileRepo := db.NewPGProfileRepo(pool)
	memberRepo := db.NewPGTreeMemberRepo(pool)
	sseHub := sse.NewHub()
	approvalSvc := service.NewApprovalService(approvalRepo, auditRepo, userRepo,
		profileRepo, memberRepo, sseHub)

	created, err := approvalSvc.RequestApproval(context.Background(),
		tree.ID, childNodeID, ownerID, srv.UserID)
	if err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}
	t.Logf("created approval: %s", created.ID)

	approved, err := approvalSvc.Approve(context.Background(), created.ID, ownerID)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	t.Logf("approved: %s, status=%s", approved.ID, approved.Status)

	// Verify audit log entries via HTTP history endpoint.
	req = approvalRequest(t, srv.Server.URL, http.MethodGet,
		"/api/v1/approvals/history?approval_id="+created.ID.String(), ownerID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("GET history: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("GET history: status=%d; error=%+v", resp.StatusCode, errBody)
	}

	var historyResp struct {
		Entries []db.AuditEntry `json:"entries"`
		Limit   int             `json:"limit"`
		Offset  int             `json:"offset"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&historyResp); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if len(historyResp.Entries) < 2 {
		t.Fatalf("expected at least 2 audit entries, got %d", len(historyResp.Entries))
	}
	t.Logf("audit trail has %d entries", len(historyResp.Entries))

	// Verify the audit entry actions.
	var hasRequested, hasApproved bool
	for _, e := range historyResp.Entries {
		t.Logf("  audit entry: action=%s, id=%s", e.Action, e.ID)
		switch e.Action {
		case db.AuditActionApprovalRequested:
			hasRequested = true
		case db.AuditActionApprovalGranted:
			hasApproved = true
			if e.NewStatus == nil || *e.NewStatus != "approved" {
				t.Fatalf("audit NewStatus = %v, want approved", e.NewStatus)
			}
		}
	}
	if !hasRequested {
		t.Fatal("audit trail missing approval_requested entry")
	}
	if !hasApproved {
		t.Fatal("audit trail missing approval_granted entry")
	}
	t.Logf("audit trail verified: has requested=%v, has approved=%v", hasRequested, hasApproved)

	// Also verify tree-scoped history.
	req = approvalRequest(t, srv.Server.URL, http.MethodGet,
		"/api/v1/approvals/history?tree_id="+tree.ID.String(), ownerID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("GET history by tree: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET history by tree: status=%d", resp.StatusCode)
	}
	var treeHistory struct {
		Entries []db.AuditEntry `json:"entries"`
	}
	json.NewDecoder(resp.Body).Decode(&treeHistory)
	if len(treeHistory.Entries) < 2 {
		t.Fatalf("expected at least 2 audit entries by tree, got %d", len(treeHistory.Entries))
	}
	t.Logf("tree-scoped history: %d entries", len(treeHistory.Entries))
}

// ---------------------------------------------------------------------------
// TestBE12c_AuthIntegration
//
// Full integration flow:
//   1. Create a test user in the DB
//   2. Use JWT for that user to create a tree
//   3. Create an approval via the service layer
//   4. Approve the approval via HTTP
//   5. Verify the tree is still accessible (integration is working)
// ---------------------------------------------------------------------------

func TestBE12c_AuthIntegration(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv := newTestServerWithApprovals(t, pool)
	defer srv.Cleanup()

	// 1. Create a test user.
	ownerID := ensureTestUser(t, pool)
	t.Logf("step 1: created user %s", ownerID)

	// 2. Create a tree via HTTP (using the user's JWT).
	tree := createTestTree(t, srv)
	t.Logf("step 2: created tree %s", tree.ID)

	// 3. Create a child node.
	nodeBody := map[string]any{
		"content":        "Integration test child node",
		"content_format": "markdown",
		"node_type":      "message",
		"parent_id":      tree.RootNodeID.String(),
	}
	req := approvalRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/nodes/"+tree.ID.String()+"/nodes", srv.UserID, nodeBody)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST child node: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST child node: status=%d", resp.StatusCode)
	}
	var createResult service.CreateNodeResult
	json.NewDecoder(resp.Body).Decode(&createResult)
	childNodeID := createResult.Node.ID
	t.Logf("step 3: created child node %s", childNodeID)

	// 4. Create an approval via the service layer.
	approvalRepo := db.NewPGApprovalRepo(pool)
	auditRepo := db.NewPGAuditRepo(pool)
	userRepo := db.NewPGUserRepo(pool)
	profileRepo := db.NewPGProfileRepo(pool)
	memberRepo := db.NewPGTreeMemberRepo(pool)
	sseHub := sse.NewHub()
	approvalSvc := service.NewApprovalService(approvalRepo, auditRepo, userRepo,
		profileRepo, memberRepo, sseHub)

	created, err := approvalSvc.RequestApproval(context.Background(),
		tree.ID, childNodeID, ownerID, srv.UserID)
	if err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}
	t.Logf("step 4: created approval %s (status=%s)", created.ID, created.Status)

	// 5. Approve via HTTP (as owner).
	req = approvalRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/approvals/"+created.ID.String()+"/approve", ownerID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST approve: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("POST approve: status=%d; error=%+v", resp.StatusCode, errBody)
	}
	t.Logf("step 5: approved successfully")

	// 6. Verify the tree is still accessible (integration working).
	req = approvalRequest(t, srv.Server.URL, http.MethodGet,
		"/api/v1/trees/"+tree.ID.String(), srv.UserID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("GET tree after flow: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET tree after flow: status=%d, want=%d", resp.StatusCode, http.StatusOK)
	}
	t.Logf("step 6: tree still accessible after full approval flow")

	// 7. Verify audit trail has entries.
	req = approvalRequest(t, srv.Server.URL, http.MethodGet,
		"/api/v1/approvals/history?approval_id="+created.ID.String(), ownerID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("GET history: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET history: status=%d", resp.StatusCode)
	}
	var historyResp struct {
		Entries []db.AuditEntry `json:"entries"`
	}
	json.NewDecoder(resp.Body).Decode(&historyResp)
	if len(historyResp.Entries) < 2 {
		t.Fatalf("expected at least 2 audit entries in full flow, got %d", len(historyResp.Entries))
	}
	t.Logf("step 7: audit trail has %d entries — integration flow complete", len(historyResp.Entries))
}
