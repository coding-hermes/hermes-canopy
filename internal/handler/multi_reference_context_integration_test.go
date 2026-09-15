// Endpoint tests for the §9.3 reference-context read route and the §10 SSE
// convergence vocabulary, against a real PostgreSQL and a real SSE hub.
//
// Covers: the 200 envelope for a multi-reference reply, the 404
// REFERENCE_CONTEXT_NOT_FOUND answer for a non-multi-reference node AND for a
// non-existent node (no existence oracle), include_content=false,
// max_source_tokens truncation, verify_hash change detection, per-node
// membership, and the post-commit event order node_added → N edge_added
// (selection_order) → multi_reference_converged with its §10.2 field set.
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
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/service"
	"github.com/coding-hermes/hermes-canopy/internal/sse"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

// --- Harness -----------------------------------------------------------------

// mrContextServer wires the phase-2 surfaces exactly as the production router
// does: the two tree-scoped PL-06 routes (membership-gated) plus the flat
// §9.3 read route (per-node membership resolved in the handler) over one real
// hub, so a test can subscribe a real client to the tree stream.
func mrContextServer(t *testing.T, pool *pgxpool.Pool) (*httptest.Server, sse.SSEHub) {
	t.Helper()
	require.NoError(t, insertSentinelUser(t, pool))

	nodeRepo := db.NewPGNodeRepo(pool)
	edgeRepo := db.NewPGEdgeRepo(pool)
	treeSvc := service.NewTreeService(db.NewPGTreeRepo(pool), nodeRepo, edgeRepo, pool).
		WithReferenceSelection(service.NewReferenceSelectionSigner(multiReferenceTestSecret, nil), 8000)
	hub := sse.NewHubWithConfig(sse.HubConfig{PruneInterval: -1, DrainTimeout: 100 * time.Millisecond})
	t.Cleanup(func() { _ = hub.Shutdown(context.Background()) })

	nodeSvc := service.NewNodeService(nodeRepo, edgeRepo, pool, hub).
		WithReferenceSelection(treeSvc)

	h := NewMultiReferenceHandler(treeSvc, nodeSvc).
		WithMembership(db.NewPGTreeMemberRepo(pool))
	membership := TreeMembershipMiddleware(db.NewPGTreeMemberRepo(pool))

	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(AuthMiddleware("canopy-dev-secret"))
		r.With(membership).Post("/trees/{tree_id}/reference-selections", h.ValidateReferenceSelection)
		r.With(membership).Post("/trees/{tree_id}/multi-reference-replies", h.CreateMultiReferenceReply)
		r.Get("/nodes/{node_id}/reference-context", h.GetReferenceContext)
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, hub
}

// mrGet performs an authenticated GET and returns status + raw body.
func mrGet(t *testing.T, srv *httptest.Server, path, auth string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", auth)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, raw
}

// mrForeignAuth returns a valid token for a user who is NOT a tree member.
func mrForeignAuth(t *testing.T) string {
	t.Helper()
	return "Bearer " + signedToken(t, "canopy-dev-secret", jwt.MapClaims{
		"sub": uuid.New().String(),
		"exp": time.Now().Add(time.Hour).Unix(),
	})
}

// mrCreateReply runs preflight + create for the given sources and returns the
// decoded 201 envelope plus the created node id.
func mrCreateReply(t *testing.T, srv *httptest.Server, treeID uuid.UUID, sources []uuid.UUID, content string) (map[string]any, uuid.UUID) {
	t.Helper()
	envelope := mrPreflight(t, srv, treeID, sources)
	status, body := mrPost(t, srv, mrCreatePath(treeID), map[string]any{
		"selection_token": mrSelectionToken(t, envelope),
		"content":         content,
	})
	require.Equal(t, http.StatusCreated, status, "create body: %s", string(body))
	created := mrDecode(t, body)
	nodeID := uuid.MustParse(created["node"].(map[string]any)["id"].(string))
	return created, nodeID
}

// mrContextSources reads context.sources as typed maps.
func mrContextSources(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	envelope := mrDecode(t, body)
	ctxObj, ok := envelope["context"].(map[string]any)
	require.True(t, ok, "context missing: %s", string(body))
	raw, ok := ctxObj["sources"].([]any)
	require.True(t, ok, "context.sources missing: %s", string(body))
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		obj, ok := item.(map[string]any)
		require.True(t, ok)
		out = append(out, obj)
	}
	return out
}

func mrContextPath(nodeID uuid.UUID) string {
	return "/api/v1/nodes/" + nodeID.String() + "/reference-context"
}

// mrEvents snapshots a capture client's received events, in order.
func mrEvents(c *transportTestClient) []sse.SSEEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]sse.SSEEvent(nil), c.events...)
}

// --- §9.3: 200 envelope ------------------------------------------------------

func TestReferenceContextRead_HappyPath(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := mrContextServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root message")
	srcA := mrNode(t, pool, treeID, &root, "the first approach stores edges")
	srcB := mrNode(t, pool, treeID, &root, "the second approach stores trees")

	created, nodeID := mrCreateReply(t, srv, treeID, []uuid.UUID{srcA, srcB}, "both, ordered")
	createCtx := created["reference_context"].(map[string]any)

	status, body := mrGet(t, srv, mrContextPath(nodeID), authHeader(t))
	require.Equal(t, http.StatusOK, status, "read body: %s", string(body))
	envelope := mrDecode(t, body)

	// §9.3 top level.
	assert.Equal(t, nodeID.String(), envelope["node_id"])
	assert.Equal(t, treeID.String(), envelope["tree_id"])
	assert.Equal(t, "multi_reference", envelope["parent_mode"])
	assert.Equal(t, srcA.String(), envelope["primary_source_id"])
	assert.NotContains(t, envelope, "source_changed_since_creation", "an untouched snapshot is not flagged")

	ctxObj, ok := envelope["context"].(map[string]any)
	require.True(t, ok, "context missing: %s", string(body))

	// Sources in persisted selection order, labelled R1..RN.
	sources := mrContextSources(t, body)
	require.Len(t, sources, 2)
	assert.Equal(t, "R1", sources[0]["source_label"])
	assert.Equal(t, srcA.String(), sources[0]["node_id"])
	assert.Equal(t, "the first approach stores edges", sources[0]["content"])
	assert.Equal(t, false, sources[0]["truncated"])
	assert.Equal(t, "R2", sources[1]["source_label"])
	assert.Equal(t, srcB.String(), sources[1]["node_id"])
	assert.Equal(t, "the second approach stores trees", sources[1]["content"])

	// Provenance figures come from creation, not from a fresh computation.
	assert.Equal(t, createCtx["manifest_hash"], ctxObj["manifest_hash"])
	assert.Regexp(t, `^[a-f0-9]{64}$`, ctxObj["manifest_hash"])
	assert.IsType(t, true, ctxObj["is_synthetic_merge_point"])
	assert.EqualValues(t, 8192, ctxObj["token_budget"])
	assert.Greater(t, ctxObj["tokens_used"], float64(0))

	branchSpan, ok := ctxObj["branch_span"].(map[string]any)
	require.True(t, ok, "branch_span missing: %s", string(body))
	assert.Equal(t, root.String(), branchSpan["common_ancestor_id"])
	require.NotNil(t, branchSpan["source_branches"])
}

func TestReferenceContextRead_FollowsCanonicalSelectionOrder(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := mrContextServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")
	srcA := mrNode(t, pool, treeID, &root, "a")
	srcB := mrNode(t, pool, treeID, &root, "b")
	srcC := mrNode(t, pool, treeID, &root, "c")

	// Selection order is user intent: B first, then C, then A.
	envelope := mrPreflight(t, srv, treeID, []uuid.UUID{srcB, srcC, srcA})
	status, body := mrPost(t, srv, mrCreatePath(treeID), map[string]any{
		"selection_token": mrSelectionToken(t, envelope),
		"content":         "ordered reply",
	})
	require.Equal(t, http.StatusCreated, status, "create body: %s", string(body))
	nodeID := uuid.MustParse(mrDecode(t, body)["node"].(map[string]any)["id"].(string))

	status, body = mrGet(t, srv, mrContextPath(nodeID), authHeader(t))
	require.Equal(t, http.StatusOK, status, "read body: %s", string(body))

	sources := mrContextSources(t, body)
	require.Len(t, sources, 3)
	for i, want := range []uuid.UUID{srcB, srcC, srcA} {
		assert.Equalf(t, want.String(), sources[i]["node_id"], "source %d follows selection order", i)
		assert.Equalf(t, db.ReferenceSourceLabel(i), sources[i]["source_label"], "source %d label", i)
	}
}

// --- §9.3/§9.4: 404 without an existence oracle ------------------------------

func TestReferenceContextRead_NotFoundForNonMultiReferenceNode(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := mrContextServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")
	ordinary := mrNode(t, pool, treeID, &root, "an ordinary lineage message")

	status, body := mrGet(t, srv, mrContextPath(ordinary), authHeader(t))
	assert.Equal(t, http.StatusNotFound, status, "body: %s", string(body))
	assert.Equal(t, "REFERENCE_CONTEXT_NOT_FOUND", mrErrorCode(t, body))

	// A node that does not exist answers with the SAME code and message, so
	// the route cannot be used to test existence.
	status2, body2 := mrGet(t, srv, mrContextPath(uuid.New()), authHeader(t))
	assert.Equal(t, http.StatusNotFound, status2, "body: %s", string(body2))
	assert.Equal(t, "REFERENCE_CONTEXT_NOT_FOUND", mrErrorCode(t, body2))
	assert.Equal(t, mrDecode(t, body)["error"], mrDecode(t, body2)["error"],
		"the two 404s are indistinguishable")
}

func TestReferenceContextRead_NotFoundForDeletedNode(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := mrContextServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")
	srcA := mrNode(t, pool, treeID, &root, "a")
	srcB := mrNode(t, pool, treeID, &root, "b")
	_, nodeID := mrCreateReply(t, srv, treeID, []uuid.UUID{srcA, srcB}, "reply")

	_, err := pool.Exec(context.Background(),
		`UPDATE nodes SET deleted_at = clock_timestamp() WHERE id = $1`, nodeID)
	require.NoError(t, err)

	status, body := mrGet(t, srv, mrContextPath(nodeID), authHeader(t))
	assert.Equal(t, http.StatusNotFound, status, "body: %s", string(body))
	assert.Equal(t, "REFERENCE_CONTEXT_NOT_FOUND", mrErrorCode(t, body))
}

// --- §9.3 query parameters ---------------------------------------------------

func TestReferenceContextRead_IncludeContentFalseOmitsContent(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := mrContextServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")
	srcA := mrNode(t, pool, treeID, &root, "secret A")
	srcB := mrNode(t, pool, treeID, &root, "secret B")
	_, nodeID := mrCreateReply(t, srv, treeID, []uuid.UUID{srcA, srcB}, "reply")

	status, body := mrGet(t, srv, mrContextPath(nodeID)+"?include_content=false", authHeader(t))
	require.Equal(t, http.StatusOK, status, "body: %s", string(body))

	sources := mrContextSources(t, body)
	require.Len(t, sources, 2)
	for _, src := range sources {
		_, present := src["content"]
		assert.False(t, present, "content must be OMITTED, not empty: %v", src)
		assert.Contains(t, src, "truncated")
		assert.Contains(t, src, "source_label")
	}
	assert.NotContains(t, string(body), "secret A")

	// Default (include_content=true) still carries the content.
	status, body = mrGet(t, srv, mrContextPath(nodeID), authHeader(t))
	require.Equal(t, http.StatusOK, status)
	assert.Contains(t, string(body), "secret A")
}

func TestReferenceContextRead_MaxSourceTokensTruncates(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := mrContextServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")
	// ≈ 3,000 tokens: over the default per-source ceiling.
	long := strings.Repeat("long-form source text ", 600)
	srcA := mrNode(t, pool, treeID, &root, long)
	srcB := mrNode(t, pool, treeID, &root, "short source")
	_, nodeID := mrCreateReply(t, srv, treeID, []uuid.UUID{srcA, srcB}, "reply")

	// Default allowance = the per-source allocation recorded at creation
	// (§6.2 ceiling of 2,048 tokens).
	status, body := mrGet(t, srv, mrContextPath(nodeID), authHeader(t))
	require.Equal(t, http.StatusOK, status, "body: %s", string(body))
	sources := mrContextSources(t, body)
	assert.Equal(t, true, sources[0]["truncated"])
	assert.Less(t, len(sources[0]["content"].(string)), len(long))
	assert.Contains(t, sources[0]["content"], "tokens omitted from source R1")
	assert.Equal(t, false, sources[1]["truncated"], "a short source is untouched")

	// An explicit allowance truncates harder.
	status, body = mrGet(t, srv, mrContextPath(nodeID)+"?max_source_tokens=256", authHeader(t))
	require.Equal(t, http.StatusOK, status, "body: %s", string(body))
	small := mrContextSources(t, body)[0]["content"].(string)
	assert.Contains(t, small, "tokens omitted from source R1")
	assert.Less(t, len(small), len(sources[0]["content"].(string)))

	// Above the documented maximum the allowance is CLAMPED to 2,048 and the
	// request still succeeds.
	status, body = mrGet(t, srv, mrContextPath(nodeID)+"?max_source_tokens=100000", authHeader(t))
	require.Equal(t, http.StatusOK, status, "body: %s", string(body))
	clamped := mrContextSources(t, body)[0]["content"].(string)
	assert.Equal(t, true, mrContextSources(t, body)[0]["truncated"])
	assert.Equal(t, sources[0]["content"], clamped, "99999 clamps to the same 2,048-token allowance")

	// A non-positive or malformed value is rejected.
	status, body = mrGet(t, srv, mrContextPath(nodeID)+"?max_source_tokens=0", authHeader(t))
	assert.Equal(t, http.StatusBadRequest, status, "body: %s", string(body))
	assert.Equal(t, "INVALID_MAX_SOURCE_TOKENS", mrErrorCode(t, body))

	status, body = mrGet(t, srv, mrContextPath(nodeID)+"?max_source_tokens=abc", authHeader(t))
	assert.Equal(t, http.StatusBadRequest, status, "body: %s", string(body))
	assert.Equal(t, "INVALID_MAX_SOURCE_TOKENS", mrErrorCode(t, body))
}

func TestReferenceContextRead_VerifyHashDetectsMutatedSource(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := mrContextServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")
	srcA := mrNode(t, pool, treeID, &root, "original A")
	srcB := mrNode(t, pool, treeID, &root, "original B")
	created, nodeID := mrCreateReply(t, srv, treeID, []uuid.UUID{srcA, srcB}, "reply")
	storedHash := created["reference_context"].(map[string]any)["manifest_hash"]

	// A source edited after creation must be visible to a verifying read.
	_, err := pool.Exec(context.Background(),
		`UPDATE nodes SET content = 'silently rewritten B' WHERE id = $1`, srcB)
	require.NoError(t, err)

	status, body := mrGet(t, srv, mrContextPath(nodeID), authHeader(t))
	require.Equal(t, http.StatusOK, status, "body: %s", string(body))
	envelope := mrDecode(t, body)
	assert.Equal(t, true, envelope["source_changed_since_creation"])
	// The reported hash remains the creation provenance, never a fresh one.
	assert.Equal(t, storedHash, envelope["context"].(map[string]any)["manifest_hash"])
	assert.Contains(t, string(body), "silently rewritten B", "the current content is what the route returns")

	// verify_hash=false drops the assertion entirely.
	status, body = mrGet(t, srv, mrContextPath(nodeID)+"?verify_hash=false", authHeader(t))
	require.Equal(t, http.StatusOK, status, "body: %s", string(body))
	assert.NotContains(t, mrDecode(t, body), "source_changed_since_creation")
}

// --- §9.3: authorization -----------------------------------------------------

func TestReferenceContextRead_NonMemberForbidden(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := mrContextServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")
	srcA := mrNode(t, pool, treeID, &root, "a")
	srcB := mrNode(t, pool, treeID, &root, "b")
	_, nodeID := mrCreateReply(t, srv, treeID, []uuid.UUID{srcA, srcB}, "reply")

	status, body := mrGet(t, srv, mrContextPath(nodeID), mrForeignAuth(t))
	assert.Equal(t, http.StatusForbidden, status, "body: %s", string(body))
	assert.Equal(t, "NOT_TREE_MEMBER", mrErrorCode(t, body))

	// The member still reads it (control).
	status, body = mrGet(t, srv, mrContextPath(nodeID), authHeader(t))
	assert.Equal(t, http.StatusOK, status, "body: %s", string(body))
}

func TestReferenceContextRead_DeletedTreeGone(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := mrContextServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")
	srcA := mrNode(t, pool, treeID, &root, "a")
	srcB := mrNode(t, pool, treeID, &root, "b")
	_, nodeID := mrCreateReply(t, srv, treeID, []uuid.UUID{srcA, srcB}, "reply")

	// Same gate the tree-scoped surfaces apply (BUG-043): members of a
	// soft-deleted tree get 410.
	_, err := pool.Exec(context.Background(),
		`UPDATE trees SET deleted_at = clock_timestamp() WHERE id = $1`, treeID)
	require.NoError(t, err)

	status, body := mrGet(t, srv, mrContextPath(nodeID), authHeader(t))
	assert.Equal(t, http.StatusGone, status, "body: %s", string(body))
	assert.Equal(t, "TREE_DELETED", mrErrorCode(t, body))
}

func TestReferenceContextRead_InvalidNodeID(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv, _ := mrContextServer(t, pool)

	status, body := mrGet(t, srv, "/api/v1/nodes/not-a-uuid/reference-context", authHeader(t))
	assert.Equal(t, http.StatusBadRequest, status, "body: %s", string(body))
	assert.Equal(t, "INVALID_NODE_ID", mrErrorCode(t, body))
}

// --- §10: SSE convergence vocabulary ----------------------------------------

func TestMultiReferenceConvergence_SSEEventOrder(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv, hub := mrContextServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")
	srcA := mrNode(t, pool, treeID, &root, "source A")
	srcB := mrNode(t, pool, treeID, &root, "source B")
	srcC := mrNode(t, pool, treeID, &root, "source C")

	// A REAL client subscribed to the tree stream before the write.
	client := newTransportTestClient("pl06-p2-capture", testUserID, treeID)
	require.NoError(t, hub.Subscribe(context.Background(), treeID, client))

	envelope := mrPreflight(t, srv, treeID, []uuid.UUID{srcC, srcA, srcB})
	status, body := mrPost(t, srv, mrCreatePath(treeID), map[string]any{
		"selection_token": mrSelectionToken(t, envelope),
		"content":         "converged reply",
	})
	require.Equal(t, http.StatusCreated, status, "create body: %s", string(body))
	created := mrDecode(t, body)
	nodeID := uuid.MustParse(created["node"].(map[string]any)["id"].(string))
	createEdges := created["edges"].([]any)

	events := mrEvents(client)
	require.Len(t, events, 1+3+1, "expected node_added + 3 edge_added + the composite")

	// (1) node_added first — existing behaviour, kept.
	assert.Equal(t, "node_added", events[0].Type)

	// (2) exactly N edge_added, ordered by metadata.selection_order.
	var selectionOrders []int
	for i := 1; i <= 3; i++ {
		require.Equal(t, "edge_added", events[i].Type, "event %d", i)
		var payload map[string]any
		require.NoError(t, json.Unmarshal(events[i].Data, &payload))
		meta, ok := payload["metadata"].(map[string]any)
		require.True(t, ok, "edge event carries the §5.2 metadata: %s", string(events[i].Data))
		order, ok := meta["selection_order"].(float64)
		require.True(t, ok)
		selectionOrders = append(selectionOrders, int(order))
		assert.Equal(t, "context_source", meta["role"])
		assert.Equal(t, db.ReferenceSourceLabel(int(order)), meta["source_label"])
		// The same edge the 201 returned, in the same position.
		assert.Equal(t, createEdges[i-1].(map[string]any)["id"], payload["id"])
		assert.Equal(t, "reference", payload["edge_type"])
		assert.Equal(t, nodeID.String(), payload["target_node_id"])
	}
	assert.Equal(t, []int{0, 1, 2}, selectionOrders, "edges are ordered by metadata.selection_order")

	// (3) exactly one composite, last.
	last := events[len(events)-1]
	assert.Equal(t, "multi_reference_converged", last.Type)

	var composite map[string]any
	require.NoError(t, json.Unmarshal(last.Data, &composite))

	// §10.2: exactly this field set.
	want := map[string]bool{
		"tree_id": true, "node_id": true, "parent_mode": true, "primary_source_id": true,
		"source_node_ids": true, "edge_ids": true, "is_synthetic_merge_point": true,
		"common_ancestor_id": true, "context_manifest_hash": true, "created_at": true,
	}
	for key := range composite {
		assert.Truef(t, want[key], "unexpected composite field %q", key)
	}
	for key := range want {
		_, present := composite[key]
		assert.Truef(t, present, "composite missing %q: %s", key, string(last.Data))
	}

	assert.Equal(t, treeID.String(), composite["tree_id"])
	assert.Equal(t, nodeID.String(), composite["node_id"])
	assert.Equal(t, "multi_reference", composite["parent_mode"])
	assert.Equal(t, srcC.String(), composite["primary_source_id"], "primary is the reordered first source")

	sourceIDs := composite["source_node_ids"].([]any)
	edgeIDs := composite["edge_ids"].([]any)
	require.Len(t, sourceIDs, 3)
	require.Len(t, edgeIDs, 3)
	assert.Equal(t, []any{srcC.String(), srcA.String(), srcB.String()}, sourceIDs)
	for i, edge := range createEdges {
		assert.Equal(t, edge.(map[string]any)["id"], edgeIDs[i], "edge_ids share the sources' order")
	}

	assert.IsType(t, true, composite["is_synthetic_merge_point"])
	assert.Equal(t, root.String(), composite["common_ancestor_id"])

	manifestHash, ok := composite["context_manifest_hash"].(string)
	require.True(t, ok)
	assert.Regexp(t, `^[a-f0-9]{64}$`, manifestHash)
	assert.Equal(t, created["reference_context"].(map[string]any)["manifest_hash"], manifestHash)

	createdAt, ok := composite["created_at"].(string)
	require.True(t, ok)
	parsed, err := time.Parse(time.RFC3339, createdAt)
	require.NoError(t, err, "created_at must be RFC3339: %q", createdAt)
	assert.False(t, parsed.IsZero())
}

func TestMultiReferenceConvergence_NoCompositeForOrdinaryReply(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	_, hub := mrContextServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")

	client := newTransportTestClient("pl06-p2-control", testUserID, treeID)
	require.NoError(t, hub.Subscribe(context.Background(), treeID, client))

	// An ordinary reply goes through the standard node create endpoint on
	// this harness's service; it must not emit the composite.
	nodeSvc := service.NewNodeService(db.NewPGNodeRepo(pool), db.NewPGEdgeRepo(pool), pool, hub)
	_, err := nodeSvc.Create(context.Background(), treeID, service.CreateNodeInput{
		ParentID: root, Content: "ordinary reply", ContentFormat: "markdown",
		NodeType: "message", EdgeType: "reply", AuthorID: testUserID, TreeID: treeID,
	})
	require.NoError(t, err)

	for _, ev := range mrEvents(client) {
		assert.NotEqual(t, "multi_reference_converged", ev.Type,
			"the composite is emitted only by a multi-reference creation (§10.1)")
		assert.NotEqual(t, "edge_added", ev.Type,
			"edge_added belongs to the reference-set creation path")
	}
}

// --- §9.4: the read route's code is in the catalog ---------------------------

func TestReferenceContextNotFoundCatalogEntry(t *testing.T) {
	apiErr, ok := service.ReferenceErrorFrom(service.NewReferenceAPIError(service.ErrReferenceContextNotFound, ""))
	require.True(t, ok)
	assert.Equal(t, "REFERENCE_CONTEXT_NOT_FOUND", apiErr.Code)
	assert.Equal(t, http.StatusNotFound, apiErr.Status)
}
