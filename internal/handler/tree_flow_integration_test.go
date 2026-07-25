// Package handler provides HTTP handlers for Canopy REST endpoints.
// This file contains INT-01: End-to-end tree flow integration tests
// exercising the full tree lifecycle against a real PostgreSQL instance.
package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/service"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/sse"
	"github.com/totalwindupflightsystems/hermes-canopy/internal/testutil"
)

// ---------------------------------------------------------------------------
// INT-01: Full tree lifecycle end-to-end
//
// Covers the complete flow:
//   1. POST /api/v1/trees → create a tree
//   2. POST /api/v1/nodes/{tree_id}/nodes → add child nodes
//   3. PATCH /api/v1/nodes/nodes/{node_id} → edit node content
//   4. Create approval via service layer → POST /api/v1/approvals/{id}/approve
//   5. POST /api/v1/approvals/{id}/approve → approve the pending approval
//   6. GET /api/v1/approvals/history → verify audit trail
//   7. GET /api/v1/trees/{tree_id} → verify tree integrity after full flow
// ---------------------------------------------------------------------------

func TestINT01_FullTreeFlow(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv := newTestServerWithApprovals(t, pool)
	defer srv.Cleanup()

	// Create a test user (owner of the approval).
	ownerID := ensureTestUser(t, pool)
	t.Logf("created test user: %s", ownerID)

	// ── Step 1: Create a tree via HTTP ──────────────────────────────────
	createBody := map[string]any{
		"title":       "INT-01 Full Tree Flow Test",
		"description": "End-to-end tree flow integration test",
		"rootMessage": map[string]any{
			"content":       "# Root Node\n\nThis is the root of the INT-01 test tree.",
			"contentFormat": "markdown",
			"nodeType":      "message",
		},
	}
	req := approvalRequest(t, srv.Server.URL, http.MethodPost, "/api/v1/trees",
		srv.UserID, createBody)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("step 1 — POST trees: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("step 1 — POST trees: status=%d, want %d; error=%+v",
			resp.StatusCode, http.StatusCreated, errBody)
	}

	var tree service.Tree
	if err := json.NewDecoder(resp.Body).Decode(&tree); err != nil {
		t.Fatalf("step 1 — decode tree: %v", err)
	}
	if tree.ID == uuid.Nil {
		t.Fatal("step 1 — tree.ID is nil UUID")
	}
	if tree.Title != "INT-01 Full Tree Flow Test" {
		t.Fatalf("step 1 — tree.Title = %q, want %q",
			tree.Title, "INT-01 Full Tree Flow Test")
	}
	if tree.RootNodeID == uuid.Nil {
		t.Fatal("step 1 — tree.RootNodeID is nil UUID")
	}
	location := resp.Header.Get("Location")
	if location == "" {
		t.Fatal("step 1 — Location header missing on create")
	}
	t.Logf("step 1 ✓ created tree: id=%s, root=%s, location=%s",
		tree.ID, tree.RootNodeID, location)

	// ── Step 2: Add child nodes via HTTP ─────────────────────────────────
	// Create multiple child nodes under the root to test the DAG structure.
	nodeContents := []string{
		"## First Child\n\nThis is the first child node under the root.",
		"## Second Child\n\nThis is a second branch from the root node.",
		"## Third Child\n\nThird branch with additional content for testing.",
	}
	var childNodeIDs []uuid.UUID
	for i, content := range nodeContents {
		nodeBody := map[string]any{
			"content":        content,
			"content_format": "markdown",
			"node_type":      "message",
			"parent_id":      tree.RootNodeID.String(),
		}
		req = approvalRequest(t, srv.Server.URL, http.MethodPost,
			"/api/v1/nodes/"+tree.ID.String()+"/nodes",
			srv.UserID, nodeBody)
		resp, err = srv.Server.Client().Do(req)
		if err != nil {
			t.Fatalf("step 2 — POST node %d: %v", i+1, err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusCreated {
			bodyBytes, _ := io.ReadAll(resp.Body)
			t.Fatalf("step 2 — POST node %d: status=%d, body=%s",
				i+1, resp.StatusCode, string(bodyBytes))
		}

		var createResult service.CreateNodeResult
		if err := json.NewDecoder(resp.Body).Decode(&createResult); err != nil {
			t.Fatalf("step 2 — decode node %d: %v", i+1, err)
		}
		if createResult.Node == nil {
			t.Fatalf("step 2 — node %d has nil Node", i+1)
		}
		if createResult.Node.ID == uuid.Nil {
			t.Fatalf("step 2 — node %d has nil ID", i+1)
		}
		if createResult.Edge == nil {
			t.Fatalf("step 2 — node %d has nil Edge", i+1)
		}
		childNodeIDs = append(childNodeIDs, createResult.Node.ID)
		t.Logf("step 2 ✓ created child node %d: id=%s, content=%q",
			i+1, createResult.Node.ID, createResult.Node.Content[:40]+"...")
	}

	// Verify node retrieval via HTTP GET.
	for i, nodeID := range childNodeIDs {
		req = approvalRequest(t, srv.Server.URL, http.MethodGet,
			"/api/v1/nodes/"+tree.ID.String()+"/nodes/"+nodeID.String(),
			srv.UserID, nil)
		resp, err = srv.Server.Client().Do(req)
		if err != nil {
			t.Fatalf("step 2 — GET node %d: %v", i+1, err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("step 2 — GET node %d: status=%d, want %d",
				i+1, resp.StatusCode, http.StatusOK)
		}

		var nodeDetail service.NodeDetail
		if err := json.NewDecoder(resp.Body).Decode(&nodeDetail); err != nil {
			t.Fatalf("step 2 — decode node detail %d: %v", i+1, err)
		}
		if nodeDetail.ID != nodeID {
			t.Fatalf("step 2 — node %d ID mismatch: got %s, want %s",
				i+1, nodeDetail.ID, nodeID)
		}
		if nodeDetail.Content != nodeContents[i] {
			t.Fatalf("step 2 — node %d content mismatch", i+1)
		}
		t.Logf("step 2 ✓ verified node %d: content matches", i+1)
	}

	// ── Step 3: Edit node content via HTTP PATCH ────────────────────────
	targetNodeID := childNodeIDs[0]
	newContent := "## Updated First Child\n\nThis content has been modified via PATCH.\n\n### Added Section\nExtra content for the updated node."
	updateBody := map[string]any{
		"content": newContent,
	}
	req = approvalRequest(t, srv.Server.URL, http.MethodPatch,
		"/api/v1/nodes/nodes/"+targetNodeID.String(),
		srv.UserID, updateBody)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("step 3 — PATCH node: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("step 3 — PATCH node: status=%d, want %d; error=%+v",
			resp.StatusCode, http.StatusOK, errBody)
	}

	var updatedNode service.NodeDetail
	if err := json.NewDecoder(resp.Body).Decode(&updatedNode); err != nil {
		t.Fatalf("step 3 — decode updated node: %v", err)
	}
	if updatedNode.Content != newContent {
		t.Fatalf("step 3 — updated.Content = %q, want %q",
			updatedNode.Content, newContent)
	}
	if updatedNode.ID != targetNodeID {
		t.Fatalf("step 3 — updated node ID mismatch: got %s, want %s",
			updatedNode.ID, targetNodeID)
	}
	t.Logf("step 3 ✓ edited node %s: content updated successfully", targetNodeID)

	// Verify the edit persisted.
	req = approvalRequest(t, srv.Server.URL, http.MethodGet,
		"/api/v1/nodes/"+tree.ID.String()+"/nodes/"+targetNodeID.String(),
		srv.UserID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("step 3 — GET edited node: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("step 3 — GET edited node: status=%d", resp.StatusCode)
	}
	var refetched service.NodeDetail
	json.NewDecoder(resp.Body).Decode(&refetched)
	if refetched.Content != newContent {
		t.Fatalf("step 3 — persisted content = %q, want %q",
			refetched.Content, newContent)
	}
	t.Logf("step 3 ✓ edit persisted: content verified on re-fetch")

	// ── Step 4: Create approval via service layer ───────────────────────
	// (No HTTP endpoint for RequestApproval yet; use service layer to
	// create, then verify and approve via HTTP.)
	approvalRepo := db.NewPGApprovalRepo(pool)
	auditRepo := db.NewPGAuditRepo(pool)
	userRepo := db.NewPGUserRepo(pool)
	profileRepo := db.NewPGProfileRepo(pool)
	memberRepo := db.NewPGTreeMemberRepo(pool)
	sseHub := sse.NewHub()
	approvalSvc := service.NewApprovalService(approvalRepo, auditRepo, userRepo,
		profileRepo, memberRepo, sseHub)

	created, err := approvalSvc.RequestApproval(context.Background(),
		tree.ID, targetNodeID, ownerID, srv.UserID)
	if err != nil {
		t.Fatalf("step 4 — RequestApproval: %v", err)
	}
	if created.ID == uuid.Nil {
		t.Fatal("step 4 — created approval has nil ID")
	}
	if created.Status != db.ApprovalStatusPending {
		t.Fatalf("step 4 — approval status = %q, want %q",
			created.Status, db.ApprovalStatusPending)
	}
	t.Logf("step 4 ✓ created approval: id=%s, status=%s, node=%s",
		created.ID, created.Status, created.NodeID)

	// Verify the approval is visible via HTTP GET /approvals.
	req = approvalRequest(t, srv.Server.URL, http.MethodGet,
		"/api/v1/approvals/"+created.ID.String(), ownerID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("step 4 — GET approval: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("step 4 — GET approval: status=%d, want %d",
			resp.StatusCode, http.StatusOK)
	}
	var fetchedApproval db.Approval
	json.NewDecoder(resp.Body).Decode(&fetchedApproval)
	if fetchedApproval.ID != created.ID {
		t.Fatalf("step 4 — fetched approval ID mismatch")
	}
	t.Logf("step 4 ✓ approval visible via HTTP GET: id=%s", fetchedApproval.ID)

	// Verify in pending list.
	req = approvalRequest(t, srv.Server.URL, http.MethodGet,
		"/api/v1/approvals/pending?tree_id="+tree.ID.String(),
		ownerID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("step 4 — GET pending: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("step 4 — GET pending: status=%d", resp.StatusCode)
	}
	var pendingResp struct {
		Approvals []db.Approval `json:"approvals"`
		Total     int           `json:"total"`
	}
	json.NewDecoder(resp.Body).Decode(&pendingResp)
	if pendingResp.Total < 1 {
		t.Fatal("step 4 — expected at least 1 pending approval")
	}
	found := false
	for _, a := range pendingResp.Approvals {
		if a.ID == created.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("step 4 — approval %s not found in pending list", created.ID)
	}
	t.Logf("step 4 ✓ approval visible in pending list (total=%d)", pendingResp.Total)

	// ── Step 5: Approve via HTTP ────────────────────────────────────────
	req = approvalRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/approvals/"+created.ID.String()+"/approve",
		ownerID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("step 5 — POST approve: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("step 5 — POST approve: status=%d, want %d; error=%+v",
			resp.StatusCode, http.StatusOK, errBody)
	}

	var approved db.Approval
	if err := json.NewDecoder(resp.Body).Decode(&approved); err != nil {
		t.Fatalf("step 5 — decode approved: %v", err)
	}
	if approved.Status != db.ApprovalStatusApproved {
		t.Fatalf("step 5 — approved status = %q, want %q",
			approved.Status, db.ApprovalStatusApproved)
	}
	if approved.DecidedBy == nil || *approved.DecidedBy != ownerID {
		t.Fatalf("step 5 — DecidedBy = %v, want %s",
			approved.DecidedBy, ownerID)
	}
	t.Logf("step 5 ✓ approved successfully: status=%s, decided_by=%v",
		approved.Status, approved.DecidedBy)

	// Verify approval is gone from pending list.
	req = approvalRequest(t, srv.Server.URL, http.MethodGet,
		"/api/v1/approvals/pending?tree_id="+tree.ID.String(),
		ownerID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("step 5 — GET pending after approve: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("step 5 — GET pending after approve: status=%d", resp.StatusCode)
	}
	var pendingAfter struct {
		Total int `json:"total"`
	}
	json.NewDecoder(resp.Body).Decode(&pendingAfter)
	if pendingAfter.Total != 0 {
		t.Fatalf("step 5 — pending total = %d, want 0", pendingAfter.Total)
	}
	t.Logf("step 5 ✓ pending list empty after approval")

	// ── Step 6: Verify audit trail via HTTP ─────────────────────────────
	req = approvalRequest(t, srv.Server.URL, http.MethodGet,
		"/api/v1/approvals/history?approval_id="+created.ID.String(),
		ownerID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("step 6 — GET history: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("step 6 — GET history: status=%d; error=%+v",
			resp.StatusCode, errBody)
	}

	var historyResp struct {
		Entries []db.AuditEntry `json:"entries"`
		Limit   int             `json:"limit"`
		Offset  int             `json:"offset"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&historyResp); err != nil {
		t.Fatalf("step 6 — decode history: %v", err)
	}
	if len(historyResp.Entries) < 2 {
		t.Fatalf("step 6 — expected at least 2 audit entries, got %d",
			len(historyResp.Entries))
	}

	var hasRequested, hasApproved bool
	for _, e := range historyResp.Entries {
		switch e.Action {
		case db.AuditActionApprovalRequested:
			hasRequested = true
		case db.AuditActionApprovalGranted:
			hasApproved = true
			if e.NewStatus == nil || *e.NewStatus != db.ApprovalStatusApproved {
				t.Fatalf("step 6 — audit NewStatus = %v, want %q",
					e.NewStatus, db.ApprovalStatusApproved)
			}
		}
	}
	if !hasRequested {
		t.Fatal("step 6 — audit trail missing approval_requested entry")
	}
	if !hasApproved {
		t.Fatal("step 6 — audit trail missing approval_granted entry")
	}
	t.Logf("step 6 ✓ audit trail verified: %d entries (has_requested=%v, has_approved=%v)",
		len(historyResp.Entries), hasRequested, hasApproved)

	// Also verify tree-scoped audit history.
	req = approvalRequest(t, srv.Server.URL, http.MethodGet,
		"/api/v1/approvals/history?tree_id="+tree.ID.String(),
		ownerID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("step 6 — GET tree history: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("step 6 — GET tree history: status=%d", resp.StatusCode)
	}
	var treeHistory struct {
		Entries []db.AuditEntry `json:"entries"`
	}
	json.NewDecoder(resp.Body).Decode(&treeHistory)
	if len(treeHistory.Entries) < 2 {
		t.Fatalf("step 6 — expected at least 2 tree-scoped audit entries, got %d",
			len(treeHistory.Entries))
	}
	t.Logf("step 6 ✓ tree-scoped audit history: %d entries",
		len(treeHistory.Entries))

	// ── Step 7: Verify tree integrity after full flow ────────────────────
	req = approvalRequest(t, srv.Server.URL, http.MethodGet,
		"/api/v1/trees/"+tree.ID.String(), srv.UserID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("step 7 — GET tree: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("step 7 — GET tree: status=%d, want %d",
			resp.StatusCode, http.StatusOK)
	}

	var finalTree service.Tree
	if err := json.NewDecoder(resp.Body).Decode(&finalTree); err != nil {
		t.Fatalf("step 7 — decode final tree: %v", err)
	}
	if finalTree.ID != tree.ID {
		t.Fatalf("step 7 — tree ID mismatch: got %s, want %s",
			finalTree.ID, tree.ID)
	}
	if finalTree.Title != tree.Title {
		t.Fatalf("step 7 — tree title changed unexpectedly")
	}
	if finalTree.RootNodeID != tree.RootNodeID {
		t.Fatalf("step 7 — root node ID changed unexpectedly")
	}
	t.Logf("step 7 ✓ tree integrity verified after full flow")

	// Verify all three child nodes still accessible.
	for i, nodeID := range childNodeIDs {
		req = approvalRequest(t, srv.Server.URL, http.MethodGet,
			"/api/v1/nodes/"+tree.ID.String()+"/nodes/"+nodeID.String(),
			srv.UserID, nil)
		resp, err = srv.Server.Client().Do(req)
		if err != nil {
			t.Fatalf("step 7 — GET node %d after flow: %v", i+1, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("step 7 — GET node %d after flow: status=%d",
				i+1, resp.StatusCode)
		}
		var nd service.NodeDetail
		json.NewDecoder(resp.Body).Decode(&nd)
		if nd.ID != nodeID {
			t.Fatalf("step 7 — node %d ID mismatch after flow", i+1)
		}
		// First node should have the updated content.
		if i == 0 && nd.Content != newContent {
			t.Fatalf("step 7 — node 0 content reverted unexpectedly")
		}
	}
	t.Logf("step 7 ✓ all %d child nodes accessible after full flow", len(childNodeIDs))

	t.Logf("INT-01 ✓ complete: full tree lifecycle passed (tree=%s)", tree.ID)
}

// TestINT01_TreeFlowWithMultipleNodes tests a larger DAG structure with
// branching nodes and verifies the graph remains consistent.
func TestINT01_TreeFlowWithBranching(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv := newTestServerWithApprovals(t, pool)
	defer srv.Cleanup()

	ownerID := ensureTestUser(t, pool)

	// Create tree.
	tree := createTestTree(t, srv)
	t.Logf("created tree: %s (root: %s)", tree.ID, tree.RootNodeID)

	// Create a chain: root → A → B → C.
	type nodeLink struct {
		id       uuid.UUID
		parentID uuid.UUID
		content  string
	}
	nodes := []nodeLink{
		{parentID: tree.RootNodeID, content: "Node A — first level"},
		{parentID: uuid.Nil, content: "Node B — second level (child of A)"}, // will set after A created
		{parentID: uuid.Nil, content: "Node C — third level (child of B)"},  // will set after B created
	}

	// Create A.
	nodes[0].id = createChildNodeForFlow(t, srv, tree.ID, tree.RootNodeID, nodes[0].content)

	// Create B as child of A.
	nodes[1].parentID = nodes[0].id
	nodes[1].id = createChildNodeForFlow(t, srv, tree.ID, nodes[0].id, nodes[1].content)

	// Create C as child of B.
	nodes[2].parentID = nodes[1].id
	nodes[2].id = createChildNodeForFlow(t, srv, tree.ID, nodes[1].id, nodes[2].content)

	t.Logf("chain: root → A(%s) → B(%s) → C(%s)",
		nodes[0].id, nodes[1].id, nodes[2].id)

	// Edit node B.
	newContent := "Node B — updated with additional content for branching test"
	updateBody := map[string]any{"content": newContent}
	req := approvalRequest(t, srv.Server.URL, http.MethodPatch,
		"/api/v1/nodes/nodes/"+nodes[1].id.String(),
		srv.UserID, updateBody)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("PATCH node B: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH node B: status=%d", resp.StatusCode)
	}
	t.Logf("edited node B successfully")

	// Create an approval for node C and approve via HTTP.
	approvalRepo := db.NewPGApprovalRepo(pool)
	auditRepo := db.NewPGAuditRepo(pool)
	userRepo := db.NewPGUserRepo(pool)
	profileRepo := db.NewPGProfileRepo(pool)
	memberRepo := db.NewPGTreeMemberRepo(pool)
	sseHub := sse.NewHub()
	approvalSvc := service.NewApprovalService(approvalRepo, auditRepo, userRepo,
		profileRepo, memberRepo, sseHub)

	created, err := approvalSvc.RequestApproval(context.Background(),
		tree.ID, nodes[2].id, ownerID, srv.UserID)
	if err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}
	t.Logf("created approval: %s for node C", created.ID)

	// Approve.
	req = approvalRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/approvals/"+created.ID.String()+"/approve",
		ownerID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST approve: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST approve: status=%d", resp.StatusCode)
	}
	var approved db.Approval
	json.NewDecoder(resp.Body).Decode(&approved)
	if approved.Status != db.ApprovalStatusApproved {
		t.Fatalf("approval status = %q, want approved", approved.Status)
	}
	t.Logf("approved successfully")

	// Verify audit trail.
	req = approvalRequest(t, srv.Server.URL, http.MethodGet,
		"/api/v1/approvals/history?approval_id="+created.ID.String(),
		ownerID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("GET history: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET history: status=%d", resp.StatusCode)
	}
	var hist struct {
		Entries []db.AuditEntry `json:"entries"`
	}
	json.NewDecoder(resp.Body).Decode(&hist)
	if len(hist.Entries) < 2 {
		t.Fatalf("audit trail: expected >=2 entries, got %d", len(hist.Entries))
	}
	t.Logf("audit trail: %d entries", len(hist.Entries))

	// Verify full tree is accessible.
	req = approvalRequest(t, srv.Server.URL, http.MethodGet,
		"/api/v1/trees/"+tree.ID.String(), srv.UserID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("GET tree: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET tree: status=%d", resp.StatusCode)
	}
	t.Logf("INT-01 branching: ✓ tree accessible, full flow passed")
}

// ── Helper ─────────────────────────────────────────────────────────────

// createChildNodeForFlow creates a child node via HTTP and returns its ID.
func createChildNodeForFlow(t *testing.T, srv *approvalTestServer,
	treeID, parentID uuid.UUID, content string) uuid.UUID {
	t.Helper()

	nodeBody := map[string]any{
		"content":        content,
		"content_format": "markdown",
		"node_type":      "message",
		"parent_id":      parentID.String(),
	}
	req := approvalRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/nodes/"+treeID.String()+"/nodes",
		srv.UserID, nodeBody)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST node: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST node: status=%d, body=%s", resp.StatusCode, string(bodyBytes))
	}

	var result service.CreateNodeResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode node: %v", err)
	}
	if result.Node == nil || result.Node.ID == uuid.Nil {
		t.Fatal("node has nil result or id")
	}
	return result.Node.ID
}
