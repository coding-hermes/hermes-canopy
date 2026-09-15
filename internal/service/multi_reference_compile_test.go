// Database-backed tests for the §6 compile-surface loader
// (TreeServiceImpl.LoadCompileSelection): the persisted selection as the
// context compiler consumes it, including the two fallbacks the §6 wiring
// names (branch root when the stored span does not carry a source, and the
// §5.2 label/colour when the reference edge's metadata is absent).
package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/sse"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

// --- Harness -----------------------------------------------------------------

func compileTestTree(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	treeID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO trees (id, owner_id, title) VALUES ($1, $2, 'Compile Loader Tree')`,
		treeID, uuid.New())
	require.NoError(t, err, "insert tree")
	return treeID
}

func compileTestNode(t *testing.T, pool *pgxpool.Pool, treeID uuid.UUID, parentID *uuid.UUID, content string) uuid.UUID {
	t.Helper()
	nodeID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO nodes (id, tree_id, parent_id, author_id, content, node_type)
         VALUES ($1, $2, $3, $4, $5, 'message')`,
		nodeID, treeID, parentID, uuid.New(), content)
	require.NoError(t, err, "insert node")
	return nodeID
}

// compileTestServices wires the two services the loader runs behind, exactly
// as production does.
func compileTestServices(t *testing.T, pool *pgxpool.Pool) (*TreeServiceImpl, *NodeServiceImpl) {
	t.Helper()
	nodeRepo := db.NewPGNodeRepo(pool)
	edgeRepo := db.NewPGEdgeRepo(pool)
	hub := sse.NewHubWithConfig(sse.HubConfig{PruneInterval: -1, DrainTimeout: 100 * time.Millisecond})
	t.Cleanup(func() { _ = hub.Shutdown(context.Background()) })

	treeSvc := NewTreeService(db.NewPGTreeRepo(pool), nodeRepo, edgeRepo, pool).
		WithReferenceSelection(NewReferenceSelectionSigner("compile-loader-test-secret", nil), 8000)
	nodeSvc := NewNodeService(nodeRepo, edgeRepo, pool, hub).WithReferenceSelection(treeSvc)
	return treeSvc, nodeSvc
}

// compileTestReply creates a real multi-reference reply through the §9.1/§9.2
// service path and returns the created node id.
func compileTestReply(t *testing.T, pool *pgxpool.Pool, treeSvc *TreeServiceImpl, nodeSvc *NodeServiceImpl, treeID uuid.UUID, ordered []uuid.UUID, content string) uuid.UUID {
	t.Helper()
	ctx := WithRequester(context.Background(), uuid.New())
	preview, err := treeSvc.ValidateReferenceSelection(ctx, treeID, ReferenceSelectionInput{
		SourceNodeIDs:        ordered,
		ProfileContextBudget: 16384,
	})
	require.NoError(t, err, "preflight")

	created, err := nodeSvc.CreateMultiReferenceReply(ctx, treeID, CreateMultiReferenceReplyInput{
		SelectionToken: preview.SelectionToken,
		Content:        content,
	})
	require.NoError(t, err, "create reply")
	return created.Node.ID
}

// --- Canonical order + §5.2 fallbacks ----------------------------------------

func TestLoadCompileSelection_CanonicalOrderAndPresentationFallbacks(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	treeSvc, nodeSvc := compileTestServices(t, pool)
	treeID := compileTestTree(t, pool)
	root := compileTestNode(t, pool, treeID, nil, "root")
	srcA := compileTestNode(t, pool, treeID, &root, "the first approach stores edges")
	srcB := compileTestNode(t, pool, treeID, &root, "the second approach stores trees")
	nodeID := compileTestReply(t, pool, treeSvc, nodeSvc, treeID, []uuid.UUID{srcA, srcB}, "reply")

	// Remove the §5.2 metadata from the SECOND edge: its label and colour can
	// then only come from the derivation fallback, so the assertions below
	// prove the fallback ran rather than echoing stored values.
	_, err := pool.Exec(context.Background(),
		`UPDATE edges SET metadata = '{}'::jsonb WHERE target_id = $1 AND source_id = $2 AND edge_type = $3`,
		nodeID, srcB, db.EdgeTypeReference)
	require.NoError(t, err)

	sel, err := treeSvc.LoadCompileSelection(context.Background(), nodeID)
	require.NoError(t, err)
	require.NotNil(t, sel)
	assert.Equal(t, treeID, sel.TreeID)
	assert.Equal(t, srcA, sel.Metadata.PrimarySourceID)
	assert.Equal(t, []uuid.UUID{srcA, srcB}, sel.Metadata.CanonicalSourceIDs)
	require.Len(t, sel.Sources, 2)

	for i, want := range []struct {
		nodeID  uuid.UUID
		content string
		author  bool
	}{
		{nodeID: srcA, content: "the first approach stores edges", author: true},
		{nodeID: srcB, content: "the second approach stores trees", author: true},
	} {
		src := sel.Sources[i]
		assert.Equal(t, want.nodeID, src.NodeID, "source %d follows canonical selection order", i)
		assert.Equal(t, want.content, src.Content, "source %d content", i)
		assert.Equal(t, "message", src.NodeType, "source %d node type", i)
		assert.NotEqual(t, uuid.Nil, src.AuthorID, "source %d author", i)
		assert.Greater(t, src.SequenceNum, int64(0), "source %d sequence", i)
		assert.False(t, src.CreatedAt.IsZero(), "source %d created_at", i)
		assert.NotEmpty(t, src.ContentHash, "source %d live hash", i)
		assert.NotEqual(t, uuid.Nil, src.BranchRootID, "source %d branch root", i)

		// §5.2: the accessible label and the deterministic colour key, in
		// canonical position.
		assert.Equal(t, db.ReferenceSourceLabel(i), src.Label, "source %d label", i)
		assert.Equal(t, db.ReferenceColorKey(treeID, nodeID, want.nodeID), src.ColorKey, "source %d colour key", i)
	}

	// The live hash is the node's current content hash.
	var storedHash string
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT content_hash FROM nodes WHERE id = $1`, srcA).Scan(&storedHash))
	assert.Equal(t, storedHash, sel.Sources[0].ContentHash)
}

// --- Branch-root fallback ----------------------------------------------------

func TestLoadCompileSelection_BranchRootFallsBackToSourceRow(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	treeSvc, nodeSvc := compileTestServices(t, pool)
	treeID := compileTestTree(t, pool)
	root := compileTestNode(t, pool, treeID, nil, "root")
	srcA := compileTestNode(t, pool, treeID, &root, "source A")
	srcB := compileTestNode(t, pool, treeID, &root, "source B")
	nodeID := compileTestReply(t, pool, treeSvc, nodeSvc, treeID, []uuid.UUID{srcA, srcB}, "reply")

	// Control: the stored span carries every source, and each source
	// diverges directly from the shared ancestor, so it is its own root.
	sel, err := treeSvc.LoadCompileSelection(context.Background(), nodeID)
	require.NoError(t, err)
	require.Len(t, sel.Sources, 2)
	require.NotNil(t, sel.Metadata.BranchSpan)
	for i, src := range sel.Sources {
		assert.Equal(t, src.NodeID, src.BranchRootID, "source %d is its own branch root", i)
	}

	// Drop the span entirely: the stored record no longer names a branch root
	// for any source, so each source's own row is the only anchor. The digest
	// is untouched, so the selection still verifies.
	_, err = pool.Exec(context.Background(), `
        UPDATE nodes
        SET metadata = jsonb_set(metadata, '{multi_reference}',
                                 (metadata->'multi_reference') - 'branchSpan')
        WHERE id = $1`, nodeID)
	require.NoError(t, err)

	sel, err = treeSvc.LoadCompileSelection(context.Background(), nodeID)
	require.NoError(t, err, "a selection with no branch span still verifies")
	require.Nil(t, sel.Metadata.BranchSpan)
	require.Len(t, sel.Sources, 2)
	for i, src := range sel.Sources {
		assert.Equal(t, src.NodeID, src.BranchRootID,
			"source %d falls back to its own row when the span carries no entry", i)
	}
}

// --- No selection ------------------------------------------------------------

func TestLoadCompileSelection_NilWhenTheTargetHasNone(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	treeSvc, nodeSvc := compileTestServices(t, pool)
	treeID := compileTestTree(t, pool)
	root := compileTestNode(t, pool, treeID, nil, "root")
	ordinary := compileTestNode(t, pool, treeID, &root, "an ordinary reply")
	srcA := compileTestNode(t, pool, treeID, &root, "source A")
	srcB := compileTestNode(t, pool, treeID, &root, "source B")
	reply := compileTestReply(t, pool, treeSvc, nodeSvc, treeID, []uuid.UUID{srcA, srcB}, "reply")

	cases := []struct {
		name string
		id   uuid.UUID
	}{
		{"ordinary lineage node", ordinary},
		{"node that does not exist", uuid.New()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sel, err := treeSvc.LoadCompileSelection(context.Background(), tc.id)
			require.NoError(t, err)
			assert.Nil(t, sel, "no selection: the compiler answers for this node as it did before §6")
		})
	}

	// A soft-deleted target compiles nothing either (the compiler's own read
	// answers NODE_NOT_FOUND).
	_, err := pool.Exec(context.Background(),
		`UPDATE nodes SET deleted_at = clock_timestamp() WHERE id = $1`, reply)
	require.NoError(t, err)
	sel, err := treeSvc.LoadCompileSelection(context.Background(), reply)
	require.NoError(t, err)
	assert.Nil(t, sel)
}

// --- Staleness identity ------------------------------------------------------

func TestLoadCompileSelection_StaleSourcesCarryTheCatalogCode(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	treeSvc, nodeSvc := compileTestServices(t, pool)
	treeID := compileTestTree(t, pool)
	root := compileTestNode(t, pool, treeID, nil, "root")
	srcA := compileTestNode(t, pool, treeID, &root, "source A")
	srcB := compileTestNode(t, pool, treeID, &root, "source B")
	nodeID := compileTestReply(t, pool, treeSvc, nodeSvc, treeID, []uuid.UUID{srcA, srcB}, "reply")

	_, err := pool.Exec(context.Background(),
		`UPDATE nodes SET content = 'rewritten after creation' WHERE id = $1`, srcB)
	require.NoError(t, err)

	sel, err := treeSvc.LoadCompileSelection(context.Background(), nodeID)
	assert.Nil(t, sel, "a stale selection is never partially returned")
	apiErr, ok := ReferenceErrorFrom(err)
	require.True(t, ok, "want a catalog error, got %v", err)
	assert.Equal(t, "REFERENCE_SELECTION_STALE", apiErr.Code)
	assert.Equal(t, 409, apiErr.Status)
}
