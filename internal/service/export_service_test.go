package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

func TestImportTreeWithEdgesPersistsEdgeSequence(t *testing.T) {
	pool := testutil.NewIntegrationPool(t)
	ctx := context.Background()
	ownerID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (id, hermes_user_id, display_name) VALUES ($1, $2, $3)`,
		ownerID, "export-import-"+ownerID.String(), "Export Import Test",
	); err != nil {
		t.Fatalf("insert owner: %v", err)
	}

	rootID, childOneID, childTwoID := uuid.New(), uuid.New(), uuid.New()
	input := &ExportData{
		Tree: ExportTree{
			Title:      "Tree with ordered edges",
			RootNodeID: rootID,
		},
		Nodes: []db.Node{
			{ID: rootID, AuthorID: ownerID, Content: "root", ContentFormat: db.ContentFormatPlain, NodeType: db.NodeTypeMessage},
			{ID: childOneID, AuthorID: ownerID, Content: "first child", ContentFormat: db.ContentFormatPlain, NodeType: db.NodeTypeMessage, ParentID: &rootID},
			{ID: childTwoID, AuthorID: ownerID, Content: "second child", ContentFormat: db.ContentFormatPlain, NodeType: db.NodeTypeMessage, ParentID: &rootID},
		},
		Edges: []db.Edge{
			{SourceID: rootID, TargetID: childOneID, EdgeType: db.EdgeTypeReply, SequenceNum: 3},
			{SourceID: rootID, TargetID: childTwoID, EdgeType: db.EdgeTypeReply, SequenceNum: 7},
		},
	}

	service := NewExportService(
		db.NewPGTreeRepo(pool),
		db.NewPGNodeRepo(pool),
		db.NewPGEdgeRepo(pool),
		pool,
	)
	result, err := service.ImportTree(ctx, input, ownerID)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 2, result.EdgeCount)

	rows, err := pool.Query(ctx,
		`SELECT sequence_num FROM edges WHERE tree_id = $1 ORDER BY sequence_num`, result.TreeID)
	require.NoError(t, err)
	defer rows.Close()
	var edgeSequences []int64
	for rows.Next() {
		var sequence int64
		require.NoError(t, rows.Scan(&sequence))
		edgeSequences = append(edgeSequences, sequence)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []int64{3, 7}, edgeSequences)

	var importedNodeCount int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM nodes WHERE tree_id = $1 AND sequence_num IS NOT NULL`, result.TreeID,
	).Scan(&importedNodeCount))
	require.Equal(t, len(input.Nodes), importedNodeCount)
}

// TestExportImportRoundTripWithEdges proves the documented backup/restore
// story for a REAL edged tree: export a tree built through the live PG repos
// (2 nodes joined by a reply edge), import the payload, and require the node
// and edge counts to match with edge sequence_num preserved. Regression for
// DF-HERMES-CANOPY-51: the import edge INSERT used to omit edges.sequence_num
// (NOT NULL), so every edged import died 23502 wrapped as a 503
// "database unavailable".
func TestExportImportRoundTripWithEdges(t *testing.T) {
	pool := testutil.NewIntegrationPool(t)
	ctx := context.Background()

	// trees.owner_id references profiles(id); profiles.owner_id references
	// users(id) — seed both.
	userID := uuid.New()
	_, err := pool.Exec(ctx,
		`INSERT INTO users (id, hermes_user_id, display_name) VALUES ($1, $2, 'Roundtrip Owner')`,
		userID, "roundtrip-"+userID.String())
	require.NoError(t, err, "insert owner user")
	ownerID := uuid.New()
	_, err = pool.Exec(ctx,
		`INSERT INTO profiles (id, owner_id, name, display_name) VALUES ($1, $2, 'Roundtrip Owner', 'Roundtrip Owner')`,
		ownerID, userID)
	require.NoError(t, err, "insert owner profile")

	svc := NewExportService(
		db.NewPGTreeRepo(pool),
		db.NewPGNodeRepo(pool),
		db.NewPGEdgeRepo(pool),
		pool,
	)

	// Build the source tree through the live tree service so defaults
	// (sequence_num trigger, FKs) are exactly production-shaped.
	treeSvc := NewTreeService(db.NewPGTreeRepo(pool), db.NewPGNodeRepo(pool), db.NewPGEdgeRepo(pool), pool)
	created, err := treeSvc.CreateTree(ctx, CreateTreeParams{
		OwnerID:       userID,
		Title:         "DF-51 roundtrip tree",
		RootContent:   "root",
		ContentFormat: FormatPlain,
		NodeType:      NodeTypeMessage,
	})
	require.NoError(t, err, "create tree")
	treeID := created.ID
	rootNodeID := created.RootNodeID

	var replyTargetID, replyID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO nodes (tree_id, parent_id, author_id, content, content_format, node_type)
		 VALUES ($1, $2, $3, 'reply', 'plain', 'message') RETURNING id`,
		treeID, rootNodeID, ownerID).Scan(&replyTargetID))
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO edges (tree_id, source_id, target_id, edge_type, sequence_num)
		 VALUES ($1, $2, $3, 'reply', 4) RETURNING id`,
		treeID, rootNodeID, replyTargetID).Scan(&replyID))

	// Export the real tree, then import the payload as a fresh copy.
	exported, err := svc.ExportTree(ctx, treeID)
	require.NoError(t, err, "export")
	require.NotEmpty(t, exported.Edges, "export must carry edges")

	imported, err := svc.ImportTree(ctx, exported, userID)
	require.NoError(t, err, "import of an edged export must not fail")
	require.NotNil(t, imported)
	require.Equal(t, len(exported.Nodes), imported.NodeCount, "node count must match")
	require.Equal(t, len(exported.Edges), imported.EdgeCount, "edge count must match")

	// Every imported edge keeps a non-null sequence_num.
	var nullSequences int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM edges WHERE tree_id = $1 AND sequence_num IS NULL`, imported.TreeID,
	).Scan(&nullSequences))
	require.Equal(t, 0, nullSequences, "imported edges must carry sequence_num")

	// The reply edge survives with its ordering intact.
	var edgeCount int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM edges WHERE tree_id = $1 AND edge_type = 'reply'`, imported.TreeID,
	).Scan(&edgeCount))
	require.Equal(t, 1, edgeCount, "reply edge must survive the round trip")
}
