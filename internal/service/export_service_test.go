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
