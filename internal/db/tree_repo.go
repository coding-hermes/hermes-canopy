// Tree repository. Pure data access — no service-layer or business
// logic. Schema-validated; see migrations/000002_trees.up.sql.

package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned by repository Get* methods when the requested
// row does not exist (or has been soft-deleted).
var ErrNotFound = errors.New("db: row not found")

// TreeRepo defines tree-scoped persistence operations.
type TreeRepo interface {
	Create(ctx context.Context, tree *Tree) (*Tree, error)
	GetByID(ctx context.Context, id uuid.UUID) (*Tree, error)
	GetByOwner(ctx context.Context, ownerID uuid.UUID) ([]Tree, error)
	Update(ctx context.Context, tree *Tree) (*Tree, error)
	SoftDelete(ctx context.Context, id uuid.UUID) error
	List(ctx context.Context, limit, offset int) ([]Tree, error)
	Search(ctx context.Context, query string, limit, offset int) ([]Tree, error)
}

// PGTreeRepo is the pgx-backed TreeRepo implementation.
type PGTreeRepo struct {
	pool *pgxpool.Pool
}

// NewPGTreeRepo wires the repo to a pgxpool. The pool is owned by the
// caller — typically the parent db.DB — and is not closed here.
func NewPGTreeRepo(pool *pgxpool.Pool) *PGTreeRepo {
	return &PGTreeRepo{pool: pool}
}

const treeColumns = `id, owner_id, title, description, root_node_id,
    metadata, created_at, edited_at, deleted_at`

// scanTree row-scans a trees row into a *Tree. Centralised here so all
// read paths stay in lockstep with the column list above.
func scanTree(row pgx.Row, t *Tree) error {
	return row.Scan(
		&t.ID, &t.OwnerID, &t.Title, &t.Description, &t.RootNodeID,
		&t.Metadata, &t.CreatedAt, &t.EditedAt, &t.DeletedAt,
	)
}

// Create inserts a new tree. ID, CreatedAt, and (optionally) Metadata
// are populated by the database defaults when zero-valued; the
// returned *Tree contains the server-assigned values.
func (r *PGTreeRepo) Create(ctx context.Context, tree *Tree) (*Tree, error) {
	if tree == nil {
		return nil, errors.New("db: tree is nil")
	}
	row := r.pool.QueryRow(ctx, `
        INSERT INTO trees (owner_id, title, description, root_node_id, metadata)
        VALUES ($1, $2, $3, $4, COALESCE($5, '{}'::jsonb))
        RETURNING `+treeColumns,
		tree.OwnerID, tree.Title, tree.Description, tree.RootNodeID, tree.Metadata,
	)
	var out Tree
	if err := scanTree(row, &out); err != nil {
		return nil, fmt.Errorf("db: insert tree: %w", err)
	}
	return &out, nil
}

// GetByID returns the active tree with the given ID. Soft-deleted
// trees are treated as not found.
func (r *PGTreeRepo) GetByID(ctx context.Context, id uuid.UUID) (*Tree, error) {
	var t Tree
	err := scanTree(r.pool.QueryRow(ctx, `
        SELECT `+treeColumns+`
        FROM trees
        WHERE id = $1 AND deleted_at IS NULL`, id), &t)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: select tree: %w", err)
	}
	return &t, nil
}

// GetByOwner returns all active trees owned by ownerID, newest first.
func (r *PGTreeRepo) GetByOwner(ctx context.Context, ownerID uuid.UUID) ([]Tree, error) {
	rows, err := r.pool.Query(ctx, `
        SELECT `+treeColumns+`
        FROM trees
        WHERE owner_id = $1 AND deleted_at IS NULL
        ORDER BY created_at DESC`, ownerID)
	if err != nil {
		return nil, fmt.Errorf("db: select trees by owner: %w", err)
	}
	defer rows.Close()

	var out []Tree
	for rows.Next() {
		var t Tree
		if err := scanTree(rows, &t); err != nil {
			return nil, fmt.Errorf("db: scan tree: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Update replaces mutable fields and bumps edited_at via SQL.
func (r *PGTreeRepo) Update(ctx context.Context, tree *Tree) (*Tree, error) {
	if tree == nil {
		return nil, errors.New("db: tree is nil")
	}
	row := r.pool.QueryRow(ctx, `
        UPDATE trees
        SET title = $2, description = $3, root_node_id = $4, metadata = $5,
            edited_at = clock_timestamp()
        WHERE id = $1 AND deleted_at IS NULL
        RETURNING `+treeColumns,
		tree.ID, tree.Title, tree.Description, tree.RootNodeID, tree.Metadata,
	)
	var out Tree
	if err := scanTree(row, &out); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("db: update tree: %w", err)
	}
	return &out, nil
}

// SoftDelete marks the tree (and via cascade, its nodes/edges) as
// deleted. Returns ErrNotFound if no active row exists.
func (r *PGTreeRepo) SoftDelete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `
        UPDATE trees
        SET deleted_at = clock_timestamp()
        WHERE id = $1 AND deleted_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("db: soft-delete tree: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// List returns a page of active trees ordered by created_at desc.
// limit is clamped to [1, 200]; offset must be >= 0.
func (r *PGTreeRepo) List(ctx context.Context, limit, offset int) ([]Tree, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := r.pool.Query(ctx, `
        SELECT `+treeColumns+`
        FROM trees
        WHERE deleted_at IS NULL
        ORDER BY created_at DESC
        LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("db: list trees: %w", err)
	}
	defer rows.Close()

	var out []Tree
	for rows.Next() {
		var t Tree
		if err := scanTree(rows, &t); err != nil {
			return nil, fmt.Errorf("db: scan tree: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Search does a case-insensitive ILIKE match on title and description.
// Empty/whitespace query returns ErrNotFound rather than an empty
// result, so callers can cheaply distinguish "no such query" from
// "no matches".
func (r *PGTreeRepo) Search(ctx context.Context, query string, limit, offset int) ([]Tree, error) {
	if query == "" {
		return nil, ErrNotFound
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	pattern := "%" + query + "%"
	rows, err := r.pool.Query(ctx, `
        SELECT `+treeColumns+`
        FROM trees
        WHERE deleted_at IS NULL
          AND (title ILIKE $1 OR description ILIKE $1)
        ORDER BY created_at DESC
        LIMIT $2 OFFSET $3`, pattern, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("db: search trees: %w", err)
	}
	defer rows.Close()

	var out []Tree
	for rows.Next() {
		var t Tree
		if err := scanTree(rows, &t); err != nil {
			return nil, fmt.Errorf("db: scan tree: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
