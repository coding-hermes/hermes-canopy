package service

import (
	"context"
	"encoding/json"
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
		`INSERT INTO nodes (tree_id, parent_id, author_id, content, content_format, node_type, metadata)
		 VALUES ($1, $2, $3, 'reply', 'plain', 'message', '{"reviewed":true,"score":4}'::jsonb) RETURNING id`,
		treeID, rootNodeID, ownerID).Scan(&replyTargetID))
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO edges (tree_id, source_id, target_id, edge_type, sequence_num)
		 VALUES ($1, $2, $3, 'reply', 4) RETURNING id`,
		treeID, rootNodeID, replyTargetID).Scan(&replyID))

	require.NoError(t, pool.QueryRow(ctx,
		`UPDATE nodes SET metadata = '{"pinned":true,"labels":["root"]}'::jsonb WHERE id = $1 RETURNING id`,
		rootNodeID).Scan(new(uuid.UUID)))

	// Export the real tree, encode it on the public wire, then decode the
	// payload before importing so both native metadata and import compatibility
	// are exercised at the actual export/import boundary.
	exported, err := svc.ExportTree(ctx, treeID)
	require.NoError(t, err, "export")
	require.NotEmpty(t, exported.Edges, "export must carry edges")

	wire, err := json.Marshal(exported)
	require.NoError(t, err, "marshal export")
	var wireEnvelope struct {
		Nodes []struct {
			Content  string          `json:"content"`
			Metadata json.RawMessage `json:"metadata"`
		} `json:"nodes"`
	}
	require.NoError(t, json.Unmarshal(wire, &wireEnvelope), "inspect export wire")
	require.Len(t, wireEnvelope.Nodes, 2)
	for _, node := range wireEnvelope.Nodes {
		var metadata map[string]any
		require.NoError(t, json.Unmarshal(node.Metadata, &metadata),
			"exported %q metadata must be native JSON", node.Content)
	}

	var decoded ExportData
	require.NoError(t, json.Unmarshal(wire, &decoded), "unmarshal export")
	imported, err := svc.ImportTree(ctx, &decoded, userID)
	require.NoError(t, err, "import of an edged export must not fail")
	require.NotNil(t, imported)
	require.Equal(t, len(exported.Nodes), imported.NodeCount, "node count must match")
	require.Equal(t, len(exported.Edges), imported.EdgeCount, "edge count must match")

	var importedRootMetadata []byte
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT metadata FROM nodes WHERE tree_id = $1 AND content = 'root'`, imported.TreeID,
	).Scan(&importedRootMetadata))
	require.JSONEq(t, `{"pinned":true,"labels":["root"]}`, string(importedRootMetadata),
		"root metadata must survive export -> wire decode -> import")
	var importedReplyMetadata []byte
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT metadata FROM nodes WHERE tree_id = $1 AND content = 'reply'`, imported.TreeID,
	).Scan(&importedReplyMetadata))
	require.JSONEq(t, `{"reviewed":true,"score":4}`, string(importedReplyMetadata),
		"reply metadata must survive export -> wire decode -> import")

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

// DF-HERMES-CANOPY-54a: a version-2 export carries topics + resolved refs,
// and importing that payload reproduces them under NEW ids (topic parent
// order-independent, resolved_by falls back to the importer's profile).
func TestExportImportRoundTripTopicsAndResolvedRefs(t *testing.T) {
	pool := testutil.NewIntegrationPool(t)
	ctx := context.Background()

	userID := uuid.New()
	_, err := pool.Exec(ctx,
		`INSERT INTO users (id, hermes_user_id, display_name) VALUES ($1, $2, 'Topics Export Owner')`,
		userID, "topics-export-"+userID.String())
	require.NoError(t, err, "insert owner user")
	ownerID := uuid.New()
	_, err = pool.Exec(ctx,
		`INSERT INTO profiles (id, owner_id, name, display_name) VALUES ($1, $2, 'Topics Export Owner', 'Topics Export Owner')`,
		ownerID, userID)
	require.NoError(t, err, "insert owner profile")

	topicRepo := db.NewPGTopicRepo(pool)
	refRepo := db.NewPGReferenceRepo(pool)
	svc := NewExportService(
		db.NewPGTreeRepo(pool),
		db.NewPGNodeRepo(pool),
		db.NewPGEdgeRepo(pool),
		pool,
	).WithTopicReferences(topicRepo, refRepo)

	treeSvc := NewTreeService(db.NewPGTreeRepo(pool), db.NewPGNodeRepo(pool), db.NewPGEdgeRepo(pool), pool)
	created, err := treeSvc.CreateTree(ctx, CreateTreeParams{
		OwnerID:       userID,
		Title:         "DF-54 topics roundtrip tree",
		RootContent:   "root",
		ContentFormat: FormatPlain,
		NodeType:      NodeTypeMessage,
	})
	require.NoError(t, err, "create tree")
	treeID := created.ID
	rootNodeID := created.RootNodeID

	// Two topics on the tree: one parent, one archived child, so the
	// parent_topic_id second pass and the archived-status inclusion are
	// both exercised.
	var parentTopicID, childTopicID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO topics (tree_id, root_node_id, title, description, slug, status, topic_tags, node_count)
		 VALUES ($1, $2, 'Parent Topic', 'parent', 'parent-topic', 'active', ARRAY['alpha'], 3) RETURNING id`,
		treeID, rootNodeID).Scan(&parentTopicID))
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO topics (tree_id, root_node_id, title, description, slug, parent_topic_id, status, node_count, archived_at)
		 VALUES ($1, $2, 'Child Topic', '', 'child-topic', $3, 'archived', 2, now()) RETURNING id`,
		treeID, rootNodeID, parentTopicID).Scan(&childTopicID))

	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO node_resolved_refs (node_id, tree_id, topic_id, raw_ref, slug, resolved_by, context_hash)
		 VALUES ($1, $2, $3, '#parent-topic', 'parent-topic', $4, 'h1') RETURNING id`,
		rootNodeID, treeID, parentTopicID, ownerID).Scan(new(uuid.UUID)))

	exported, err := svc.ExportTree(ctx, treeID)
	require.NoError(t, err, "export")
	require.Len(t, exported.Topics, 2, "both active and archived topics exported")
	require.Len(t, exported.ResolvedRefs, 1, "resolved ref exported")
	require.Equal(t, 2, exported.Version)
	parentExported := false
	childExported := false
	for _, tp := range exported.Topics {
		if tp.ID == parentTopicID {
			parentExported = true
		}
		if tp.ID == childTopicID {
			childExported = true
		}
	}
	require.True(t, parentExported, "parent topic exported")
	require.True(t, childExported, "archived child topic exported")

	wire, err := json.Marshal(exported)
	require.NoError(t, err, "marshal wire payload")
	var decoded ExportData
	require.NoError(t, json.Unmarshal(wire, &decoded), "decode wire payload")

	result, err := svc.ImportTree(ctx, &decoded, userID)
	require.NoError(t, err, "import")
	require.Equal(t, 2, result.TopicCount)
	require.Equal(t, 1, result.ResolvedRefCount)

	// The imported tree carries BOTH topics with a remapped parent edge and
	// the resolved reference pointing at the remapped topic + node.
	var gotTopics int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM topics WHERE tree_id = $1`, result.TreeID).Scan(&gotTopics))
	require.Equal(t, 2, gotTopics)
	var parentRemapped, childParent uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT id FROM topics WHERE tree_id = $1 AND slug = 'parent-topic'`, result.TreeID).Scan(&parentRemapped))
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT parent_topic_id FROM topics WHERE tree_id = $1 AND slug = 'child-topic'`, result.TreeID).Scan(&childParent))
	require.Equal(t, parentRemapped, childParent, "child topic parent remapped to the imported parent id")
	var refCount, refByProfile int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT COUNT(*), COUNT(*) FILTER (WHERE resolved_by = $2) FROM node_resolved_refs WHERE tree_id = $1`,
		result.TreeID, ownerID).Scan(&refCount, &refByProfile))
	require.Equal(t, 1, refCount)
	require.Equal(t, 1, refByProfile, "resolved_by remapped to the importer's profile")

	// A second round trip is id-shaped, not name-shaped: importing again
	// must not collide on the (tree_id, slug) unique index.
	result2, err := svc.ImportTree(ctx, &decoded, userID)
	require.NoError(t, err, "second import")
	require.Equal(t, 2, result2.TopicCount)
}
