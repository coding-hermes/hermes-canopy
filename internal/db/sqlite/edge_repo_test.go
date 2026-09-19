package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

func TestSQLiteEdgeRepoCRUD(t *testing.T) {
	s, trees, nodes, edges := newGraphRepos(t)
	ctx := context.Background()
	tree := mustTree(t, ctx, trees, "edges")
	root := mustNode(t, ctx, nodes, tree.ID, nil, "root")
	child := mustNode(t, ctx, nodes, tree.ID, nil, "child")
	other := mustNode(t, ctx, nodes, tree.ID, nil, "other")

	created, err := edges.Create(ctx, &db.Edge{
		TreeID:   tree.ID,
		SourceID: root.ID,
		TargetID: child.ID,
		EdgeType: db.EdgeTypeReply,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == uuid.Nil {
		t.Error("Create returned a nil id")
	}
	if created.SequenceNum != 1 {
		t.Errorf("first edge sequence_num = %d, want 1", created.SequenceNum)
	}
	if created.EdgeType != db.EdgeTypeReply || created.DeletedAt != nil {
		t.Errorf("edge_type/deleted_at = %q/%v, want reply/nil", created.EdgeType, created.DeletedAt)
	}
	if created.CreatedAt.IsZero() {
		t.Error("created_at is zero")
	}
	if string(created.Metadata) != "{}" {
		t.Errorf("default metadata = %q, want {}", created.Metadata)
	}
	// Same JSON-storage rule as the nodes table: TEXT, not blob (package comment, point 5).
	if typ := storedText(t, ctx, s, `SELECT typeof(metadata) FROM edges WHERE id = ?`, created.ID.String()); typ != "text" {
		t.Errorf("typeof(edges.metadata) = %q, want \"text\"", typ)
	}

	got, err := edges.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ID != created.ID || got.SourceID != root.ID || got.TargetID != child.ID || got.TreeID != tree.ID {
		t.Errorf("GetByID = %+v, want the created edge", got)
	}
	for _, tc := range []struct {
		name string
		got  func() ([]db.Edge, error)
	}{
		{"GetBySource", func() ([]db.Edge, error) { return edges.GetBySource(ctx, root.ID) }},
		{"GetByTarget", func() ([]db.Edge, error) { return edges.GetByTarget(ctx, child.ID) }},
		{"GetByTree", func() ([]db.Edge, error) { return edges.GetByTree(ctx, tree.ID) }},
	} {
		list, err := tc.got()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(list) != 1 || list[0].ID != created.ID {
			t.Errorf("%s = %d edges, want the created edge", tc.name, len(list))
		}
	}

	// The sequence counter is tree-wide, continuing the convention PGEdgeRepo documents for
	// the service's reply/fork writes; an explicit non-zero value is honored.
	second := mustEdgeNamed(t, ctx, edges, tree.ID, root.ID, other.ID, db.EdgeTypeFork)
	if second.SequenceNum != 2 {
		t.Errorf("second edge sequence_num = %d, want 2", second.SequenceNum)
	}
	third := mustNode(t, ctx, nodes, tree.ID, nil, "third")
	explicit, err := edges.Create(ctx, &db.Edge{
		TreeID: tree.ID, SourceID: child.ID, TargetID: third.ID, SequenceNum: 9,
	})
	if err != nil {
		t.Fatalf("Create with an explicit sequence: %v", err)
	}
	if explicit.SequenceNum != 9 {
		t.Errorf("explicit sequence_num = %d, want 9", explicit.SequenceNum)
	}
	if explicit.EdgeType != db.EdgeTypeReply {
		t.Errorf("empty edge_type stored as %q, want the 'reply' default", explicit.EdgeType)
	}

	if err := edges.SoftDelete(ctx, created.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if _, err := edges.GetByID(ctx, created.ID); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("GetByID(soft-deleted) error = %v, want db.ErrNotFound", err)
	}
	if err := edges.SoftDelete(ctx, created.ID); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("second SoftDelete error = %v, want db.ErrNotFound", err)
	}
	if list, err := edges.GetBySource(ctx, root.ID); err != nil || len(list) != 1 || list[0].ID != second.ID {
		t.Errorf("GetBySource after SoftDelete = %d edges (err %v), want only the fork", len(list), err)
	}
	if n := countRows(t, ctx, s, `SELECT COUNT(*) FROM edges WHERE id = ?`, created.ID.String()); n != 1 {
		t.Errorf("soft-deleted edge row count = %d, want the row to survive", n)
	}

	// Error semantics.
	if _, err := edges.Create(ctx, nil); err == nil {
		t.Error("Create(nil) succeeded, want an error")
	}
	if _, err := edges.GetByID(ctx, uuid.New()); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("GetByID(unknown id) error = %v, want db.ErrNotFound", err)
	}
	if list, err := edges.GetByTree(ctx, uuid.New()); err != nil || len(list) != 0 {
		t.Errorf("GetByTree(unknown tree) = %d edges (err %v), want 0", len(list), err)
	}
}

func TestSQLiteEdgeRepoCreateRejectsInvalidParentSets(t *testing.T) {
	s, trees, nodes, edges := newGraphRepos(t)
	ctx := context.Background()
	tree := mustTree(t, ctx, trees, "rules")
	a := mustNode(t, ctx, nodes, tree.ID, nil, "a")
	b := mustNode(t, ctx, nodes, tree.ID, nil, "b")
	c := mustNode(t, ctx, nodes, tree.ID, nil, "c")
	sys, err := nodes.Create(ctx, &db.Node{TreeID: tree.ID, AuthorID: uuid.New(), Content: "sys", NodeType: db.NodeTypeSystem})
	if err != nil {
		t.Fatalf("Create system node: %v", err)
	}
	syn, err := nodes.Create(ctx, &db.Node{TreeID: tree.ID, AuthorID: uuid.New(), Content: "syn", NodeType: db.NodeTypeSynthesis})
	if err != nil {
		t.Fatalf("Create synthesis node: %v", err)
	}
	dead := mustNode(t, ctx, nodes, tree.ID, nil, "dead")
	if err := nodes.SoftDelete(ctx, dead.ID); err != nil {
		t.Fatalf("SoftDelete(dead): %v", err)
	}

	// The two edges that MUST succeed: a plain reply, and a second parent on a synthesis
	// target (SPEC-API-04 keeps multi-parent synthesis).
	mustEdge(t, ctx, edges, tree.ID, a.ID, c.ID)
	mustEdge(t, ctx, edges, tree.ID, a.ID, syn.ID)
	mustEdge(t, ctx, edges, tree.ID, b.ID, syn.ID)

	random := uuid.New()
	cases := []struct {
		name string
		edge *db.Edge
		want error
	}{
		{
			name: "self edge",
			edge: &db.Edge{TreeID: tree.ID, SourceID: a.ID, TargetID: a.ID, EdgeType: db.EdgeTypeReply},
			want: db.ErrSelfEdge,
		},
		{
			name: "unknown target",
			edge: &db.Edge{TreeID: tree.ID, SourceID: a.ID, TargetID: random, EdgeType: db.EdgeTypeReply},
			want: db.ErrNotFound,
		},
		{
			name: "soft-deleted target",
			edge: &db.Edge{TreeID: tree.ID, SourceID: a.ID, TargetID: dead.ID, EdgeType: db.EdgeTypeReply},
			want: db.ErrNotFound,
		},
		{
			name: "system target",
			edge: &db.Edge{TreeID: tree.ID, SourceID: a.ID, TargetID: sys.ID, EdgeType: db.EdgeTypeReply},
			want: db.ErrSystemNodeParentForbidden,
		},
		{
			name: "reference edge on a synthesis target",
			edge: &db.Edge{TreeID: tree.ID, SourceID: b.ID, TargetID: syn.ID, EdgeType: db.EdgeTypeReference},
			want: db.ErrReferenceTargetType,
		},
		{
			name: "reference edge on a message target",
			edge: &db.Edge{TreeID: tree.ID, SourceID: a.ID, TargetID: b.ID, EdgeType: db.EdgeTypeReference},
			want: db.ErrReferenceRequiresMultiReferenceMode,
		},
		{
			name: "second parent on a message target",
			edge: &db.Edge{TreeID: tree.ID, SourceID: b.ID, TargetID: c.ID, EdgeType: db.EdgeTypeReply},
			want: db.ErrMultipleParents,
		},
	}
	for _, tc := range cases {
		out, err := edges.Create(ctx, tc.edge)
		if out != nil {
			t.Errorf("%s: Create returned an edge (%s) alongside the error", tc.name, out.ID)
		}
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v, want %v", tc.name, err, tc.want)
		}
	}

	// An unknown SOURCE passes the target rules and is caught by the FK on edges.source_id.
	_, err = edges.Create(ctx, &db.Edge{TreeID: tree.ID, SourceID: random, TargetID: b.ID, EdgeType: db.EdgeTypeReply})
	if err == nil {
		t.Fatal("Create with an unknown source_id succeeded; foreign keys are not enforced")
	}
	if !isForeignKeyError(err) {
		t.Errorf("unknown source error = %v, want a foreign-key failure", err)
	}

	// Every rejection was rolled back: only the three valid edges exist, and the sequence
	// counter reflects them alone.
	if n := countRows(t, ctx, s, `SELECT COUNT(*) FROM edges WHERE tree_id = ?`, tree.ID.String()); n != 3 {
		t.Errorf("edge count after rejections = %d, want 3", n)
	}
}

func TestSQLiteEdgeRepoFailedInsertLeavesNoResidue(t *testing.T) {
	s, trees, nodes, edges := newGraphRepos(t)
	ctx := context.Background()
	tree := mustTree(t, ctx, trees, "atomic")
	a := mustNode(t, ctx, nodes, tree.ID, nil, "a")
	b := mustNode(t, ctx, nodes, tree.ID, nil, "b")
	syn, err := nodes.Create(ctx, &db.Node{TreeID: tree.ID, AuthorID: uuid.New(), Content: "syn", NodeType: db.NodeTypeSynthesis})
	if err != nil {
		t.Fatalf("Create synthesis node: %v", err)
	}

	first := mustEdge(t, ctx, edges, tree.ID, a.ID, syn.ID)
	if first.SequenceNum != 1 {
		t.Fatalf("first edge sequence_num = %d, want 1", first.SequenceNum)
	}

	// The same (source, target, type) twice: a synthesis target passes the parent rules, so
	// this reaches the schema's UNIQUE constraint — the one failure path that has already
	// allocated a sequence number inside the transaction.
	_, err = edges.Create(ctx, &db.Edge{TreeID: tree.ID, SourceID: a.ID, TargetID: syn.ID, EdgeType: db.EdgeTypeReply})
	if err == nil {
		t.Fatal("duplicate (source, target, edge_type) succeeded, want chk_unique_edge to reject it")
	}
	if n := countRows(t, ctx, s, `SELECT COUNT(*) FROM edges WHERE tree_id = ?`, tree.ID.String()); n != 1 {
		t.Errorf("edge count after the rejected insert = %d, want 1", n)
	}
	// The rollback must also release the allocated sequence: the next edge continues from 1.
	next := mustEdge(t, ctx, edges, tree.ID, b.ID, syn.ID)
	if next.SequenceNum != 2 {
		t.Errorf("next edge sequence_num = %d, want 2 — the rolled-back insert consumed one", next.SequenceNum)
	}
}

func TestSQLiteEdgeRepoMove(t *testing.T) {
	_, trees, nodes, edges := newGraphRepos(t)
	ctx := context.Background()
	tree := mustTree(t, ctx, trees, "move")
	root := mustNode(t, ctx, nodes, tree.ID, nil, "root")
	newSource := mustNode(t, ctx, nodes, tree.ID, nil, "new source")
	target := mustNode(t, ctx, nodes, tree.ID, nil, "target")

	created := mustEdge(t, ctx, edges, tree.ID, root.ID, target.ID)
	moved, err := edges.Move(ctx, created.ID, newSource.ID)
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	if moved.SourceID != newSource.ID || moved.TargetID != target.ID {
		t.Errorf("Move produced source/target %s/%s, want %s/%s", moved.SourceID, moved.TargetID, newSource.ID, target.ID)
	}
	if moved.TreeID != tree.ID || moved.EdgeType != db.EdgeTypeReply {
		t.Errorf("Move changed tree_id/edge_type to %s/%s", moved.TreeID, moved.EdgeType)
	}
	if moved.SequenceNum != 1 {
		t.Errorf("sequence_num for the new source = %d, want 1 (a fresh per-source counter)", moved.SequenceNum)
	}
	if list, err := edges.GetBySource(ctx, root.ID); err != nil || len(list) != 0 {
		t.Errorf("old source still has %d edges (err %v), want 0", len(list), err)
	}

	// Moving to the source it already has is PG's `source_id != $2` guard: not found.
	if _, err := edges.Move(ctx, created.ID, newSource.ID); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("Move to the current source error = %v, want db.ErrNotFound", err)
	}
	// Moving so that the source becomes the target would create a self-edge.
	if _, err := edges.Move(ctx, created.ID, target.ID); !errors.Is(err, db.ErrSelfEdge) {
		t.Errorf("Move to the target error = %v, want db.ErrSelfEdge", err)
	}
	if _, err := edges.Move(ctx, uuid.New(), newSource.ID); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("Move(unknown edge) error = %v, want db.ErrNotFound", err)
	}
	// The rejected moves left the row untouched.
	after, err := edges.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID after rejected moves: %v", err)
	}
	if after.SourceID != newSource.ID || after.SequenceNum != moved.SequenceNum {
		t.Errorf("rejected Move changed the row: source %s sequence %d", after.SourceID, after.SequenceNum)
	}
}

func TestSQLiteEdgeRepoParentsSiblingsCounts(t *testing.T) {
	_, trees, nodes, edges := newGraphRepos(t)
	ctx := context.Background()
	tree := mustTree(t, ctx, trees, "aggregate")
	root := mustNode(t, ctx, nodes, tree.ID, nil, "root")
	a := mustNode(t, ctx, nodes, tree.ID, nil, "a")
	b := mustNode(t, ctx, nodes, tree.ID, nil, "b")
	empty := mustTree(t, ctx, trees, "empty")

	first := mustEdge(t, ctx, edges, tree.ID, root.ID, a.ID)
	second := mustEdge(t, ctx, edges, tree.ID, root.ID, b.ID)

	parents, err := edges.GetParents(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetParents: %v", err)
	}
	if len(parents) != 1 || parents[0].ID != root.ID {
		t.Errorf("GetParents(a) = %v, want [root]", nodeIDs(parents))
	}
	siblings, err := edges.GetSiblings(ctx, root.ID, a.ID)
	if err != nil {
		t.Fatalf("GetSiblings: %v", err)
	}
	if got := nodeIDs(siblings); len(got) != 2 || got[0] != a.ID || got[1] != b.ID {
		t.Errorf("GetSiblings(root, a) = %v, want [a b]", got)
	}

	counts, err := edges.GetEdgeCounts(ctx, tree.ID)
	if err != nil {
		t.Fatalf("GetEdgeCounts: %v", err)
	}
	if counts.Total != 2 || counts.Active != 2 {
		t.Errorf("edge counts = %d/%d, want 2/2", counts.Total, counts.Active)
	}
	if counts.ByType[db.EdgeTypeReply] != 2 {
		t.Errorf("ByType = %v, want two replies", counts.ByType)
	}
	if counts.TreeID != tree.ID {
		t.Errorf("TreeID = %s, want %s", counts.TreeID, tree.ID)
	}

	// An empty tree reports zeros and a non-nil (empty) breakdown map.
	none, err := edges.GetEdgeCounts(ctx, empty.ID)
	if err != nil {
		t.Fatalf("GetEdgeCounts(empty tree): %v", err)
	}
	if none.Total != 0 || none.Active != 0 || len(none.ByType) != 0 {
		t.Errorf("GetEdgeCounts(empty) = %+v, want zeros", none)
	}
	if none.ByType == nil {
		t.Error("GetEdgeCounts returned a nil ByType map")
	}

	// A soft-deleted edge leaves the totals and drops out of the active counts and the
	// sibling set.
	if err := edges.SoftDelete(ctx, second.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	counts, err = edges.GetEdgeCounts(ctx, tree.ID)
	if err != nil {
		t.Fatalf("GetEdgeCounts after SoftDelete: %v", err)
	}
	if counts.Total != 2 || counts.Active != 1 || counts.ByType[db.EdgeTypeReply] != 1 {
		t.Errorf("counts after SoftDelete = %d/%d %v, want 2/1 {reply:1}", counts.Total, counts.Active, counts.ByType)
	}
	if siblings, err := edges.GetSiblings(ctx, root.ID, a.ID); err != nil || len(siblings) != 1 {
		t.Errorf("GetSiblings after SoftDelete = %d nodes (err %v), want 1", len(siblings), err)
	}
	if list, err := edges.GetByTarget(ctx, b.ID); err != nil || len(list) != 0 {
		t.Errorf("GetByTarget(soft-deleted edge) = %d edges (err %v), want 0", len(list), err)
	}
	if first.ID == uuid.Nil {
		t.Error("first edge has a nil id")
	}
}

// TestSQLiteEdgeRepoBoundaryIsDerivedFromInterface derives the implementation boundary from
// db.EdgeRepo itself instead of restating it: every method whose signature mentions a pgx
// transaction must be ABSENT (the SQLite package must not import pgx), and every other method
// must be present. A future interface change therefore fails this test rather than silently
// leaving a hole.
func TestSQLiteEdgeRepoBoundaryIsDerivedFromInterface(t *testing.T) {
	iface := reflect.TypeOf((*db.EdgeRepo)(nil)).Elem()
	impl := reflect.TypeOf(&EdgeRepo{})

	implemented := map[string]bool{}
	pgxScoped := map[string]bool{}
	for i := 0; i < iface.NumMethod(); i++ {
		m := iface.Method(i)
		_, present := impl.MethodByName(m.Name)
		if strings.Contains(m.Type.String(), "pgx.Tx") {
			pgxScoped[m.Name] = true
			if present {
				t.Errorf("%s is implemented, but its signature is pgx.Tx-scoped: this package must stay free of pgx", m.Name)
			}
			continue
		}
		implemented[m.Name] = true
		if !present {
			t.Errorf("db.EdgeRepo.%s%s is driver-agnostic and must be implemented", m.Name, m.Type)
		}
	}

	// The premise of the split: the reference model's write path is transaction-shaped
	// against PostgreSQL.
	wantPgx := []string{"CreateReferenceSet", "GetActiveIncoming", "ValidateIncomingInvariant"}
	for _, name := range wantPgx {
		if !pgxScoped[name] {
			t.Errorf("%s is no longer pgx.Tx-scoped — re-derive the boundary instead of trusting this test", name)
		}
	}
	if len(pgxScoped) != len(wantPgx) {
		t.Errorf("pgx.Tx-scoped methods = %v, want exactly %v", sortedKeys(pgxScoped), wantPgx)
	}
	if len(implemented) == 0 {
		t.Error("no driver-agnostic method found in db.EdgeRepo; the derivation is broken")
	}
	// The whole interface is deliberately NOT satisfied, and the compiler agrees: an
	// assertion `var _ db.EdgeRepo = (*EdgeRepo)(nil)` would not build.
	if impl.Implements(iface) {
		t.Error("*EdgeRepo now satisfies db.EdgeRepo in full; the boundary comment is stale")
	}
	t.Logf("*EdgeRepo implements %d of %d db.EdgeRepo methods; pgx.Tx-scoped and out of slice: %v",
		len(implemented), iface.NumMethod(), sortedKeys(pgxScoped))
}

// TestSQLiteCoreReposSatisfyTheirInterfaces is the reflection-side companion to the
// compile-time assertions in repo.go, kept here so all three interfaces are covered in one
// place — including the two that ARE satisfied in full.
func TestSQLiteCoreReposSatisfyTheirInterfaces(t *testing.T) {
	cases := []struct {
		name      string
		iface     reflect.Type
		impl      reflect.Type
		satisfied bool
	}{
		{"TreeRepo", reflect.TypeOf((*db.TreeRepo)(nil)).Elem(), reflect.TypeOf(&TreeRepo{}), true},
		{"NodeRepo", reflect.TypeOf((*db.NodeRepo)(nil)).Elem(), reflect.TypeOf(&NodeRepo{}), true},
		{"EdgeRepo", reflect.TypeOf((*db.EdgeRepo)(nil)).Elem(), reflect.TypeOf(&EdgeRepo{}), false},
	}
	for _, tc := range cases {
		if got := tc.impl.Implements(tc.iface); got != tc.satisfied {
			t.Errorf("%s.Implements(%s) = %v, want %v", tc.name, tc.iface, got, tc.satisfied)
		}
	}
}

// sortedKeys returns a map's keys in a deterministic order, for error messages.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestSQLiteEdgeRepoGetActiveReferenceParents covers the one SPEC-PL-06 read method whose
// signature is driver-agnostic. Reference edges are seeded with plain SQL on purpose: the
// model's WRITE path (CreateReferenceSet) is pgx.Tx-scoped and out of this slice, and
// EdgeRepo.Create rejects a lone reference edge because a complete 2-20 set needs the
// node's parent_mode column. The read path is still real — real rows, real metadata, real
// json_extract ordering — so the translation is verified rather than assumed.
func TestSQLiteEdgeRepoGetActiveReferenceParents(t *testing.T) {
	s, trees, nodes, edges := newGraphRepos(t)
	ctx := context.Background()
	tree := mustTree(t, ctx, trees, "refs")
	target := mustNode(t, ctx, nodes, tree.ID, nil, "target")
	a := mustNode(t, ctx, nodes, tree.ID, nil, "a")
	b := mustNode(t, ctx, nodes, tree.ID, nil, "b")
	c := mustNode(t, ctx, nodes, tree.ID, nil, "c")

	// The metadata is built by the same exported helper the service uses, with a scrambled
	// selection_order so the ordering assertion cannot pass by insertion order alone.
	seedRef := func(seq int, source uuid.UUID, order int) {
		meta, err := json.Marshal(db.ReferenceEdgeMetadataFor(order, tree.ID, target.ID, source))
		if err != nil {
			t.Fatalf("marshal reference metadata: %v", err)
		}
		if _, err := s.DB().ExecContext(ctx, `
            INSERT INTO edges (id, tree_id, source_id, target_id, edge_type, sequence_num, metadata)
            VALUES (?, ?, ?, ?, ?, ?, ?)`,
			freshID(), tree.ID.String(), source.String(), target.ID.String(),
			db.EdgeTypeReference, seq, string(meta)); err != nil {
			t.Fatalf("seed reference edge: %v", err)
		}
	}
	seedRef(1, a.ID, 2)
	seedRef(2, b.ID, 0)
	seedRef(3, c.ID, 1)
	// A non-reference edge to the same target must be ignored by this query.
	if _, err := s.DB().ExecContext(ctx, `
        INSERT INTO edges (id, tree_id, source_id, target_id, edge_type, sequence_num)
        VALUES (?, ?, ?, ?, ?, ?)`,
		freshID(), tree.ID.String(), a.ID.String(), target.ID.String(), db.EdgeTypeReply, 4); err != nil {
		t.Fatalf("seed reply edge: %v", err)
	}

	parents, err := edges.GetActiveReferenceParents(ctx, target.ID)
	if err != nil {
		t.Fatalf("GetActiveReferenceParents: %v", err)
	}
	if len(parents) != 3 {
		t.Fatalf("GetActiveReferenceParents = %d edges, want 3", len(parents))
	}
	wantOrder := []uuid.UUID{b.ID, c.ID, a.ID}
	for i, want := range wantOrder {
		got := parents[i]
		if got.SourceID != want {
			t.Errorf("parent %d source = %s, want %s (ordered by selection_order)", i, got.SourceID, want)
		}
		if got.Edge == nil {
			t.Fatalf("parent %d has a nil Edge", i)
		}
		if got.Edge.EdgeType != db.EdgeTypeReference {
			t.Errorf("parent %d edge_type = %q, want reference", i, got.Edge.EdgeType)
		}
		if got.Index != i || got.SourceLabel != db.ReferenceSourceLabel(i) {
			t.Errorf("parent %d index/label = %d/%q, want %d/%q", i, got.Index, got.SourceLabel, i, db.ReferenceSourceLabel(i))
		}
		if wantKey := db.ReferenceColorKey(tree.ID, target.ID, want); got.ColorKey != wantKey {
			t.Errorf("parent %d colour key = %q, want %q", i, got.ColorKey, wantKey)
		}
	}

	// A soft-deleted reference edge is no longer an active parent.
	for _, e := range parents {
		if e.SourceID == c.ID {
			if err := edges.SoftDelete(ctx, e.Edge.ID); err != nil {
				t.Fatalf("SoftDelete(reference edge): %v", err)
			}
		}
	}
	remaining, err := edges.GetActiveReferenceParents(ctx, target.ID)
	if err != nil {
		t.Fatalf("GetActiveReferenceParents after soft delete: %v", err)
	}
	if len(remaining) != 2 {
		t.Errorf("remaining reference parents = %d, want 2", len(remaining))
	}
	empty, err := edges.GetActiveReferenceParents(ctx, uuid.New())
	if err != nil || len(empty) != 0 {
		t.Errorf("GetActiveReferenceParents(unknown target) = %d edges (err %v), want 0", len(empty), err)
	}
}

// mustEdgeNamed is mustEdge with an explicit edge type.
func mustEdgeNamed(t *testing.T, ctx context.Context, r *EdgeRepo, treeID, sourceID, targetID uuid.UUID, edgeType string) *db.Edge {
	t.Helper()
	e, err := r.Create(ctx, &db.Edge{TreeID: treeID, SourceID: sourceID, TargetID: targetID, EdgeType: edgeType})
	if err != nil {
		t.Fatalf("EdgeRepo.Create(%s -> %s, %s): %v", sourceID, targetID, edgeType, err)
	}
	return e
}
