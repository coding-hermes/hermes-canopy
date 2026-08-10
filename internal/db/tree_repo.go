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
	// Count returns the number of active (non-soft-deleted) trees. Used
	// by the service to report a REAL total in ListTrees instead of the
	// fetched window size. PAG-002.
	Count(ctx context.Context) (int, error)
	// ListKeyset returns a page of active trees ordered by
	// (created_at DESC, id DESC). When cursorID is nil, the first page
	// is returned. When non-nil, only rows strictly before (newer
	// created_at wins, id DESC tiebreak) the cursor row are returned —
	// Postgres row-value comparison makes this gap-free. limit is the
	// max page size (NOT limit+1; the caller passes its own window).
	// PAG-002.
	ListKeyset(ctx context.Context, cursorID *uuid.UUID, limit int) ([]Tree, error)
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

// Count returns the number of active (non-soft-deleted) trees. This is
// the REAL total used by ListTrees pagination metadata. PAG-002.
func (r *PGTreeRepo) Count(ctx context.Context) (int, error) {
	var n int
	if err := r.pool.QueryRow(ctx,
		`SELECT count(*)::int FROM trees WHERE deleted_at IS NULL`,
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("db: count trees: %w", err)
	}
	return n, nil
}

// ListKeyset returns a page of active trees using keyset (cursor)
// pagination on (created_at DESC, id DESC). When cursorID is nil the
// first page is returned; when non-nil, only rows strictly before the
// cursor row (by the same ordering) are returned via Postgres row-value
// comparison. The (created_at, id) tiebreak makes pages deterministic
// and gap-free even when created_at values collide (UUIDv7 ids are
// time-ordered but created_at can still collide under rapid inserts).
// limit is clamped to [1, 200]. PAG-002.
func (r *PGTreeRepo) ListKeyset(ctx context.Context, cursorID *uuid.UUID, limit int) ([]Tree, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows pgx.Rows
	var err error
	if cursorID == nil {
		rows, err = r.pool.Query(ctx, `
            SELECT `+treeColumns+`
            FROM trees
            WHERE deleted_at IS NULL
            ORDER BY created_at DESC, id DESC
            LIMIT $1`, limit)
	} else {
		rows, err = r.pool.Query(ctx, `
            SELECT `+treeColumns+`
            FROM trees
            WHERE deleted_at IS NULL
              AND (created_at, id) < (
                  SELECT created_at, id FROM trees WHERE id = $1
              )
            ORDER BY created_at DESC, id DESC
            LIMIT $2`, *cursorID, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("db: list trees keyset: %w", err)
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
