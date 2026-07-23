// Node repository. Implements NodeRepo with pgx against PostgreSQL.
// Single-parent rule (multi-parent only on synthesis nodes) is
// enforced in EdgeRepo.Create; this package owns node-only concerns.

package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNoActiveNode is returned when a node referenced by another
// operation has been soft-deleted. Distinct from ErrNotFound so callers
// can react differently if needed.
var ErrNoActiveNode = errors.New("db: node is soft-deleted")

// NodeRepo defines node-scoped persistence operations.
type NodeRepo interface {
	Create(ctx context.Context, node *Node) (*Node, error)
	GetByID(ctx context.Context, id uuid.UUID) (*Node, error)
	GetByTree(ctx context.Context, treeID uuid.UUID) ([]Node, error)
	GetChildren(ctx context.Context, parentID uuid.UUID) ([]Node, error)
	GetAncestors(ctx context.Context, nodeID uuid.UUID) ([]Node, error)
	GetSubtree(ctx context.Context, rootID uuid.UUID, maxDepth int) ([]Node, error)
	GetPath(ctx context.Context, fromID, toID uuid.UUID) ([]Node, error)
	Update(ctx context.Context, id uuid.UUID, content string, metadata []byte) (*Node, error)
	SoftDelete(ctx context.Context, id uuid.UUID) error
	HardDelete(ctx context.Context, id uuid.UUID) error
	GetCounts(ctx context.Context, treeID uuid.UUID) (*NodeCounts, error)
}

// PGNodeRepo is the pgx-backed NodeRepo implementation.
type PGNodeRepo struct {
	pool *pgxpool.Pool
}

// NewPGNodeRepo wires the repo to a pgxpool.
func NewPGNodeRepo(pool *pgxpool.Pool) *PGNodeRepo {
	return &PGNodeRepo{pool: pool}
}

const nodeColumns = `id, tree_id, parent_id, author_id, content,
    content_format, node_type, sequence_num, metadata, created_at,
    edited_at, deleted_at`

// scanNode centralises the column order for node row scans.
func scanNode(row pgx.Row, n *Node) error {
	return row.Scan(
		&n.ID, &n.TreeID, &n.ParentID, &n.AuthorID, &n.Content,
		&n.ContentFormat, &n.NodeType, &n.SequenceNum, &n.Metadata,
		&n.CreatedAt, &n.EditedAt, &n.DeletedAt,
	)
}

// Create inserts a new node. If the caller pre-assigns SequenceNum to
// 0 the trigger computes one; non-zero values are honored (caller's
// choice for bulk imports).
func (r *PGNodeRepo) Create(ctx context.Context, node *Node) (*Node, error) {
	if node == nil {
		return nil, errors.New("db: node is nil")
	}
	row := r.pool.QueryRow(ctx, `
        INSERT INTO nodes
            (tree_id, parent_id, author_id, content, content_format,
             node_type, sequence_num, metadata)
        VALUES ($1, $2, $3, $4, COALESCE($5, 'markdown'),
                COALESCE($6, 'message'), COALESCE(NULLIF($7, 0), NULL),
                COALESCE($8, '{}'::jsonb))
        RETURNING `+nodeColumns,
		node.TreeID, node.ParentID, node.AuthorID, node.Content,
		node.ContentFormat, node.NodeType, node.SequenceNum, node.Metadata,
	)
	var out Node
	if err := scanNode(row, &out); err != nil {
		return nil, fmt.Errorf("db: insert node: %w", err)
	}
	return &out, nil
}

// GetByID returns the active node with the given ID.
func (r *PGNodeRepo) GetByID(ctx context.Context, id uuid.UUID) (*Node, error) {
	var n Node
	err := scanNode(r.pool.QueryRow(ctx, `
        SELECT `+nodeColumns+`
        FROM nodes
        WHERE id = $1 AND deleted_at IS NULL`, id), &n)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: select node: %w", err)
	}
	return &n, nil
}

// GetByTree returns all active nodes in a tree, ordered by sequence_num.
func (r *PGNodeRepo) GetByTree(ctx context.Context, treeID uuid.UUID) ([]Node, error) {
	rows, err := r.pool.Query(ctx, `
        SELECT `+nodeColumns+`
        FROM nodes
        WHERE tree_id = $1 AND deleted_at IS NULL
        ORDER BY sequence_num ASC`, treeID)
	if err != nil {
		return nil, fmt.Errorf("db: select nodes by tree: %w", err)
	}
	defer rows.Close()
	return collectNodes(rows)
}

// GetChildren returns the active children of the given parent,
// ordered by edge.sequence_num then node.sequence_num.
func (r *PGNodeRepo) GetChildren(ctx context.Context, parentID uuid.UUID) ([]Node, error) {
	rows, err := r.pool.Query(ctx, `
        SELECT `+nodeColumns+`
        FROM nodes n
        JOIN edges e ON e.target_id = n.id
        WHERE e.source_id = $1
          AND e.deleted_at IS NULL
          AND n.deleted_at IS NULL
        ORDER BY e.sequence_num ASC, n.sequence_num ASC`, parentID)
	if err != nil {
		return nil, fmt.Errorf("db: select children: %w", err)
	}
	defer rows.Close()
	return collectNodes(rows)
}

// GetAncestors walks parent_id up to the root using a recursive CTE.
// Result includes the input node (index 0) up to the root (last).
func (r *PGNodeRepo) GetAncestors(ctx context.Context, nodeID uuid.UUID) ([]Node, error) {
	rows, err := r.pool.Query(ctx, `
        WITH RECURSIVE chain AS (
            SELECT `+nodeColumns+`
            FROM nodes
            WHERE id = $1 AND deleted_at IS NULL
            UNION ALL
            SELECT n.`+splitCols()+`
            FROM nodes n
            JOIN chain c ON n.id = c.parent_id
            WHERE n.deleted_at IS NULL
        )
        SELECT `+nodeColumns+`
        FROM chain
        ORDER BY sequence_num ASC`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("db: select ancestors: %w", err)
	}
	defer rows.Close()
	return collectNodes(rows)
}

// GetSubtree returns all descendants of rootID up to maxDepth levels
// below it. maxDepth == 0 means "unbounded" — caller is responsible
// for safety on large subtrees.
func (r *PGNodeRepo) GetSubtree(ctx context.Context, rootID uuid.UUID, maxDepth int) ([]Node, error) {
	if maxDepth < 0 {
		maxDepth = 0
	}
	rows, err := r.pool.Query(ctx, `
        WITH RECURSIVE sub AS (
            SELECT `+nodeColumns+`, 0 AS depth
            FROM nodes
            WHERE id = $1 AND deleted_at IS NULL
            UNION ALL
            SELECT n.`+splitCols()+`, sub.depth + 1
            FROM nodes n
            JOIN edges e ON e.target_id = n.id
            JOIN sub ON sub.id = e.source_id
            WHERE e.deleted_at IS NULL
              AND n.deleted_at IS NULL
              AND ($2::int = 0 OR sub.depth + 1 <= $2)
        )
        SELECT `+nodeColumns+`
        FROM sub
        ORDER BY sequence_num ASC`, rootID, maxDepth)
	if err != nil {
		return nil, fmt.Errorf("db: select subtree: %w", err)
	}
	defer rows.Close()
	return collectNodes(rows)
}

// GetPath returns the nodes on the path between two nodes (inclusive).
// Implemented via repeated CTE-driven ancestor walks — both inputs must
// be reachable from a shared root. If they are not, ErrNotFound is
// returned rather than an empty slice.
func (r *PGNodeRepo) GetPath(ctx context.Context, fromID, toID uuid.UUID) ([]Node, error) {
	var lca uuid.UUID
	err := r.pool.QueryRow(ctx, `
        WITH RECURSIVE up_from AS (
            SELECT id, parent_id FROM nodes WHERE id = $1 AND deleted_at IS NULL
            UNION ALL
            SELECT n.id, n.parent_id FROM nodes n JOIN up_from u ON n.id = u.parent_id
            WHERE n.deleted_at IS NULL
        ),
        up_to AS (
            SELECT id, parent_id FROM nodes WHERE id = $2 AND deleted_at IS NULL
            UNION ALL
            SELECT n.id, n.parent_id FROM nodes n JOIN up_to t ON n.id = t.parent_id
            WHERE n.deleted_at IS NULL
        )
        SELECT up_from.id
        FROM up_from
        JOIN up_to ON up_from.id = up_to.id
        ORDER BY up_from.id DESC
        LIMIT 1`, fromID, toID).Scan(&lca)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: find lca: %w", err)
	}

	ancFrom, err := r.GetAncestors(ctx, fromID)
	if err != nil {
		return nil, fmt.Errorf("db: ancestors from: %w", err)
	}
	ancTo, err := r.GetAncestors(ctx, toID)
	if err != nil {
		return nil, fmt.Errorf("db: ancestors to: %w", err)
	}

	lcaIdx := -1
	for i, n := range ancFrom {
		if n.ID == lca {
			lcaIdx = i
			break
		}
	}
	if lcaIdx == -1 {
		return nil, ErrNotFound
	}

	toIdx := 0
	for i, n := range ancTo {
		if n.ID == lca {
			toIdx = i
			break
		}
	}

	path := make([]Node, 0, lcaIdx+toIdx+1)
	for i := lcaIdx; i >= 0; i-- {
		path = append(path, ancFrom[i])
	}
	for i := toIdx - 1; i >= 0; i-- {
		path = append(path, ancTo[i])
	}
	return path, nil
}

// Update changes a node's content and metadata. Sets edited_at via the
// trg_node_edited_at trigger (only when content or metadata actually
// change).
func (r *PGNodeRepo) Update(ctx context.Context, id uuid.UUID, content string, metadata []byte) (*Node, error) {
	row := r.pool.QueryRow(ctx, `
        UPDATE nodes
        SET content = $2, metadata = COALESCE($3, '{}'::jsonb)
        WHERE id = $1 AND deleted_at IS NULL
        RETURNING `+nodeColumns,
		id, content, metadata,
	)
	var n Node
	if err := scanNode(row, &n); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("db: update node: %w", err)
	}
	return &n, nil
}

// SoftDelete marks the node as deleted; the FK ON DELETE SET NULL on
// parent_id means children survive with their parent pointer cleared.
func (r *PGNodeRepo) SoftDelete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `
        UPDATE nodes
        SET deleted_at = clock_timestamp()
        WHERE id = $1 AND deleted_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("db: soft-delete node: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// HardDelete permanently removes a node. The caller must verify there
// are no active children — this method does NOT cascade.
func (r *PGNodeRepo) HardDelete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM nodes WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("db: hard-delete node: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetCounts aggregates per-tree counts in a single query. TotalDepth
// is computed via a recursive CTE walking the parent chain.
func (r *PGNodeRepo) GetCounts(ctx context.Context, treeID uuid.UUID) (*NodeCounts, error) {
	c := &NodeCounts{TreeID: treeID}
	err := r.pool.QueryRow(ctx, `
        WITH tree_nodes AS (
            SELECT id, parent_id, deleted_at
            FROM nodes
            WHERE tree_id = $1
        ),
        depths AS (
            SELECT id, 0 AS depth
            FROM tree_nodes
            WHERE parent_id IS NULL
            UNION ALL
            SELECT n.id, d.depth + 1
            FROM tree_nodes n
            JOIN depths d ON n.parent_id = d.id
        ),
        node_tot AS (SELECT COUNT(*)::bigint AS total, COUNT(*) FILTER (WHERE deleted_at IS NULL)::bigint AS active FROM tree_nodes),
        edge_tot AS (
            SELECT COUNT(*)::bigint AS total,
                   COUNT(*) FILTER (WHERE deleted_at IS NULL)::bigint AS active
            FROM edges WHERE tree_id = $1
        )
        SELECT node_tot.total, node_tot.active, edge_tot.total, edge_tot.active,
               COALESCE(MAX(d.depth), 0)::int
        FROM node_tot, edge_tot, depths d
        GROUP BY node_tot.total, node_tot.active, edge_tot.total, edge_tot.active`,
		treeID,
	).Scan(&c.TotalNodes, &c.ActiveNodes, &c.TotalEdges, &c.ActiveEdges, &c.MaxDepth)
	if errors.Is(err, pgx.ErrNoRows) {
		// Empty tree — zero counts.
		c.TotalNodes = 0
		c.ActiveNodes = 0
		c.TotalEdges = 0
		c.ActiveEdges = 0
		c.MaxDepth = 0
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db: count nodes: %w", err)
	}
	return c, nil
}

// collectNodes drains a pgx.Rows into a []Node slice. Any scan error
// is returned; callers wrap with their context.
func collectNodes(rows pgx.Rows) ([]Node, error) {
	var out []Node
	for rows.Next() {
		var n Node
		if err := scanNode(rows, &n); err != nil {
			return nil, fmt.Errorf("db: scan node: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// splitCols returns the column list with the prefix-removed aliases for
// use in recursive CTE inner selects. Centralised to keep CTE SELECTs
// in lockstep with nodeColumns.
func splitCols() string {
	// Returns "id AS id, tree_id AS tree_id, ..." — ugly but unambiguous.
	// pgx accepts bare column references in recursive CTE selections.
	const c = `id, tree_id, parent_id, author_id, content, content_format,
        node_type, sequence_num, metadata, created_at, edited_at, deleted_at`
	return c
}
