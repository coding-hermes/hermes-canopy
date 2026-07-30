// Package handler provides HTTP handlers for Canopy REST endpoints.
// This file contains API-level integration tests for tree, node, and edge
// CRUD via HTTP, using a real PostgreSQL test database. Task BE-12b.
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
// Test server helper
// ---------------------------------------------------------------------------

// newTestServer creates an httptest.Server wired with real repos, services,
// SSE hub, sync engine, auth middleware, and all CRUD routes. Returns the
// server and a cleanup function.
func newTestServer(t *testing.T, pool *pgxpool.Pool) (*httptest.Server, func()) {
	t.Helper()

	// Create sentinel user so FK references to testUserID in handlers work.
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, hermes_user_id, display_name)
		VALUES ('a0000000-0000-0000-0000-000000000001', 'testuser', 'Test User')
		ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("create sentinel user: %v", err)
	}

	// Build repos directly from the pool.
	treeRepo := db.NewPGTreeRepo(pool)
	nodeRepo := db.NewPGNodeRepo(pool)
	edgeRepo := db.NewPGEdgeRepo(pool)
	eventRepo := db.NewEventRepo(pool)
	snapshotRepo := db.NewSnapshotRepo(pool)

	// Build services.
	treeSvc := service.NewTreeService(treeRepo, nodeRepo, edgeRepo, pool)
	sseHub := sse.NewHub()
	nodeSvc := service.NewNodeService(nodeRepo, edgeRepo, pool, sseHub)
	graphSvc := service.NewGraphServiceImpl(nodeRepo, edgeRepo)
	syncEngine := sync.NewEngine(eventRepo, snapshotRepo, sseHub,
		sync.DefaultEngineConfig())

	// Build chi router matching the real server's /api/v1 layout.
	r := chi.NewRouter()
	authMW := AuthMiddleware("canopy-dev-secret")

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(authMW)

		// Tree CRUD.
		treeHandler := NewTreeHandler(treeSvc, syncEngine)
		r.Mount("/trees", treeHandler.Routes())

		// Node CRUD — mounted at /nodes (no membership check).
		nodeHandler := NewNodeHandler(nodeSvc, syncEngine)
		r.Mount("/nodes", nodeHandler.Routes())

		// Graph endpoints (for subtree queries, edge listing).
		graphHandler := NewGraphHandler(graphSvc)
		r.Mount("/graph", graphHandler.Routes())
	})

	srv := httptest.NewServer(r)
	cleanup := func() {
		srv.Close()
	}

	return srv, cleanup
}

// ---------------------------------------------------------------------------
// JWT helpers
// ---------------------------------------------------------------------------

// testUserID is a stable UUID used across all integration tests.
var testUserID = uuid.MustParse("a0000000-0000-0000-0000-000000000001")

// authHeader creates an Authorization: Bearer header with a signed JWT
// for testUserID. The token is valid for 1 hour.
func authHeader(t *testing.T) string {
	t.Helper()
	tok := signedToken(t, "canopy-dev-secret", jwt.MapClaims{
		"sub": testUserID.String(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	return "Bearer " + tok
}

// authenticatedRequest builds an http.Request with method, path (relative to
// srv.URL), optional JSON body, and the standard Authorization header.
func authenticatedRequest(t *testing.T, srvURL, method, path string, body any) *http.Request {
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
	r.Header.Set("Authorization", authHeader(t))
	return r
}

// ---------------------------------------------------------------------------
// Tree CRUD integration test
// ---------------------------------------------------------------------------

func TestBE12_TreeCRUD(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv, cleanup := newTestServer(t, pool)
	defer cleanup()

	// 1. POST /api/v1/trees — create a tree.
	createBody := map[string]any{
		"title":       "Integration Test Tree",
		"description": "Created by BE-12b integration test",
		"rootMessage": map[string]any{
			"content":       "Root node content",
			"contentFormat": "markdown",
			"nodeType":      "message",
		},
	}
	req := authenticatedRequest(t, srv.URL, http.MethodPost, "/api/v1/trees", createBody)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST trees: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("POST trees: status=%d, want=%d; error=%+v", resp.StatusCode, http.StatusCreated, errBody)
	}

	var tree service.Tree
	if err := json.NewDecoder(resp.Body).Decode(&tree); err != nil {
		t.Fatalf("decode tree: %v", err)
	}
	if tree.Title != "Integration Test Tree" {
		t.Fatalf("tree.Title = %q, want %q", tree.Title, "Integration Test Tree")
	}
	if tree.ID == uuid.Nil {
		t.Fatal("tree.ID is nil UUID")
	}
	location := resp.Header.Get("Location")
	if location == "" {
		t.Fatal("Location header missing on create")
	}
	t.Logf("created tree: id=%s, location=%s", tree.ID, location)

	// 2. GET /api/v1/trees/{tree_id} — retrieve the tree.
	req = authenticatedRequest(t, srv.URL, http.MethodGet, "/api/v1/trees/"+tree.ID.String(), nil)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET tree: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET tree: status=%d, want=%d", resp.StatusCode, http.StatusOK)
	}

	// 3. GET /api/v1/trees — list trees (should contain ours).
	req = authenticatedRequest(t, srv.URL, http.MethodGet, "/api/v1/trees?sort=created_desc", nil)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET trees list: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET trees list: status=%d, want=%d", resp.StatusCode, http.StatusOK)
	}

	var list listTreesResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode tree list: %v", err)
	}
	if len(list.Trees) < 1 {
		t.Fatal("tree list has no entries; expected at least our created tree")
	}
	found := false
	for _, ts := range list.Trees {
		if ts.ID == tree.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("created tree %s not found in list response", tree.ID)
	}

	// 4. PATCH /api/v1/trees/{tree_id} — update title and description.
	newTitle := "Updated Test Tree"
	newDesc := "Updated description"
	updateBody := map[string]any{
		"title":       newTitle,
		"description": newDesc,
	}
	req = authenticatedRequest(t, srv.URL, http.MethodPatch, "/api/v1/trees/"+tree.ID.String(), updateBody)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("PATCH tree: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("PATCH tree: status=%d, want=%d; error=%+v", resp.StatusCode, http.StatusOK, errBody)
	}

	var updated service.Tree
	if err := json.NewDecoder(resp.Body).Decode(&updated); err != nil {
		t.Fatalf("decode updated tree: %v", err)
	}
	if updated.Title != newTitle {
		t.Fatalf("updated.Title = %q, want %q", updated.Title, newTitle)
	}
	if updated.Description != newDesc {
		t.Fatalf("updated.Description = %q, want %q", updated.Description, newDesc)
	}

	// 5. DELETE /api/v1/trees/{tree_id} — soft-delete.
	req = authenticatedRequest(t, srv.URL, http.MethodDelete, "/api/v1/trees/"+tree.ID.String(), nil)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE tree: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("DELETE tree: status=%d, want=%d; error=%+v", resp.StatusCode, http.StatusNoContent, errBody)
	}

	// 6. GET deleted tree → 404 or 410.
	req = authenticatedRequest(t, srv.URL, http.MethodGet, "/api/v1/trees/"+tree.ID.String(), nil)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET deleted tree: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusGone {
		t.Fatalf("GET deleted tree: status=%d, want 404 or 410", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Node CRUD integration test
// ---------------------------------------------------------------------------

func TestBE12_NodeCRUD(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv, cleanup := newTestServer(t, pool)
	defer cleanup()

	// 1. Create a tree first (nodes live inside trees).
	createBody := map[string]any{
		"title": "Node CRUD Test Tree",
		"rootMessage": map[string]any{
			"content":       "Root node content",
			"contentFormat": "markdown",
			"nodeType":      "message",
		},
	}
	req := authenticatedRequest(t, srv.URL, http.MethodPost, "/api/v1/trees", createBody)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST tree: %v", err)
	}
	var tree service.Tree
	json.NewDecoder(resp.Body).Decode(&tree)
	resp.Body.Close()
	if tree.ID == uuid.Nil {
		t.Fatal("failed to create tree for node tests")
	}
	t.Logf("created tree for node tests: %s", tree.ID)

	// 2. POST /api/v1/nodes/{tree_id}/nodes — create a child node under root.
	nodeBody := map[string]any{
		"content":        "First child node",
		"content_format": "markdown",
		"node_type":      "message",
		"parent_id":      tree.RootNodeID.String(),
	}
	t.Logf("Creating node in tree %s under parent %s", tree.ID, tree.RootNodeID)
	req = authenticatedRequest(t, srv.URL, http.MethodPost, "/api/v1/nodes/"+tree.ID.String()+"/nodes", nodeBody)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST node: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		var errBody apiErrorBody
		bodyBytes, _ := io.ReadAll(resp.Body)
		json.Unmarshal(bodyBytes, &errBody)
		t.Fatalf("POST node: status=%d, want=%d; body=%s; error=%+v", resp.StatusCode, http.StatusCreated, string(bodyBytes), errBody)
	}

	var createResult service.CreateNodeResult
	if err := json.NewDecoder(resp.Body).Decode(&createResult); err != nil {
		t.Fatalf("decode create node result: %v", err)
	}
	if createResult.Node == nil {
		t.Fatal("create node result has nil Node")
	}
	nodeID := createResult.Node.ID
	if nodeID == uuid.Nil {
		t.Fatal("created node has nil ID")
	}
	if createResult.Edge == nil {
		t.Fatal("create node result has nil Edge")
	}
	t.Logf("created node: id=%s, edge_id=%s, content=%q", nodeID, createResult.Edge.ID, createResult.Node.Content)

	// 3. GET /api/v1/nodes/{tree_id}/nodes/{node_id} — retrieve the node.
	req = authenticatedRequest(t, srv.URL, http.MethodGet,
		"/api/v1/nodes/"+tree.ID.String()+"/nodes/"+nodeID.String(), nil)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET node: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET node: status=%d, want=%d", resp.StatusCode, http.StatusOK)
	}

	var nodeDetail service.NodeDetail
	if err := json.NewDecoder(resp.Body).Decode(&nodeDetail); err != nil {
		t.Fatalf("decode node: %v", err)
	}
	if nodeDetail.Content != "First child node" {
		t.Fatalf("node.Content = %q, want %q", nodeDetail.Content, "First child node")
	}
	if nodeDetail.ChildCount < 0 {
		t.Fatalf("node.ChildCount = %d, want >= 0", nodeDetail.ChildCount)
	}

	// 4. PATCH /api/v1/nodes/nodes/{node_id} — update content.
	newContent := "Updated child node content"
	updateBody := map[string]any{
		"content": newContent,
	}
	req = authenticatedRequest(t, srv.URL, http.MethodPatch, "/api/v1/nodes/nodes/"+nodeID.String(), updateBody)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("PATCH node: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("PATCH node: status=%d, want=%d; error=%+v", resp.StatusCode, http.StatusOK, errBody)
	}

	var updatedNode service.NodeDetail
	if err := json.NewDecoder(resp.Body).Decode(&updatedNode); err != nil {
		t.Fatalf("decode updated node: %v", err)
	}
	if updatedNode.Content != newContent {
		t.Fatalf("updated.Content = %q, want %q", updatedNode.Content, newContent)
	}

	// 5. DELETE /api/v1/nodes/nodes/{node_id} — soft-delete.
	req = authenticatedRequest(t, srv.URL, http.MethodDelete, "/api/v1/nodes/nodes/"+nodeID.String(), nil)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE node: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("DELETE node: status=%d, want=%d; error=%+v", resp.StatusCode, http.StatusOK, errBody)
	}

	var deleteResult service.DeleteNodeResult
	if err := json.NewDecoder(resp.Body).Decode(&deleteResult); err != nil {
		t.Fatalf("decode delete result: %v", err)
	}
	if deleteResult.DeletedAt.IsZero() {
		t.Fatal("delete result has zero DeletedAt timestamp")
	}

	// 6. GET deleted node → 404.
	req = authenticatedRequest(t, srv.URL, http.MethodGet,
		"/api/v1/nodes/"+tree.ID.String()+"/nodes/"+nodeID.String(), nil)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET deleted node: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusGone {
		t.Fatalf("GET deleted node: status=%d, want 404 or 410", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Edge CRUD integration test
// ---------------------------------------------------------------------------

func TestBE12_EdgeCRUD(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv, cleanup := newTestServer(t, pool)
	defer cleanup()

	// 1. Create a tree.
	createBody := map[string]any{
		"title": "Edge CRUD Test Tree",
		"rootMessage": map[string]any{
			"content":       "Root node content",
			"contentFormat": "markdown",
			"nodeType":      "message",
		},
	}
	req := authenticatedRequest(t, srv.URL, http.MethodPost, "/api/v1/trees", createBody)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST tree: %v", err)
	}
	var tree service.Tree
	json.NewDecoder(resp.Body).Decode(&tree)
	resp.Body.Close()
	if tree.ID == uuid.Nil || tree.RootNodeID == uuid.Nil {
		t.Fatal("failed to create tree for edge tests")
	}
	t.Logf("created tree: %s, root: %s", tree.ID, tree.RootNodeID)

	// 2. Create a child node — this implicitly creates an edge.
	nodeBody := map[string]any{
		"content":   "Child for edge test",
		"parent_id": tree.RootNodeID.String(),
	}
	req = authenticatedRequest(t, srv.URL, http.MethodPost, "/api/v1/nodes/"+tree.ID.String()+"/nodes", nodeBody)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST child node: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("POST child node: status=%d; error=%+v", resp.StatusCode, errBody)
	}

	var createResult service.CreateNodeResult
	if err := json.NewDecoder(resp.Body).Decode(&createResult); err != nil {
		t.Fatalf("decode create node result: %v", err)
	}
	if createResult.Edge == nil {
		t.Fatal("create node result has nil Edge")
	}
	edgeID := createResult.Edge.ID
	if edgeID == uuid.Nil {
		t.Fatal("created edge has nil ID")
	}
	if createResult.Edge.EdgeType == "" {
		t.Fatal("created edge has empty EdgeType")
	}
	t.Logf("created edge: id=%s, type=%s, source=%s → target=%s",
		edgeID, createResult.Edge.EdgeType, createResult.Edge.SourceNodeID, createResult.Edge.TargetNodeID)

	// 3. List edges via graph subtree (GET /api/v1/graph/trees/{tree_id}/subtree/{node_id}).
	subtreeURL := "/api/v1/graph/trees/" + tree.ID.String() + "/subtree/" + tree.RootNodeID.String()
	req = authenticatedRequest(t, srv.URL, http.MethodGet, subtreeURL, nil)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET subtree: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody apiErrorBody
		json.NewDecoder(resp.Body).Decode(&errBody)
		t.Fatalf("GET subtree: status=%d; error=%+v", resp.StatusCode, errBody)
	}

	var graphResult service.GraphQueryResult
	if err := json.NewDecoder(resp.Body).Decode(&graphResult); err != nil {
		t.Fatalf("decode graph result: %v", err)
	}
	if len(graphResult.Edges) < 1 {
		t.Fatal("subtree result has no edges; expected at least one")
	}
	edgeFound := false
	for _, e := range graphResult.Edges {
		if e.TargetID == createResult.Node.ID {
			edgeFound = true
			t.Logf("  edge: %s -> %s, type=%s", e.SourceID, e.TargetID, e.EdgeType)
			break
		}
	}
	if !edgeFound {
		t.Fatalf("edge for node %s not found in subtree result; got %d edges",
			createResult.Node.ID, len(graphResult.Edges))
	}

	// 4. Delete edge via repo-level SoftDelete (no direct HTTP endpoint for edge deletion).
	edgeRepo := db.NewPGEdgeRepo(pool)
	if err := edgeRepo.SoftDelete(context.Background(), edgeID); err != nil {
		t.Fatalf("edgeRepo.SoftDelete: %v", err)
	}
	t.Logf("soft-deleted edge: %s", edgeID)

	// Verify edge is gone from the subtree.
	req = authenticatedRequest(t, srv.URL, http.MethodGet, subtreeURL, nil)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET subtree after delete: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET subtree after delete: status=%d", resp.StatusCode)
	}
	var result2 service.GraphQueryResult
	json.NewDecoder(resp.Body).Decode(&result2)
	for _, e := range result2.Edges {
		if e.TargetID == createResult.Node.ID && e.SourceID == tree.RootNodeID {
			t.Fatalf("deleted edge %s still visible in subtree", edgeID)
		}
	}
}

// ---------------------------------------------------------------------------
// Auth rejection test
// ---------------------------------------------------------------------------

func TestBE12_AuthRejection(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv, cleanup := newTestServer(t, pool)
	defer cleanup()

	// 1. Missing token → 401 on protected endpoints.
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/trees", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, cerr := srv.Client().Do(req)
	if cerr != nil {
		t.Fatalf("GET trees (no token): %v", cerr)
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

	// 2. Expired token → 401.
	expiredTok := signedToken(t, "canopy-dev-secret", jwt.MapClaims{
		"sub": testUserID.String(),
		"exp": time.Now().Add(-1 * time.Hour).Unix(),
	})
	req, err = http.NewRequest(http.MethodGet, srv.URL+"/api/v1/trees", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+expiredTok)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET trees (expired token): %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired token: status=%d, want=%d", resp.StatusCode, http.StatusUnauthorized)
	}
	json.NewDecoder(resp.Body).Decode(&errBody)

	// 3. Unsigned token → 401.
	unsignedTok := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
		"sub": testUserID.String(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	rawUnsigned, _ := unsignedTok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	req, err = http.NewRequest(http.MethodGet, srv.URL+"/api/v1/trees", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+rawUnsigned)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET trees (unsigned token): %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsigned token: status=%d, want=%d", resp.StatusCode, http.StatusUnauthorized)
	}

	// 4. Token with no sub claim → 401.
	noSubTok := signedToken(t, "canopy-dev-secret", jwt.MapClaims{
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	req, err = http.NewRequest(http.MethodGet, srv.URL+"/api/v1/trees", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+noSubTok)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET trees (no sub): %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no sub claim: status=%d, want=%d", resp.StatusCode, http.StatusUnauthorized)
	}
}

// ---------------------------------------------------------------------------
// Validation error test
// ---------------------------------------------------------------------------

func TestBE12_ValidationErrors(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewIntegrationPool(t)
	defer testutil.TruncateAll(t, pool)

	srv, cleanup := newTestServer(t, pool)
	defer cleanup()

	// 1. Bad UUID in tree_id → 400.
	req := authenticatedRequest(t, srv.URL, http.MethodGet,
		"/api/v1/trees/not-a-uuid", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET tree bad uuid: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad tree_id uuid: status=%d, want=%d", resp.StatusCode, http.StatusBadRequest)
	}
	var errBody apiErrorBody
	json.NewDecoder(resp.Body).Decode(&errBody)
	if errBody.Error.Code != "INVALID_TREE_ID" {
		t.Fatalf("error code = %q, want INVALID_TREE_ID", errBody.Error.Code)
	}

	// 2. Malformed JSON → 400.
	req = authenticatedRequest(t, srv.URL, http.MethodPost, "/api/v1/trees", nil)
	req.Body = io.NopCloser(bytes.NewBufferString("{not json"))
	req.Header.Set("Content-Type", "application/json")
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST trees malformed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed JSON: status=%d, want=%d", resp.StatusCode, http.StatusBadRequest)
	}
	json.NewDecoder(resp.Body).Decode(&errBody)
	if errBody.Error.Code != "INVALID_BODY" {
		t.Fatalf("error code = %q, want INVALID_BODY", errBody.Error.Code)
	}

	// 3. Bad UUID in node_id → 400.
	req = authenticatedRequest(t, srv.URL, http.MethodGet,
		"/api/v1/nodes/"+uuid.New().String()+"/nodes/not-a-uuid", nil)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET node bad uuid: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad node_id uuid: status=%d, want=%d", resp.StatusCode, http.StatusBadRequest)
	}
	json.NewDecoder(resp.Body).Decode(&errBody)
	if errBody.Error.Code != "INVALID_NODE_ID" {
		t.Fatalf("error code = %q, want INVALID_NODE_ID", errBody.Error.Code)
	}

	// 4. Create tree without title → 400 (validation).
	req = authenticatedRequest(t, srv.URL, http.MethodPost, "/api/v1/trees", map[string]any{})
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST tree no title: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing title: status=%d, want=%d", resp.StatusCode, http.StatusBadRequest)
	}
	json.NewDecoder(resp.Body).Decode(&errBody)
	if errBody.Error.Code != "VALIDATION_ERROR" {
		t.Fatalf("error code = %q, want VALIDATION_ERROR", errBody.Error.Code)
	}

	// 5. GET non-existent tree → 404.
	req = authenticatedRequest(t, srv.URL, http.MethodGet,
		"/api/v1/trees/"+uuid.New().String(), nil)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET nonexistent tree: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("nonexistent tree: status=%d, want=%d", resp.StatusCode, http.StatusNotFound)
	}
}
