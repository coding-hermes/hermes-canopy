// Endpoint tests for the multi-message reference write path (SPEC-PL-06 §9.1,
// §9.2, §9.4) against a real PostgreSQL.
//
// Exercises the §15.1 backend scenarios that belong to the write path:
// happy path at 2 and 20 sources, the 1/21-source rejections, cross-tree,
// missing, deleted, duplicate, system, primary-not-selected, invalid-id and
// budget rejections, post-preflight staleness, token tampering, request-id
// idempotency, and the parent_mode/parent_id invariants after creation.
package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

const multiReferenceTestSecret = "canopy-multi-reference-test-secret"

// --- Harness -----------------------------------------------------------------

// multiReferenceTestServer builds the real handler (auth + tree-membership
// middleware, exactly as the production router wires it) over a real pool.
func multiReferenceTestServer(t *testing.T, pool *pgxpool.Pool) *httptest.Server {
	t.Helper()
	require.NoError(t, insertSentinelUser(t, pool))

	nodeRepo := db.NewPGNodeRepo(pool)
	edgeRepo := db.NewPGEdgeRepo(pool)
	treeSvc := service.NewTreeService(db.NewPGTreeRepo(pool), nodeRepo, edgeRepo, pool).
		WithReferenceSelection(service.NewReferenceSelectionSigner(multiReferenceTestSecret, nil), 8000)
	nodeSvc := service.NewNodeService(nodeRepo, edgeRepo, pool, sse.NewHub()).
		WithReferenceSelection(treeSvc)

	h := NewMultiReferenceHandler(treeSvc, nodeSvc)
	membership := TreeMembershipMiddleware(db.NewPGTreeMemberRepo(pool))

	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(AuthMiddleware("canopy-dev-secret"))
		r.With(membership).Post("/trees/{tree_id}/reference-selections", h.ValidateReferenceSelection)
		r.With(membership).Post("/trees/{tree_id}/multi-reference-replies", h.CreateMultiReferenceReply)
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

func insertSentinelUser(t *testing.T, pool *pgxpool.Pool) error {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO users (id, hermes_user_id, display_name)
         VALUES ($1, 'multi-reference-test', 'Reference Tester')
         ON CONFLICT (id) DO NOTHING`, testUserID)
	return err
}

// mrTree creates a tree with the sentinel user as a member and returns its id.
func mrTree(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	treeID := uuid.New()
	ctx := context.Background()
	_, err := pool.Exec(ctx,
		`INSERT INTO trees (id, owner_id, title) VALUES ($1, $2, 'Multi-Reference Tree')`,
		treeID, uuid.New())
	require.NoError(t, err, "create tree")
	_, err = pool.Exec(ctx,
		`INSERT INTO tree_members (tree_id, user_id, role) VALUES ($1, $2, 'owner')`,
		treeID, testUserID)
	require.NoError(t, err, "create tree membership")
	return treeID
}

// mrNode inserts a message node (optionally with a display parent) and returns
// its id.
func mrNode(t *testing.T, pool *pgxpool.Pool, treeID uuid.UUID, parentID *uuid.UUID, content string) uuid.UUID {
	t.Helper()
	return mrTypedNode(t, pool, treeID, parentID, db.NodeTypeMessage, content)
}

func mrTypedNode(t *testing.T, pool *pgxpool.Pool, treeID uuid.UUID, parentID *uuid.UUID, nodeType, content string) uuid.UUID {
	t.Helper()
	nodeID := uuid.New()
	_, err := pool.Exec(context.Background(), `
        INSERT INTO nodes (id, tree_id, parent_id, author_id, content, node_type)
        VALUES ($1, $2, $3, $4, $5, $6)`,
		nodeID, treeID, parentID, uuid.New(), content, nodeType)
	require.NoError(t, err, "insert node")
	return nodeID
}

// mrPost sends an authenticated JSON request and returns status + raw body.
func mrPost(t *testing.T, srv *httptest.Server, path string, body any) (int, []byte) {
	t.Helper()
	req := authenticatedRequest(t, srv.URL, http.MethodPost, path, body)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, raw
}

func mrDecode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(body, &out), "body: %s", string(body))
	return out
}

// mrErrorCode extracts the §9.4 code from an error envelope.
func mrErrorCode(t *testing.T, body []byte) string {
	t.Helper()
	env := mrDecode(t, body)
	errObj, ok := env["error"].(map[string]any)
	require.True(t, ok, "error envelope missing: %s", string(body))
	code, _ := errObj["code"].(string)
	require.NotEmpty(t, code)
	return code
}

func mrPreflightPath(treeID uuid.UUID) string {
	return "/api/v1/trees/" + treeID.String() + "/reference-selections"
}

func mrCreatePath(treeID uuid.UUID) string {
	return "/api/v1/trees/" + treeID.String() + "/multi-reference-replies"
}

// mrPreflight runs a successful preflight and returns the decoded envelope.
func mrPreflight(t *testing.T, srv *httptest.Server, treeID uuid.UUID, ids []uuid.UUID) map[string]any {
	t.Helper()
	raw := make([]string, 0, len(ids))
	for _, id := range ids {
		raw = append(raw, id.String())
	}
	status, body := mrPost(t, srv, mrPreflightPath(treeID),
		map[string]any{"source_node_ids": raw, "profile_context_budget": 16384})
	require.Equal(t, http.StatusOK, status, "preflight body: %s", string(body))
	return mrDecode(t, body)
}

// mrSelectionToken returns the signed token from a preflight envelope.
func mrSelectionToken(t *testing.T, envelope map[string]any) string {
	t.Helper()
	token, ok := envelope["selection_token"].(string)
	require.True(t, ok, "selection_token missing")
	require.True(t, strings.HasPrefix(token, "mrs.v1."), "token prefix: %s", token)
	return token
}

// mrSourceIDs reads canonical_source_ids back as UUIDs.
func mrSourceIDs(t *testing.T, envelope map[string]any) []uuid.UUID {
	t.Helper()
	raw, ok := envelope["canonical_source_ids"].([]any)
	require.True(t, ok, "canonical_source_ids missing")
	out := make([]uuid.UUID, 0, len(raw))
	for _, v := range raw {
		s, _ := v.(string)
		id, err := uuid.Parse(s)
		require.NoError(t, err, "canonical source id %q", s)
		out = append(out, id)
	}
	return out
}

// mrActiveReferenceEdges returns the active reference edges of a target in
// canonical order.
func mrActiveReferenceEdges(t *testing.T, pool *pgxpool.Pool, target uuid.UUID) []struct {
	SourceID uuid.UUID
	Metadata []byte
} {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
        SELECT source_id, metadata
        FROM edges
        WHERE target_id = $1 AND edge_type = 'reference' AND deleted_at IS NULL
        ORDER BY COALESCE((metadata->>'selection_order')::int, 2147483647), sequence_num`,
		target)
	require.NoError(t, err)
	defer rows.Close()
	var out []struct {
		SourceID uuid.UUID
		Metadata []byte
	}
	for rows.Next() {
		var e struct {
			SourceID uuid.UUID
			Metadata []byte
		}
		require.NoError(t, rows.Scan(&e.SourceID, &e.Metadata))
		out = append(out, e)
	}
	require.NoError(t, rows.Err())
	return out
}

// mrNodeCount counts nodes in a tree (to prove a rejection wrote nothing).
func mrNodeCount(t *testing.T, pool *pgxpool.Pool, treeID uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT COUNT(*)::int FROM nodes WHERE tree_id = $1 AND deleted_at IS NULL`, treeID).Scan(&n))
	return n
}

// mrMultiReferenceCount counts created multi-reference targets in a tree.
// Soft-deleting a source changes the active node count, so rejections that
// happen after a mutation assert on this counter instead.
func mrMultiReferenceCount(t *testing.T, pool *pgxpool.Pool, treeID uuid.UUID) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT COUNT(*)::int FROM nodes WHERE tree_id = $1 AND parent_mode = 'multi_reference'`, treeID).Scan(&n))
	return n
}

// --- Scenario: happy path with two sources -----------------------------------

func TestMultiReferenceWritePath_HappyPathTwoSources(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv := multiReferenceTestServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root message")
	srcA := mrNode(t, pool, treeID, &root, "the first approach stores edges")
	srcB := mrNode(t, pool, treeID, &root, "the second approach stores trees")

	envelope := mrPreflight(t, srv, treeID, []uuid.UUID{srcA, srcB})

	// §9.1 success envelope.
	assert.Equal(t, treeID.String(), envelope["tree_id"])
	assert.Equal(t, []any{srcA.String(), srcB.String()}, envelope["canonical_source_ids"])
	assert.Equal(t, srcA.String(), envelope["primary_source_id"])
	assert.IsType(t, true, envelope["is_synthetic_merge_point"])
	assert.NotEmpty(t, envelope["expires_at"])
	token := mrSelectionToken(t, envelope)

	budget, ok := envelope["context_budget"].(map[string]any)
	require.True(t, ok, "context_budget missing")
	assert.EqualValues(t, 8192, budget["available_tokens"]) // min(16384*0.5, 16384)
	assert.EqualValues(t, 512, budget["minimum_required_tokens"])
	assert.NotNil(t, budget["estimated_tokens"])
	assert.IsType(t, true, budget["fits"])

	sources, ok := envelope["sources"].([]any)
	require.True(t, ok, "sources missing")
	require.Len(t, sources, 2)
	first, ok := sources[0].(map[string]any)
	require.True(t, ok)
	for _, key := range []string{"node_id", "source_label", "color_key", "branch_root_id", "content_hash", "sequence_num", "content_preview"} {
		_, present := first[key]
		assert.Truef(t, present, "sources[0] missing %q", key)
	}
	assert.Equal(t, "R1", first["source_label"])
	assert.Regexp(t, `^ref-[0-7]$`, first["color_key"])
	assert.Equal(t, "R2", sources[1].(map[string]any)["source_label"])

	// §9.2 creation.
	status, body := mrPost(t, srv, mrCreatePath(treeID), map[string]any{
		"selection_token": token,
		"content":         "The two approaches disagree on storage semantics.",
		"content_format":  "markdown",
	})
	require.Equal(t, http.StatusCreated, status, "create body: %s", string(body))
	created := mrDecode(t, body)

	node, ok := created["node"].(map[string]any)
	require.True(t, ok, "node missing: %s", string(body))
	nodeID := uuid.MustParse(node["id"].(string))
	assert.Equal(t, treeID.String(), node["tree_id"])
	assert.Equal(t, srcA.String(), node["parent_id"], "display anchor is the first canonical source")
	assert.Equal(t, "multi_reference", node["parent_mode"])
	assert.Equal(t, "message", node["node_type"], "a multi-reference reply stays a message node")
	assert.Equal(t, "The two approaches disagree on storage semantics.", node["content"])

	metadata, ok := node["metadata"].(map[string]any)
	require.True(t, ok, "node metadata missing")
	reserved, ok := metadata["multi_reference"].(map[string]any)
	require.True(t, ok, "reserved multi_reference metadata missing")
	assert.EqualValues(t, 1, reserved["version"])
	assert.Equal(t, srcA.String(), reserved["primarySourceId"])
	assert.Equal(t, []any{srcA.String(), srcB.String()}, reserved["canonicalSourceIds"])
	manifestHash, _ := reserved["contextManifestHash"].(string)
	assert.Regexp(t, `^[a-f0-9]{64}$`, manifestHash)
	assert.EqualValues(t, 8192, reserved["contextTokenBudget"])

	edges, ok := created["edges"].([]any)
	require.True(t, ok, "edges missing")
	require.Len(t, edges, 2)
	for i, want := range []uuid.UUID{srcA, srcB} {
		edge := edges[i].(map[string]any)
		assert.Equal(t, want.String(), edge["source_node_id"])
		assert.Equal(t, nodeID.String(), edge["target_node_id"])
		assert.Equal(t, "reference", edge["edge_type"])
		edgeMeta, ok := edge["metadata"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, db.ReferenceSourceLabel(i), edgeMeta["source_label"])
		assert.Equal(t, "context_source", edgeMeta["role"])
		assert.Regexp(t, `^ref-[0-7]$`, edgeMeta["color_key"])
	}

	refCtx, ok := created["reference_context"].(map[string]any)
	require.True(t, ok, "reference_context missing")
	assert.Equal(t, manifestHash, refCtx["manifest_hash"])
	assert.EqualValues(t, 2, refCtx["source_count"])
	assert.IsType(t, true, refCtx["is_synthetic_merge_point"])

	// Database truth: exactly two active reference edges, canonical order.
	stored := mrActiveReferenceEdges(t, pool, nodeID)
	require.Len(t, stored, 2)
	assert.Equal(t, srcA, stored[0].SourceID)
	assert.Equal(t, srcB, stored[1].SourceID)
}

// --- Scenario 2: the twenty-source boundary ----------------------------------

func TestMultiReferenceWritePath_TwentySources(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv := multiReferenceTestServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")

	sources := make([]uuid.UUID, 0, 20)
	for i := 0; i < 20; i++ {
		sources = append(sources, mrNode(t, pool, treeID, &root, "source"))
	}

	envelope := mrPreflight(t, srv, treeID, sources)
	assert.EqualValues(t, 5120, envelope["context_budget"].(map[string]any)["minimum_required_tokens"],
		"20 sources must reserve 20*256 tokens (§15 scenario 2)")
	assert.Len(t, mrSourceIDs(t, envelope), 20)

	status, body := mrPost(t, srv, mrCreatePath(treeID), map[string]any{
		"selection_token": mrSelectionToken(t, envelope),
		"content":         "considering twenty messages",
	})
	require.Equal(t, http.StatusCreated, status, "create body: %s", string(body))
	created := mrDecode(t, body)
	node := created["node"].(map[string]any)
	edges := created["edges"].([]any)
	require.Len(t, edges, 20, "one reference edge per source")
	assert.Equal(t, sources[0].String(), node["parent_id"])

	// Metadata sequence labels are exactly R1..R20 in canonical order.
	for i, edge := range edges {
		meta := edge.(map[string]any)["metadata"].(map[string]any)
		assert.Equal(t, db.ReferenceSourceLabel(i), meta["source_label"])
		assert.EqualValues(t, i, meta["reference_index"])
	}
	stored := mrActiveReferenceEdges(t, pool, uuid.MustParse(node["id"].(string)))
	require.Len(t, stored, 20)
	for i, want := range sources {
		assert.Equalf(t, want, stored[i].SourceID, "edge %d must follow canonical selection order", i)
	}
}

// --- Scenario 1 / 3: source-count rejections ---------------------------------

func TestMultiReferenceWritePath_OneSourceRejected(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv := multiReferenceTestServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")
	src := mrNode(t, pool, treeID, &root, "lonely source")

	status, body := mrPost(t, srv, mrPreflightPath(treeID),
		map[string]any{"source_node_ids": []string{src.String()}})
	assert.Equal(t, http.StatusBadRequest, status, "body: %s", string(body))
	assert.Equal(t, "REFERENCE_SOURCE_COUNT_TOO_LOW", mrErrorCode(t, body))
}

func TestMultiReferenceWritePath_TwentyOneSourcesRejected(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv := multiReferenceTestServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")

	raw := make([]string, 0, 21)
	for i := 0; i < 21; i++ {
		raw = append(raw, mrNode(t, pool, treeID, &root, "source").String())
	}
	before := mrNodeCount(t, pool, treeID)

	status, body := mrPost(t, srv, mrPreflightPath(treeID),
		map[string]any{"source_node_ids": raw})
	assert.Equal(t, http.StatusBadRequest, status, "body: %s", string(body))
	assert.Equal(t, "REFERENCE_SOURCE_COUNT_TOO_HIGH", mrErrorCode(t, body))
	assert.Equal(t, before, mrNodeCount(t, pool, treeID), "a rejected preflight writes no rows")

	// The creation endpoint must reject the same selection shape too.
	status, body = mrPost(t, srv, mrCreatePath(treeID), map[string]any{
		"selection_token": "mrs.v1.not-a-real-token.signature",
		"content":         "should never be written",
	})
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "REFERENCE_SELECTION_TOKEN_INVALID", mrErrorCode(t, body))
	assert.Equal(t, before, mrNodeCount(t, pool, treeID))
}

// --- Scenario 4: duplicate source ids ---------------------------------------

func TestMultiReferenceWritePath_DuplicateSourceRejected(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv := multiReferenceTestServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")
	src := mrNode(t, pool, treeID, &root, "source")

	status, body := mrPost(t, srv, mrPreflightPath(treeID),
		map[string]any{"source_node_ids": []string{src.String(), src.String()}})
	assert.Equal(t, http.StatusBadRequest, status, "body: %s", string(body))
	assert.Equal(t, "REFERENCE_SOURCE_DUPLICATE", mrErrorCode(t, body),
		"duplicates are rejected, never silently deduplicated (§15 scenario 4)")
}

// --- Scenario 6: cross-tree source ------------------------------------------

func TestMultiReferenceWritePath_CrossTreeSourceRejected(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv := multiReferenceTestServer(t, pool)
	treeID := mrTree(t, pool)
	otherTree := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")
	srcA := mrNode(t, pool, treeID, &root, "in tree")
	foreign := mrNode(t, pool, otherTree, nil, "another tree")

	status, body := mrPost(t, srv, mrPreflightPath(treeID),
		map[string]any{"source_node_ids": []string{srcA.String(), foreign.String()}})
	assert.Equal(t, http.StatusBadRequest, status, "body: %s", string(body))
	assert.Equal(t, "REFERENCE_TREE_MISMATCH", mrErrorCode(t, body))
}

// --- Scenario 7: soft-deleted source ----------------------------------------

func TestMultiReferenceWritePath_DeletedSourceRejected(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv := multiReferenceTestServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")
	srcA := mrNode(t, pool, treeID, &root, "alive")
	doomed := mrNode(t, pool, treeID, &root, "doomed")
	_, err := pool.Exec(context.Background(),
		`UPDATE nodes SET deleted_at = clock_timestamp() WHERE id = $1`, doomed)
	require.NoError(t, err)

	status, body := mrPost(t, srv, mrPreflightPath(treeID),
		map[string]any{"source_node_ids": []string{srcA.String(), doomed.String()}})
	assert.Equal(t, http.StatusGone, status, "body: %s", string(body))
	assert.Equal(t, "REFERENCE_SOURCE_DELETED", mrErrorCode(t, body))
}

// --- Scenario: missing source ------------------------------------------------

func TestMultiReferenceWritePath_MissingSourceRejected(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv := multiReferenceTestServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")
	srcA := mrNode(t, pool, treeID, &root, "present")

	status, body := mrPost(t, srv, mrPreflightPath(treeID),
		map[string]any{"source_node_ids": []string{srcA.String(), uuid.New().String()}})
	assert.Equal(t, http.StatusNotFound, status, "body: %s", string(body))
	assert.Equal(t, "REFERENCE_SOURCE_NOT_FOUND", mrErrorCode(t, body))
}

// --- Scenario 11: system sources --------------------------------------------

func TestMultiReferenceWritePath_SystemSourceRejected(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv := multiReferenceTestServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")
	srcA := mrNode(t, pool, treeID, &root, "user message")
	system := mrTypedNode(t, pool, treeID, &root, db.NodeTypeSystem, "server event")

	status, body := mrPost(t, srv, mrPreflightPath(treeID),
		map[string]any{"source_node_ids": []string{srcA.String(), system.String()}})
	assert.Equal(t, http.StatusBadRequest, status, "body: %s", string(body))
	assert.Equal(t, "REFERENCE_SOURCE_SYSTEM_FORBIDDEN", mrErrorCode(t, body))
}

// --- Scenario 5 (preflight half): primary source normalization ---------------

func TestMultiReferenceWritePath_PrimaryNotSelectedRejected(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv := multiReferenceTestServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")
	srcA := mrNode(t, pool, treeID, &root, "a")
	srcB := mrNode(t, pool, treeID, &root, "b")

	status, body := mrPost(t, srv, mrPreflightPath(treeID), map[string]any{
		"source_node_ids":   []string{srcA.String(), srcB.String()},
		"primary_source_id": uuid.New().String(),
	})
	assert.Equal(t, http.StatusBadRequest, status, "body: %s", string(body))
	assert.Equal(t, "REFERENCE_PRIMARY_NOT_SELECTED", mrErrorCode(t, body))
}

func TestMultiReferenceWritePath_PrimaryReorderChangesCanonicalOrder(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv := multiReferenceTestServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")
	srcA := mrNode(t, pool, treeID, &root, "a")
	srcB := mrNode(t, pool, treeID, &root, "b")

	status, body := mrPost(t, srv, mrPreflightPath(treeID), map[string]any{
		"source_node_ids":        []string{srcA.String(), srcB.String()},
		"primary_source_id":      srcB.String(),
		"profile_context_budget": 16384,
	})
	require.Equal(t, http.StatusOK, status, "body: %s", string(body))
	envelope := mrDecode(t, body)

	// §9.1/§11: supplying primary moves it to index 0 in canonical order.
	assert.Equal(t, []any{srcB.String(), srcA.String()}, envelope["canonical_source_ids"])
	assert.Equal(t, srcB.String(), envelope["primary_source_id"])
	sources := envelope["sources"].([]any)
	assert.Equal(t, srcB.String(), sources[0].(map[string]any)["node_id"])
	assert.Equal(t, "R1", sources[0].(map[string]any)["source_label"])

	// The created node anchors under the reordered primary (scenario 5).
	status, body = mrPost(t, srv, mrCreatePath(treeID), map[string]any{
		"selection_token": mrSelectionToken(t, envelope),
		"content":         "anchored under the reordered primary",
	})
	require.Equal(t, http.StatusCreated, status, "body: %s", string(body))
	node := mrDecode(t, body)["node"].(map[string]any)
	assert.Equal(t, srcB.String(), node["parent_id"])
}

// --- Invalid source id ------------------------------------------------------

func TestMultiReferenceWritePath_InvalidSourceIDRejected(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv := multiReferenceTestServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")
	srcA := mrNode(t, pool, treeID, &root, "a")

	status, body := mrPost(t, srv, mrPreflightPath(treeID),
		map[string]any{"source_node_ids": []string{srcA.String(), "not-a-uuid"}})
	assert.Equal(t, http.StatusBadRequest, status, "body: %s", string(body))
	assert.Equal(t, "REFERENCE_SOURCE_INVALID", mrErrorCode(t, body))
}

// --- Scenario 16: budget cannot fund every source ---------------------------

func TestMultiReferenceWritePath_BudgetExceededRejected(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv := multiReferenceTestServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")
	srcA := mrNode(t, pool, treeID, &root, "a")
	srcB := mrNode(t, pool, treeID, &root, "b")

	status, body := mrPost(t, srv, mrPreflightPath(treeID), map[string]any{
		"source_node_ids":        []string{srcA.String(), srcB.String()},
		"profile_context_budget": 10, // floor(10*0.5)=5 < 2*256
	})
	assert.Equal(t, http.StatusUnprocessableEntity, status, "body: %s", string(body))
	assert.Equal(t, "REFERENCE_CONTEXT_BUDGET_EXCEEDED", mrErrorCode(t, body))
}

// --- Scenario 8: source deleted after preflight -----------------------------

func TestMultiReferenceWritePath_DeletedAfterPreflightIsStale(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv := multiReferenceTestServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")
	srcA := mrNode(t, pool, treeID, &root, "a")
	srcB := mrNode(t, pool, treeID, &root, "b")

	envelope := mrPreflight(t, srv, treeID, []uuid.UUID{srcA, srcB})
	token := mrSelectionToken(t, envelope)
	before := mrMultiReferenceCount(t, pool, treeID)

	_, err := pool.Exec(context.Background(),
		`UPDATE nodes SET deleted_at = clock_timestamp() WHERE id = $1`, srcB)
	require.NoError(t, err)

	status, body := mrPost(t, srv, mrCreatePath(treeID), map[string]any{
		"selection_token": token,
		"content":         "should not be created",
	})
	assert.Equal(t, http.StatusConflict, status, "body: %s", string(body))
	assert.Equal(t, "REFERENCE_SELECTION_STALE", mrErrorCode(t, body))
	assert.Equal(t, before, mrMultiReferenceCount(t, pool, treeID), "a stale selection creates no node")
}

// --- Scenario 9: source edited after preflight ------------------------------

func TestMultiReferenceWritePath_EditedAfterPreflightIsStale(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv := multiReferenceTestServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")
	srcA := mrNode(t, pool, treeID, &root, "a")
	srcB := mrNode(t, pool, treeID, &root, "b")

	token := mrSelectionToken(t, mrPreflight(t, srv, treeID, []uuid.UUID{srcA, srcB}))
	before := mrMultiReferenceCount(t, pool, treeID)

	_, err := pool.Exec(context.Background(),
		`UPDATE nodes SET content = 'silently changed' WHERE id = $1`, srcA)
	require.NoError(t, err)

	status, body := mrPost(t, srv, mrCreatePath(treeID), map[string]any{
		"selection_token": token,
		"content":         "must not answer from mixed context",
	})
	assert.Equal(t, http.StatusConflict, status, "body: %s", string(body))
	assert.Equal(t, "REFERENCE_SELECTION_STALE", mrErrorCode(t, body))
	assert.Equal(t, before, mrMultiReferenceCount(t, pool, treeID))
}

// --- Token integrity --------------------------------------------------------

func TestMultiReferenceWritePath_TamperedTokenRejected(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv := multiReferenceTestServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")
	srcA := mrNode(t, pool, treeID, &root, "a")
	srcB := mrNode(t, pool, treeID, &root, "b")

	token := mrSelectionToken(t, mrPreflight(t, srv, treeID, []uuid.UUID{srcA, srcB}))
	tampered := token[:len(token)-2] + "AA"

	status, body := mrPost(t, srv, mrCreatePath(treeID), map[string]any{
		"selection_token": tampered,
		"content":         "forged",
	})
	assert.Equal(t, http.StatusBadRequest, status, "body: %s", string(body))
	assert.Equal(t, "REFERENCE_SELECTION_TOKEN_INVALID", mrErrorCode(t, body))
}

func TestMultiReferenceWritePath_TokenFromAnotherTreeRejected(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv := multiReferenceTestServer(t, pool)
	treeID := mrTree(t, pool)
	otherTree := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")
	srcA := mrNode(t, pool, treeID, &root, "a")
	srcB := mrNode(t, pool, treeID, &root, "b")

	token := mrSelectionToken(t, mrPreflight(t, srv, treeID, []uuid.UUID{srcA, srcB}))

	// Mint the same selection against the other tree, then post it to the
	// first tree's create endpoint (§9.4 REFERENCE_TREE_MISMATCH).
	otherRoot := mrNode(t, pool, otherTree, nil, "other root")
	oA := mrNode(t, pool, otherTree, &otherRoot, "oa")
	oB := mrNode(t, pool, otherTree, &otherRoot, "ob")
	otherToken := mrSelectionToken(t, mrPreflight(t, srv, otherTree, []uuid.UUID{oA, oB}))

	status, body := mrPost(t, srv, mrCreatePath(treeID), map[string]any{
		"selection_token": otherToken,
		"content":         "cross-tree token",
	})
	assert.Equal(t, http.StatusBadRequest, status, "body: %s", string(body))
	assert.Equal(t, "REFERENCE_TREE_MISMATCH", mrErrorCode(t, body))

	// The valid token for this tree still works (control).
	status, body = mrPost(t, srv, mrCreatePath(treeID), map[string]any{
		"selection_token": token,
		"content":         "in-tree token works",
	})
	assert.Equal(t, http.StatusCreated, status, "body: %s", string(body))
}

// --- Scenarios 14/19/20: request-id idempotency -----------------------------

func TestMultiReferenceWritePath_RequestIDIdempotent(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv := multiReferenceTestServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")
	srcA := mrNode(t, pool, treeID, &root, "a")
	srcB := mrNode(t, pool, treeID, &root, "b")
	token := mrSelectionToken(t, mrPreflight(t, srv, treeID, []uuid.UUID{srcA, srcB}))
	requestID := uuid.New().String()

	req := map[string]any{
		"selection_token": token,
		"content":         "idempotent reply",
		"request_id":      requestID,
	}
	status, body := mrPost(t, srv, mrCreatePath(treeID), req)
	require.Equal(t, http.StatusCreated, status, "body: %s", string(body))
	firstID := mrDecode(t, body)["node"].(map[string]any)["id"]
	after := mrNodeCount(t, pool, treeID)

	// A retry with the same payload replays the original result (§15.19).
	status, body = mrPost(t, srv, mrCreatePath(treeID), req)
	require.Equal(t, http.StatusCreated, status, "body: %s", string(body))
	replay := mrDecode(t, body)
	assert.Equal(t, firstID, replay["node"].(map[string]any)["id"], "the same node is returned")
	assert.Len(t, replay["edges"].([]any), 2)
	assert.Equal(t, after, mrNodeCount(t, pool, treeID), "a retry creates no second node")

	// A reused request id with different content is a conflict (§15.20).
	status, body = mrPost(t, srv, mrCreatePath(treeID), map[string]any{
		"selection_token": token,
		"content":         "different payload",
		"request_id":      requestID,
	})
	assert.Equal(t, http.StatusConflict, status, "body: %s", string(body))
	assert.Equal(t, "REFERENCE_REQUEST_ID_CONFLICT", mrErrorCode(t, body))
	assert.Equal(t, after, mrNodeCount(t, pool, treeID))
}

// --- §3.5 invariants after creation -----------------------------------------

func TestMultiReferenceWritePath_ParentModeInvariantsAfterCreation(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv := multiReferenceTestServer(t, pool)
	treeID := mrTree(t, pool)
	ctx := context.Background()
	root := mrNode(t, pool, treeID, nil, "root")
	srcA := mrNode(t, pool, treeID, &root, "a")
	srcB := mrNode(t, pool, treeID, &root, "b")

	envelope := mrPreflight(t, srv, treeID, []uuid.UUID{srcA, srcB})
	status, body := mrPost(t, srv, mrCreatePath(treeID), map[string]any{
		"selection_token": mrSelectionToken(t, envelope),
		"content":         "grounded reply",
	})
	require.Equal(t, http.StatusCreated, status, "body: %s", string(body))
	nodeID := uuid.MustParse(mrDecode(t, body)["node"].(map[string]any)["id"].(string))

	node, err := db.NewPGNodeRepo(pool).GetByID(ctx, nodeID)
	require.NoError(t, err)
	assert.Equal(t, db.ParentModeMultiReference, node.ParentMode)
	assert.Equal(t, db.NodeTypeMessage, node.NodeType)
	require.NotNil(t, node.ParentID)
	assert.Equal(t, srcA, *node.ParentID, "parent_id is the first canonical source")
	assert.Equal(t, treeID, node.TreeID)

	// The stored manifest agrees with the edges (§3.5 invariants 3-5).
	var reserved db.MultiReferenceMetadata
	require.NoError(t, json.Unmarshal(mrMetadataSection(t, node.Metadata, "multi_reference"), &reserved))
	assert.Equal(t, *node.ParentID, reserved.PrimarySourceID)
	assert.Equal(t, []uuid.UUID{srcA, srcB}, reserved.CanonicalSourceIDs)
	assert.Equal(t, reserved.PrimarySourceID, reserved.CanonicalSourceIDs[0])

	// The repo-level invariant validator accepts the committed target.
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	require.NoError(t, db.NewPGEdgeRepo(pool).ValidateIncomingInvariant(ctx, tx, node))

	// §15.28: an ordinary reply may target the multi-reference node (the new
	// node is the edge target; the multi-reference node is the source).
	nodeSvc := service.NewNodeService(db.NewPGNodeRepo(pool), db.NewPGEdgeRepo(pool), pool, sse.NewHub()).
		WithReferenceSelection(nil)
	child, err := nodeSvc.Create(ctx, treeID, service.CreateNodeInput{
		ParentID: nodeID, Content: "a follow-up question", ContentFormat: "markdown",
		NodeType: "message", EdgeType: "reply", AuthorID: testUserID, TreeID: treeID,
	})
	require.NoError(t, err, "a reply from a multi-reference node must be allowed")
	assert.Equal(t, nodeID, *child.Node.ParentID)
}

// mrMetadataSection extracts one top-level metadata key.
func mrMetadataSection(t *testing.T, metadata []byte, key string) []byte {
	t.Helper()
	var doc map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(metadata, &doc))
	raw, ok := doc[key]
	require.True(t, ok, "metadata key %q missing: %s", key, string(metadata))
	return raw
}

// --- §13.2 reserved metadata merge ------------------------------------------

func TestMultiReferenceWritePath_ReservedMetadataWins(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv := multiReferenceTestServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")
	srcA := mrNode(t, pool, treeID, &root, "a")
	srcB := mrNode(t, pool, treeID, &root, "b")

	token := mrSelectionToken(t, mrPreflight(t, srv, treeID, []uuid.UUID{srcA, srcB}))
	status, body := mrPost(t, srv, mrCreatePath(treeID), map[string]any{
		"selection_token": token,
		"content":         "with client metadata",
		"metadata": map[string]any{
			"agent_turn_id":   "0191a8b2-7fff-7000-9000-000000000888",
			"multi_reference": map[string]any{"version": 99, "primarySourceId": uuid.New().String()},
		},
	})
	require.Equal(t, http.StatusCreated, status, "body: %s", string(body))
	metadata := mrDecode(t, body)["node"].(map[string]any)["metadata"].(map[string]any)

	// Client metadata is retained; the reserved key is server-built (§13.2).
	assert.Equal(t, "0191a8b2-7fff-7000-9000-000000000888", metadata["agent_turn_id"])
	reserved := metadata["multi_reference"].(map[string]any)
	assert.EqualValues(t, 1, reserved["version"], "client-supplied multi_reference must not win")
	assert.Equal(t, srcA.String(), reserved["primarySourceId"])
}

// --- Wire contract: unknown fields and malformed bodies ---------------------

func TestMultiReferenceWritePath_RejectsUnknownFields(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv := multiReferenceTestServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")
	srcA := mrNode(t, pool, treeID, &root, "a")

	status, body := mrPost(t, srv, mrPreflightPath(treeID), map[string]any{
		"source_node_ids": []string{srcA.String(), srcA.String()},
		"unknown_field":   true,
	})
	assert.Equal(t, http.StatusBadRequest, status, "body: %s", string(body))
	assert.Equal(t, "INVALID_BODY", mrErrorCode(t, body))
}
