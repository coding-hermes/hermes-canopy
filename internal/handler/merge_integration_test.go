// Endpoint tests for the merge (synthesis node) write path — SPEC-API-04 §3 —
// against a real PostgreSQL.
//
// The router under test is assembled from the production middleware chain
// (BodySizeLimit → AuthMiddleware → TreeMembershipMiddleware) and the real
// services, so every row of the §3.3 validation table is exercised through
// HTTP, not through the service API. The production mount itself (a real
// newRouter walk, with the same middleware class as the other tree-scoped
// routes) is asserted by TestRouteParityDocumentedNodeRoutes in
// internal/server — this package cannot build newRouter without an import
// cycle.
//
// Covers: AC2 happy path + envelope + graph re-read, AC3 the full §3.3 table,
// AC4 atomicity (no partial writes), AC5 SSE event order, AC6 chained merges.
package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/service"
	"github.com/coding-hermes/hermes-canopy/internal/sse"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

// mergeTestSecret matches the secret authenticatedRequest/authHeader sign with.
const mergeTestSecret = "canopy-dev-secret"

// --- Harness -----------------------------------------------------------------

// mergeHarness owns one httptest server (the production middleware chain) and
// one author user, so `author_display_name` is deterministic per test.
type mergeHarness struct {
	srv         *httptest.Server
	pool        *pgxpool.Pool
	hub         sse.SSEHub
	userID      uuid.UUID
	displayName string
}

func newMergeHarness(t *testing.T, pool *pgxpool.Pool) *mergeHarness {
	t.Helper()

	userID := uuid.New()
	displayName := "Merge Author"
	_, err := pool.Exec(context.Background(),
		`INSERT INTO users (id, hermes_user_id, display_name) VALUES ($1, $2, $3)`,
		userID, "merge-author-"+userID.String(), displayName)
	require.NoError(t, err, "insert merge author")

	nodeRepo := db.NewPGNodeRepo(pool)
	edgeRepo := db.NewPGEdgeRepo(pool)
	hub := sse.NewHub()
	nodeSvc := service.NewNodeService(nodeRepo, edgeRepo, pool, hub)
	mergeSvc := service.NewMergeService(pool, hub)

	mergeHandler := NewMergeHandler(mergeSvc)
	nodeHandler := NewNodeHandler(nodeSvc, nil)
	membership := TreeMembershipMiddleware(db.NewPGTreeMemberRepo(pool))

	r := chi.NewRouter()
	// Production global middleware (internal/server/server.go).
	r.Use(BodySizeLimit(1024 * 1024))
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(AuthMiddleware(mergeTestSecret))
		r.With(membership).Post("/trees/{tree_id}/merge", mergeHandler.CreateMerge)
		// The tree-scoped node surface, mounted exactly as production mounts
		// it — used to re-read the graph after a merge (§3.6).
		treeNodes := chi.NewRouter()
		treeNodes.Use(membership)
		treeNodes.Mount("/", nodeHandler.TreeRoutes())
		r.Mount("/trees/{tree_id}/nodes", treeNodes)
	})

	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return &mergeHarness{srv: srv, pool: pool, hub: hub, userID: userID, displayName: displayName}
}

// mergeTree is one tree fixture: a tree row, its root node and its membership.
type mergeTree struct {
	ID     uuid.UUID
	RootID uuid.UUID
}

// newTree creates a tree owned by the harness author with a root node.
func (h *mergeHarness) newTree(t *testing.T) mergeTree {
	t.Helper()
	ctx := context.Background()
	treeID := uuid.New()
	_, err := h.pool.Exec(ctx,
		`INSERT INTO trees (id, owner_id, title) VALUES ($1, $2, 'Merge Tree')`, treeID, h.userID)
	require.NoError(t, err, "create tree")
	_, err = h.pool.Exec(ctx,
		`INSERT INTO tree_members (tree_id, user_id, role) VALUES ($1, $2, 'owner')`, treeID, h.userID)
	require.NoError(t, err, "create tree membership")

	rootID := h.node(t, treeID, nil)
	_, err = h.pool.Exec(ctx, `UPDATE trees SET root_node_id = $1 WHERE id = $2`, rootID, treeID)
	require.NoError(t, err, "set tree root")
	return mergeTree{ID: treeID, RootID: rootID}
}

// node inserts a message node (optionally with a display parent) authored by
// the harness author and returns its id. sequence_num is left to the table
// trigger, exactly like the production create path.
func (h *mergeHarness) node(t *testing.T, treeID uuid.UUID, parentID *uuid.UUID) uuid.UUID {
	t.Helper()
	nodeID := uuid.New()
	_, err := h.pool.Exec(context.Background(), `
        INSERT INTO nodes (id, tree_id, parent_id, author_id, content, node_type)
        VALUES ($1, $2, $3, $4, 'seed', 'message')`,
		nodeID, treeID, parentID, h.userID)
	require.NoError(t, err, "insert node")
	return nodeID
}

// softDelete marks a node deleted without going through the not-yet-mounted
// DELETE route.
func (h *mergeHarness) softDelete(t *testing.T, nodeID uuid.UUID) {
	t.Helper()
	_, err := h.pool.Exec(context.Background(),
		`UPDATE nodes SET deleted_at = clock_timestamp(), content = '' WHERE id = $1`, nodeID)
	require.NoError(t, err, "soft-delete node")
}

func (h *mergeHarness) mergePath(treeID uuid.UUID) string {
	return "/api/v1/trees/" + treeID.String() + "/merge"
}

func (h *mergeHarness) nodesPath(treeID uuid.UUID) string {
	return "/api/v1/trees/" + treeID.String() + "/nodes"
}

// mergeRequest builds an authenticated request for an arbitrary user id (the
// non-member row of the §3.3 table needs a token that is valid but belongs to
// nobody in the tree).
func (h *mergeHarness) mergeRequest(t *testing.T, userID uuid.UUID, method, path string, body any) *http.Request {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		require.NoError(t, err, "marshal body")
		reader = strings.NewReader(string(raw))
	}
	req, err := http.NewRequest(method, h.srv.URL+path, reader)
	require.NoError(t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+signedToken(t, mergeTestSecret, mapClaims(userID)))
	return req
}

func mapClaims(userID uuid.UUID) map[string]any {
	return map[string]any{"sub": userID.String(), "exp": time.Now().Add(time.Hour).Unix()}
}

// do executes a request and returns status + raw body.
func (h *mergeHarness) do(t *testing.T, req *http.Request) (int, []byte) {
	t.Helper()
	resp, err := h.srv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, raw
}

// postMerge issues POST /trees/{id}/merge as the harness author.
func (h *mergeHarness) postMerge(t *testing.T, treeID uuid.UUID, body any) (int, []byte) {
	t.Helper()
	return h.do(t, h.mergeRequest(t, h.userID, http.MethodPost, h.mergePath(treeID), body))
}

// mergeBody builds a §3.2 request body.
func mergeBody(sourceIDs []uuid.UUID, extra map[string]any) map[string]any {
	raw := make([]string, 0, len(sourceIDs))
	for _, id := range sourceIDs {
		raw = append(raw, id.String())
	}
	body := map[string]any{"source_node_ids": raw, "content": "synthesis of two branches"}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

// mergeCounts returns the tree's total node and edge rows (soft-deleted
// included) — the probe AC4 uses to prove nothing partial was written.
func (h *mergeHarness) mergeCounts(t *testing.T, treeID uuid.UUID) (int, int) {
	t.Helper()
	ctx := context.Background()
	var nodes, edges int
	require.NoError(t, h.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM nodes WHERE tree_id = $1`, treeID).Scan(&nodes))
	require.NoError(t, h.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM edges WHERE tree_id = $1`, treeID).Scan(&edges))
	return nodes, edges
}

// mergeMaxSequence returns the tree's current maximum node sequence_num.
func (h *mergeHarness) mergeMaxSequence(t *testing.T, treeID uuid.UUID) int64 {
	t.Helper()
	var seq int64
	require.NoError(t, h.pool.QueryRow(context.Background(),
		`SELECT COALESCE(MAX(sequence_num), 0) FROM nodes WHERE tree_id = $1`, treeID).Scan(&seq))
	return seq
}

// mergeErrorEnvelope is the repo's error envelope (handler_util.writeError).
type mergeErrorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func mergeErrorCode(t *testing.T, raw []byte) string {
	t.Helper()
	var env mergeErrorEnvelope
	require.NoError(t, json.Unmarshal(raw, &env), "decode error envelope: %s", string(raw))
	require.NotEmpty(t, env.Error.Code, "error envelope carries no code: %s", string(raw))
	return env.Error.Code
}

func decodeMergeResult(t *testing.T, raw []byte) service.CreateMergeResult {
	t.Helper()
	var out service.CreateMergeResult
	require.NoError(t, json.Unmarshal(raw, &out), "decode 201 envelope: %s", string(raw))
	require.NotNil(t, out.Node, "201 envelope has no node: %s", string(raw))
	return out
}

// --- AC2: happy path through the production middleware chain -----------------

func TestMergeIntegration_HappyPath(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	h := newMergeHarness(t, pool)

	tree := h.newTree(t)
	branchA := h.node(t, tree.ID, &tree.RootID)
	branchB := h.node(t, tree.ID, &tree.RootID)
	beforeSeq := h.mergeMaxSequence(t, tree.ID)
	beforeNodes, beforeEdges := h.mergeCounts(t, tree.ID)

	metadata := map[string]any{
		"merge_summary": "Resolved branch divergence on tree storage strategy",
		"decision":      "CTE with index optimization",
	}
	status, raw := h.postMerge(t, tree.ID, mergeBody([]uuid.UUID{branchA, branchB}, map[string]any{
		"content_format":   "markdown",
		"target_parent_id": tree.RootID.String(),
		"metadata":         metadata,
	}))
	require.Equal(t, http.StatusCreated, status, "merge body: %s", string(raw))
	result := decodeMergeResult(t, raw)

	node := result.Node
	assert.Equal(t, tree.ID, node.TreeID, "node.tree_id")
	assert.Equal(t, "synthesis", node.NodeType, "node.node_type")
	require.NotNil(t, node.ParentID, "node.parent_id must be the placement target")
	assert.Equal(t, tree.RootID, *node.ParentID, "node.parent_id")
	assert.Equal(t, tree.RootID, *node.ParentID, "target_parent_id echoed as parent_id")
	assert.Equal(t, 1, node.Depth, "depth = target.depth+1 (root has depth 0)")
	assert.Equal(t, beforeSeq+1, node.SequenceNum, "sequence_num = previous max + 1")
	assert.Equal(t, 0, node.ChildCount, "child_count is 0 on creation")
	assert.Nil(t, node.EditedAt, "edited_at must be null")
	assert.Nil(t, node.DeletedAt, "deleted_at must be null")
	assert.Equal(t, h.userID, node.AuthorID, "author_id comes from the JWT subject")
	assert.Equal(t, h.displayName, node.AuthorDisplayName, "author_display_name (JOIN users)")
	assert.Equal(t, "markdown", node.ContentFormat, "content_format")

	var echoed map[string]any
	require.NoError(t, json.Unmarshal(node.Metadata, &echoed), "metadata: %s", node.Metadata)
	assert.Equal(t, metadata, echoed, "metadata echoed verbatim")

	// The §3.6 envelope's nulls must be literal nulls, not omitted keys.
	assert.Contains(t, string(raw), `"edited_at":null`, "envelope must carry edited_at:null")
	assert.Contains(t, string(raw), `"deleted_at":null`, "envelope must carry deleted_at:null")

	// edges: exactly N+1, parent reply edge first, then the sources in request order.
	require.Len(t, result.Edges, 3, "1 parent edge + 2 synthesis edges")
	assert.Equal(t, "reply", result.Edges[0].EdgeType)
	assert.Equal(t, tree.RootID, result.Edges[0].SourceNodeID)
	assert.Equal(t, node.ID, result.Edges[0].TargetNodeID)
	for i, sourceID := range []uuid.UUID{branchA, branchB} {
		edge := result.Edges[i+1]
		assert.Equal(t, "synthesis", edge.EdgeType, "edge %d type", i+1)
		assert.Equal(t, sourceID, edge.SourceNodeID, "edge %d source (request order)", i+1)
		assert.Equal(t, node.ID, edge.TargetNodeID, "edge %d target", i+1)
		assert.NotEqual(t, uuid.Nil, edge.ID)
	}
	require.Len(t, result.MergedSourceIDs, 2)
	assert.Equal(t, []uuid.UUID{branchA, branchB}, result.MergedSourceIDs, "merged_source_ids echoes request order")

	// The edges are REAL ROWS: re-read the graph through the API.
	status, raw = h.do(t, h.mergeRequest(t, h.userID, http.MethodGet, h.nodesPath(tree.ID), nil))
	require.Equal(t, http.StatusOK, status, "list nodes: %s", string(raw))
	var listed struct {
		Nodes []service.NodeDetail `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal(raw, &listed))
	var found *service.NodeDetail
	for i := range listed.Nodes {
		if listed.Nodes[i].ID == node.ID {
			found = &listed.Nodes[i]
			break
		}
	}
	require.NotNil(t, found, "synthesis node is not reachable through GET /trees/{id}/nodes")
	assert.Equal(t, "synthesis", found.NodeType)

	// …and reachable from BOTH sources: the incoming edges are rows, not just
	// a response body.
	var incomingSources []uuid.UUID
	var incomingTypes []string
	rows, err := pool.Query(context.Background(), `
        SELECT source_id, edge_type FROM edges
        WHERE target_id = $1 AND deleted_at IS NULL
        ORDER BY sequence_num`, node.ID)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var src uuid.UUID
		var edgeType string
		require.NoError(t, rows.Scan(&src, &edgeType))
		incomingSources = append(incomingSources, src)
		incomingTypes = append(incomingTypes, edgeType)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []uuid.UUID{tree.RootID, branchA, branchB}, incomingSources)
	assert.Equal(t, []string{"reply", "synthesis", "synthesis"}, incomingTypes)

	// Row counts moved by exactly one node and three edges.
	afterNodes, afterEdges := h.mergeCounts(t, tree.ID)
	assert.Equal(t, beforeNodes+1, afterNodes, "exactly one node written")
	assert.Equal(t, beforeEdges+3, afterEdges, "exactly N+1 edges written")

	t.Run("100 sources create 101 edges", func(t *testing.T) {
		sources := make([]uuid.UUID, 0, 100)
		for i := 0; i < 100; i++ {
			sources = append(sources, h.node(t, tree.ID, &tree.RootID))
		}
		status, raw := h.postMerge(t, tree.ID, mergeBody(sources, nil))
		require.Equal(t, http.StatusCreated, status, "100-source merge: %s", string(raw))
		big := decodeMergeResult(t, raw)
		assert.Len(t, big.Edges, 101, "1 parent edge + 100 synthesis edges")
		assert.Equal(t, sources, big.MergedSourceIDs)
	})

	t.Run("default target is the tree root", func(t *testing.T) {
		a := h.node(t, tree.ID, &tree.RootID)
		b := h.node(t, tree.ID, &tree.RootID)
		status, raw := h.postMerge(t, tree.ID, mergeBody([]uuid.UUID{a, b}, nil))
		require.Equal(t, http.StatusCreated, status, "default-target merge: %s", string(raw))
		result := decodeMergeResult(t, raw)
		require.NotNil(t, result.Node.ParentID)
		assert.Equal(t, tree.RootID, *result.Node.ParentID, "omitted target_parent_id defaults to the root")
		if assert.NotEmpty(t, result.Edges) {
			assert.Equal(t, "reply", result.Edges[0].EdgeType)
			assert.Equal(t, tree.RootID, result.Edges[0].SourceNodeID)
		}
	})
}

// --- AC3: the §3.3 validation table through the router -----------------------

func TestMergeIntegration_ValidationTable(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	h := newMergeHarness(t, pool)

	tree := h.newTree(t)
	okA := h.node(t, tree.ID, &tree.RootID)
	okB := h.node(t, tree.ID, &tree.RootID)
	deleted := h.node(t, tree.ID, &tree.RootID)
	h.softDelete(t, deleted)
	deletedTarget := h.node(t, tree.ID, &tree.RootID)
	h.softDelete(t, deletedTarget)

	otherTree := h.newTree(t)
	otherNode := h.node(t, otherTree.ID, &otherTree.RootID)

	deletedTree := h.newTree(t)
	_, err := pool.Exec(context.Background(),
		`UPDATE trees SET deleted_at = clock_timestamp() WHERE id = $1`, deletedTree.ID)
	require.NoError(t, err, "soft-delete tree")

	// 101 sources for the MAX row; 100 is valid (§11.1 scenarios 18-19).
	manySources := func(n int) []uuid.UUID {
		out := make([]uuid.UUID, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, h.node(t, tree.ID, &tree.RootID))
		}
		return out
	}

	cases := []struct {
		name        string
		path        string
		body        any
		asNonMember bool
		wantStatus  int
		wantCode    string
	}{
		{
			name:       "source_node_ids not an array",
			body:       map[string]any{"source_node_ids": "not-an-array", "content": "x"},
			wantStatus: 400, wantCode: "INVALID_SOURCE_NODE_IDS",
		},
		{
			name:       "source_node_ids array of non-strings",
			body:       map[string]any{"source_node_ids": []int{1, 2}, "content": "x"},
			wantStatus: 400, wantCode: "INVALID_SOURCE_NODE_IDS",
		},
		{
			name:       "source_node_ids missing",
			body:       map[string]any{"content": "x"},
			wantStatus: 400, wantCode: "INVALID_SOURCE_NODE_IDS",
		},
		{
			name:       "one source node",
			body:       mergeBody([]uuid.UUID{okA}, nil),
			wantStatus: 400, wantCode: "MIN_SOURCE_NODES",
		},
		{
			name:       "101 source nodes",
			body:       mergeBody(manySources(101), nil),
			wantStatus: 400, wantCode: "MAX_SOURCE_NODES",
		},
		{
			name:       "duplicate source nodes",
			body:       mergeBody([]uuid.UUID{okA, okB, okA}, nil),
			wantStatus: 400, wantCode: "DUPLICATE_SOURCE_NODES",
		},
		{
			name:       "malformed source uuid",
			body:       map[string]any{"source_node_ids": []string{okA.String(), "not-a-uuid"}, "content": "x"},
			wantStatus: 400, wantCode: "INVALID_SOURCE_NODE_ID",
		},
		{
			name:       "source node does not exist",
			body:       mergeBody([]uuid.UUID{okA, uuid.New()}, nil),
			wantStatus: 404, wantCode: "SOURCE_NODE_NOT_FOUND",
		},
		{
			name:       "source node soft-deleted",
			body:       mergeBody([]uuid.UUID{okA, deleted}, nil),
			wantStatus: 410, wantCode: "SOURCE_NODE_DELETED",
		},
		{
			name:       "source node from another tree",
			body:       mergeBody([]uuid.UUID{okA, otherNode}, nil),
			wantStatus: 400, wantCode: "TREE_MISMATCH",
		},
		{
			name: "content exceeds 65536 characters",
			body: mergeBody([]uuid.UUID{okA, okB}, map[string]any{
				"content": strings.Repeat("c", 65537),
			}),
			wantStatus: 400, wantCode: "CONTENT_TOO_LONG",
		},
		{
			name: "invalid content_format",
			body: mergeBody([]uuid.UUID{okA, okB}, map[string]any{
				"content_format": "html",
			}),
			wantStatus: 400, wantCode: "INVALID_CONTENT_FORMAT",
		},
		{
			name: "invalid target_parent_id",
			body: mergeBody([]uuid.UUID{okA, okB}, map[string]any{
				"target_parent_id": "not-a-uuid",
			}),
			wantStatus: 400, wantCode: "INVALID_TARGET_PARENT_ID",
		},
		{
			name: "target parent does not exist",
			body: mergeBody([]uuid.UUID{okA, okB}, map[string]any{
				"target_parent_id": uuid.New().String(),
			}),
			wantStatus: 404, wantCode: "TARGET_PARENT_NOT_FOUND",
		},
		{
			name: "target parent soft-deleted",
			body: mergeBody([]uuid.UUID{okA, okB}, map[string]any{
				"target_parent_id": deletedTarget.String(),
			}),
			wantStatus: 409, wantCode: "TARGET_PARENT_DELETED",
		},
		{
			name: "target parent in another tree",
			body: mergeBody([]uuid.UUID{okA, okB}, map[string]any{
				"target_parent_id": otherTree.RootID.String(),
			}),
			wantStatus: 400, wantCode: "TREE_MISMATCH",
		},
		{
			name: "metadata exceeds 16KB",
			body: mergeBody([]uuid.UUID{okA, okB}, map[string]any{
				"metadata": map[string]any{"blob": strings.Repeat("m", 17000)},
			}),
			wantStatus: 400, wantCode: "METADATA_TOO_LARGE",
		},
		{
			name: "source equals explicit target parent",
			body: mergeBody([]uuid.UUID{okA, okB}, map[string]any{
				"target_parent_id": okA.String(),
			}),
			wantStatus: 400, wantCode: "SOURCE_TARGET_OVERLAP",
		},
		{
			name:       "source equals the defaulted root target",
			body:       mergeBody([]uuid.UUID{tree.RootID, okA}, nil),
			wantStatus: 400, wantCode: "SOURCE_TARGET_OVERLAP",
		},
		{
			name:       "request body exceeds 1MB",
			body:       mergeBody([]uuid.UUID{okA, okB}, map[string]any{"content": strings.Repeat("b", 1024*1024+1)}),
			wantStatus: 413, wantCode: "REQUEST_TOO_LARGE",
		},
		{
			name:        "user is not a tree member",
			body:        mergeBody([]uuid.UUID{okA, okB}, nil),
			asNonMember: true,
			wantStatus:  403, wantCode: "NOT_TREE_MEMBER",
		},
		{
			name:       "tree is soft-deleted",
			path:       h.mergePath(deletedTree.ID),
			body:       mergeBody([]uuid.UUID{deletedTree.RootID, h.node(t, deletedTree.ID, &deletedTree.RootID)}, nil),
			wantStatus: 410, wantCode: "TREE_DELETED",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.path
			if path == "" {
				path = h.mergePath(tree.ID)
			}
			userID := h.userID
			if tc.asNonMember {
				userID = uuid.New()
			}
			status, raw := h.do(t, h.mergeRequest(t, userID, http.MethodPost, path, tc.body))
			assert.Equal(t, tc.wantStatus, status, "body: %s", string(raw))
			assert.Equal(t, tc.wantCode, mergeErrorCode(t, raw))
		})
	}
}

// --- AC4: atomicity — no partial writes -------------------------------------

// TestMergeIntegration_AtomicityNoPartialWrites proves a rejected merge leaves
// NO node and NO edge behind, and proves the count probe is not vacuous by
// running a successful merge against the same probe.
func TestMergeIntegration_AtomicityNoPartialWrites(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	h := newMergeHarness(t, pool)

	tree := h.newTree(t)
	okA := h.node(t, tree.ID, &tree.RootID)
	okB := h.node(t, tree.ID, &tree.RootID)
	deleted := h.node(t, tree.ID, &tree.RootID)
	h.softDelete(t, deleted)

	otherTree := h.newTree(t)
	foreign := h.node(t, otherTree.ID, &otherTree.RootID)

	beforeNodes, beforeEdges := h.mergeCounts(t, tree.ID)
	beforeSeq := h.mergeMaxSequence(t, tree.ID)

	// Failure arm 1: a source from another tree.
	status, raw := h.postMerge(t, tree.ID, mergeBody([]uuid.UUID{okA, foreign}, nil))
	require.Equal(t, http.StatusBadRequest, status, "cross-tree source: %s", string(raw))
	require.Equal(t, "TREE_MISMATCH", mergeErrorCode(t, raw))
	afterNodes, afterEdges := h.mergeCounts(t, tree.ID)
	assert.Equal(t, beforeNodes, afterNodes, "a rejected merge must not create a node")
	assert.Equal(t, beforeEdges, afterEdges, "a rejected merge must not create an edge")
	assert.Equal(t, beforeSeq, h.mergeMaxSequence(t, tree.ID), "a rejected merge must not consume a sequence_num")

	// Failure arm 2: a soft-deleted source.
	status, raw = h.postMerge(t, tree.ID, mergeBody([]uuid.UUID{okA, deleted}, nil))
	require.Equal(t, http.StatusGone, status, "deleted source: %s", string(raw))
	require.Equal(t, "SOURCE_NODE_DELETED", mergeErrorCode(t, raw))
	afterNodes, afterEdges = h.mergeCounts(t, tree.ID)
	assert.Equal(t, beforeNodes, afterNodes, "a rejected merge must not create a node")
	assert.Equal(t, beforeEdges, afterEdges, "a rejected merge must not create an edge")

	// No synthesis node leaked into the graph either.
	var synthesis int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM nodes WHERE tree_id = $1 AND node_type = 'synthesis'`, tree.ID).Scan(&synthesis))
	assert.Zero(t, synthesis, "rejected merges must not leave synthesis nodes")

	// Positive control: the same probe DOES see a committed merge.
	status, raw = h.postMerge(t, tree.ID, mergeBody([]uuid.UUID{okA, okB}, nil))
	require.Equal(t, http.StatusCreated, status, "control merge: %s", string(raw))
	control := decodeMergeResult(t, raw)
	afterNodes, afterEdges = h.mergeCounts(t, tree.ID)
	assert.Equal(t, beforeNodes+1, afterNodes, "control: exactly one node written")
	assert.Equal(t, beforeEdges+3, afterEdges, "control: exactly N+1 edges written")
	assert.Equal(t, beforeSeq+1, control.Node.SequenceNum,
		"the failed attempts consumed no sequence_num — the control takes previous max + 1")
}

// --- AC5: SSE events, in order ----------------------------------------------

// TestMergeIntegration_SSEEventOrder asserts the §3.8 broadcast sequence on a
// subscriber of the tree: node_added, then one edge_added per created edge
// (parent edge first, sources in request order), then the composite
// tree_merged as the LAST event.
func TestMergeIntegration_SSEEventOrder(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	h := newMergeHarness(t, pool)

	tree := h.newTree(t)
	sourceA := h.node(t, tree.ID, &tree.RootID)
	sourceB := h.node(t, tree.ID, &tree.RootID)

	client := newTransportTestClient("merge-sse", h.userID, tree.ID)
	require.NoError(t, h.hub.Subscribe(context.Background(), tree.ID, client))
	t.Cleanup(func() { h.hub.Unsubscribe(tree.ID, client.ID()) })

	status, raw := h.postMerge(t, tree.ID, mergeBody([]uuid.UUID{sourceA, sourceB}, map[string]any{
		"target_parent_id": tree.RootID.String(),
	}))
	require.Equal(t, http.StatusCreated, status, "merge: %s", string(raw))
	result := decodeMergeResult(t, raw)

	client.mu.Lock()
	events := append([]sse.SSEEvent(nil), client.events...)
	client.mu.Unlock()

	// The hub broadcasts synchronously inside the request, so every event of
	// this merge is already delivered when the 201 comes back.
	require.Len(t, events, 5, "1 node_added + 3 edge_added + 1 tree_merged")
	types := make([]string, 0, len(events))
	for _, ev := range events {
		types = append(types, ev.Type)
	}
	assert.Equal(t, []string{"node_added", "edge_added", "edge_added", "edge_added", "tree_merged"}, types,
		"event order per §3.8")

	// node_added carries the synthesis node.
	var nodeAdded map[string]any
	require.NoError(t, json.Unmarshal(events[0].Data, &nodeAdded))
	assert.Equal(t, result.Node.ID.String(), nodeAdded["node_id"])
	assert.Equal(t, tree.ID.String(), nodeAdded["tree_id"])

	// edge_added payloads are the §3.6 edge representation, parent first.
	type edgePayload struct {
		ID           uuid.UUID `json:"id"`
		TreeID       uuid.UUID `json:"tree_id"`
		SourceNodeID uuid.UUID `json:"source_node_id"`
		TargetNodeID uuid.UUID `json:"target_node_id"`
		EdgeType     string    `json:"edge_type"`
		CreatedAt    time.Time `json:"created_at"`
	}
	decoded := make([]edgePayload, 0, 3)
	for _, ev := range events[1:4] {
		var payload edgePayload
		require.NoError(t, json.Unmarshal(ev.Data, &payload), "edge payload: %s", string(ev.Data))
		decoded = append(decoded, payload)
	}
	assert.Equal(t, tree.RootID, decoded[0].SourceNodeID)
	assert.Equal(t, "reply", decoded[0].EdgeType)
	assert.Equal(t, result.Node.ID, decoded[0].TargetNodeID)
	assert.Equal(t, sourceA, decoded[1].SourceNodeID)
	assert.Equal(t, "synthesis", decoded[1].EdgeType)
	assert.Equal(t, sourceB, decoded[2].SourceNodeID)
	assert.Equal(t, "synthesis", decoded[2].EdgeType)
	for i, payload := range decoded {
		assert.Equal(t, result.Edges[i].ID, payload.ID, "edge event %d carries the created edge id", i)
		assert.False(t, payload.CreatedAt.IsZero(), "edge event %d carries created_at", i)
	}

	// tree_merged is the composite last event.
	var merged map[string]any
	require.NoError(t, json.Unmarshal(events[4].Data, &merged))
	assert.Equal(t, result.Node.ID.String(), merged["merge_node_id"])
	assert.Equal(t, tree.ID.String(), merged["tree_id"])
	rawSources, ok := merged["source_node_ids"].([]any)
	require.True(t, ok, "tree_merged must carry source_node_ids: %s", string(events[4].Data))
	gotSources := make([]string, 0, len(rawSources))
	for _, v := range rawSources {
		s, _ := v.(string)
		gotSources = append(gotSources, s)
	}
	assert.Equal(t, []string{sourceA.String(), sourceB.String()}, gotSources, "request order")
	assert.NotEmpty(t, merged["timestamp"], "tree_merged carries a timestamp")
}

// --- AC6: chained merges ----------------------------------------------------

// TestMergeIntegration_ChainedMerge merges a synthesis node with a plain node
// (§3.2: a source may itself be a synthesis node) and asserts the synthesis
// edge from the synthesis source — the "merge of merges" of §12 edge case 2.
func TestMergeIntegration_ChainedMerge(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	h := newMergeHarness(t, pool)

	tree := h.newTree(t)
	a := h.node(t, tree.ID, &tree.RootID)
	b := h.node(t, tree.ID, &tree.RootID)
	c := h.node(t, tree.ID, &tree.RootID)

	status, raw := h.postMerge(t, tree.ID, mergeBody([]uuid.UUID{a, b}, nil))
	require.Equal(t, http.StatusCreated, status, "first merge: %s", string(raw))
	first := decodeMergeResult(t, raw)
	require.Equal(t, "synthesis", first.Node.NodeType)

	status, raw = h.postMerge(t, tree.ID, mergeBody([]uuid.UUID{first.Node.ID, c}, map[string]any{
		"target_parent_id": tree.RootID.String(),
	}))
	require.Equal(t, http.StatusCreated, status, "chained merge: %s", string(raw))
	second := decodeMergeResult(t, raw)

	assert.Equal(t, "synthesis", second.Node.NodeType, "a chained merge is still a synthesis node")
	assert.NotEqual(t, first.Node.ID, second.Node.ID, "chained merge creates a NEW node")
	require.Len(t, second.Edges, 3)
	assert.Equal(t, "reply", second.Edges[0].EdgeType)
	assert.Equal(t, tree.RootID, second.Edges[0].SourceNodeID)
	assert.Equal(t, "synthesis", second.Edges[1].EdgeType)
	assert.Equal(t, first.Node.ID, second.Edges[1].SourceNodeID, "the synthesis source keeps a synthesis edge")
	assert.Equal(t, "synthesis", second.Edges[2].EdgeType)
	assert.Equal(t, c, second.Edges[2].SourceNodeID)
	assert.Equal(t, []uuid.UUID{first.Node.ID, c}, second.MergedSourceIDs)

	// The chain is real in the graph: the second synthesis node is reachable
	// from the first synthesis node.
	var chained int
	require.NoError(t, pool.QueryRow(context.Background(), `
        SELECT COUNT(*) FROM edges
        WHERE source_id = $1 AND target_id = $2 AND edge_type = 'synthesis'`,
		first.Node.ID, second.Node.ID).Scan(&chained))
	assert.Equal(t, 1, chained, "synthesis→synthesis edge must be a real row")

	// A synthesis node is first-class: it can also be the merge TARGET spine.
	status, raw = h.postMerge(t, tree.ID, mergeBody([]uuid.UUID{a, c}, map[string]any{
		"target_parent_id": first.Node.ID.String(),
	}))
	require.Equal(t, http.StatusCreated, status, "merge into a synthesis target: %s", string(raw))
	third := decodeMergeResult(t, raw)
	require.NotNil(t, third.Node.ParentID)
	assert.Equal(t, first.Node.ID, *third.Node.ParentID)
	assert.Equal(t, 2, third.Node.Depth, "depth = the synthesis parent's depth + 1")
}
