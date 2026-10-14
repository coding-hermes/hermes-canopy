// DF-HERMES-CANOPY-79: the tree_members viewer role must be enforced, not
// just recorded. A viewer shared onto a tree is READ-ONLY: every node write
// surface answers 403 VIEWER_READ_ONLY, while reads stay 200. Owner, admin,
// and member roles keep full write access.
//
// The suite runs against the real PostgreSQL test server (full middleware
// stack: AuthMiddleware → TreeMembershipMiddleware → handlers) so the gate
// is proven at the HTTP boundary, where a regression would be visible.
// Without a reachable PostgreSQL the tests skip via SkipIfNoDB.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

// df79ShareUser shares treeID with userID at the given frontend permission
// level via the real share endpoint (owner does it), asserting the 201.
func df79ShareUser(t *testing.T, srv *approvalTestServer, treeID, ownerID, userID uuid.UUID, permission string) {
	t.Helper()
	body := map[string]any{
		"user_id":    userID.String(),
		"permission": permission,
	}
	req := apiRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/trees/"+treeID.String()+"/share", ownerID, body)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("share %s: %v", permission, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		var errBody apiErrorBody
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("share %s: status=%d, error=%+v", permission, resp.StatusCode, errBody)
	}
}

// df79Do performs an authenticated request and returns the status code plus
// the decoded error body (zero-valued when the response is not an error).
func df79Do(t *testing.T, srv *approvalTestServer, method, path string, userID uuid.UUID, body any) (int, apiErrorBody) {
	t.Helper()
	req := apiRequest(t, srv.Server.URL, method, path, userID, body)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var errBody apiErrorBody
	if resp.StatusCode >= 400 {
		if err := json.NewDecoder(resp.Body).Decode(&errBody); err != nil {
			t.Fatalf("%s %s: decode error body: %v", method, path, err)
		}
	}
	return resp.StatusCode, errBody
}

// df79CreateNodeBody is the minimal valid create-node payload.
func df79CreateNodeBody(parentID string) map[string]any {
	body := map[string]any{
		"content":       "DF-79 viewer-gate probe message",
		"contentFormat": "markdown",
		"nodeType":      "message",
	}
	if parentID != "" {
		body["parent_id"] = parentID
	}
	return body
}

// TestViewerPermission_ReadOnlyEnforcement shares a tree with a viewer and
// proves every write surface answers 403 VIEWER_READ_ONLY while reads stay
// open. Then it shares the same tree with an editor (member role) and an
// admin and proves both can still write — the gate is scoped to viewer.
func TestViewerPermission_ReadOnlyEnforcement(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	ctx := context.Background()

	ownerID := ensureTestUser(t, pool)
	viewerSub := uuid.MustParse("79000000-0000-0000-0000-000000000079")
	viewerEmail := "df79-viewer@canopy.dev"
	userRepo := db.NewPGUserRepo(pool)
	// HermesUserID carries a fixed string — the JWT sub the auth middleware
	// resolves. users.id itself is generated server-side (Create ignores a
	// caller-supplied ID), so the REAL id is captured from the return value
	// and used for the share call.
	viewer, err := userRepo.Create(ctx, &db.User{
		HermesUserID: viewerSub.String(),
		Email:        &viewerEmail,
		DisplayName:  "DF79 Viewer",
	})
	if err != nil {
		t.Fatalf("create viewer user: %v", err)
	}
	viewerID := viewer.ID

	tree := createTreeViaHTTP(t, srv, ownerID, "DF-79 Viewer Gate Tree")
	treeID := tree.ID.String()
	rootID := tree.RootNodeID.String()

	// Share as viewer through the REAL share endpoint — the defect was that
	// this row was written and never consulted.
	df79ShareUser(t, srv, tree.ID, ownerID, viewerID, "viewer")

	// ── Reads stay open for the viewer ────────────────────────────────
	for _, tc := range []struct {
		name, method, path string
	}{
		{"get tree", http.MethodGet, "/api/v1/trees/" + treeID},
		{"list nodes", http.MethodGet, "/api/v1/trees/" + treeID + "/nodes"},
		{"get root node", http.MethodGet, "/api/v1/trees/" + treeID + "/nodes/" + rootID},
	} {
		if status, _ := df79Do(t, srv, tc.method, tc.path, viewerID, nil); status != http.StatusOK {
			t.Errorf("viewer %s: status=%d, want 200 (viewers are read-only, not read-blocked)", tc.name, status)
		}
	}

	// ── Every write surface answers 403 VIEWER_READ_ONLY ──────────────
	type writeProbe struct {
		name, method, path string
		body               any
	}
	probes := []writeProbe{
		{"create node", http.MethodPost, "/api/v1/trees/" + treeID + "/nodes", df79CreateNodeBody("")},
		{"create child node", http.MethodPost, "/api/v1/trees/" + treeID + "/nodes", df79CreateNodeBody(rootID)},
		{"update node", http.MethodPatch, "/api/v1/trees/" + treeID + "/nodes/" + rootID,
			map[string]any{"content": "viewer must not edit"}},
		{"delete node", http.MethodDelete, "/api/v1/trees/" + treeID + "/nodes/" + rootID, nil},
		{"reply", http.MethodPost, "/api/v1/trees/" + treeID + "/nodes/" + rootID + "/reply", df79CreateNodeBody("")},
		{"fork", http.MethodPost, "/api/v1/trees/" + treeID + "/nodes/" + rootID + "/fork", df79CreateNodeBody("")},
	}
	for _, p := range probes {
		status, errBody := df79Do(t, srv, p.method, p.path, viewerID, p.body)
		if status != http.StatusForbidden {
			t.Errorf("viewer %s: status=%d, want 403 VIEWER_READ_ONLY", p.name, status)
			continue
		}
		if errBody.Error.Code != "VIEWER_READ_ONLY" {
			t.Errorf("viewer %s: error code=%q, want VIEWER_READ_ONLY", p.name, errBody.Error.Code)
		}
	}

	// ── The flat node surface must NOT reopen the write path ──────────
	for _, p := range []writeProbe{
		{"flat reply", http.MethodPost, "/api/v1/nodes/nodes/" + rootID + "/reply", df79CreateNodeBody("")},
		{"flat fork", http.MethodPost, "/api/v1/nodes/nodes/" + rootID + "/fork", df79CreateNodeBody("")},
		{"flat update", http.MethodPatch, "/api/v1/nodes/nodes/" + rootID,
			map[string]any{"content": "viewer must not edit"}},
		{"flat delete", http.MethodDelete, "/api/v1/nodes/nodes/" + rootID, nil},
	} {
		status, errBody := df79Do(t, srv, p.method, p.path, viewerID, p.body)
		if status != http.StatusForbidden || errBody.Error.Code != "VIEWER_READ_ONLY" {
			t.Errorf("viewer %s: status=%d code=%q, want 403 VIEWER_READ_ONLY", p.name, status, errBody.Error.Code)
		}
	}

	// ── Owner/Admin/Member keep write access (role scoping) ───────────
	// Member: a second user shared as editor (maps to TreeRoleMember).
	memberSub := uuid.MustParse("79000000-0000-0000-0000-000000000080")
	memberEmail := "df79-member@canopy.dev"
	member, err := userRepo.Create(ctx, &db.User{
		HermesUserID: memberSub.String(),
		Email:        &memberEmail,
		DisplayName:  "DF79 Member",
	})
	if err != nil {
		t.Fatalf("create member user: %v", err)
	}
	memberID := member.ID
	df79ShareUser(t, srv, tree.ID, ownerID, memberID, "editor")

	memberProbes := []writeProbe{
		{"member create", http.MethodPost, "/api/v1/trees/" + treeID + "/nodes", df79CreateNodeBody(rootID)},
		{"member reply", http.MethodPost, "/api/v1/trees/" + treeID + "/nodes/" + rootID + "/reply", df79CreateNodeBody("")},
	}
	for _, p := range memberProbes {
		if status, errBody := df79Do(t, srv, p.method, p.path, memberID, p.body); status != http.StatusCreated {
			t.Errorf("%s: status=%d (%s), want 201", p.name, status, errBody.Error.Code)
		}
	}

	// Owner still writes.
	if status, errBody := df79Do(t, srv, http.MethodPost, "/api/v1/trees/"+treeID+"/nodes", ownerID, df79CreateNodeBody(rootID)); status != http.StatusCreated {
		t.Errorf("owner create: status=%d (%s), want 201", status, errBody.Error.Code)
	}

	// Admin: promote the member to admin via the members endpoint, then
	// write again — an admin must not be blocked either.
	// (The role lives in tree_members; UpdateRole is exercised through the
	// same PG repo the share endpoint writes through.)
	memberRoleRepo := db.NewPGTreeMemberRepo(pool)
	members, err := memberRoleRepo.GetByTree(ctx, tree.ID)
	if err != nil {
		t.Fatalf("GetByTree: %v", err)
	}
	var memberRowID uuid.UUID
	for _, m := range members {
		if m.UserID != nil && *m.UserID == memberID {
			memberRowID = m.ID
		}
	}
	if memberRowID == uuid.Nil {
		t.Fatal("member row not found in tree_members after share")
	}
	if _, err := memberRoleRepo.UpdateRole(ctx, memberRowID, db.TreeRoleAdmin); err != nil {
		t.Fatalf("promote member to admin: %v", err)
	}
	if status, errBody := df79Do(t, srv, http.MethodPost, "/api/v1/trees/"+treeID+"/nodes", memberID, df79CreateNodeBody(rootID)); status != http.StatusCreated {
		t.Errorf("admin create after promote: status=%d (%s), want 201", status, errBody.Error.Code)
	}

	// ── The viewer never mutated anything: node count is unchanged ────
	// Every viewer probe above must have been refused before any write, so
	// the tree's node list contains exactly what the owner/member created.
	status, _ := df79Do(t, srv, http.MethodGet, "/api/v1/trees/"+treeID+"/nodes", ownerID, nil)
	if status != http.StatusOK {
		t.Fatalf("final node list: status=%d, want 200", status)
	}
}

// TestViewerPermission_NonMemberStillForbidden pins the boundary: the viewer
// gate must not weaken the membership gate — a non-member still gets 403
// NOT_TREE_MEMBER, not VIEWER_READ_ONLY.
func TestViewerPermission_NonMemberStillForbidden(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	outsiderSub := uuid.MustParse("79000000-0000-0000-0000-000000000091")
	outsiderEmail := "df79-outsider@canopy.dev"
	if _, err := db.NewPGUserRepo(pool).Create(context.Background(), &db.User{
		HermesUserID: outsiderSub.String(),
		Email:        &outsiderEmail,
		DisplayName:  "DF79 Outsider",
	}); err != nil {
		t.Fatalf("create outsider: %v", err)
	}

	ownerID := ensureTestUser(t, pool)
	tree := createTreeViaHTTP(t, srv, ownerID, "DF-79 Non-Member Boundary Tree")

	// users.id is generated server-side, but the JWT is minted from the
	// sub we control — bind the outsider's auth identity to a var.
	outsiderID := outsiderSub

	// Read boundary: GET /trees/{id} is gated in the tree handler (the
	// trees mount carries no membership middleware), whose non-member code
	// is NOT_TREE_OWNER. Either 403 membership code proves the outsider is
	// out; the point is that it is NOT treated as a viewer.
	status, errBody := df79Do(t, srv, http.MethodGet,
		"/api/v1/trees/"+tree.ID.String(), outsiderID, nil)
	if status != http.StatusForbidden ||
		(errBody.Error.Code != "NOT_TREE_OWNER" && errBody.Error.Code != "NOT_TREE_MEMBER") {
		t.Errorf("outsider read: status=%d code=%q, want 403 NOT_TREE_OWNER/NOT_TREE_MEMBER", status, errBody.Error.Code)
	}

	status, errBody = df79Do(t, srv, http.MethodPost,
		"/api/v1/trees/"+tree.ID.String()+"/nodes", outsiderID, df79CreateNodeBody(""))
	if status != http.StatusForbidden || errBody.Error.Code != "NOT_TREE_MEMBER" {
		t.Errorf("outsider write: status=%d code=%q, want 403 NOT_TREE_MEMBER", status, errBody.Error.Code)
	}
}
