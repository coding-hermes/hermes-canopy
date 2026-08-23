// Package handler — BUG-043 regression: tree-scoped node routes must return
// 410 TREE_DELETED when the tree is soft-deleted.
//
// Unlike most harnesses here (which stub the membership checker), this file
// wires the REAL db.PGTreeMemberRepo as the TreeMemberChecker and mounts
// routes exactly as internal/server/server.go does — the tree-scoped node
// mount under TreeMembershipMiddleware plus the tree CRUD mount — so the
// deleted-tree gate is exercised against real PostgreSQL end-to-end.
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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/service"
	"github.com/coding-hermes/hermes-canopy/internal/sse"
	"github.com/coding-hermes/hermes-canopy/internal/sync"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

// newDeletedGateTestServer wires the production route layout: tree CRUD at
// /trees and the tree-scoped node mount at /trees/{tree_id}/nodes behind the
// REAL TreeMembershipMiddleware backed by db.PGTreeMemberRepo.
func newDeletedGateTestServer(t *testing.T, pool *pgxpool.Pool) *httptest.Server {
	t.Helper()

	treeRepo := db.NewPGTreeRepo(pool)
	nodeRepo := db.NewPGNodeRepo(pool)
	edgeRepo := db.NewPGEdgeRepo(pool)
	eventRepo := db.NewEventRepo(pool)
	snapshotRepo := db.NewSnapshotRepo(pool)
	memberRepo := db.NewPGTreeMemberRepo(pool)
	userRepo := db.NewPGUserRepo(pool)

	treeSvc := service.NewTreeService(treeRepo, nodeRepo, edgeRepo, pool)
	sseHub := sse.NewHub()
	nodeSvc := service.NewNodeService(nodeRepo, edgeRepo, pool, sseHub)
	syncEngine := sync.NewEngine(eventRepo, snapshotRepo, sseHub,
		sync.DefaultEngineConfig())

	r := chi.NewRouter()
	authMW := AuthMiddleware("canopy-dev-secret")
	membershipMW := TreeMembershipMiddleware(memberRepo)

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(authMW)

		// Tree CRUD (same as production: no membership middleware on the
		// mount; handlers self-gate ownership/deletion).
		treeHandler := NewTreeHandler(treeSvc, syncEngine).
			WithShares(userRepo, memberRepo, sseHub)
		r.Mount("/trees", treeHandler.Routes())

		// Tree-scoped node CRUD behind membership + deleted-tree middleware
		// (mirrors server.go lines ~137-140).
		nodeHandler := NewNodeHandler(nodeSvc, syncEngine)
		treeNodes := chi.NewRouter()
		treeNodes.Use(membershipMW)
		treeNodes.Mount("/", nodeHandler.TreeRoutes())
		r.Mount("/trees/{tree_id}/nodes", treeNodes)
	})

	return httptest.NewServer(r)
}

// bug043SeedTree creates profile + tree + membership + root node owned by
// the sentinel user (testUserID) and returns tree and node IDs.
func bug043SeedTree(t *testing.T, pool *pgxpool.Pool, title string) (treeID, nodeID uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	require.NoError(t, pool.Ping(ctx))
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, hermes_user_id, display_name)
		VALUES ('a0000000-0000-0000-0000-000000000001', 'testuser', 'Test User')
		ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("seed sentinel user: %v", err)
	}

	profileID := uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO profiles (id, owner_id, profile_type, name, display_name)
		VALUES ($1, 'a0000000-0000-0000-0000-000000000001', 'human', $2, $3)
		ON CONFLICT (id) DO NOTHING`,
		profileID, "bug043-"+profileID.String()[:8], "Bug043 User")
	require.NoError(t, err, "seed profile")

	treeID = uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO trees (id, owner_id, title)
		VALUES ($1, 'a0000000-0000-0000-0000-000000000001', $2)`, treeID, title)
	require.NoError(t, err, "seed tree")

	_, err = pool.Exec(ctx, `INSERT INTO tree_members (tree_id, user_id, role)
		VALUES ($1, 'a0000000-0000-0000-0000-000000000001', 'owner')`, treeID)
	require.NoError(t, err, "seed tree member")

	nodeID = uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO nodes (id, tree_id, author_id, content)
		VALUES ($1, $2, 'a0000000-0000-0000-0000-000000000001', 'root node')`, nodeID, treeID)
	require.NoError(t, err, "seed node")

	return treeID, nodeID
}

// bug043Bearer signs a JWT for the given subject with the dev secret.
func bug043Bearer(t *testing.T, sub string) string {
	t.Helper()
	tok := signedToken(t, "canopy-dev-secret", jwt.MapClaims{
		"sub": sub,
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	return tok
}

// bug043Do performs an authenticated request and returns the response; the
// caller closes the body.
func bug043Do(t *testing.T, srvURL, method, path, token string, body any) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, srvURL+path, rdr)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func bug043DecodeError(t *testing.T, resp *http.Response) apiErrorBody {
	t.Helper()
	var body apiErrorBody
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	return body
}

const bug043SentinelSub = "a0000000-0000-0000-0000-000000000001"

// TestBUG043_DeletedTreeGatesTreeScopedNodeRoutes covers acceptance criteria
// 1-3: list/get-by-id/create on a soft-deleted tree all return 410
// TREE_DELETED, matching the tree handler's GetTree contract.
func TestBUG043_DeletedTreeGatesTreeScopedNodeRoutes(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()

	srv := newDeletedGateTestServer(t, pool)
	defer srv.Close()

	treeID, nodeID := bug043SeedTree(t, pool, "BUG-043 Deleted Gate")
	token := bug043Bearer(t, bug043SentinelSub)

	// Sanity: before deletion the routes serve normally.
	resp := bug043Do(t, srv.URL, http.MethodGet,
		"/api/v1/trees/"+treeID.String()+"/nodes", token, nil)
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "pre-delete list should be 200; body=%s", body)

	// Soft-delete through the repo (same path DELETE /trees/{id} takes).
	require.NoError(t, db.NewPGTreeRepo(pool).SoftDelete(ctx, treeID))

	// Baseline: GET the tree itself → 410 TREE_DELETED (existing contract).
	resp = bug043Do(t, srv.URL, http.MethodGet,
		"/api/v1/trees/"+treeID.String(), token, nil)
	errBody := bug043DecodeError(t, resp)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusGone, resp.StatusCode, "GET tree after delete")
	assert.Equal(t, "TREE_DELETED", errBody.Error.Code)

	// Criterion 1: list nodes on deleted tree → 410 TREE_DELETED.
	resp = bug043Do(t, srv.URL, http.MethodGet,
		"/api/v1/trees/"+treeID.String()+"/nodes", token, nil)
	errBody = bug043DecodeError(t, resp)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusGone, resp.StatusCode, "list nodes on deleted tree")
	assert.Equal(t, "TREE_DELETED", errBody.Error.Code)
	assert.Equal(t, "tree has been deleted", errBody.Error.Message)

	// Criterion 2: get node by ID on deleted tree → 410 TREE_DELETED.
	resp = bug043Do(t, srv.URL, http.MethodGet,
		"/api/v1/trees/"+treeID.String()+"/nodes/"+nodeID.String(), token, nil)
	errBody = bug043DecodeError(t, resp)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusGone, resp.StatusCode, "get node on deleted tree")
	assert.Equal(t, "TREE_DELETED", errBody.Error.Code)

	// Criterion 3: create node on deleted tree (valid body) → 410 TREE_DELETED.
	resp = bug043Do(t, srv.URL, http.MethodPost,
		"/api/v1/trees/"+treeID.String()+"/nodes", token, map[string]any{
			"parent_id": nodeID.String(),
			"content":   "should be rejected",
		})
	errBody = bug043DecodeError(t, resp)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusGone, resp.StatusCode, "create node on deleted tree")
	assert.Equal(t, "TREE_DELETED", errBody.Error.Code)
}

// TestBUG043_LiveTreeRoutesUnchanged covers acceptance criteria 4: live-tree
// behaviour is unchanged (list/get/create succeed for members; non-members
// still get 403 NOT_TREE_MEMBER).
func TestBUG043_LiveTreeRoutesUnchanged(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)

	srv := newDeletedGateTestServer(t, pool)
	defer srv.Close()

	treeID, nodeID := bug043SeedTree(t, pool, "BUG-043 Live Control")
	token := bug043Bearer(t, bug043SentinelSub)

	// List → 200 containing the seeded node.
	resp := bug043Do(t, srv.URL, http.MethodGet,
		"/api/v1/trees/"+treeID.String()+"/nodes", token, nil)
	listRaw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "list on live tree; body=%s", listRaw)
	assert.Contains(t, string(listRaw), nodeID.String(), "seeded node should appear in list")

	// Get by ID → 200.
	resp = bug043Do(t, srv.URL, http.MethodGet,
		"/api/v1/trees/"+treeID.String()+"/nodes/"+nodeID.String(), token, nil)
	getRaw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "get on live tree; body=%s", getRaw)

	// Create → 201.
	resp = bug043Do(t, srv.URL, http.MethodPost,
		"/api/v1/trees/"+treeID.String()+"/nodes", token, map[string]any{
			"parent_id": nodeID.String(),
			"content":   "live tree accepts writes",
		})
	createRaw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "create on live tree; body=%s", createRaw)
	var created struct {
		Node struct {
			ID string `json:"id"`
		} `json:"node"`
	}
	require.NoError(t, json.Unmarshal(createRaw, &created))
	assert.NotEmpty(t, created.Node.ID)

	// Non-member on the SAME live tree → 403 NOT_TREE_MEMBER (unchanged).
	nonMember := bug043Bearer(t, uuid.New().String())
	resp = bug043Do(t, srv.URL, http.MethodGet,
		"/api/v1/trees/"+treeID.String()+"/nodes", nonMember, nil)
	errBody := bug043DecodeError(t, resp)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "non-member on live tree")
	assert.Equal(t, "NOT_TREE_MEMBER", errBody.Error.Code)
}
