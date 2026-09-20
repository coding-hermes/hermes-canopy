package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

type treeRepoStub struct {
	trees []db.Tree
}

// CountNodesByTreeIDs returns a deterministic fake: every stub tree gets
// len(trees) nodes so list tests can assert the count plumbing runs.
func (r *treeRepoStub) CountNodesByTreeIDs(_ context.Context, treeIDs []uuid.UUID) (map[uuid.UUID]int, error) {
	counts := make(map[uuid.UUID]int, len(treeIDs))
	for _, id := range treeIDs {
		counts[id] = len(r.trees)
	}
	return counts, nil
}

// LastActivityByTreeIDs returns a deterministic fake: every stub tree's
// last activity is its root-node position marker in CreatedAt (the stub
// sets no nodes, so the value only needs to be present and stable).
func (r *treeRepoStub) LastActivityByTreeIDs(_ context.Context, treeIDs []uuid.UUID) (map[uuid.UUID]time.Time, error) {
	latest := make(map[uuid.UUID]time.Time, len(treeIDs))
	for _, id := range treeIDs {
		latest[id] = mustParseTime("2026-07-23T12:00:00Z")
	}
	return latest, nil
}

func (r *treeRepoStub) Create(_ context.Context, tree *db.Tree) (*db.Tree, error) {
	tree.ID = uuid.MustParse("00000000-0000-7000-8000-000000000001")
	tree.CreatedAt = mustParseTime("2026-07-23T10:00:00Z")
	return tree, nil
}
func (r *treeRepoStub) GetByID(_ context.Context, id uuid.UUID) (*db.Tree, error) {
	for i := range r.trees {
		if r.trees[i].ID == id {
			return &r.trees[i], nil
		}
	}
	return nil, db.ErrNotFound
}
func (r *treeRepoStub) GetByOwner(_ context.Context, _ uuid.UUID) ([]db.Tree, error) {
	return r.trees, nil
}
func (r *treeRepoStub) Update(_ context.Context, tree *db.Tree) (*db.Tree, error) {
	for i := range r.trees {
		if r.trees[i].ID == tree.ID {
			r.trees[i] = *tree
			edited := mustParseTime("2026-07-23T11:00:00Z")
			r.trees[i].EditedAt = &edited
			return &r.trees[i], nil
		}
	}
	return nil, db.ErrNotFound
}
func (r *treeRepoStub) SoftDelete(_ context.Context, _ uuid.UUID) error { return nil }
func (r *treeRepoStub) List(_ context.Context, limit, offset int) ([]db.Tree, error) {
	if offset > len(r.trees) {
		return []db.Tree{}, nil
	}
	end := offset + limit
	if end > len(r.trees) {
		end = len(r.trees)
	}
	return r.trees[offset:end], nil
}
func (r *treeRepoStub) Search(_ context.Context, _ string, limit, offset int) ([]db.Tree, error) {
	return r.List(nil, limit, offset)
}

// Count returns the number of trees in the stub. PAG-002.
func (r *treeRepoStub) Count(_ context.Context) (int, error) {
	return len(r.trees), nil
}

// ListKeyset simulates keyset pagination over the stub's in-memory slice.
// Trees are ordered by CreatedAt DESC then ID DESC (string compare). When
// cursorID is nil, the first `limit` rows are returned. When non-nil, rows
// strictly after the cursor (by the same ordering) are returned. PAG-002.
func (r *treeRepoStub) ListKeyset(_ context.Context, cursorID *uuid.UUID, limit int) ([]db.Tree, error) {
	// Copy and sort by (created_at DESC, id DESC) to mirror the repo.
	sorted := make([]db.Tree, len(r.trees))
	copy(sorted, r.trees)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].CreatedAt.Equal(sorted[j].CreatedAt) {
			return sorted[i].ID.String() > sorted[j].ID.String()
		}
		return sorted[i].CreatedAt.After(sorted[j].CreatedAt)
	})

	var start int
	if cursorID != nil {
		for i, t := range sorted {
			if t.ID == *cursorID {
				start = i + 1
				break
			}
		}
	}

	if start >= len(sorted) {
		return []db.Tree{}, nil
	}
	end := start + limit
	if end > len(sorted) {
		end = len(sorted)
	}
	return sorted[start:end], nil
}

type nodeRepoStub struct{}

func (n *nodeRepoStub) Create(_ context.Context, node *db.Node) (*db.Node, error) { return node, nil }
func (n *nodeRepoStub) GetByID(_ context.Context, _ uuid.UUID) (*db.Node, error) {
	return nil, db.ErrNotFound
}
func (n *nodeRepoStub) GetByTree(_ context.Context, _ uuid.UUID) ([]db.Node, error) { return nil, nil }
func (n *nodeRepoStub) GetChildren(_ context.Context, _ uuid.UUID) ([]db.Node, error) {
	return nil, nil
}
func (n *nodeRepoStub) GetAncestors(_ context.Context, _ uuid.UUID) ([]db.Node, error) {
	return nil, nil
}
func (n *nodeRepoStub) GetSubtree(_ context.Context, _ uuid.UUID, _ int) ([]db.Node, error) {
	return nil, nil
}
func (n *nodeRepoStub) GetPath(_ context.Context, _, _ uuid.UUID) ([]db.Node, error) { return nil, nil }
func (n *nodeRepoStub) Update(_ context.Context, _ uuid.UUID, _ string, _ []byte) (*db.Node, error) {
	return nil, nil
}
func (n *nodeRepoStub) SoftDelete(_ context.Context, _ uuid.UUID) error { return nil }
func (n *nodeRepoStub) HardDelete(_ context.Context, _ uuid.UUID) error { return nil }
func (n *nodeRepoStub) GetCounts(_ context.Context, _ uuid.UUID) (*db.NodeCounts, error) {
	return nil, nil
}

type edgeRepoStub struct{}

func (e *edgeRepoStub) Create(_ context.Context, edge *db.Edge) (*db.Edge, error) { return edge, nil }
func (e *edgeRepoStub) GetByID(_ context.Context, _ uuid.UUID) (*db.Edge, error) {
	return nil, db.ErrNotFound
}
func (e *edgeRepoStub) GetBySource(_ context.Context, _ uuid.UUID) ([]db.Edge, error) {
	return nil, nil
}
func (e *edgeRepoStub) GetByTarget(_ context.Context, _ uuid.UUID) ([]db.Edge, error) {
	return nil, nil
}
func (e *edgeRepoStub) GetByTree(_ context.Context, _ uuid.UUID) ([]db.Edge, error)  { return nil, nil }
func (e *edgeRepoStub) SoftDelete(_ context.Context, _ uuid.UUID) error              { return nil }
func (e *edgeRepoStub) GetParents(_ context.Context, _ uuid.UUID) ([]db.Node, error) { return nil, nil }
func (e *edgeRepoStub) GetSiblings(_ context.Context, _, _ uuid.UUID) ([]db.Node, error) {
	return nil, nil
}
func (e *edgeRepoStub) GetEdgeCounts(_ context.Context, _ uuid.UUID) (*db.EdgeCounts, error) {
	return nil, nil
}
func (e *edgeRepoStub) Move(_ context.Context, _ uuid.UUID, _ uuid.UUID) (*db.Edge, error) {
	return nil, nil
}

// SPEC-PL-06 §12 additions — the stub is only used for non-reference paths,
// so the reference-set methods stay inert.
func (e *edgeRepoStub) CreateReferenceSet(_ context.Context, _ pgx.Tx, _ db.CreateReferenceSetInput) ([]*db.Edge, error) {
	return nil, nil
}
func (e *edgeRepoStub) GetActiveIncoming(_ context.Context, _ pgx.Tx, _ uuid.UUID) ([]*db.Edge, error) {
	return nil, nil
}
func (e *edgeRepoStub) GetActiveReferenceParents(_ context.Context, _ uuid.UUID) ([]db.ReferenceParentEdge, error) {
	return nil, nil
}
func (e *edgeRepoStub) ValidateIncomingInvariant(_ context.Context, _ pgx.Tx, _ *db.Node) error {
	return nil
}

func TestValidateCreateTree_ValidInput(t *testing.T) {
	p := CreateTreeParams{
		Title:         "Test Tree",
		OwnerID:       uuid.New(),
		RootContent:   "Hello",
		ContentFormat: FormatMarkdown,
		NodeType:      NodeTypeMessage,
	}
	if err := validateCreateTree(p); err != nil {
		t.Fatalf("validateCreateTree() error = %v", err)
	}
}

func TestValidateCreateTree_TitleRequired(t *testing.T) {
	p := CreateTreeParams{
		Title:         "",
		OwnerID:       uuid.New(),
		RootContent:   "hello",
		ContentFormat: FormatMarkdown,
		NodeType:      NodeTypeMessage,
	}
	if err := validateCreateTree(p); err != ErrTitleRequired {
		t.Fatalf("validateCreateTree() = %v, want ErrTitleRequired", err)
	}
}

func TestListTrees_ReturnsPage(t *testing.T) {
	repo := &treeRepoStub{
		trees: []db.Tree{
			{ID: uuid.MustParse("00000000-0000-7000-8000-000000000001")},
		},
	}
	svc := &TreeServiceImpl{
		treeRepo: repo,
		nodeRepo: &nodeRepoStub{},
		edgeRepo: &edgeRepoStub{},
	}

	page, err := svc.ListTrees(context.Background(), ListTreesParams{
		Limit:  10,
		Status: TreeStatusActive,
		Sort:   SortCreatedDesc,
	})
	if err != nil {
		t.Fatalf("ListTrees() error = %v", err)
	}
	if len(page.Trees) != 1 {
		t.Fatalf("ListTrees() = %d trees, expected 1", len(page.Trees))
	}
	if page.HasMore {
		t.Fatal("ListTrees() HasMore = true, want false (only 1 tree)")
	}
}

func TestListTrees_ClampsLimit(t *testing.T) {
	repo := &treeRepoStub{trees: []db.Tree{}}
	svc := &TreeServiceImpl{
		treeRepo: repo,
		nodeRepo: &nodeRepoStub{},
		edgeRepo: &edgeRepoStub{},
	}

	page, err := svc.ListTrees(context.Background(), ListTreesParams{
		Limit:  0,
		Status: TreeStatusActive,
	})
	if err != nil {
		t.Fatalf("ListTrees() error = %v", err)
	}
	if page.Limit != 50 {
		t.Fatalf("ListTrees() limit = %d, expected 50 (default)", page.Limit)
	}
}

func TestListTrees_EmptyList(t *testing.T) {
	repo := &treeRepoStub{trees: []db.Tree{}}
	svc := &TreeServiceImpl{
		treeRepo: repo,
		nodeRepo: &nodeRepoStub{},
		edgeRepo: &edgeRepoStub{},
	}

	page, err := svc.ListTrees(context.Background(), ListTreesParams{
		Status: TreeStatusActive,
	})
	if err != nil {
		t.Fatalf("ListTrees() error = %v", err)
	}
	if len(page.Trees) != 0 {
		t.Fatalf("ListTrees() = %d trees, expected 0", len(page.Trees))
	}
}

// --- PAG-002: keyset pagination unit tests ----------------------------------

// makeKeysetTestTrees builds n trees with strictly increasing created_at
// timestamps and decreasing UUIDs (to avoid ties) so keyset ordering is
// deterministic.
func makeKeysetTestTrees(n int) []db.Tree {
	trees := make([]db.Tree, n)
	base := mustParseTime("2026-01-01T00:00:00Z")
	for i := 0; i < n; i++ {
		trees[i] = db.Tree{
			ID:        uuid.MustParse(fmt.Sprintf("00000000-0000-7000-8000-%012d", n-i)),
			OwnerID:   uuid.MustParse("00000000-0000-7000-8000-000000000099"),
			Title:     fmt.Sprintf("Tree %d", i),
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}
	}
	return trees
}

// TestListTrees_Keyset_TotalIsRealCount verifies that Total reflects the
// real count of active trees, not the fetched window size. Seeds 5 trees,
// requests limit=2, asserts Total=5.
func TestListTrees_Keyset_TotalIsRealCount(t *testing.T) {
	repo := &treeRepoStub{trees: makeKeysetTestTrees(5)}
	svc := &TreeServiceImpl{
		treeRepo: repo,
		nodeRepo: &nodeRepoStub{},
		edgeRepo: &edgeRepoStub{},
	}

	page, err := svc.ListTrees(context.Background(), ListTreesParams{
		Limit:  2,
		Status: TreeStatusActive,
		Sort:   SortCreatedDesc,
	})
	if err != nil {
		t.Fatalf("ListTrees() error = %v", err)
	}
	if page.Total != 5 {
		t.Fatalf("Total = %d, want 5 (real count, not window)", page.Total)
	}
	if len(page.Trees) != 2 {
		t.Fatalf("Trees = %d, want 2 (limit)", len(page.Trees))
	}
	if !page.HasMore {
		t.Fatal("HasMore = false, want true (5 > limit 2)")
	}
}

// TestListTrees_Keyset_TwoPageWalk walks two pages with limit=2 over 5
// trees and asserts no overlap, correct ordering, and correct HasMore.
func TestListTrees_Keyset_TwoPageWalk(t *testing.T) {
	trees := makeKeysetTestTrees(5)
	repo := &treeRepoStub{trees: trees}
	svc := &TreeServiceImpl{
		treeRepo: repo,
		nodeRepo: &nodeRepoStub{},
		edgeRepo: &edgeRepoStub{},
	}

	// Page 1: no cursor, limit=2.
	page1, err := svc.ListTrees(context.Background(), ListTreesParams{
		Limit:  2,
		Status: TreeStatusActive,
		Sort:   SortCreatedDesc,
	})
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1.Trees) != 2 {
		t.Fatalf("page1: %d trees, want 2", len(page1.Trees))
	}
	if page1.Total != 5 {
		t.Fatalf("page1 Total = %d, want 5", page1.Total)
	}
	if !page1.HasMore {
		t.Fatal("page1 HasMore = false, want true")
	}
	if page1.NextCursor == nil {
		t.Fatal("page1 NextCursor = nil, want non-nil")
	}

	// Page 2: cursor from page1.
	page2, err := svc.ListTrees(context.Background(), ListTreesParams{
		Limit:  2,
		Status: TreeStatusActive,
		Sort:   SortCreatedDesc,
		Cursor: page1.NextCursor,
	})
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2.Trees) != 2 {
		t.Fatalf("page2: %d trees, want 2", len(page2.Trees))
	}
	if page2.Total != 5 {
		t.Fatalf("page2 Total = %d, want 5", page2.Total)
	}
	if !page2.HasMore {
		t.Fatal("page2 HasMore = false, want true (5 - 4 fetched = 1 more)")
	}

	// No overlap: all 4 IDs should be distinct.
	seen := make(map[uuid.UUID]bool)
	for _, ts := range append(page1.Trees, page2.Trees...) {
		if seen[ts.ID] {
			t.Fatalf("tree %s appeared in both pages (overlap)", ts.ID)
		}
		seen[ts.ID] = true
	}
}

// TestListTrees_Keyset_ThreePageWalk walks 3 pages with limit=2 over 5
// trees, asserting no dupes, no gaps, and eventually HasMore=false.
func TestListTrees_Keyset_ThreePageWalk(t *testing.T) {
	trees := makeKeysetTestTrees(5)
	repo := &treeRepoStub{trees: trees}
	svc := &TreeServiceImpl{
		treeRepo: repo,
		nodeRepo: &nodeRepoStub{},
		edgeRepo: &edgeRepoStub{},
	}

	var allIDs []uuid.UUID
	cursor := (*uuid.UUID)(nil)
	pageNum := 0

	for {
		pageNum++
		if pageNum > 10 {
			t.Fatal("too many pages, possible infinite loop")
		}
		page, err := svc.ListTrees(context.Background(), ListTreesParams{
			Limit:  2,
			Status: TreeStatusActive,
			Sort:   SortCreatedDesc,
			Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("page %d: %v", pageNum, err)
		}
		for _, ts := range page.Trees {
			allIDs = append(allIDs, ts.ID)
		}
		if !page.HasMore {
			break
		}
		cursor = page.NextCursor
	}

	if len(allIDs) != 5 {
		t.Fatalf("walked %d unique IDs, want 5", len(allIDs))
	}

	// Check no dupes.
	seen := make(map[uuid.UUID]bool)
	for _, id := range allIDs {
		if seen[id] {
			t.Fatalf("duplicate ID %s in walk", id)
		}
		seen[id] = true
	}

	// Check all source trees are covered.
	for _, tr := range trees {
		if !seen[tr.ID] {
			t.Fatalf("tree %s missing from walk (gap)", tr.ID)
		}
	}
}

func mustParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// GAP-094: ListTrees stamps each summary's LastActivity from the repo's
// MAX(nodes.created_at) map — the resume affordance sorts on it. The wire
// shape is asserted too: the field is a POINTER precisely because
// `omitempty` does not omit a zero time.Time, so an inactive tree must come
// back without the key rather than reporting activity at year 1.
func TestListTrees_FillsLastActivity(t *testing.T) {
	idA := uuid.MustParse("00000000-0000-7000-8000-00000000000a")
	idB := uuid.MustParse("00000000-0000-7000-8000-00000000000b")
	repo := &treeRepoStub{
		trees: []db.Tree{
			{ID: idA, CreatedAt: mustParseTime("2026-07-23T10:00:00Z")},
			{ID: idB, CreatedAt: mustParseTime("2026-07-23T09:00:00Z")},
		},
	}
	svc := &TreeServiceImpl{
		treeRepo: repo,
		nodeRepo: &nodeRepoStub{},
		edgeRepo: &edgeRepoStub{},
	}

	page, err := svc.ListTrees(context.Background(), ListTreesParams{
		Limit:  10,
		Status: TreeStatusActive,
		Sort:   SortCreatedDesc,
	})
	if err != nil {
		t.Fatalf("ListTrees() error = %v", err)
	}
	want := mustParseTime("2026-07-23T12:00:00Z") // the stub's deterministic answer
	for _, ts := range page.Trees {
		if ts.LastActivity == nil {
			t.Errorf("tree %s: LastActivity is nil, want the repo-supplied %v", ts.ID, want)
			continue
		}
		if !ts.LastActivity.Equal(want) {
			t.Errorf("tree %s: LastActivity = %v, want %v", ts.ID, ts.LastActivity, want)
		}
	}

	// Wire shape: present and RFC3339 for an active tree, ABSENT for a tree
	// the repo reported no activity for.
	if len(page.Trees) == 0 {
		t.Fatal("ListTrees returned no trees to check the wire shape on")
	}
	b, err := json.Marshal(page.Trees[0])
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatalf("unmarshal summary: %v", err)
	}
	if raw, ok := fields["last_activity"]; !ok {
		t.Errorf("wire shape %s has no last_activity key, want the timestamp", b)
	} else if got := string(raw); got != `"2026-07-23T12:00:00Z"` {
		t.Errorf("last_activity = %s, want \"2026-07-23T12:00:00Z\"", got)
	}

	// A summary the repo returned nothing for stays keyless.
	quiet := TreeSummary{ID: uuid.New(), Title: "quiet", CreatedAt: mustParseTime("2026-07-23T10:00:00Z")}
	empty, err := json.Marshal(quiet)
	if err != nil {
		t.Fatalf("marshal quiet summary: %v", err)
	}
	var quietFields map[string]json.RawMessage
	if err := json.Unmarshal(empty, &quietFields); err != nil {
		t.Fatalf("unmarshal quiet summary: %v", err)
	}
	if raw, ok := quietFields["last_activity"]; ok {
		t.Errorf("inactive tree wire shape carries last_activity = %s, want the key absent", raw)
	}
}
