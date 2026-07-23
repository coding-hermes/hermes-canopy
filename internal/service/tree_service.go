package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
)

var ErrValidation = errors.New("service: validation error")

type ValidationError struct {
	Code    string
	Message string
}

func (e *ValidationError) Error() string { return e.Message }
func (e *ValidationError) Unwrap() error { return ErrValidation }

type TreeService struct{ db *db.DB }

func NewTreeService(database *db.DB) *TreeService { return &TreeService{db: database} }

type RootMessageInput struct {
	Content       string `json:"content"`
	ContentFormat string `json:"contentFormat"`
	NodeType      string `json:"nodeType"`
}

type CreateTreeInput struct {
	Title       string           `json:"title"`
	Description string           `json:"description"`
	AuthorID    uuid.UUID        `json:"authorId"`
	RootMessage RootMessageInput `json:"rootMessage"`
}

type UpdateTreeInput struct {
	Title       *string `json:"title"`
	Description *string `json:"description"`
}

type TreeDetail struct {
	db.Tree
	NodeCount   int64 `json:"nodeCount"`
	MemberCount int64 `json:"memberCount"`
	BranchCount int64 `json:"branchCount"`
	Depth       int   `json:"depth"`
}

type ListTreesInput struct {
	Cursor *uuid.UUID
	Limit  int
	Sort   string
	Status string
	Search string
}

type TreePage struct {
	Trees      []db.Tree
	NextCursor *uuid.UUID
	HasMore    bool
	Total      int
	Limit      int
}

func ValidateCreateTree(in *CreateTreeInput) error {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return validation("TITLE_REQUIRED", "title is required")
	}
	if len([]rune(in.Title)) > 200 {
		return validation("TITLE_TOO_LONG", "title must not exceed 200 characters")
	}
	if len([]rune(in.Description)) > 2000 {
		return validation("DESCRIPTION_TOO_LONG", "description must not exceed 2000 characters")
	}
	if in.AuthorID == uuid.Nil {
		return validation("AUTHOR_ID_REQUIRED", "authorId is required")
	}
	if strings.TrimSpace(in.RootMessage.Content) == "" {
		return validation("ROOT_CONTENT_REQUIRED", "root message content is required")
	}
	if len([]rune(in.RootMessage.Content)) > 100000 {
		return validation("ROOT_CONTENT_TOO_LARGE", "root message content must not exceed 100000 characters")
	}
	if in.RootMessage.ContentFormat == "" {
		in.RootMessage.ContentFormat = db.ContentFormatMarkdown
	}
	if in.RootMessage.ContentFormat != db.ContentFormatMarkdown && in.RootMessage.ContentFormat != db.ContentFormatPlain && in.RootMessage.ContentFormat != db.ContentFormatRich {
		return validation("INVALID_CONTENT_FORMAT", "invalid root message contentFormat")
	}
	if in.RootMessage.NodeType == "" {
		in.RootMessage.NodeType = db.NodeTypeMessage
	}
	if in.RootMessage.NodeType != db.NodeTypeMessage && in.RootMessage.NodeType != db.NodeTypeSystem && in.RootMessage.NodeType != db.NodeTypeSynthesis {
		return validation("INVALID_NODE_TYPE", "invalid root message nodeType")
	}
	return nil
}

func (s *TreeService) CreateTree(ctx context.Context, in CreateTreeInput) (*TreeDetail, error) {
	if err := ValidateCreateTree(&in); err != nil {
		return nil, err
	}
	tx, err := s.db.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("service: begin create tree: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var tree db.Tree
	err = tx.QueryRow(ctx, `INSERT INTO trees (owner_id,title,description,metadata) VALUES ($1,$2,$3,'{}') RETURNING id,owner_id,title,description,root_node_id,metadata,created_at,edited_at,deleted_at`, in.AuthorID, in.Title, in.Description).Scan(&tree.ID, &tree.OwnerID, &tree.Title, &tree.Description, &tree.RootNodeID, &tree.Metadata, &tree.CreatedAt, &tree.EditedAt, &tree.DeletedAt)
	if err != nil {
		return nil, fmt.Errorf("service: insert tree: %w", err)
	}
	var rootID uuid.UUID
	err = tx.QueryRow(ctx, `INSERT INTO nodes (tree_id,parent_id,author_id,content,content_format,node_type,metadata) VALUES ($1,NULL,$2,$3,$4,$5,'{}') RETURNING id`, tree.ID, in.AuthorID, in.RootMessage.Content, in.RootMessage.ContentFormat, in.RootMessage.NodeType).Scan(&rootID)
	if err != nil {
		return nil, fmt.Errorf("service: insert root node: %w", err)
	}
	if _, err = tx.Exec(ctx, `UPDATE trees SET root_node_id=$2 WHERE id=$1`, tree.ID, rootID); err != nil {
		return nil, fmt.Errorf("service: set root node: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("service: commit create tree: %w", err)
	}
	tree.RootNodeID = &rootID
	return &TreeDetail{Tree: tree, NodeCount: 1}, nil
}

func (s *TreeService) ListTrees(ctx context.Context, in ListTreesInput) (*TreePage, error) {
	if in.Limit == 0 {
		in.Limit = 50
	}
	if in.Limit < 1 {
		in.Limit = 1
	}
	if in.Limit > 100 {
		in.Limit = 100
	}
	if in.Sort == "" {
		in.Sort = "created_desc"
	}
	if in.Status == "" {
		in.Status = "active"
	}
	if !validSort(in.Sort) {
		return nil, validation("INVALID_SORT", "invalid sort value")
	}
	if in.Status != "active" && in.Status != "deleted" && in.Status != "all" {
		return nil, validation("INVALID_STATUS", "invalid status value")
	}
	if in.Search != "" && len([]rune(in.Search)) < 3 {
		return nil, validation("SEARCH_TOO_SHORT", "search must contain at least 3 characters")
	}
	// Existing repositories expose active records only. Fetch enough rows to apply
	// cursor ordering while preserving the repository boundary.
	fetch := 200
	var trees []db.Tree
	var err error
	if in.Search != "" {
		trees, err = s.db.Trees.Search(ctx, in.Search, fetch, 0)
	} else {
		trees, err = s.db.Trees.List(ctx, fetch, 0)
	}
	if err != nil && !(errors.Is(err, db.ErrNotFound) && in.Search != "") {
		return nil, fmt.Errorf("service: list trees: %w", err)
	}
	if trees == nil {
		trees = []db.Tree{}
	}
	sortTrees(trees, in.Sort)
	filtered := trees[:0]
	for _, tree := range trees {
		if in.Cursor != nil && !afterCursor(tree.ID, *in.Cursor, in.Sort) {
			continue
		}
		filtered = append(filtered, tree)
	}
	total := len(filtered)
	hasMore := total > in.Limit
	if hasMore {
		filtered = filtered[:in.Limit]
	}
	var next *uuid.UUID
	if hasMore && len(filtered) > 0 {
		id := filtered[len(filtered)-1].ID
		next = &id
	}
	return &TreePage{Trees: filtered, NextCursor: next, HasMore: hasMore, Total: total, Limit: in.Limit}, nil
}

func (s *TreeService) GetTree(ctx context.Context, id uuid.UUID, includeStats bool) (*TreeDetail, error) {
	tree, err := s.db.Trees.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("service: get tree: %w", err)
	}
	out := &TreeDetail{Tree: *tree}
	if includeStats {
		counts, err := s.db.Nodes.GetCounts(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("service: tree counts: %w", err)
		}
		out.NodeCount = counts.ActiveNodes
		out.Depth = counts.MaxDepth
	}
	return out, nil
}

func (s *TreeService) UpdateTree(ctx context.Context, id uuid.UUID, in UpdateTreeInput) (*TreeDetail, error) {
	if in.Title == nil && in.Description == nil {
		return nil, validation("NO_FIELDS", "at least one field is required")
	}
	tree, err := s.db.Trees.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("service: get tree for update: %w", err)
	}
	if in.Title != nil {
		title := strings.TrimSpace(*in.Title)
		if title == "" {
			return nil, validation("TITLE_REQUIRED", "title is required")
		}
		if len([]rune(title)) > 200 {
			return nil, validation("TITLE_TOO_LONG", "title must not exceed 200 characters")
		}
		tree.Title = title
	}
	if in.Description != nil {
		if len([]rune(*in.Description)) > 2000 {
			return nil, validation("DESCRIPTION_TOO_LONG", "description must not exceed 2000 characters")
		}
		tree.Description = *in.Description
	}
	updated, err := s.db.Trees.Update(ctx, tree)
	if err != nil {
		return nil, fmt.Errorf("service: update tree: %w", err)
	}
	return &TreeDetail{Tree: *updated}, nil
}

func (s *TreeService) DeleteTree(ctx context.Context, id uuid.UUID) error {
	if err := s.db.Trees.SoftDelete(ctx, id); err != nil {
		return fmt.Errorf("service: delete tree: %w", err)
	}
	return nil
}
func validation(code, message string) error { return &ValidationError{Code: code, Message: message} }
func validSort(v string) bool {
	switch v {
	case "created_asc", "created_desc", "updated_asc", "updated_desc", "title_asc", "title_desc":
		return true
	}
	return false
}
func afterCursor(id, cursor uuid.UUID, order string) bool {
	cmp := strings.Compare(id.String(), cursor.String())
	return (order == "created_asc" && cmp > 0) || (order != "created_asc" && cmp < 0)
}
func sortTrees(t []db.Tree, order string) {
	sort.SliceStable(t, func(i, j int) bool {
		switch order {
		case "created_asc":
			return t[i].ID.String() < t[j].ID.String()
		case "updated_asc":
			return t[i].CreatedAt.Before(t[j].CreatedAt)
		case "updated_desc":
			return t[i].CreatedAt.After(t[j].CreatedAt)
		case "title_asc":
			return strings.ToLower(t[i].Title) < strings.ToLower(t[j].Title)
		case "title_desc":
			return strings.ToLower(t[i].Title) > strings.ToLower(t[j].Title)
		default:
			return t[i].ID.String() > t[j].ID.String()
		}
	})
}
