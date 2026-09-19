package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

func TestSQLiteNodeRepoCreateAssignsPerTreeSequence(t *testing.T) {
	_, trees, nodes, _ := newGraphRepos(t)
	ctx := context.Background()
	treeA := mustTree(t, ctx, trees, "A")
	treeB := mustTree(t, ctx, trees, "B")

	// No BEFORE INSERT trigger can fill a NOT NULL column in SQLite, so the repository
	// allocates the sequence itself — per tree, in the insert's own transaction.
	for want := int64(1); want <= 3; want++ {
		n := mustNode(t, ctx, nodes, treeA.ID, nil, "node")
		if n.SequenceNum != want {
			t.Fatalf("tree A node %d: sequence_num = %d, want %d", want, n.SequenceNum, want)
		}
	}
	// A second tree starts its own counter.
	if n := mustNode(t, ctx, nodes, treeB.ID, nil, "other tree"); n.SequenceNum != 1 {
		t.Errorf("tree B first node sequence_num = %d, want 1 (sequences are tree-scoped)", n.SequenceNum)
	}
	// A caller-supplied non-zero sequence wins, matching PG's
	// `WHEN (NEW.sequence_num IS NULL)` default-only trigger; the counter continues from it.
	explicit, err := nodes.Create(ctx, &db.Node{
		TreeID:      treeA.ID,
		AuthorID:    uuid.New(),
		Content:     "explicit",
		SequenceNum: 7,
	})
	if err != nil {
		t.Fatalf("Create with an explicit sequence: %v", err)
	}
	if explicit.SequenceNum != 7 {
		t.Errorf("explicit sequence_num = %d, want 7", explicit.SequenceNum)
	}
	if next := mustNode(t, ctx, nodes, treeA.ID, nil, "after explicit"); next.SequenceNum != 8 {
		t.Errorf("sequence after an explicit 7 = %d, want 8", next.SequenceNum)
	}

	// GetByTree is ordered by sequence_num and carries every stored field.
	all, err := nodes.GetByTree(ctx, treeA.ID)
	if err != nil {
		t.Fatalf("GetByTree: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("GetByTree = %d nodes, want 5", len(all))
	}
	for i, n := range all {
		if n.TreeID != treeA.ID {
			t.Errorf("node %d tree_id = %s, want %s", i, n.TreeID, treeA.ID)
		}
		if n.AuthorID == uuid.Nil {
			t.Errorf("node %d has a nil author_id", i)
		}
		if i > 0 && all[i-1].SequenceNum > n.SequenceNum {
			t.Errorf("GetByTree not ordered by sequence_num: %d before %d", all[i-1].SequenceNum, n.SequenceNum)
		}
	}
	if otherTree, err := nodes.GetByTree(ctx, treeB.ID); err != nil || len(otherTree) != 1 {
		t.Errorf("GetByTree(tree B) = %d nodes (err %v), want 1", len(otherTree), err)
	}
}

func TestSQLiteNodeRepoContentHashMatchesPGFormula(t *testing.T) {
	s, trees, nodes, _ := newGraphRepos(t)
	ctx := context.Background()
	tree := mustTree(t, ctx, trees, "hash")

	const content = "héllo 世界 🚀 — naïve ✓"
	created := mustNode(t, ctx, nodes, tree.ID, nil, content)

	// The value on disk must equal sha256 over the UTF-8 bytes — PostgreSQL's
	// encode(sha256(convert_to(content,'UTF8')),'hex'), the corrected form from migration
	// 000025 that the SQLite store can only produce in Go.
	want := indepHash(content)
	if got := storedContentHash(t, ctx, s, created.ID); got != want {
		t.Errorf("stored content_hash = %s, want %s (UTF-8 sha256)", got, want)
	}
	// Guard against the two shapes that would silently pass a weaker test: a hash of the
	// ASCII-only bytes would differ, and a missing hash cannot be stored at all (NOT NULL).
	if got := storedContentHash(t, ctx, s, created.ID); got == indepHash("hello world") {
		t.Error("stored hash matches an unrelated content; the hash is not content-derived")
	}
	if len(want) != 64 || strings.ToLower(want) != want {
		t.Fatalf("test expectation is not a lowercase 64-char hex digest: %q", want)
	}
	// Content survived the round trip byte-for-byte (no encoding mangling).
	if got, err := nodes.GetByID(ctx, created.ID); err != nil || got.Content != content {
		t.Errorf("GetByID content = %q (err %v), want the exact UTF-8 content", got.Content, err)
	}
}

func TestSQLiteNodeRepoUpdateRecomputesHashAndEditedAt(t *testing.T) {
	s, trees, nodes, _ := newGraphRepos(t)
	ctx := context.Background()
	tree := mustTree(t, ctx, trees, "update")
	created := mustNode(t, ctx, nodes, tree.ID, nil, "one")

	// PG recomputes content_hash in trg_node_content_hash; with no SQL hash function the
	// update path owns it. A stale hash after an edit is the defect 000025 fixed.
	updated, err := nodes.Update(ctx, created.ID, "two", []byte(`{"m":1}`))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Content != "two" {
		t.Errorf("content = %q, want \"two\"", updated.Content)
	}
	if string(updated.Metadata) != `{"m":1}` {
		t.Errorf("metadata = %q", updated.Metadata)
	}
	if got, want := storedContentHash(t, ctx, s, created.ID), indepHash("two"); got != want {
		t.Errorf("content_hash after update = %s, want %s", got, want)
	}

	// trg_node_edited_at fires only when content or metadata actually change: seed a
	// sentinel, run a no-op update, and the sentinel must survive.
	sentinel := setNodeEditedAt(t, ctx, s, created.ID, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	noop, err := nodes.Update(ctx, created.ID, "two", []byte(`{"m":1}`))
	if err != nil {
		t.Fatalf("no-op Update: %v", err)
	}
	if noop.EditedAt == nil || EncodeTime(*noop.EditedAt) != sentinel {
		t.Errorf("no-op update changed edited_at to %v, want the untouched sentinel %s", noop.EditedAt, sentinel)
	}
	if got, want := storedContentHash(t, ctx, s, created.ID), indepHash("two"); got != want {
		t.Errorf("no-op update changed content_hash to %s, want %s", got, want)
	}

	// A real edit bumps edited_at (via the trigger) and the hash.
	edited, err := nodes.Update(ctx, created.ID, "three", []byte(`{"m":1}`))
	if err != nil {
		t.Fatalf("second Update: %v", err)
	}
	if edited.EditedAt == nil {
		t.Fatal("a content change did not set edited_at")
	}
	if EncodeTime(*edited.EditedAt) == sentinel {
		t.Error("edited_at still holds the seeded sentinel after a real edit")
	}
	if got, want := storedContentHash(t, ctx, s, created.ID), indepHash("three"); got != want {
		t.Errorf("content_hash after second update = %s, want %s", got, want)
	}

	// Error semantics.
	if _, err := nodes.Update(ctx, uuid.New(), "x", nil); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("Update(unknown id) error = %v, want db.ErrNotFound", err)
	}
	if _, err := nodes.Create(ctx, nil); err == nil {
		t.Error("Create(nil) succeeded, want an error")
	}
	if _, err := nodes.GetByID(ctx, uuid.New()); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("GetByID(unknown id) error = %v, want db.ErrNotFound", err)
	}
}

func TestSQLiteNodeRepoRejectsBrokenWritesWithoutResidue(t *testing.T) {
	s, trees, nodes, _ := newGraphRepos(t)
	ctx := context.Background()
	tree := mustTree(t, ctx, trees, "fk")
	first := mustNode(t, ctx, nodes, tree.ID, nil, "root")

	// 1. Unknown tree → the FK is enforced (the D6 pragma set exists for exactly this).
	_, err := nodes.Create(ctx, &db.Node{TreeID: uuid.New(), AuthorID: uuid.New(), Content: "orphan"})
	if err == nil {
		t.Fatal("Create with an unknown tree_id succeeded; foreign keys are not enforced")
	}
	if !isForeignKeyError(err) {
		t.Errorf("unknown tree error = %v, want a foreign-key failure", err)
	}

	// 2. Unknown parent → the self-referencing FK (nodes.parent_id → nodes.id).
	ghost := uuid.New()
	_, err = nodes.Create(ctx, &db.Node{TreeID: tree.ID, AuthorID: uuid.New(), ParentID: &ghost, Content: "no parent"})
	if err == nil {
		t.Fatal("Create with an unknown parent_id succeeded")
	}
	if !isForeignKeyError(err) {
		t.Errorf("unknown parent error = %v, want a foreign-key failure", err)
	}

	// 3. The closed value sets the DDL keeps: content_format and node_type.
	if _, err := nodes.Create(ctx, &db.Node{
		TreeID: tree.ID, AuthorID: uuid.New(), Content: "bad format", ContentFormat: "html",
	}); err == nil {
		t.Error("Create with content_format='html' succeeded, want chk_content_format to reject it")
	}
	if _, err := nodes.Create(ctx, &db.Node{
		TreeID: tree.ID, AuthorID: uuid.New(), Content: "bad type", NodeType: "chart",
	}); err == nil {
		t.Error("Create with node_type='chart' succeeded, want chk_node_type to reject it")
	}

	// Failure atomicity: three rejected inserts must leave no rows at all, and must not
	// consume the per-tree sequence the next successful insert reads.
	if n := countRows(t, ctx, s, `SELECT COUNT(*) FROM nodes WHERE tree_id = ?`, tree.ID.String()); n != 1 {
		t.Fatalf("node count after rejected inserts = %d, want 1 (only the first node)", n)
	}
	next := mustNode(t, ctx, nodes, tree.ID, nil, "after failures")
	if next.SequenceNum != 2 {
		t.Errorf("sequence after failed inserts = %d, want 2 — a rolled-back insert consumed a number", next.SequenceNum)
	}
	if first.SequenceNum != 1 {
		t.Errorf("first node sequence = %d, want 1", first.SequenceNum)
	}

	// Defaults match the PG repo's COALESCEs when the caller leaves the fields empty.
	filled, err := nodes.Create(ctx, &db.Node{TreeID: tree.ID, AuthorID: uuid.New(), Content: "defaults"})
	if err != nil {
		t.Fatalf("Create with empty format/type: %v", err)
	}
	if filled.ContentFormat != db.ContentFormatMarkdown || filled.NodeType != db.NodeTypeMessage {
		t.Errorf("defaults = %q/%q, want markdown/message", filled.ContentFormat, filled.NodeType)
	}
	if string(filled.Metadata) != "{}" {
		t.Errorf("empty metadata stored as %q, want {}", filled.Metadata)
	}
}

func TestSQLiteNodeRepoGraphWalks(t *testing.T) {
	_, trees, nodes, edges := newGraphRepos(t)
	ctx := context.Background()
	tree := mustTree(t, ctx, trees, "graph")

	// root → a → {b, c, syn}, and b → syn, so `syn` is reachable from root by two paths.
	root := mustNode(t, ctx, nodes, tree.ID, nil, "root")
	a := mustNode(t, ctx, nodes, tree.ID, &root.ID, "a")
	b := mustNode(t, ctx, nodes, tree.ID, &a.ID, "b")
	c := mustNode(t, ctx, nodes, tree.ID, &a.ID, "c")
	syn, err := nodes.Create(ctx, &db.Node{
		TreeID: tree.ID, AuthorID: uuid.New(), ParentID: &a.ID,
		Content: "syn", NodeType: db.NodeTypeSynthesis,
	})
	if err != nil {
		t.Fatalf("Create synthesis node: %v", err)
	}

	mustEdge(t, ctx, edges, tree.ID, root.ID, a.ID)
	mustEdge(t, ctx, edges, tree.ID, a.ID, b.ID)
	mustEdge(t, ctx, edges, tree.ID, a.ID, c.ID)
	mustEdge(t, ctx, edges, tree.ID, a.ID, syn.ID)
	mustEdge(t, ctx, edges, tree.ID, b.ID, syn.ID)

	// Children are derived from the edges, ordered by edge.sequence_num.
	children, err := nodes.GetChildren(ctx, a.ID)
	if err != nil {
		t.Fatalf("GetChildren: %v", err)
	}
	if len(children) != 3 {
		t.Fatalf("GetChildren(a) = %d nodes, want 3", len(children))
	}
	if children[0].ID != b.ID || children[1].ID != c.ID || children[2].ID != syn.ID {
		t.Errorf("GetChildren(a) order = [%s %s %s], want [b c syn] by edge sequence",
			children[0].ID, children[1].ID, children[2].ID)
	}
	if orphan, err := nodes.GetChildren(ctx, uuid.New()); err != nil || len(orphan) != 0 {
		t.Errorf("GetChildren(unknown) = %d nodes (err %v), want 0", len(orphan), err)
	}

	// Ancestors walk parent_id up to the root and are ordered by sequence_num (root first),
	// the order PGNodeRepo's SQL produces.
	ancestors, err := nodes.GetAncestors(ctx, b.ID)
	if err != nil {
		t.Fatalf("GetAncestors: %v", err)
	}
	if len(ancestors) != 3 || ancestors[0].ID != root.ID || ancestors[1].ID != a.ID || ancestors[2].ID != b.ID {
		t.Errorf("GetAncestors(b) = %v, want [root a b]", nodeIDs(ancestors))
	}

	// Subtree deduplicates: `syn` is reachable twice but must be returned once (GAP-073).
	subtree, err := nodes.GetSubtree(ctx, root.ID, 0)
	if err != nil {
		t.Fatalf("GetSubtree: %v", err)
	}
	if len(subtree) != 5 {
		t.Errorf("GetSubtree(root, unbounded) = %d nodes, want 5 (syn must not be duplicated): %v",
			len(subtree), nodeIDs(subtree))
	}
	seen := map[uuid.UUID]int{}
	for _, n := range subtree {
		seen[n.ID]++
	}
	for id, count := range seen {
		if count != 1 {
			t.Errorf("GetSubtree returned %s %d times, want exactly once", id, count)
		}
	}
	// The depth bound is owned by the CTE, so maxDepth=1 stops below a.
	shallow, err := nodes.GetSubtree(ctx, root.ID, 1)
	if err != nil {
		t.Fatalf("GetSubtree(root, 1): %v", err)
	}
	if len(shallow) != 2 {
		t.Errorf("GetSubtree(root, 1) = %v, want [root a]", nodeIDs(shallow))
	}

	// Paths: from → LCA → to, inclusive.
	cases := []struct {
		name     string
		from, to uuid.UUID
		want     []uuid.UUID
	}{
		{"siblings", b.ID, c.ID, []uuid.UUID{b.ID, a.ID, c.ID}},
		{"parent to child", a.ID, b.ID, []uuid.UUID{a.ID, b.ID}},
		{"root to leaf", root.ID, b.ID, []uuid.UUID{root.ID, a.ID, b.ID}},
		{"same node", c.ID, c.ID, []uuid.UUID{c.ID}},
	}
	for _, tc := range cases {
		path, err := nodes.GetPath(ctx, tc.from, tc.to)
		if err != nil {
			t.Fatalf("GetPath(%s): %v", tc.name, err)
		}
		got := nodeIDs(path)
		if len(got) != len(tc.want) {
			t.Errorf("GetPath(%s) = %v, want %v", tc.name, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("GetPath(%s)[%d] = %s, want %s", tc.name, i, got[i], tc.want[i])
			}
		}
	}
	// Two nodes with no shared root report ErrNotFound rather than an empty slice.
	otherTree := mustTree(t, ctx, trees, "other")
	lonely := mustNode(t, ctx, nodes, otherTree.ID, nil, "lonely")
	if _, err := nodes.GetPath(ctx, root.ID, lonely.ID); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("GetPath across trees error = %v, want db.ErrNotFound", err)
	}
}

func TestSQLiteNodeRepoSchemaKeepsSoftDeleteOutOfReads(t *testing.T) {
	s, trees, nodes, edges := newGraphRepos(t)
	ctx := context.Background()
	tree := mustTree(t, ctx, trees, "soft")
	root := mustNode(t, ctx, nodes, tree.ID, nil, "root")
	child := mustNode(t, ctx, nodes, tree.ID, &root.ID, "child")
	mustEdge(t, ctx, edges, tree.ID, root.ID, child.ID)

	if err := nodes.SoftDelete(ctx, child.ID); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if _, err := nodes.GetByID(ctx, child.ID); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("GetByID(soft-deleted) error = %v, want db.ErrNotFound", err)
	}
	if err := nodes.SoftDelete(ctx, child.ID); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("second SoftDelete error = %v, want db.ErrNotFound", err)
	}
	if all, err := nodes.GetByTree(ctx, tree.ID); err != nil || len(all) != 1 {
		t.Errorf("GetByTree = %d nodes (err %v), want only the surviving root", len(all), err)
	}
	// The child's edge is still there but the node filters it out of GetChildren.
	if kids, err := nodes.GetChildren(ctx, root.ID); err != nil || len(kids) != 0 {
		t.Errorf("GetChildren after soft delete = %d nodes (err %v), want 0", len(kids), err)
	}
	// The row survives on disk for recovery/audit.
	if n := countRows(t, ctx, s, `SELECT COUNT(*) FROM nodes WHERE id = ?`, child.ID.String()); n != 1 {
		t.Errorf("soft-deleted node row count = %d, want the row to survive", n)
	}

	// A soft-deleted PARENT keeps its children's display anchor: nodes.parent_id is ON DELETE
	// SET NULL, which a soft delete does not trigger.
	if err := nodes.SoftDelete(ctx, root.ID); err != nil {
		t.Fatalf("SoftDelete(root): %v", err)
	}
	anchor := storedText(t, ctx, s, `SELECT parent_id FROM nodes WHERE id = ?`, child.ID.String())
	if anchor != root.ID.String() {
		t.Errorf("child parent_id after a soft-deleted parent = %q, want %s", anchor, root.ID)
	}
}

func TestSQLiteNodeRepoHardDeleteCascades(t *testing.T) {
	s, trees, nodes, edges := newGraphRepos(t)
	ctx := context.Background()
	tree := mustTree(t, ctx, trees, "hard")
	parent := mustNode(t, ctx, nodes, tree.ID, nil, "parent")
	child := mustNode(t, ctx, nodes, tree.ID, &parent.ID, "child")
	mustEdge(t, ctx, edges, tree.ID, parent.ID, child.ID)

	if err := nodes.HardDelete(ctx, parent.ID); err != nil {
		t.Fatalf("HardDelete: %v", err)
	}
	// nodes.parent_id is ON DELETE SET NULL: the child survives with a cleared anchor.
	survivor, err := nodes.GetByID(ctx, child.ID)
	if err != nil {
		t.Fatalf("child after parent HardDelete: %v", err)
	}
	if survivor.ParentID != nil {
		t.Errorf("child parent_id = %s, want NULL after the parent was deleted", survivor.ParentID)
	}
	// edges.source_id / edges.target_id are ON DELETE CASCADE.
	if n := countRows(t, ctx, s, `SELECT COUNT(*) FROM edges WHERE tree_id = ?`, tree.ID.String()); n != 0 {
		t.Errorf("edges after HardDelete = %d, want 0 (cascade)", n)
	}
	if err := nodes.HardDelete(ctx, parent.ID); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("second HardDelete error = %v, want db.ErrNotFound", err)
	}
}

func TestSQLiteNodeRepoGetCounts(t *testing.T) {
	_, trees, nodes, edges := newGraphRepos(t)
	ctx := context.Background()
	tree := mustTree(t, ctx, trees, "counts")

	empty, err := nodes.GetCounts(ctx, tree.ID)
	if err != nil {
		t.Fatalf("GetCounts(empty tree): %v", err)
	}
	if empty.TotalNodes != 0 || empty.ActiveNodes != 0 || empty.TotalEdges != 0 || empty.ActiveEdges != 0 || empty.MaxDepth != 0 {
		t.Errorf("GetCounts(empty) = %+v, want all zero", empty)
	}
	if empty.TreeID != tree.ID {
		t.Errorf("GetCounts TreeID = %s, want %s", empty.TreeID, tree.ID)
	}

	root := mustNode(t, ctx, nodes, tree.ID, nil, "root")
	a := mustNode(t, ctx, nodes, tree.ID, &root.ID, "a")
	b := mustNode(t, ctx, nodes, tree.ID, &a.ID, "b")
	c := mustNode(t, ctx, nodes, tree.ID, &a.ID, "c")
	mustEdge(t, ctx, edges, tree.ID, root.ID, a.ID)
	mustEdge(t, ctx, edges, tree.ID, a.ID, b.ID)
	mustEdge(t, ctx, edges, tree.ID, a.ID, c.ID)

	counts, err := nodes.GetCounts(ctx, tree.ID)
	if err != nil {
		t.Fatalf("GetCounts: %v", err)
	}
	if counts.TotalNodes != 4 || counts.ActiveNodes != 4 {
		t.Errorf("node counts = %d/%d, want 4/4", counts.TotalNodes, counts.ActiveNodes)
	}
	if counts.TotalEdges != 3 || counts.ActiveEdges != 3 {
		t.Errorf("edge counts = %d/%d, want 3/3", counts.TotalEdges, counts.ActiveEdges)
	}
	if counts.MaxDepth != 2 {
		t.Errorf("MaxDepth = %d, want 2 (root → a → b)", counts.MaxDepth)
	}

	// Soft-deleting a node moves active counts but not totals; the same for an edge.
	if err := nodes.SoftDelete(ctx, c.ID); err != nil {
		t.Fatalf("SoftDelete(c): %v", err)
	}
	if err := edges.SoftDelete(ctx, (mustEdgesBySource(t, ctx, edges, a.ID))[0].ID); err != nil {
		t.Fatalf("SoftDelete(edge): %v", err)
	}
	counts, err = nodes.GetCounts(ctx, tree.ID)
	if err != nil {
		t.Fatalf("GetCounts after soft deletes: %v", err)
	}
	if counts.TotalNodes != 4 || counts.ActiveNodes != 3 {
		t.Errorf("node counts after soft delete = %d/%d, want 4/3", counts.TotalNodes, counts.ActiveNodes)
	}
	if counts.TotalEdges != 3 || counts.ActiveEdges != 2 {
		t.Errorf("edge counts after soft delete = %d/%d, want 3/2", counts.TotalEdges, counts.ActiveEdges)
	}
	if counts.MaxDepth != 2 {
		t.Errorf("MaxDepth after deleting b's edge = %d, want 2: depths walk parent_id, not edges", counts.MaxDepth)
	}
}

func TestSQLiteNodeRepoParentModeIsNotStoredInCoreSchema(t *testing.T) {
	s, trees, nodes, _ := newGraphRepos(t)
	ctx := context.Background()

	// pgx repos read nodes.parent_mode; the wave-1 SQLite batch stops at migration 000010, and
	// parent_mode arrives with 000047. The column is absent, so db.Node.ParentMode stays at its
	// zero value — an honest "not stored here" rather than a fabricated 'lineage'.
	var columns int
	if err := s.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('nodes') WHERE name = 'parent_mode'`).Scan(&columns); err != nil {
		t.Fatalf("pragma table_info(nodes): %v", err)
	}
	if columns != 0 {
		t.Errorf("nodes.parent_mode exists in the SQLite core schema; that migration is a later wave")
	}

	created := mustNode(t, ctx, nodes, mustTree(t, ctx, trees, "mode").ID, nil, "x")
	if created.ParentMode != "" {
		t.Errorf("ParentMode = %q, want the zero value (the column does not exist)", created.ParentMode)
	}
}

func TestSQLiteReposWithoutStoreReportErrNoStore(t *testing.T) {
	ctx := context.Background()

	// A nil store is a wiring mistake: every method must say so instead of panicking.
	if _, err := NewTreeRepo(nil).GetByID(ctx, uuid.New()); !errors.Is(err, ErrNoStore) {
		t.Errorf("TreeRepo(nil).GetByID error = %v, want ErrNoStore", err)
	}
	if _, err := NewNodeRepo(nil).GetByTree(ctx, uuid.New()); !errors.Is(err, ErrNoStore) {
		t.Errorf("NodeRepo(nil).GetByTree error = %v, want ErrNoStore", err)
	}
	if _, err := NewEdgeRepo(nil).GetByTree(ctx, uuid.New()); !errors.Is(err, ErrNoStore) {
		t.Errorf("EdgeRepo(nil).GetByTree error = %v, want ErrNoStore", err)
	}
	if _, err := NewTreeRepo(nil).Create(ctx, &db.Tree{}); !errors.Is(err, ErrNoStore) {
		t.Errorf("TreeRepo(nil).Create error = %v, want ErrNoStore", err)
	}
	if err := NewNodeRepo(nil).SoftDelete(ctx, uuid.New()); !errors.Is(err, ErrNoStore) {
		t.Errorf("NodeRepo(nil).SoftDelete error = %v, want ErrNoStore", err)
	}
	if _, err := NewEdgeRepo(nil).Create(ctx, &db.Edge{}); !errors.Is(err, ErrNoStore) {
		t.Errorf("EdgeRepo(nil).Create error = %v, want ErrNoStore", err)
	}
	// A zero-value Store is the same condition: no pool.
	if _, err := NewTreeRepo(&Store{}).Count(ctx); !errors.Is(err, ErrNoStore) {
		t.Errorf("TreeRepo(empty Store).Count error = %v, want ErrNoStore", err)
	}
}

// mustEdgesBySource returns the edges leaving one node, in sequence order; it fails the test
// when the node has none, which is always a mistake in the callers below.
func mustEdgesBySource(t *testing.T, ctx context.Context, r *EdgeRepo, sourceID uuid.UUID) []db.Edge {
	t.Helper()
	out, err := r.GetBySource(ctx, sourceID)
	if err != nil {
		t.Fatalf("GetBySource: %v", err)
	}
	if len(out) == 0 {
		t.Fatalf("GetBySource(%s) returned no edges", sourceID)
	}
	return out
}

// nodeIDs projects a node slice onto its ids, for readable assertions.
func nodeIDs(nodes []db.Node) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.ID)
	}
	return out
}
