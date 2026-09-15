// Endpoint tests for the SPEC-PL-06 §6 compile-surface caller: GET
// /api/v1/context/{node_id} on a real PostgreSQL, driving the real context
// compiler out of internal/context and the real persisted selection loaded by
// internal/service.
//
// Covers: the §6.1 block for a multi-reference reply (source_count, one header
// per source in canonical selection order, the STORED manifest hash carried
// verbatim), the byte-identical output of a non-multi-reference node,
// REFERENCE_SELECTION_STALE (409) for a source that changed or was deleted
// after creation, REFERENCE_CONTEXT_BUDGET_EXCEEDED (422) for a budget that
// cannot reserve 256 tokens per source, and a corrupt reserved manifest
// failing loudly (500).
package handler

import (
	"context"
	"fmt"
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

	"github.com/coding-hermes/hermes-canopy/internal/card"
	ctxpkg "github.com/coding-hermes/hermes-canopy/internal/context"
	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/service"
	"github.com/coding-hermes/hermes-canopy/internal/sse"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

// --- Harness -----------------------------------------------------------------

// ctxStubTopics is an empty topic reader: these tests exercise the
// multi-reference block, so reference resolution must contribute nothing.
type ctxStubTopics struct{}

func (ctxStubTopics) GetBySlug(context.Context, uuid.UUID, string) (*db.Topic, error) {
	return nil, nil
}

func (ctxStubTopics) GetTopicsForNode(context.Context, uuid.UUID) ([]db.Topic, error) {
	return nil, nil
}

func (ctxStubTopics) GetResolvedTopicsForNode(context.Context, uuid.UUID) ([]db.Topic, error) {
	return nil, nil
}

// ctxStubCards is an empty card reader (includeCards is off in these tests).
type ctxStubCards struct{}

func (ctxStubCards) GetByContextHash(context.Context, string) ([]card.Card, error) {
	return nil, nil
}

// ctxCompileServer wires the compile route exactly as the production router
// does: the REAL context compiler over the real PG node repo, and the REAL
// TreeServiceImpl as the handler's selection loader. The preflight/create
// routes from the phase-2 harness are mounted too, so a test creates its
// multi-reference reply through the actual §9.1/§9.2 endpoints.
func ctxCompileServer(t *testing.T, pool *pgxpool.Pool) *httptest.Server {
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

	mrHandler := NewMultiReferenceHandler(treeSvc, nodeSvc).
		WithMembership(db.NewPGTreeMemberRepo(pool))
	membership := TreeMembershipMiddleware(db.NewPGTreeMemberRepo(pool))

	compiler := ctxpkg.NewCompiler(nodeRepo, ctxStubTopics{}, ctxStubCards{}, ctxpkg.NewTokenEstimator(), 5)
	ctxHandler := NewContextHandler(compiler, 8000).WithReferenceSelectionLoader(treeSvc)

	r := chi.NewRouter()
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(AuthMiddleware("canopy-dev-secret"))
		r.With(membership).Post("/trees/{tree_id}/reference-selections", mrHandler.ValidateReferenceSelection)
		r.With(membership).Post("/trees/{tree_id}/multi-reference-replies", mrHandler.CreateMultiReferenceReply)
		r.Get("/context/{node_id}", ctxHandler.Compile)
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv
}

// ctxCompilePath is the compile surface under test.
func ctxCompilePath(nodeID uuid.UUID) string {
	return "/api/v1/context/" + nodeID.String()
}

// ctxCompile runs a compile request and returns the status + raw body.
func ctxCompile(t *testing.T, srv *httptest.Server, path string) (int, []byte) {
	t.Helper()
	return mrGet(t, srv, path, authHeader(t))
}

// ctxCompileContent runs a compile request and returns the compiled content.
func ctxCompileContent(t *testing.T, srv *httptest.Server, path string) string {
	t.Helper()
	status, body := ctxCompile(t, srv, path)
	require.Equal(t, http.StatusOK, status, "compile body: %s", string(body))
	content, ok := mrDecode(t, body)["content"].(string)
	require.True(t, ok, "content missing: %s", string(body))
	return content
}

// ctxStoredManifestHash reads the hash creation persisted in the reserved
// metadata block — the provenance record the compiler must carry verbatim.
func ctxStoredManifestHash(t *testing.T, pool *pgxpool.Pool, nodeID uuid.UUID) string {
	t.Helper()
	var hash string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT metadata->'multi_reference'->>'contextManifestHash' FROM nodes WHERE id = $1`,
		nodeID).Scan(&hash))
	require.NotEmpty(t, hash, "node %s carries no stored manifest hash", nodeID)
	return hash
}

// ctxNodeAuthor reads a node's author id.
func ctxNodeAuthor(t *testing.T, pool *pgxpool.Pool, nodeID uuid.UUID) uuid.UUID {
	t.Helper()
	var author uuid.UUID
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT author_id FROM nodes WHERE id = $1`, nodeID).Scan(&author))
	return author
}

// ctxSourceHeaderAt returns the §6.1 header starting at marker, up to its
// closing bracket.
func ctxSourceHeaderAt(t *testing.T, content, marker string) (string, int) {
	t.Helper()
	idx := strings.Index(content, marker)
	require.GreaterOrEqual(t, idx, 0, "missing %q in:\n%s", marker, content)
	end := strings.Index(content[idx:], "]")
	require.GreaterOrEqual(t, end, 0, "unterminated header %q in:\n%s", marker, content)
	return content[idx : idx+end+1], idx
}

// --- A: the §6.1 block -------------------------------------------------------

func TestContextCompileMultiReference_PersistedSelectionRendersBlock(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv := ctxCompileServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root message")
	srcA := mrNode(t, pool, treeID, &root, "the first approach stores edges")
	srcB := mrNode(t, pool, treeID, &root, "the second approach stores trees")
	srcC := mrNode(t, pool, treeID, &root, "the third approach stores neither")

	// Selection order is user intent: C, then A, then B.
	created, nodeID := mrCreateReply(t, srv, treeID, []uuid.UUID{srcC, srcA, srcB}, "synthesis reply")
	require.EqualValues(t, 3, created["reference_context"].(map[string]any)["source_count"])
	storedHash := ctxStoredManifestHash(t, pool, nodeID)

	status, body := ctxCompile(t, srv, ctxCompilePath(nodeID))
	require.Equal(t, http.StatusOK, status, "compile body: %s", string(body))
	envelope := mrDecode(t, body)
	content, ok := envelope["content"].(string)
	require.True(t, ok, "content missing: %s", string(body))

	// The block is the highest-priority block of the turn: it comes first.
	require.True(t, strings.HasPrefix(content, `<canopy_multi_reference version="1"`),
		"content must start with the §6.1 block, got: %q", firstLine(content))
	require.Contains(t, content, `</canopy_multi_reference>`)
	require.Contains(t, content, `source_count="3"`)
	require.Contains(t, content, `target_tree_id="`+treeID.String()+`"`)
	require.Contains(t, content, `primary_source_id="`+srcC.String()+`"`)

	// One [Source Rn | ...] header per source, in canonical selection order,
	// each naming the source node and the §5.2 colour key the edge carries.
	previous := -1
	for i, want := range []uuid.UUID{srcC, srcA, srcB} {
		label := db.ReferenceSourceLabel(i)
		header, idx := ctxSourceHeaderAt(t, content, "[Source "+label+" |")
		require.Greater(t, idx, previous, "source %d (%s) is out of canonical order", i, label)
		previous = idx
		assert.Contains(t, header, "node_id="+want.String(), "header %s", label)
		assert.Contains(t, header, "color="+db.ReferenceColorKey(treeID, nodeID, want), "header %s", label)
	}
	// Every source's content is placed (all three fit their allocations).
	for _, want := range []uuid.UUID{srcA, srcB, srcC} {
		assert.Contains(t, content, want.String())
	}
	assert.Contains(t, content, "the first approach stores edges")
	assert.Contains(t, content, "the second approach stores trees")
	assert.Contains(t, content, "the third approach stores neither")

	// §6.3 manifest entry: source_count and the STORED hash, verbatim.
	manifest, ok := envelope["manifest"].(map[string]any)
	require.True(t, ok, "manifest missing: %s", string(body))
	mrEntry, ok := manifest["multiReference"].(map[string]any)
	require.True(t, ok, "manifest.multiReference missing: %s", string(body))
	assert.EqualValues(t, 3, mrEntry["sourceCount"])
	assert.Equal(t, storedHash, mrEntry["manifestHash"],
		"the manifest hash must be the stored provenance record, never a fresh digest")
	assert.Equal(t, srcC.String(), mrEntry["primarySourceId"])
	assert.EqualValues(t, 4000, mrEntry["tokenBudget"], "min(floor(8000*0.50), 16384)")

	// The per-source manifest rows are the compiled sources, in order.
	srcRows, ok := mrEntry["sources"].([]any)
	require.True(t, ok, "manifest.multiReference.sources missing: %s", string(body))
	require.Len(t, srcRows, 3)
	for i, want := range []uuid.UUID{srcC, srcA, srcB} {
		row := srcRows[i].(map[string]any)
		assert.Equal(t, want.String(), row["nodeId"], "manifest source %d", i)
		assert.Equal(t, db.ReferenceSourceLabel(i), row["sourceLabel"], "manifest source %d", i)
		assert.EqualValues(t, i+1, row["referenceIndex"], "manifest source %d", i)
		assert.Equal(t, storedHash, mrEntry["manifestHash"])
	}
}

// firstLine keeps a failure message readable.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func TestContextCompileMultiReference_ReadsEdgeMetadataLabelAndColor(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv := ctxCompileServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")
	srcA := mrNode(t, pool, treeID, &root, "source A")
	srcB := mrNode(t, pool, treeID, &root, "source B")
	_, nodeID := mrCreateReply(t, srv, treeID, []uuid.UUID{srcA, srcB}, "reply")

	// The §5.2 presentation fields are read from the EDGE, not re-derived:
	// rewrite them on the second edge and the block must follow.
	_, err := pool.Exec(context.Background(), `
        UPDATE edges
        SET metadata = metadata || '{"source_label":"RX","color_key":"ref-7"}'::jsonb
        WHERE target_id = $1 AND source_id = $2 AND edge_type = 'reference'`,
		nodeID, srcB)
	require.NoError(t, err)

	content := ctxCompileContent(t, srv, ctxCompilePath(nodeID))
	header, _ := ctxSourceHeaderAt(t, content, "[Source RX |")
	assert.Contains(t, header, "color=ref-7")
	assert.Contains(t, header, "node_id="+srcB.String())

	// The untouched edge keeps its own persisted label and colour.
	other, idx := ctxSourceHeaderAt(t, content, "[Source "+db.ReferenceSourceLabel(0)+" |")
	assert.Contains(t, other, "node_id="+srcA.String())
	assert.Contains(t, other, "color="+db.ReferenceColorKey(treeID, nodeID, srcA))
	assert.Less(t, idx, strings.Index(content, "[Source RX |"),
		"selection_order still decides the order")
}

// --- B: a non-multi-reference node is untouched ------------------------------

func TestContextCompileMultiReference_OrdinaryNodeOutputUnchanged(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv := ctxCompileServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root message")
	child := mrNode(t, pool, treeID, &root, "child message")

	status, body := ctxCompile(t, srv, ctxCompilePath(child))
	require.Equal(t, http.StatusOK, status, "compile body: %s", string(body))
	envelope := mrDecode(t, body)

	// PINNED: the exact pre-change output for a two-node lineage, byte for
	// byte — "--- node <id> (<author>) ---\n<content>", oldest first (the
	// compiler's ancestry read is ordered by sequence_num ascending and it
	// renders the slice from its tail). The pin was verified against the
	// pre-change binary at HEAD 2da2689 before this test was written, and
	// nothing here may add, reorder or wrap that text.
	want := fmt.Sprintf("--- node %s (%s) ---\n%s\n\n--- node %s (%s) ---\n%s",
		root, ctxNodeAuthor(t, pool, root), "root message",
		child, ctxNodeAuthor(t, pool, child), "child message")
	assert.Equal(t, want, envelope["content"])

	// No selection, so no §6.1 block and no manifest entry.
	assert.NotContains(t, envelope["content"], "canopy_multi_reference")
	manifest := envelope["manifest"].(map[string]any)
	assert.NotContains(t, manifest, "multiReference")
}

// --- C: staleness is §9.3's decision -----------------------------------------

func TestContextCompileMultiReference_StaleSelectionConflicts(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv := ctxCompileServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")
	srcA := mrNode(t, pool, treeID, &root, "the first approach stores edges")
	srcB := mrNode(t, pool, treeID, &root, "the second approach stores trees")
	_, nodeID := mrCreateReply(t, srv, treeID, []uuid.UUID{srcA, srcB}, "reply")

	// Control: an untouched snapshot compiles.
	status, body := ctxCompile(t, srv, ctxCompilePath(nodeID))
	require.Equal(t, http.StatusOK, status, "control compile body: %s", string(body))

	// (1) A source edited after creation: the live snapshot digest no longer
	// matches the stored one, so the request fails instead of mixing
	// snapshots.
	_, err := pool.Exec(context.Background(),
		`UPDATE nodes SET content = 'silently rewritten B' WHERE id = $1`, srcB)
	require.NoError(t, err)

	status, body = ctxCompile(t, srv, ctxCompilePath(nodeID))
	require.Equal(t, http.StatusConflict, status, "body: %s", string(body))
	assert.Equal(t, "REFERENCE_SELECTION_STALE", mrErrorCode(t, body))
	assert.NotContains(t, string(body), "canopy_multi_reference", "no block is rendered for a stale selection")

	// (2) The content hash is a function of the live content: restoring the
	// original text restores verification — the check is the live snapshot,
	// not a latch.
	_, err = pool.Exec(context.Background(),
		`UPDATE nodes SET content = 'the second approach stores trees' WHERE id = $1`, srcB)
	require.NoError(t, err)
	status, body = ctxCompile(t, srv, ctxCompilePath(nodeID))
	require.Equal(t, http.StatusOK, status, "restored compile body: %s", string(body))

	// (3) A soft-deleted source is the same provenance change (§2 decision
	// 37: the edge survives the source, so the reply stays auditable).
	_, err = pool.Exec(context.Background(),
		`UPDATE nodes SET deleted_at = clock_timestamp() WHERE id = $1`, srcA)
	require.NoError(t, err)
	status, body = ctxCompile(t, srv, ctxCompilePath(nodeID))
	require.Equal(t, http.StatusConflict, status, "body: %s", string(body))
	assert.Equal(t, "REFERENCE_SELECTION_STALE", mrErrorCode(t, body))
}

// --- D: the §6.2 admission rule ----------------------------------------------

func TestContextCompileMultiReference_BudgetTooSmall(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv := ctxCompileServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")
	srcA := mrNode(t, pool, treeID, &root, "source A")
	srcB := mrNode(t, pool, treeID, &root, "source B")
	srcC := mrNode(t, pool, treeID, &root, "source C")
	_, nodeID := mrCreateReply(t, srv, treeID, []uuid.UUID{srcA, srcB, srcC}, "reply")

	// 3 sources x 256 tokens = 768 required; a 1000-token turn reserves
	// floor(1000 * 0.50) = 500, which cannot fund the selection.
	status, body := ctxCompile(t, srv, ctxCompilePath(nodeID)+"?budget=1000")
	require.Equal(t, http.StatusUnprocessableEntity, status, "body: %s", string(body))
	assert.Equal(t, "REFERENCE_CONTEXT_BUDGET_EXCEEDED", mrErrorCode(t, body))
	assert.NotContains(t, string(body), "canopy_multi_reference", "never a block plus an error")

	// 2,000 reserves 1,000 — enough — and the request compiles with the
	// block and the selection's own §6.3 budget accounting.
	status, body = ctxCompile(t, srv, ctxCompilePath(nodeID)+"?budget=2000")
	require.Equal(t, http.StatusOK, status, "body: %s", string(body))
	compiled := mrDecode(t, body)
	require.Contains(t, compiled["content"], `source_count="3"`)
	assert.EqualValues(t, 1000, compiled["manifest"].(map[string]any)["multiReference"].(map[string]any)["tokenBudget"],
		"min(floor(2000*0.50), 16384)")
}

// --- requirement 4: a corrupt reserved manifest fails loudly -----------------

func TestContextCompileMultiReference_CorruptReservedManifestFailsLoudly(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	srv := ctxCompileServer(t, pool)
	treeID := mrTree(t, pool)
	root := mrNode(t, pool, treeID, nil, "root")
	srcA := mrNode(t, pool, treeID, &root, "source A")
	srcB := mrNode(t, pool, treeID, &root, "source B")
	_, nodeID := mrCreateReply(t, srv, treeID, []uuid.UUID{srcA, srcB}, "reply")

	// (1) parent_mode says multi_reference but the reserved block is gone:
	// the creation path writes both in one transaction, so this is a corrupt
	// row and must never be answered with a fabricated empty block.
	_, err := pool.Exec(context.Background(),
		`UPDATE nodes SET metadata = metadata - 'multi_reference' WHERE id = $1`, nodeID)
	require.NoError(t, err)

	status, body := ctxCompile(t, srv, ctxCompilePath(nodeID))
	require.Equal(t, http.StatusInternalServerError, status, "body: %s", string(body))
	assert.Equal(t, "CONTEXT_COMPILE_ERROR", mrErrorCode(t, body))
	assert.NotContains(t, string(body), "canopy_multi_reference")

	// (2) The block is present but its digest is not a digest: unverifiable,
	// so still a loud failure rather than a silent unverified compile.
	_, err = pool.Exec(context.Background(), `
        UPDATE nodes
        SET metadata = jsonb_set(metadata, '{multi_reference,contextManifestHash}', '"not-a-digest"')
        WHERE id = $1`, nodeID)
	require.NoError(t, err)

	status, body = ctxCompile(t, srv, ctxCompilePath(nodeID))
	require.Equal(t, http.StatusInternalServerError, status, "body: %s", string(body))
	assert.Equal(t, "CONTEXT_COMPILE_ERROR", mrErrorCode(t, body))
}
