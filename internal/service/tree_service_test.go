package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
)

type treeRepoStub struct {
	trees []db.Tree
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
func (r *treeRepoStub) GetByOwner(_ context.Context, _ uuid.UUID) ([]db.Tree, error) { return r.trees, nil }
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

type nodeRepoStub struct{}

func (n *nodeRepoStub) Create(_ context.Context, node *db.Node) (*db.Node, error)  { return node, nil }
func (n *nodeRepoStub) GetByID(_ context.Context, _ uuid.UUID) (*db.Node, error)    { return nil, db.ErrNotFound }
func (n *nodeRepoStub) GetByTree(_ context.Context, _ uuid.UUID) ([]db.Node, error)  { return nil, nil }
func (n *nodeRepoStub) GetChildren(_ context.Context, _ uuid.UUID) ([]db.Node, error) { return nil, nil }
func (n *nodeRepoStub) GetAncestors(_ context.Context, _ uuid.UUID) ([]db.Node, error) { return nil, nil }
func (n *nodeRepoStub) GetSubtree(_ context.Context, _ uuid.UUID, _ int) ([]db.Node, error) { return nil, nil }
func (n *nodeRepoStub) GetPath(_ context.Context, _, _ uuid.UUID) ([]db.Node, error) { return nil, nil }
func (n *nodeRepoStub) Update(_ context.Context, _ uuid.UUID, _ string, _ []byte) (*db.Node, error) { return nil, nil }
func (n *nodeRepoStub) SoftDelete(_ context.Context, _ uuid.UUID) error { return nil }
func (n *nodeRepoStub) HardDelete(_ context.Context, _ uuid.UUID) error { return nil }
func (n *nodeRepoStub) GetCounts(_ context.Context, _ uuid.UUID) (*db.NodeCounts, error) { return nil, nil }

type edgeRepoStub struct{}

func (e *edgeRepoStub) Create(_ context.Context, edge *db.Edge) (*db.Edge, error) { return edge, nil }
func (e *edgeRepoStub) GetByID(_ context.Context, _ uuid.UUID) (*db.Edge, error)   { return nil, db.ErrNotFound }
func (e *edgeRepoStub) GetBySource(_ context.Context, _ uuid.UUID) ([]db.Edge, error) { return nil, nil }
func (e *edgeRepoStub) GetByTarget(_ context.Context, _ uuid.UUID) ([]db.Edge, error) { return nil, nil }
func (e *edgeRepoStub) GetByTree(_ context.Context, _ uuid.UUID) ([]db.Edge, error) { return nil, nil }
func (e *edgeRepoStub) SoftDelete(_ context.Context, _ uuid.UUID) error { return nil }
func (e *edgeRepoStub) GetParents(_ context.Context, _ uuid.UUID) ([]db.Node, error) { return nil, nil }
func (e *edgeRepoStub) GetSiblings(_ context.Context, _, _ uuid.UUID) ([]db.Node, error) { return nil, nil }
func (e *edgeRepoStub) GetEdgeCounts(_ context.Context, _ uuid.UUID) (*db.EdgeCounts, error) { return nil, nil }
func (e *edgeRepoStub) Move(_ context.Context, _ uuid.UUID, _ uuid.UUID) (*db.Edge, error) { return nil, nil }

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

func mustParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}
