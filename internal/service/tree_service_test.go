package service

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
)

type treeRepoStub struct {
	trees []db.Tree
}

func (r *treeRepoStub) Create(context.Context, *db.Tree) (*db.Tree, error) { return nil, nil }
func (r *treeRepoStub) GetByID(context.Context, uuid.UUID) (*db.Tree, error) {
	return nil, db.ErrNotFound
}
func (r *treeRepoStub) GetByOwner(context.Context, uuid.UUID) ([]db.Tree, error) { return nil, nil }
func (r *treeRepoStub) Update(context.Context, *db.Tree) (*db.Tree, error)       { return nil, nil }
func (r *treeRepoStub) SoftDelete(context.Context, uuid.UUID) error              { return nil }
func (r *treeRepoStub) List(context.Context, int, int) ([]db.Tree, error)        { return r.trees, nil }
func (r *treeRepoStub) Search(context.Context, string, int, int) ([]db.Tree, error) {
	return r.trees, nil
}

func TestListTreesClampsLimitAndPaginatesByCursor(t *testing.T) {
	first := uuid.MustParse("00000000-0000-7000-8000-000000000003")
	cursor := uuid.MustParse("00000000-0000-7000-8000-000000000002")
	last := uuid.MustParse("00000000-0000-7000-8000-000000000001")
	repo := &treeRepoStub{trees: []db.Tree{{ID: first}, {ID: cursor}, {ID: last}}}
	svc := NewTreeService(&db.DB{Trees: repo})

	page, err := svc.ListTrees(context.Background(), ListTreesInput{Limit: 1, Cursor: &cursor, Sort: "created_desc", Status: "active"})
	if err != nil {
		t.Fatalf("ListTrees() error = %v", err)
	}
	if len(page.Trees) != 1 || page.Trees[0].ID != last {
		t.Fatalf("ListTrees() trees = %#v", page.Trees)
	}
	if page.HasMore {
		t.Fatal("ListTrees() hasMore = true, want false")
	}
}

func TestValidateCreateTreeDefaultsRootFields(t *testing.T) {
	in := CreateTreeInput{Title: "Tree", AuthorID: uuid.New(), RootMessage: RootMessageInput{Content: "hello"}}
	if err := ValidateCreateTree(&in); err != nil {
		t.Fatalf("ValidateCreateTree() error = %v", err)
	}
	if in.RootMessage.ContentFormat != db.ContentFormatMarkdown || in.RootMessage.NodeType != db.NodeTypeMessage {
		t.Fatalf("defaults = %q, %q", in.RootMessage.ContentFormat, in.RootMessage.NodeType)
	}
}
