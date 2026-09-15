// Repository-level tests for the multi-message reference model.
//
// Covers SPEC-PL-06 §12 / §12.1 (EdgeRepo.Create decision table,
// CreateReferenceSet, GetActiveIncoming, ValidateIncomingInvariant) and the
// §5.2 edge metadata contract, plus §15.1 scenarios 1-2, 4-7 and 9-11 at the
// repository layer. Runs against a real PostgreSQL (testutil shared pool).
package db_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

// --- helpers -----------------------------------------------------------------

// refTestTree creates a tree row and returns its id.
func refTestTree(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	treeID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO trees (id, owner_id, title) VALUES ($1, $2, 'Reference Test Tree')`,
		treeID, uuid.New())
	require.NoError(t, err, "create tree")
	return treeID
}

// refTestNode inserts a node directly so tests can control parent_id,
// parent_mode and node_type.
func refTestNode(t *testing.T, pool *pgxpool.Pool, treeID uuid.UUID, parentID *uuid.UUID, nodeType string, parentMode db.ParentMode, content string) uuid.UUID {
	t.Helper()
	nodeID := uuid.New()
	_, err := pool.Exec(context.Background(), `
        INSERT INTO nodes (id, tree_id, parent_id, parent_mode, author_id, content, node_type)
        VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		nodeID, treeID, parentID, string(parentMode), uuid.New(), content, nodeType)
	require.NoError(t, err, "insert node")
	return nodeID
}

// refTestTx opens a transaction and guarantees it is rolled back.
func refTestTx(t *testing.T, pool *pgxpool.Pool) pgx.Tx {
	t.Helper()
	tx, err := pool.Begin(context.Background())
	require.NoError(t, err, "begin tx")
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return tx
}

func refTestEdgeRepo(pool *pgxpool.Pool) *db.PGEdgeRepo { return db.NewPGEdgeRepo(pool) }

// refTestEdge builds an edge for the generic Create path. edges.sequence_num
// is NOT NULL and has no default, so every inserted edge must carry one.
func refTestEdge(treeID, sourceID, targetID uuid.UUID, edgeType string) *db.Edge {
	return &db.Edge{
		TreeID:      treeID,
		SourceID:    sourceID,
		TargetID:    targetID,
		EdgeType:    edgeType,
		SequenceNum: 1,
	}
}

// --- Scenario 1: parent_mode default + constraints (migration 000047) --------

func TestMultiReferenceMigration_ParentModeDefaultAndConstraints(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()

	// A node inserted without parent_mode gets the 'lineage' default and the
	// column is NOT NULL (SPEC-PL-06 §3.1).
	nodeID := uuid.New()
	treeID := refTestTree(t, pool)
	_, err := pool.Exec(ctx,
		`INSERT INTO nodes (id, tree_id, author_id, content) VALUES ($1, $2, $3, 'plain')`,
		nodeID, treeID, uuid.New())
	require.NoError(t, err)

	var mode string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT parent_mode FROM nodes WHERE id = $1`, nodeID).Scan(&mode))
	assert.Equal(t, string(db.ParentModeLineage), mode)

	// chk_nodes_parent_mode rejects anything outside the enumerated set.
	_, err = pool.Exec(ctx,
		`INSERT INTO nodes (id, tree_id, author_id, content, parent_mode)
         VALUES ($1, $2, $3, 'bad', 'synthesis_mode')`, uuid.New(), treeID, uuid.New())
	require.Error(t, err, "chk_nodes_parent_mode must reject an unknown mode")

	// chk_multi_reference_has_display_parent requires a display anchor.
	_, err = pool.Exec(ctx,
		`INSERT INTO nodes (id, tree_id, author_id, content, parent_mode)
         VALUES ($1, $2, $3, 'anchorless', 'multi_reference')`, uuid.New(), treeID, uuid.New())
	require.Error(t, err, "chk_multi_reference_has_display_parent must require parent_id")

	// Both partial indexes from §3.1 exist.
	var indexes int
	require.NoError(t, pool.QueryRow(ctx, `
        SELECT COUNT(*)::int FROM pg_indexes
        WHERE indexname IN ('idx_edges_active_reference_target', 'idx_nodes_multi_reference')`).Scan(&indexes))
	assert.Equal(t, 2, indexes, "both partial indexes from SPEC-PL-06 §3.1 must exist")
}

// --- Scenario 2: CreateReferenceSet happy path -------------------------------

func TestCreateReferenceSet_TwoSources(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := refTestEdgeRepo(pool)

	treeID := refTestTree(t, pool)
	srcA := refTestNode(t, pool, treeID, nil, db.NodeTypeMessage, db.ParentModeLineage, "source A")
	srcB := refTestNode(t, pool, treeID, nil, db.NodeTypeMessage, db.ParentModeLineage, "source B")
	target := refTestNode(t, pool, treeID, &srcA, db.NodeTypeMessage, db.ParentModeMultiReference, "reply")

	tx := refTestTx(t, pool)
	edges, err := repo.CreateReferenceSet(ctx, tx, db.CreateReferenceSetInput{
		TreeID:         treeID,
		TargetID:       target,
		OrderedSources: []uuid.UUID{srcA, srcB},
	})
	require.NoError(t, err)
	require.Len(t, edges, 2, "exactly one reference edge per source")

	for i, want := range []uuid.UUID{srcA, srcB} {
		assert.Equal(t, db.EdgeTypeReference, edges[i].EdgeType)
		assert.Equal(t, want, edges[i].SourceID)
		assert.Equal(t, target, edges[i].TargetID)
		var meta db.ReferenceEdgeMetadata
		require.NoError(t, json.Unmarshal(edges[i].Metadata, &meta))
		assert.Equal(t, i, meta.ReferenceIndex)
		assert.Equal(t, i, meta.SelectionOrder)
		assert.Equal(t, db.ReferenceSourceLabel(i), meta.SourceLabel)
		assert.Equal(t, db.ReferenceEdgeRole, meta.Role)
		assert.Equal(t, db.ReferenceColorKey(treeID, target, want), meta.ColorKey)
		assert.Regexp(t, `^ref-[0-7]$`, meta.ColorKey)
	}
	// Sequence numbers preserve the canonical selection order.
	assert.Less(t, edges[0].SequenceNum, edges[1].SequenceNum)

	// GetActiveIncoming returns the set in the same deterministic order.
	incoming, err := repo.GetActiveIncoming(ctx, tx, target)
	require.NoError(t, err)
	require.Len(t, incoming, 2)
	assert.Equal(t, srcA, incoming[0].SourceID)
	assert.Equal(t, srcB, incoming[1].SourceID)

	// GetActiveReferenceParents reads through the pool (no tx parameter per
	// §12), so the set must be committed first.
	require.NoError(t, tx.Commit(ctx))

	// The §5.2 metadata is readable back through GetActiveReferenceParents,
	// ordered by selection_order.
	parents, err := repo.GetActiveReferenceParents(ctx, target)
	require.NoError(t, err)
	require.Len(t, parents, 2)
	assert.Equal(t, srcA, parents[0].SourceID)
	assert.Equal(t, "R1", parents[0].SourceLabel)
	assert.Equal(t, srcB, parents[1].SourceID)
	assert.Equal(t, "R2", parents[1].SourceLabel)
}

// --- Scenario 6: count errors ------------------------------------------------

func TestCreateReferenceSet_RejectsSourceCounts(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := refTestEdgeRepo(pool)

	treeID := refTestTree(t, pool)
	one := refTestNode(t, pool, treeID, nil, db.NodeTypeMessage, db.ParentModeLineage, "only source")
	target := refTestNode(t, pool, treeID, &one, db.NodeTypeMessage, db.ParentModeMultiReference, "reply")

	tx := refTestTx(t, pool)

	_, err := repo.CreateReferenceSet(ctx, tx, db.CreateReferenceSetInput{
		TreeID: treeID, TargetID: target, OrderedSources: []uuid.UUID{one},
	})
	assert.ErrorIs(t, err, db.ErrReferenceSourceCount, "one source is below the 2-20 band")

	twentyOne := make([]uuid.UUID, 0, 21)
	for i := 0; i < 21; i++ {
		twentyOne = append(twentyOne, refTestNode(t, pool, treeID, nil, db.NodeTypeMessage, db.ParentModeLineage, "src"))
	}
	_, err = repo.CreateReferenceSet(ctx, tx, db.CreateReferenceSetInput{
		TreeID: treeID, TargetID: target, OrderedSources: twentyOne,
	})
	assert.ErrorIs(t, err, db.ErrReferenceSourceCount, "21 sources is above the band")

	// Twenty sources are accepted (the §15.2 upper boundary).
	_, err = repo.CreateReferenceSet(ctx, tx, db.CreateReferenceSetInput{
		TreeID: treeID, TargetID: target, OrderedSources: twentyOne[:20],
	})
	assert.NoError(t, err, "20 sources must be accepted")
}

// --- Scenario 4 (repository half): duplicate source ids ----------------------

func TestCreateReferenceSet_RejectsDuplicateSource(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := refTestEdgeRepo(pool)

	treeID := refTestTree(t, pool)
	src := refTestNode(t, pool, treeID, nil, db.NodeTypeMessage, db.ParentModeLineage, "dup")
	target := refTestNode(t, pool, treeID, &src, db.NodeTypeMessage, db.ParentModeMultiReference, "reply")

	tx := refTestTx(t, pool)
	_, err := repo.CreateReferenceSet(ctx, tx, db.CreateReferenceSetInput{
		TreeID: treeID, TargetID: target, OrderedSources: []uuid.UUID{src, src},
	})
	assert.ErrorIs(t, err, db.ErrReferenceSourceDuplicate)
}

func TestCreateReferenceSet_RequiresTransaction(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	repo := refTestEdgeRepo(pool)
	_, err := repo.CreateReferenceSet(context.Background(), nil, db.CreateReferenceSetInput{
		TreeID: uuid.New(), TargetID: uuid.New(), OrderedSources: []uuid.UUID{uuid.New(), uuid.New()},
	})
	assert.Error(t, err, "a reference set must be written inside a caller transaction (§4.2)")
}

// --- Scenarios 11-12: target invariant validation ----------------------------

func TestValidateIncomingInvariant_MultiReference(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := refTestEdgeRepo(pool)

	treeID := refTestTree(t, pool)
	srcA := refTestNode(t, pool, treeID, nil, db.NodeTypeMessage, db.ParentModeLineage, "a")
	srcB := refTestNode(t, pool, treeID, nil, db.NodeTypeMessage, db.ParentModeLineage, "b")
	target := refTestNode(t, pool, treeID, &srcA, db.NodeTypeMessage, db.ParentModeMultiReference, "reply")

	tx := refTestTx(t, pool)
	_, err := repo.CreateReferenceSet(ctx, tx, db.CreateReferenceSetInput{
		TreeID: treeID, TargetID: target, OrderedSources: []uuid.UUID{srcA, srcB},
	})
	require.NoError(t, err)

	targetNode := &db.Node{ID: target, TreeID: treeID, ParentID: &srcA,
		ParentMode: db.ParentModeMultiReference, NodeType: db.NodeTypeMessage}
	assert.NoError(t, repo.ValidateIncomingInvariant(ctx, tx, targetNode),
		"a complete 2-source set with the display anchor in the set is valid")

	// Scenario 11: the display anchor must be one of the active sources.
	orphan := refTestNode(t, pool, treeID, nil, db.NodeTypeMessage, db.ParentModeLineage, "not a source")
	broken := &db.Node{ID: target, TreeID: treeID, ParentID: &orphan,
		ParentMode: db.ParentModeMultiReference, NodeType: db.NodeTypeMessage}
	err = repo.ValidateIncomingInvariant(ctx, tx, broken)
	assert.ErrorIs(t, err, db.ErrReferenceParentInvariant,
		"a display anchor outside the reference sources must fail validation")
}

func TestValidateIncomingInvariant_MultiReferenceCountAndType(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := refTestEdgeRepo(pool)

	treeID := refTestTree(t, pool)
	srcA := refTestNode(t, pool, treeID, nil, db.NodeTypeMessage, db.ParentModeLineage, "a")
	target := refTestNode(t, pool, treeID, &srcA, db.NodeTypeMessage, db.ParentModeMultiReference, "reply")

	// One reference edge is below the 2-20 band (§3.5 invariant 1).
	_, err := pool.Exec(ctx, `
        INSERT INTO edges (tree_id, source_id, target_id, edge_type, sequence_num)
        VALUES ($1, $2, $3, 'reference', 1)`, treeID, srcA, target)
	require.NoError(t, err)

	tx := refTestTx(t, pool)
	err = repo.ValidateIncomingInvariant(ctx, tx, &db.Node{ID: target, TreeID: treeID, ParentID: &srcA,
		ParentMode: db.ParentModeMultiReference, NodeType: db.NodeTypeMessage})
	assert.ErrorIs(t, err, db.ErrReferenceParentInvariant, "a single reference parent is invalid")

	// A reply edge mixed into a multi_reference target (§3.5 invariant 6).
	other := refTestNode(t, pool, treeID, nil, db.NodeTypeMessage, db.ParentModeLineage, "o")
	_, err = pool.Exec(ctx, `
        INSERT INTO edges (tree_id, source_id, target_id, edge_type, sequence_num)
        VALUES ($1, $2, $3, 'reply', 2)`, treeID, other, target)
	require.NoError(t, err)

	err = repo.ValidateIncomingInvariant(ctx, tx, &db.Node{ID: target, TreeID: treeID, ParentID: &srcA,
		ParentMode: db.ParentModeMultiReference, NodeType: db.NodeTypeMessage})
	assert.ErrorIs(t, err, db.ErrReferenceParentInvariant, "a reply edge into a multi_reference target is invalid")
}

// --- §12.1 decision table ----------------------------------------------------

func TestEdgeCreateDecisionTable(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := refTestEdgeRepo(pool)

	treeID := refTestTree(t, pool)
	src := refTestNode(t, pool, treeID, nil, db.NodeTypeMessage, db.ParentModeLineage, "src")
	lineage := refTestNode(t, pool, treeID, nil, db.NodeTypeMessage, db.ParentModeLineage, "lineage")
	multi := refTestNode(t, pool, treeID, &src, db.NodeTypeMessage, db.ParentModeMultiReference, "multi")
	synth := refTestNode(t, pool, treeID, nil, db.NodeTypeSynthesis, db.ParentModeLineage, "synthesis")
	system := refTestNode(t, pool, treeID, nil, db.NodeTypeSystem, db.ParentModeLineage, "system")

	// Row 1: message/lineage + reply with no existing parent → allow.
	first, err := repo.Create(ctx, refTestEdge(treeID, src, lineage, db.EdgeTypeReply))
	require.NoError(t, err)
	require.NotNil(t, first)

	// Row 2: message/lineage + any with an existing parent → ErrMultipleParents.
	_, err = repo.Create(ctx, refTestEdge(treeID, src, lineage, db.EdgeTypeFork))
	assert.ErrorIs(t, err, db.ErrMultipleParents)

	// Row 3: message/lineage + reference → ErrReferenceRequiresMultiReferenceMode
	// (§15.9: a lineage message rejects a standalone reference edge).
	_, err = repo.Create(ctx, refTestEdge(treeID, src, lineage, db.EdgeTypeReference))
	assert.ErrorIs(t, err, db.ErrReferenceRequiresMultiReferenceMode)

	// Row 4/5: message/multi_reference + a lone reference edge through the
	// generic path → rejected; only CreateReferenceSet may build the set.
	_, err = repo.Create(ctx, refTestEdge(treeID, src, multi, db.EdgeTypeReference))
	assert.ErrorIs(t, err, db.ErrReferenceParentInvariant)

	// Row 5: message/multi_reference + reply/fork/synthesis → rejected (§15.30).
	for _, edgeType := range []string{db.EdgeTypeReply, db.EdgeTypeFork, db.EdgeTypeSynthesis} {
		_, err = repo.Create(ctx, refTestEdge(treeID, src, multi, edgeType))
		assert.ErrorIsf(t, err, db.ErrReferenceParentInvariant,
			"%s into a multi_reference target must be rejected", edgeType)
	}

	// Row 7: synthesis + reference → ErrReferenceTargetType (§15.31).
	_, err = repo.Create(ctx, refTestEdge(treeID, src, synth, db.EdgeTypeReference))
	assert.ErrorIs(t, err, db.ErrReferenceTargetType)

	// Row 6: synthesis keeps its SPEC-API-04 multi-parent synthesis behavior
	// (§15.10).
	synthSrc2 := refTestNode(t, pool, treeID, nil, db.NodeTypeMessage, db.ParentModeLineage, "src2")
	_, err = repo.Create(ctx, refTestEdge(treeID, src, synth, db.EdgeTypeSynthesis))
	require.NoError(t, err)
	_, err = repo.Create(ctx, refTestEdge(treeID, synthSrc2, synth, db.EdgeTypeSynthesis))
	assert.NoError(t, err, "synthesis targets remain multi-parent")

	// Row 8: system + any user-created edge → ErrSystemNodeParentForbidden.
	_, err = repo.Create(ctx, refTestEdge(treeID, src, system, db.EdgeTypeReply))
	assert.ErrorIs(t, err, db.ErrSystemNodeParentForbidden)
}

// --- §15.9 / lineage invariant ----------------------------------------------

func TestValidateIncomingInvariant_LineageRejectsStandaloneReference(t *testing.T) {
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()
	repo := refTestEdgeRepo(pool)

	treeID := refTestTree(t, pool)
	src := refTestNode(t, pool, treeID, nil, db.NodeTypeMessage, db.ParentModeLineage, "src")
	target := refTestNode(t, pool, treeID, &src, db.NodeTypeMessage, db.ParentModeLineage, "plain")

	_, err := pool.Exec(ctx, `
        INSERT INTO edges (tree_id, source_id, target_id, edge_type, sequence_num)
        VALUES ($1, $2, $3, 'reference', 1)`, treeID, src, target)
	require.NoError(t, err)

	tx := refTestTx(t, pool)
	err = repo.ValidateIncomingInvariant(ctx, tx, &db.Node{ID: target, TreeID: treeID, ParentID: &src,
		ParentMode: db.ParentModeLineage, NodeType: db.NodeTypeMessage})
	require.Error(t, err)
	assert.True(t, errors.Is(err, db.ErrReferenceRequiresMultiReferenceMode) || errors.Is(err, db.ErrMultipleParents),
		"a reference edge into a lineage message must be rejected, got %v", err)
}
