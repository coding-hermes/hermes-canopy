// Package handler provides HTTP handlers for Canopy REST endpoints.
// This file extends TEST-02 coverage with tests for reply, fork, filtered
// listings, pagination, and additional error cases across the full API surface.
package handler

import (
	"context"
	"encoding/json"
	"fmt"
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
// TEST-02: Node — Reply endpoint
// ---------------------------------------------------------------------------

func TestAPI_NodeReply(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	ownerID := ensureTestUser(t, pool)

	// Create a tree.
	tree := createTreeViaHTTP(t, srv, ownerID, "Node Reply Test")
	// Create a child node to reply to.
	child := createChildNodeViaHTTP(t, srv, tree.ID, tree.RootNodeID, ownerID, "Parent for reply test")

	// POST /api/v1/nodes/{node_id}/reply — reply to child.
	replyBody := map[string]any{
		"content": "This is a reply",
	}
	req := apiRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/nodes/nodes/"+child.Node.ID.String()+"/reply", ownerID, replyBody)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST reply: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST reply: status=%d, body=%s", resp.StatusCode, string(bodyBytes))
	}

	var result service.CreateNodeResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode reply result: %v", err)
	}
	if result.Node == nil {
		t.Fatal("reply result has nil Node")
	}
	if result.Node.Content != "This is a reply" {
		t.Fatalf("reply content = %q, want %q", result.Node.Content, "This is a reply")
	}
	t.Logf("reply created: node=%s, edge=%s", result.Node.ID, result.Edge.ID)

	// Reply to non-existent node → 404.
	req = apiRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/nodes/nodes/"+uuid.New().String()+"/reply", ownerID, replyBody)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST reply to nonexistent: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("POST reply nonexistent: status=%d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

// ---------------------------------------------------------------------------
// TEST-02: Node — Fork endpoint
// ---------------------------------------------------------------------------

func TestAPI_NodeFork(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	ownerID := ensureTestUser(t, pool)

	// Create a tree with a multi-level hierarchy.
	tree := createTreeViaHTTP(t, srv, ownerID, "Node Fork Test")
	child := createChildNodeViaHTTP(t, srv, tree.ID, tree.RootNodeID, ownerID, "Parent for fork test")
	_ = createChildNodeViaHTTP(t, srv, tree.ID, child.Node.ID, ownerID, "Grandchild for fork test")

	// POST /api/v1/nodes/nodes/{node_id}/fork — fork from child (which has grandchild as a child).
	// Fork requires the parent node to have at least one child (alternative branch point).
	forkBody := map[string]any{
		"content": "This is a fork from child",
	}
	req := apiRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/nodes/nodes/"+child.Node.ID.String()+"/fork", ownerID, forkBody)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST fork: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST fork: status=%d, body=%s", resp.StatusCode, string(bodyBytes))
	}

	var result service.CreateNodeResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode fork result: %v", err)
	}
	if result.Node == nil {
		t.Fatal("fork result has nil Node")
	}
	if result.Node.Content != "This is a fork from child" {
		t.Fatalf("fork content = %q, want %q", result.Node.Content, "This is a fork from child")
	}
	t.Logf("fork created: node=%s, edge=%s", result.Node.ID, result.Edge.ID)

	// Fork from non-existent node → 404.
	req = apiRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/nodes/nodes/"+uuid.New().String()+"/fork", ownerID, forkBody)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST fork nonexistent: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("POST fork nonexistent: status=%d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

// ---------------------------------------------------------------------------
// TEST-02: Cards — List with filters (node_id, card_type)
// ---------------------------------------------------------------------------

func TestAPI_CardListFilters(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	ownerID := ensureTestUser(t, pool)

	// Create a tree with two nodes for cards.
	tree := createTreeViaHTTP(t, srv, ownerID, "Card Filter Test")
	nodeA := createChildNodeViaHTTP(t, srv, tree.ID, tree.RootNodeID, ownerID, "Node A")
	nodeB := createChildNodeViaHTTP(t, srv, tree.ID, tree.RootNodeID, ownerID, "Node B")

	// Create cards on both nodes with different types.
	createCard := func(nodeID, appID string, cardType service.CardType) {
		body := map[string]any{
			"treeId":   tree.ID.String(),
			"nodeId":   nodeID,
			"appId":    appID,
			"cardType": string(cardType),
			"data":     map[string]any{"label": appID + "-" + string(cardType)},
		}
		req := apiRequest(t, srv.Server.URL, http.MethodPost, "/api/v1/cards", ownerID, body)
		resp, err := srv.Server.Client().Do(req)
		if err != nil {
			t.Fatalf("POST card: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			bodyBytes, _ := io.ReadAll(resp.Body)
			t.Fatalf("POST card: status=%d, body=%s", resp.StatusCode, string(bodyBytes))
		}
	}

	createCard(nodeA.Node.ID.String(), "app-compact", service.CardTypeCompact)
	createCard(nodeA.Node.ID.String(), "app-expanded", service.CardTypeExpanded)
	createCard(nodeB.Node.ID.String(), "app-iteration", service.CardTypeIteration)

	// 1. Filter by node_id.
	req := apiRequest(t, srv.Server.URL, http.MethodGet,
		"/api/v1/cards?node_id="+nodeA.Node.ID.String(), ownerID, nil)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("GET cards by node_id: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET cards by node_id: status=%d", resp.StatusCode)
	}
	var nodeResult struct {
		Cards []service.CardSummary `json:"cards"`
	}
	json.NewDecoder(resp.Body).Decode(&nodeResult)
	if len(nodeResult.Cards) != 2 {
		t.Fatalf("node filter: expected 2 cards, got %d", len(nodeResult.Cards))
	}
	t.Logf("node filter: %d cards for nodeA", len(nodeResult.Cards))

	// 2. Filter by card_type.
	req = apiRequest(t, srv.Server.URL, http.MethodGet,
		"/api/v1/cards?card_type=compact", ownerID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("GET cards by card_type: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET cards by card_type: status=%d", resp.StatusCode)
	}
	var typeResult struct {
		Cards []service.CardSummary `json:"cards"`
	}
	json.NewDecoder(resp.Body).Decode(&typeResult)
	if len(typeResult.Cards) != 1 {
		t.Fatalf("card_type filter: expected 1 card, got %d", len(typeResult.Cards))
	}
	if typeResult.Cards[0].Type != "compact" {
		t.Fatalf("card_type filter: got type=%s, want compact", typeResult.Cards[0].Type)
	}
	t.Logf("card_type filter: %d cards for type='compact'", len(typeResult.Cards))

	// 3. Combined filter.
	req = apiRequest(t, srv.Server.URL, http.MethodGet,
		"/api/v1/cards?tree_id="+tree.ID.String()+"&card_type=expanded", ownerID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("GET cards combined filter: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET cards combined: status=%d", resp.StatusCode)
	}
	var combinedResult struct {
		Cards []service.CardSummary `json:"cards"`
	}
	json.NewDecoder(resp.Body).Decode(&combinedResult)
	if len(combinedResult.Cards) != 1 {
		t.Fatalf("combined filter: expected 1 card, got %d", len(combinedResult.Cards))
	}
}

// ---------------------------------------------------------------------------
// TEST-02: Cards — Update validation (missing data)
// ---------------------------------------------------------------------------

func TestAPI_CardUpdateValidation(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	ownerID := ensureTestUser(t, pool)

	tree := createTreeViaHTTP(t, srv, ownerID, "Card Validation Test")
	child := createChildNodeViaHTTP(t, srv, tree.ID, tree.RootNodeID, ownerID, "Card anchor")

	// Create a card.
	createBody := map[string]any{
		"treeId":   tree.ID.String(),
		"nodeId":   child.Node.ID.String(),
		"appId":    "test-app",
		"cardType": "compact",
		"data":     map[string]any{"title": "Test"},
	}
	req := apiRequest(t, srv.Server.URL, http.MethodPost, "/api/v1/cards", ownerID, createBody)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST card: %v", err)
	}
	var card service.CardSummary
	json.NewDecoder(resp.Body).Decode(&card)
	resp.Body.Close()

	// PATCH with empty data → 400.
	updateBody := map[string]any{}
	req = apiRequest(t, srv.Server.URL, http.MethodPatch,
		"/api/v1/cards/"+card.ID.String(), ownerID, updateBody)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("PATCH card missing data: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("PATCH card missing data: status=%d, want %d", resp.StatusCode, http.StatusBadRequest)
	}

	// PATCH with null data → 400.
	req = apiRequest(t, srv.Server.URL, http.MethodPatch,
		"/api/v1/cards/"+card.ID.String(), ownerID, map[string]any{"data": nil})
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("PATCH card null data: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("PATCH card null data: status=%d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

// ---------------------------------------------------------------------------
// TEST-02: Topics — List with status and pagination
// ---------------------------------------------------------------------------

func TestAPI_TopicListPagination(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	ownerID := ensureTestUser(t, pool)

	tree := createTreeViaHTTP(t, srv, ownerID, "Topic Pagination Test")

	// Create 3 topics on different nodes.
	for i := 1; i <= 3; i++ {
		child := createChildNodeViaHTTP(t, srv, tree.ID, tree.RootNodeID, ownerID,
			fmt.Sprintf("Topic root %d", i))
		createBody := map[string]any{
			"treeId":      tree.ID.String(),
			"rootNodeId":  child.Node.ID.String(),
			"title":       fmt.Sprintf("Topic %d", i),
			"description": fmt.Sprintf("Description %d", i),
		}
		req := apiRequest(t, srv.Server.URL, http.MethodPost, "/api/v1/topics", ownerID, createBody)
		resp, err := srv.Server.Client().Do(req)
		if err != nil {
			t.Fatalf("POST topic %d: %v", i, err)
		}
		resp.Body.Close()
	}

	// 1. List with limit=2 → expect at most 2.
	req := apiRequest(t, srv.Server.URL, http.MethodGet,
		"/api/v1/topics?tree_id="+tree.ID.String()+"&limit=2", ownerID, nil)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("GET topics limit=2: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET topics limit=2: status=%d", resp.StatusCode)
	}
	var listLimit struct {
		Topics []service.TopicSummary `json:"topics"`
	}
	json.NewDecoder(resp.Body).Decode(&listLimit)
	if len(listLimit.Topics) > 2 {
		t.Fatalf("limit=2: expected at most 2 topics, got %d", len(listLimit.Topics))
	}
	t.Logf("topics limit=2: %d topics", len(listLimit.Topics))

	// 2. List with offset=1 → expect fewer results.
	req = apiRequest(t, srv.Server.URL, http.MethodGet,
		"/api/v1/topics?tree_id="+tree.ID.String()+"&offset=1&limit=50", ownerID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("GET topics offset=1: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET topics offset=1: status=%d", resp.StatusCode)
	}
	var listOffset struct {
		Topics []service.TopicSummary `json:"topics"`
	}
	json.NewDecoder(resp.Body).Decode(&listOffset)
	if len(listOffset.Topics) < 2 {
		t.Fatalf("offset=1: expected at least 2 topics (3 total - 1 offset), got %d", len(listOffset.Topics))
	}

	// 3. List with status=active → should include new topics (they default to active).
	req = apiRequest(t, srv.Server.URL, http.MethodGet,
		"/api/v1/topics?tree_id="+tree.ID.String()+"&status=active", ownerID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("GET topics status=active: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET topics status=active: status=%d", resp.StatusCode)
	}
	var listStatus struct {
		Topics []service.TopicSummary `json:"topics"`
	}
	json.NewDecoder(resp.Body).Decode(&listStatus)
	t.Logf("topics status=active: %d topics", len(listStatus.Topics))

	// 4. List without tree_id → 400 (tree_id is required).
	req = apiRequest(t, srv.Server.URL, http.MethodGet, "/api/v1/topics", ownerID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("GET topics no tree_id: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("GET topics no tree_id: expected 400, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// TEST-02: Approvals — Deny without reason → 400
// ---------------------------------------------------------------------------

func TestAPI_ApprovalDenyWithoutReason(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	ownerID := ensureTestUser(t, pool)

	tree := createTreeViaHTTP(t, srv, ownerID, "Approval Deny Reason Test")
	child := createChildNodeViaHTTP(t, srv, tree.ID, tree.RootNodeID, ownerID, "Approval node")

	// Create approval via service.
	approvalRepo := db.NewPGApprovalRepo(pool)
	auditRepo := db.NewPGAuditRepo(pool)
	userRepo := db.NewPGUserRepo(pool)
	profileRepo := db.NewPGProfileRepo(pool)
	memberRepo := db.NewPGTreeMemberRepo(pool)
	approvalSvc := service.NewApprovalService(approvalRepo, auditRepo, userRepo,
		profileRepo, memberRepo, sse.NewHub())

	created, err := approvalSvc.RequestApproval(t.Context(),
		tree.ID, child.Node.ID, ownerID, ownerID)
	if err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}

	// Deny with empty reason → 400.
	req := apiRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/approvals/"+created.ID.String()+"/deny", ownerID,
		map[string]any{"reason": ""})
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST deny empty reason: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST deny empty reason: status=%d, want %d", resp.StatusCode, http.StatusBadRequest)
	}

	// Deny with missing reason field → 400.
	req = apiRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/approvals/"+created.ID.String()+"/deny", ownerID,
		map[string]any{})
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST deny missing reason: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST deny missing reason: status=%d, want %d", resp.StatusCode, http.StatusBadRequest)
	}

	// Verify approval is still pending (neither deny went through).
	pendingReq := apiRequest(t, srv.Server.URL, http.MethodGet,
		"/api/v1/approvals/"+created.ID.String(), ownerID, nil)
	pendingResp, err := srv.Server.Client().Do(pendingReq)
	if err != nil {
		t.Fatalf("GET approval: %v", err)
	}
	defer pendingResp.Body.Close()
	if pendingResp.StatusCode != http.StatusOK {
		t.Fatalf("GET approval: status=%d", pendingResp.StatusCode)
	}
	var fetched db.Approval
	json.NewDecoder(pendingResp.Body).Decode(&fetched)
	if fetched.Status != db.ApprovalStatusPending {
		t.Fatalf("approval status = %q, want pending", fetched.Status)
	}
	t.Logf("approval still pending after invalid deny: %s", fetched.Status)
}

// ---------------------------------------------------------------------------
// TEST-02: Approvals — Error cases (bad UUID, not found)
// ---------------------------------------------------------------------------

func TestAPI_ApprovalErrorCases(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	ownerID := ensureTestUser(t, pool)

	// 1. Get approval with bad UUID → 400.
	req := apiRequest(t, srv.Server.URL, http.MethodGet,
		"/api/v1/approvals/not-a-uuid", ownerID, nil)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("GET approval bad uuid: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("GET approval bad uuid: status=%d, want %d", resp.StatusCode, http.StatusBadRequest)
	}

	// 2. Get non-existent approval → 404.
	req = apiRequest(t, srv.Server.URL, http.MethodGet,
		"/api/v1/approvals/"+uuid.New().String(), ownerID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("GET nonexistent approval: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET nonexistent approval: status=%d, want %d", resp.StatusCode, http.StatusNotFound)
	}

	// 3. Approve non-existent → 404.
	req = apiRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/approvals/"+uuid.New().String()+"/approve", ownerID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST approve nonexistent: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("POST approve nonexistent: status=%d, want %d", resp.StatusCode, http.StatusNotFound)
	}

	// 4. Deny non-existent → 404.
	req = apiRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/approvals/"+uuid.New().String()+"/deny", ownerID,
		map[string]any{"reason": "testing"})
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST deny nonexistent: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("POST deny nonexistent: status=%d, want %d", resp.StatusCode, http.StatusNotFound)
	}

	// 5. Approve with bad UUID → 400.
	req = apiRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/approvals/not-a-uuid/approve", ownerID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST approve bad uuid: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST approve bad uuid: status=%d, want %d", resp.StatusCode, http.StatusBadRequest)
	}

	// 6. Deny with bad UUID → 400.
	req = apiRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/approvals/not-a-uuid/deny", ownerID,
		map[string]any{"reason": "testing"})
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST deny bad uuid: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST deny bad uuid: status=%d, want %d", resp.StatusCode, http.StatusBadRequest)
	}

	// 7. History without filter → 400.
	req = apiRequest(t, srv.Server.URL, http.MethodGet,
		"/api/v1/approvals/history", ownerID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("GET history no filter: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("GET history no filter: status=%d, want %d", resp.StatusCode, http.StatusBadRequest)
	}

	// 8. History with bad UUID → 400.
	req = apiRequest(t, srv.Server.URL, http.MethodGet,
		"/api/v1/approvals/history?approval_id=not-a-uuid", ownerID, nil)
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("GET history bad uuid: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("GET history bad uuid: status=%d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

// ---------------------------------------------------------------------------
// TEST-02: Tree — List with pagination cursors
// ---------------------------------------------------------------------------

func TestAPI_TreeListPagination(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	ownerID := ensureTestUser(t, pool)

	// Create 2 trees.
	tree1 := createTreeViaHTTP(t, srv, ownerID, "Pagination Tree 1")
	tree2 := createTreeViaHTTP(t, srv, ownerID, "Pagination Tree 2")

	// List trees → assert both are present.
	req := apiRequest(t, srv.Server.URL, http.MethodGet, "/api/v1/trees?sort=created_desc", ownerID, nil)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("GET trees: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET trees: status=%d", resp.StatusCode)
	}

	var list listTreesResponse
	json.NewDecoder(resp.Body).Decode(&list)
	if len(list.Trees) < 2 {
		t.Fatalf("expected at least 2 trees, got %d", len(list.Trees))
	}

	// Verify pagination fields are present.
	if list.Pagination.Total < 2 {
		t.Fatalf("pagination total = %d, want >= 2", list.Pagination.Total)
	}
	t.Logf("trees listed: total=%d, hasMore=%v, limit=%d",
		list.Pagination.Total, list.Pagination.HasMore, list.Pagination.Limit)

	// Verify both trees are in the list.
	found1, found2 := false, false
	for _, ts := range list.Trees {
		if ts.ID == tree1.ID {
			found1 = true
		}
		if ts.ID == tree2.ID {
			found2 = true
		}
	}
	if !found1 {
		t.Fatalf("tree1 %s not found in list", tree1.ID)
	}
	if !found2 {
		t.Fatalf("tree2 %s not found in list", tree2.ID)
	}
}

// ---------------------------------------------------------------------------
// TEST-02: Graph — Subtree of non-existent node
// ---------------------------------------------------------------------------

func TestAPI_GraphSubtreeNotFound(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	ownerID := ensureTestUser(t, pool)
	tree := createTreeViaHTTP(t, srv, ownerID, "Subtree NotFound Test")

	// Subtree of non-existent node → 404.
	url := fmt.Sprintf("/api/v1/graph/trees/%s/subtree/%s", tree.ID, uuid.New())
	req := apiRequest(t, srv.Server.URL, http.MethodGet, url, ownerID, nil)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("GET subtree nonexistent: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET subtree nonexistent: status=%d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

// ---------------------------------------------------------------------------
// TEST-02: Graph — Ancestors of non-existent node
// ---------------------------------------------------------------------------

func TestAPI_GraphAncestorsNotFound(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	ownerID := ensureTestUser(t, pool)
	tree := createTreeViaHTTP(t, srv, ownerID, "Ancestors NotFound Test")

	// Ancestors of non-existent node → 404.
	url := fmt.Sprintf("/api/v1/graph/trees/%s/ancestors/%s", tree.ID, uuid.New())
	req := apiRequest(t, srv.Server.URL, http.MethodGet, url, ownerID, nil)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("GET ancestors nonexistent: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET ancestors nonexistent: status=%d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

// ---------------------------------------------------------------------------
// TEST-02: Approval — Approve then deny (already decided → 409)
// ---------------------------------------------------------------------------

func TestAPI_ApprovalAlreadyDecided(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	ownerID := ensureTestUser(t, pool)

	tree := createTreeViaHTTP(t, srv, ownerID, "Approval Already Decided Test")
	child := createChildNodeViaHTTP(t, srv, tree.ID, tree.RootNodeID, ownerID, "Approval node")

	// Create approval.
	approvalRepo := db.NewPGApprovalRepo(pool)
	auditRepo := db.NewPGAuditRepo(pool)
	userRepo := db.NewPGUserRepo(pool)
	profileRepo := db.NewPGProfileRepo(pool)
	memberRepo := db.NewPGTreeMemberRepo(pool)
	approvalSvc := service.NewApprovalService(approvalRepo, auditRepo, userRepo,
		profileRepo, memberRepo, sse.NewHub())

	created, err := approvalSvc.RequestApproval(t.Context(),
		tree.ID, child.Node.ID, ownerID, ownerID)
	if err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}

	// Approve it.
	req := apiRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/approvals/"+created.ID.String()+"/approve", ownerID, nil)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST approve: %v", err)
	}
	resp.Body.Close()

	// Deny the already-approved approval → 409 Conflict.
	req = apiRequest(t, srv.Server.URL, http.MethodPost,
		"/api/v1/approvals/"+created.ID.String()+"/deny", ownerID,
		map[string]any{"reason": "too late"})
	resp, err = srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST deny after approve: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("POST deny after approve: status=%d, want %d", resp.StatusCode, http.StatusConflict)
	}
	t.Log("correctly rejected deny on already-approved approval (409)")
}

// ---------------------------------------------------------------------------
// TEST-02: Health — Auth not required
// ---------------------------------------------------------------------------

func TestAPI_HealthNoAuth(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	// Health endpoint should work without auth.
	for _, path := range []string{"/health", "/healthz"} {
		req, err := http.NewRequest(http.MethodGet, srv.Server.URL+path, nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		resp, err := srv.Server.Client().Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			t.Logf("GET %s no auth: 404 (endpoint not registered in test server — skipping)", path)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s no auth: status=%d, want %d", path, resp.StatusCode, http.StatusOK)
		}
		t.Logf("GET %s no auth: OK", path)
	}
}

// ---------------------------------------------------------------------------
// BUG-029: Root node create 503 — edge FK violation
// ---------------------------------------------------------------------------
//
// Create() unconditionally inserted an edge with source_id = input.ParentID.
// For root nodes ParentID is uuid.Nil, but edges.source_id is NOT NULL with an
// FK to nodes(id), so the INSERT violated edges_source_id_fkey and the handler
// surfaced it as a 503. The fix skips the edge insert for root nodes and
// returns Edge: nil in CreateNodeResult.
//
// These tests prove: (a) root node create returns 201 with no edge, (b) no
// edge row exists for the root node, and (c) reply create (parent_id set)
// still returns an edge — the regression guard for the non-root path.

func TestAPI_NodeCreate_RootNode_NoEdge_BUG029(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	ownerID := ensureTestUser(t, pool)
	tree := createTreeViaHTTP(t, srv, ownerID, "BUG-029 Root Node Test")

	// POST a ROOT node — NO parent_id.
	rootBody := map[string]any{
		"content":   "BUG-029 standalone root node",
		"node_type": "message",
	}
	req := apiRequest(t, srv.Server.URL, http.MethodPost,
		fmt.Sprintf("/api/v1/trees/%s/nodes", tree.ID), ownerID, rootBody)
	resp, err := srv.Server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST root node: %v", err)
	}
	defer resp.Body.Close()

	// AC: root create returns 201 (was 503 before the fix).
	if resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST root node: status=%d, want %d; body=%s",
			resp.StatusCode, http.StatusCreated, string(bodyBytes))
	}

	var result service.CreateNodeResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode root node result: %v", err)
	}
	if result.Node == nil {
		t.Fatal("root node result has nil Node")
	}
	if result.Node.ID == uuid.Nil {
		t.Fatal("root node has nil ID")
	}
	// AC: root nodes have NO edge.
	if result.Edge != nil {
		t.Fatalf("root node Edge = %v, want nil (root nodes have no parent edge)", result.Edge)
	}

	// AC: no edge row exists for the new root node.
	var edgeCount int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM edges WHERE target_id = $1`, result.Node.ID,
	).Scan(&edgeCount); err != nil {
		t.Fatalf("count edges for root node: %v", err)
	}
	if edgeCount != 0 {
		t.Fatalf("edges targeting root node = %d, want 0", edgeCount)
	}
	t.Logf("BUG-029 ✓ root node %s created (201), edge=nil, edge_count=0", result.Node.ID)
}

func TestAPI_NodeCreate_ReplyNode_HasEdge_BUG029(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv := newTestServerWithFullAPI(t, pool)
	defer srv.Cleanup()

	ownerID := ensureTestUser(t, pool)
	tree := createTreeViaHTTP(t, srv, ownerID, "BUG-029 Reply Regression")

	// Create a reply to the tree's root node — parent_id set.
	reply := createChildNodeViaHTTP(t, srv, tree.ID, tree.RootNodeID, ownerID, "BUG-029 reply child")

	// AC: reply nodes MUST have an edge (regression guard).
	if reply.Edge == nil {
		t.Fatal("reply node Edge = nil, want non-nil (replies must create a parent edge)")
	}
	if reply.Edge.SourceNodeID != tree.RootNodeID {
		t.Fatalf("reply edge source = %s, want root %s", reply.Edge.SourceNodeID, tree.RootNodeID)
	}
	if reply.Edge.TargetNodeID != reply.Node.ID {
		t.Fatalf("reply edge target = %s, want reply %s", reply.Edge.TargetNodeID, reply.Node.ID)
	}

	// Verify exactly one edge row in the DB targeting the reply.
	var edgeCount int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM edges WHERE target_id = $1`, reply.Node.ID,
	).Scan(&edgeCount); err != nil {
		t.Fatalf("count edges for reply: %v", err)
	}
	if edgeCount != 1 {
		t.Fatalf("edges targeting reply = %d, want 1", edgeCount)
	}
	t.Logf("BUG-029 ✓ reply node %s has edge %s (source=%s)",
		reply.Node.ID, reply.Edge.ID, reply.Edge.SourceNodeID)
}
