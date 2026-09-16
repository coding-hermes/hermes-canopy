// SPEC-PL-06 §7.1 / §5.2 — the graph subtree read must hand the frontend
// edges that carry their PERSISTED database identity (`edges.id`) and their
// decoded JSONB metadata, so convergence edges can be rendered with React
// Flow edge ids that mean something (§7.1) and coloured from the
// server-computed `color_key` (§5.2).
//
// DB-backed (shared integration pool) because the properties under test are
// about what the row actually holds after a real CreateReferenceSet — a repo
// stub could not tell a persisted id from a synthesised one.
//
// Run: CANOPY_TEST_ALLOW_SHARED_DB=1 go test -count=1 -run 'TestPL06P5' ./internal/service/
package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/testutil"
)

// --- helpers ----------------------------------------------------------------

func pl06P5Tree(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	treeID := uuid.New()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO trees (id, owner_id, title) VALUES ($1, $2, 'PL06-P5 Graph Tree')`,
		treeID, uuid.New())
	if err != nil {
		t.Fatalf("insert tree: %v", err)
	}
	return treeID
}

func pl06P5Node(t *testing.T, pool *pgxpool.Pool, treeID uuid.UUID, parentID *uuid.UUID, parentMode db.ParentMode, content string) uuid.UUID {
	t.Helper()
	nodeID := uuid.New()
	_, err := pool.Exec(context.Background(), `
        INSERT INTO nodes (id, tree_id, parent_id, parent_mode, author_id, content, node_type)
        VALUES ($1, $2, $3, $4, $5, $6, 'message')`,
		nodeID, treeID, parentID, string(parentMode), uuid.New(), content)
	if err != nil {
		t.Fatalf("insert node %q: %v", content, err)
	}
	return nodeID
}

// pl06P5LineageEdge inserts the display-parent edge the way the node service
// does (explicit sequence_num — edges.sequence_num is NOT NULL and has no
// default). GetSubtree walks EDGES, so a node reached only through parent_id
// is not part of the subtree.
func pl06P5LineageEdge(t *testing.T, pool *pgxpool.Pool, treeID, sourceID, targetID uuid.UUID, seq int64) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
        INSERT INTO edges (tree_id, source_id, target_id, edge_type, sequence_num, metadata)
        VALUES ($1, $2, $3, $4, $5, '{}'::jsonb)`,
		treeID, sourceID, targetID, db.EdgeTypeReply, seq)
	if err != nil {
		t.Fatalf("insert lineage edge %s → %s: %v", sourceID, targetID, err)
	}
}

func pl06P5EdgeByID(edges []GraphEdgeSummary, id uuid.UUID) (GraphEdgeSummary, bool) {
	for _, e := range edges {
		if e.ID == id {
			return e, true
		}
	}
	return GraphEdgeSummary{}, false
}

// TestPL06P5_GetSubtree_ReferenceEdgesCarryPersistedIDAndMetadata is the A3
// acceptance: a persisted `reference` edge comes back with its REAL
// `edges.id` and its §5.2 metadata intact, including the source the target
// does NOT use as its display anchor (proving GetSubtree traverses reference
// edges, not just the parent_id chain).
func TestPL06P5_GetSubtree_ReferenceEdgesCarryPersistedIDAndMetadata(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()

	treeID := pl06P5Tree(t, pool)
	root := pl06P5Node(t, pool, treeID, nil, db.ParentModeLineage, "Root")
	sourceA := pl06P5Node(t, pool, treeID, &root, db.ParentModeLineage, "Message A")
	sourceB := pl06P5Node(t, pool, treeID, &root, db.ParentModeLineage, "Message B")
	sourceC := pl06P5Node(t, pool, treeID, &root, db.ParentModeLineage, "Message C")

	// Display anchor is the primary source (§3.5 invariant 3); the other
	// reference edges exist only in the edges table.
	reply := pl06P5Node(t, pool, treeID, &sourceA, db.ParentModeMultiReference, "Agent reply")

	// Lineage display edges root → A/B/C (what the node service writes).
	pl06P5LineageEdge(t, pool, treeID, root, sourceA, 1)
	pl06P5LineageEdge(t, pool, treeID, root, sourceB, 2)
	pl06P5LineageEdge(t, pool, treeID, root, sourceC, 3)

	edgeRepo := db.NewPGEdgeRepo(pool)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	wantSources := []uuid.UUID{sourceA, sourceB, sourceC}
	persisted, err := edgeRepo.CreateReferenceSet(ctx, tx, db.CreateReferenceSetInput{
		TreeID:         treeID,
		TargetID:       reply,
		OrderedSources: wantSources,
	})
	if err != nil {
		t.Fatalf("CreateReferenceSet: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit reference set: %v", err)
	}
	if len(persisted) != len(wantSources) {
		t.Fatalf("persisted %d reference edges, want %d", len(persisted), len(wantSources))
	}

	svc := NewGraphServiceImpl(db.NewPGNodeRepo(pool), edgeRepo)
	result, err := svc.GetSubtree(ctx, root, 0)
	if err != nil {
		t.Fatalf("GetSubtree: %v", err)
	}

	// The whole tree is in scope, including the two sources that are not
	// the reply's display anchor.
	for _, want := range []uuid.UUID{sourceA, sourceB, sourceC, reply} {
		found := false
		for _, n := range result.Nodes {
			if n.ID == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("subtree nodes missing %s", want)
		}
	}

	for i, row := range persisted {
		summary, ok := pl06P5EdgeByID(result.Edges, row.ID)
		if !ok {
			t.Fatalf("reference edge %s (index %d) missing from subtree edges", row.ID, i)
		}
		if summary.SourceID != wantSources[i] {
			t.Errorf("edge %s source_id = %s, want %s", row.ID, summary.SourceID, wantSources[i])
		}
		if summary.TargetID != reply {
			t.Errorf("edge %s target_id = %s, want %s", row.ID, summary.TargetID, reply)
		}
		if summary.EdgeType != db.EdgeTypeReference {
			t.Errorf("edge %s edge_type = %q, want %q", row.ID, summary.EdgeType, db.EdgeTypeReference)
		}

		// §5.2: the metadata object must survive the read intact.
		if summary.Metadata == nil {
			t.Fatalf("edge %s metadata is nil, want the persisted §5.2 object", row.ID)
		}
		wantColorKey := db.ReferenceColorKey(treeID, reply, wantSources[i])
		if got := summary.Metadata["reference_index"]; got != float64(i) {
			t.Errorf("edge %s reference_index = %#v, want %d", row.ID, got, i)
		}
		if got := summary.Metadata["selection_order"]; got != float64(i) {
			t.Errorf("edge %s selection_order = %#v, want %d", row.ID, got, i)
		}
		if got := summary.Metadata["source_label"]; got != db.ReferenceSourceLabel(i) {
			t.Errorf("edge %s source_label = %#v, want %q", row.ID, got, db.ReferenceSourceLabel(i))
		}
		if got := summary.Metadata["color_key"]; got != wantColorKey {
			t.Errorf("edge %s color_key = %#v, want %q", row.ID, got, wantColorKey)
		}
		if got := summary.Metadata["role"]; got != db.ReferenceEdgeRole {
			t.Errorf("edge %s role = %#v, want %q", row.ID, got, db.ReferenceEdgeRole)
		}
	}
}

// TestPL06P5_GetSubtree_PlainReplyEdgeUnaffected pins that the additive
// fields did not change a lineage reply edge: same identity, same endpoints,
// and a metadata column holding the schema's `{}` default still round-trips
// as an OMITTED key (never "metadata": null) so a client cannot mistake
// "no metadata" for a failed decode.
func TestPL06P5_GetSubtree_PlainReplyEdgeUnaffected(t *testing.T) {
	testutil.SkipIfNoDB(t)
	pool := testutil.NewSharedIntegrationPool(t)
	ctx := context.Background()

	treeID := pl06P5Tree(t, pool)
	root := pl06P5Node(t, pool, treeID, nil, db.ParentModeLineage, "Root")
	child := pl06P5Node(t, pool, treeID, &root, db.ParentModeLineage, "Child")

	edgeRepo := db.NewPGEdgeRepo(pool)
	// Metadata nil → the schema's `{}` default (db/edge_repo.go COALESCE).
	// sequence_num is NOT NULL with no default, so the caller supplies it
	// exactly as the node service does.
	created, err := edgeRepo.Create(ctx, &db.Edge{
		TreeID:      treeID,
		SourceID:    root,
		TargetID:    child,
		EdgeType:    db.EdgeTypeReply,
		SequenceNum: 1,
	})
	if err != nil {
		t.Fatalf("Create reply edge: %v", err)
	}

	svc := NewGraphServiceImpl(db.NewPGNodeRepo(pool), edgeRepo)
	result, err := svc.GetSubtree(ctx, root, 0)
	if err != nil {
		t.Fatalf("GetSubtree: %v", err)
	}

	summary, ok := pl06P5EdgeByID(result.Edges, created.ID)
	if !ok {
		t.Fatalf("reply edge %s missing from subtree edges", created.ID)
	}
	if summary.SourceID != root || summary.TargetID != child {
		t.Errorf("reply edge endpoints = %s → %s, want %s → %s",
			summary.SourceID, summary.TargetID, root, child)
	}
	if summary.EdgeType != db.EdgeTypeReply {
		t.Errorf("reply edge edge_type = %q, want %q", summary.EdgeType, db.EdgeTypeReply)
	}
	if summary.Metadata != nil {
		t.Errorf("reply edge metadata = %#v, want nil for an empty column", summary.Metadata)
	}

	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal subtree: %v", err)
	}
	if strings.Contains(string(raw), `"metadata":null`) {
		t.Errorf("subtree JSON emitted a null metadata key: %s", raw)
	}
	if !strings.Contains(string(raw), `"id":"`+created.ID.String()+`"`) {
		t.Errorf("subtree JSON is missing the persisted edge id %s", created.ID)
	}
}

// TestPL06P5_DecodeEdgeMetadata_EmptySpellings covers the column shapes the
// decoder must treat identically (no DB): NULL/absent bytes, the JSON `null`
// literal, and the schema's `{}` default. A malformed value must ERROR rather
// than publish an edge whose renderer metadata was silently dropped.
func TestPL06P5_DecodeEdgeMetadata_EmptySpellings(t *testing.T) {
	edgeID := uuid.New()

	cases := []struct {
		name    string
		raw     []byte
		wantNil bool
		wantErr bool
	}{
		{name: "nil column", raw: nil, wantNil: true},
		{name: "empty bytes", raw: []byte{}, wantNil: true},
		{name: "whitespace", raw: []byte("  \n"), wantNil: true},
		{name: "json null literal", raw: []byte("null"), wantNil: true},
		{name: "empty object", raw: []byte("{}"), wantNil: true},
		{
			name:    "reference metadata survives",
			raw:     []byte(`{"reference_index":2,"source_label":"R3","color_key":"ref-4","selection_order":2,"role":"context_source"}`),
			wantNil: false,
		},
		{name: "malformed jsonb", raw: []byte(`{"broken"`), wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta, err := decodeEdgeMetadata(db.Edge{ID: edgeID, Metadata: tc.raw})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("decodeEdgeMetadata(%q) error = nil, want a decode error", tc.raw)
				}
				if !strings.Contains(err.Error(), edgeID.String()) {
					t.Errorf("error %q does not name the corrupt edge %s", err, edgeID)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeEdgeMetadata(%q) error = %v", tc.raw, err)
			}
			if tc.wantNil {
				if meta != nil {
					t.Fatalf("decodeEdgeMetadata(%q) = %#v, want nil", tc.raw, meta)
				}
				return
			}
			if meta == nil {
				t.Fatalf("decodeEdgeMetadata(%q) = nil, want the decoded object", tc.raw)
			}
			if meta["source_label"] != "R3" || meta["color_key"] != "ref-4" {
				t.Errorf("decoded metadata = %#v, want the §5.2 values", meta)
			}
		})
	}
}
