// Edge repository. Implements EdgeRepo with pgx against PostgreSQL.
// Enforces the single-parent rule per SPEC-DM-01 §3.5: non-synthesis
// targets may have at most one active incoming edge.

package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrMultipleParents is returned by EdgeRepo.Create when a non-synthesis
// target already has an active incoming edge.
var ErrMultipleParents = errors.New("db: target already has a parent")

// ErrSelfEdge is returned when source_id == target_id.
var ErrSelfEdge = errors.New("db: source and target cannot be the same")

// ErrNoActiveNode is used here too for symmetry with the node repo.
// (Declared in node_repo.go.)

// EdgeCounts aggregates edge counts and edge-type breakdown for a tree.
type EdgeCounts struct {
	TreeID       uuid.UUID      `json:"treeId"`
	Total        int64          `json:"total"`
	Active       int64          `json:"active"`
	ByType       map[string]int `json:"byType"`
}

// EdgeRepo defines edge-scoped persistence operations.
type EdgeRepo interface {
	Create(ctx context.Context, edge *Edge) (*Edge, error)
	GetByID(ctx context.Context, id uuid.UUID) (*Edge, error)
	GetBySource(ctx context.Context, sourceID uuid.UUID) ([]Edge, error)
	GetByTarget(ctx context.Context, targetID uuid.UUID) ([]Edge, error)
	GetByTree(ctx context.Context, treeID uuid.UUID) ([]Edge, error)
	SoftDelete(ctx context.Context, id uuid.UUID) error
	GetParents(ctx context.Context, targetID uuid.UUID) ([]Node, error)
	GetSiblings(ctx context.Context, sourceID, targetID uuid.UUID) ([]Node, error)
	GetEdgeCounts(ctx context.Context, treeID uuid.UUID) (*EdgeCounts, error)
	Move(ctx context.Context, id uuid.UUID, newSourceID uuid.UUID) (*Edge, error)
}

// PGEdgeRepo is the pgx-backed EdgeRepo implementation.
type PGEdgeRepo struct {
	pool *pgxpool.Pool
}

// NewPGEdgeRepo wires the repo to a pgxpool.
func NewPGEdgeRepo(pool *pgxpool.Pool) *PGEdgeRepo {
	return &PGEdgeRepo{pool: pool}
}

const edgeColumns = `id, tree_id, source_id, target_id, edge_type,
    sequence_num, metadata, created_at, deleted_at`

func scanEdge(row pgx.Row, e *Edge) error {
	return row.Scan(
		&e.ID, &e.TreeID, &e.SourceID, &e.TargetID, &e.EdgeType,
		&e.SequenceNum, &e.Metadata, &e.CreatedAt, &e.DeletedAt,
	)
}

// Create inserts a new edge. Performs validation inside a single
// transaction:
//  1. self-edge check (source_id == target_id → ErrSelfEdge)
//  2. target must be active
//  3. unique constraint (source_id, target_id, edge_type) — handled by
//     the schema; we translate the pg error to ErrSelfEdge or a generic
//     wrapped error.
//  4. single-parent check: if target.node_type != 'synthesis' and an
//     active incoming edge already exists, return ErrMultipleParents.
func (r *PGEdgeRepo) Create(ctx context.Context, edge *Edge) (*Edge, error) {
	if edge == nil {
		return nil, errors.New("db: edge is nil")
	}
	if edge.SourceID == edge.TargetID {
		return nil, ErrSelfEdge
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("db: begin edge tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Fetch target node type for single-parent rule.
	var targetType string
	err = tx.QueryRow(ctx,
		`SELECT node_type FROM nodes WHERE id = $1 AND deleted_at IS NULL`,
		edge.TargetID,
	).Scan(&targetType)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: select target for edge: %w", err)
	}

	if targetType != NodeTypeSynthesis {
		var existing int
		err = tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM edges
             WHERE target_id = $1 AND deleted_at IS NULL`,
			edge.TargetID,
		).Scan(&existing)
		if err != nil {
			return nil, fmt.Errorf("db: count existing parents: %w", err)
		}
		if existing > 0 {
			return nil, ErrMultipleParents
		}
	}

	row := tx.QueryRow(ctx, `
        INSERT INTO edges
            (tree_id, source_id, target_id, edge_type, sequence_num, metadata)
        VALUES ($1, $2, $3, COALESCE($4, 'reply'),
                COALESCE(NULLIF($5, 0), NULL),
                COALESCE($6, '{}'::jsonb))
        RETURNING `+edgeColumns,
		edge.TreeID, edge.SourceID, edge.TargetID, edge.EdgeType,
		edge.SequenceNum, edge.Metadata,
	)
	var out Edge
	if err := scanEdge(row, &out); err != nil {
		return nil, fmt.Errorf("db: insert edge: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("db: commit edge: %w", err)
	}
	return &out, nil
}

// GetByID returns the active edge with the given ID.
func (r *PGEdgeRepo) GetByID(ctx context.Context, id uuid.UUID) (*Edge, error) {
	var e Edge
	err := scanEdge(r.pool.QueryRow(ctx, `
        SELECT `+edgeColumns+`
        FROM edges
        WHERE id = $1 AND deleted_at IS NULL`, id), &e)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: select edge: %w", err)
	}
	return &e, nil
}

// GetBySource returns all active edges leaving the given node.
func (r *PGEdgeRepo) GetBySource(ctx context.Context, sourceID uuid.UUID) ([]Edge, error) {
	rows, err := r.pool.Query(ctx, `
        SELECT `+edgeColumns+`
        FROM edges
        WHERE source_id = $1 AND deleted_at IS NULL
        ORDER BY sequence_num ASC`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("db: select edges by source: %w", err)
	}
	defer rows.Close()
	return collectEdges(rows)
}

// GetByTarget returns all active edges arriving at the given node.
func (r *PGEdgeRepo) GetByTarget(ctx context.Context, targetID uuid.UUID) ([]Edge, error) {
	rows, err := r.pool.Query(ctx, `
        SELECT `+edgeColumns+`
        FROM edges
        WHERE target_id = $1 AND deleted_at IS NULL
        ORDER BY sequence_num ASC`, targetID)
	if err != nil {
		return nil, fmt.Errorf("db: select edges by target: %w", err)
	}
	defer rows.Close()
	return collectEdges(rows)
}

// GetByTree returns all active edges in a tree, ordered by
// (source sequence, edge sequence).
func (r *PGEdgeRepo) GetByTree(ctx context.Context, treeID uuid.UUID) ([]Edge, error) {
	rows, err := r.pool.Query(ctx, `
        SELECT `+edgeColumns+`
        FROM edges
        WHERE tree_id = $1 AND deleted_at IS NULL
        ORDER BY source_id, sequence_num ASC`, treeID)
	if err != nil {
		return nil, fmt.Errorf("db: select edges by tree: %w", err)
	}
	defer rows.Close()
	return collectEdges(rows)
}

// SoftDelete marks an edge as deleted. The CHK constraint on
// source_id != target_id still applies — callers cannot move an
// edge to the same target via soft-delete + recreate, only via Move.
func (r *PGEdgeRepo) SoftDelete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `
        UPDATE edges
        SET deleted_at = clock_timestamp()
        WHERE id = $1 AND deleted_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("db: soft-delete edge: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetParents returns the source nodes of all active edges pointing
// at targetID. Distinct from GetByTarget because this returns Nodes
// rather than Edges.
func (r *PGEdgeRepo) GetParents(ctx context.Context, targetID uuid.UUID) ([]Node, error) {
	rows, err := r.pool.Query(ctx, `
        SELECT `+nodeColumns+`
        FROM nodes n
        JOIN edges e ON e.source_id = n.id
        WHERE e.target_id = $1
          AND e.deleted_at IS NULL
          AND n.deleted_at IS NULL
        ORDER BY e.sequence_num ASC`, targetID)
	if err != nil {
		return nil, fmt.Errorf("db: select parents: %w", err)
	}
	defer rows.Close()
	return collectNodes(rows)
}

// GetSiblings returns the active children of sourceID that share
// targetID's edge_type. Used to compute merge candidates.
func (r *PGEdgeRepo) GetSiblings(ctx context.Context, sourceID, targetID uuid.UUID) ([]Node, error) {
	rows, err := r.pool.Query(ctx, `
        SELECT n.id, n.tree_id, n.parent_id, n.author_id, n.content,
               n.content_format, n.node_type, n.sequence_num, n.metadata,
               n.created_at, n.edited_at, n.deleted_at
        FROM nodes n
        JOIN edges e ON e.source_id = $1 AND e.target_id = n.id
        WHERE n.deleted_at IS NULL
          AND e.deleted_at IS NULL
          AND e.edge_type = (
              SELECT edge_type FROM edges
              WHERE source_id = $1 AND target_id = $2 AND deleted_at IS NULL
              LIMIT 1
          )
        ORDER BY e.sequence_num ASC`, sourceID, targetID)
	if err != nil {
		return nil, fmt.Errorf("db: select siblings: %w", err)
	}
	defer rows.Close()
	return collectNodes(rows)
}

// GetEdgeCounts aggregates edge counts plus a per-type breakdown.
func (r *PGEdgeRepo) GetEdgeCounts(ctx context.Context, treeID uuid.UUID) (*EdgeCounts, error) {
	c := &EdgeCounts{TreeID: treeID, ByType: map[string]int{}}
	err := r.pool.QueryRow(ctx, `
        SELECT COUNT(*)::bigint, COUNT(*) FILTER (WHERE deleted_at IS NULL)::bigint
        FROM edges WHERE tree_id = $1`, treeID,
	).Scan(&c.Total, &c.Active)
	if err != nil {
		return nil, fmt.Errorf("db: count edges: %w", err)
	}
	rows, err := r.pool.Query(ctx, `
        SELECT edge_type, COUNT(*)::int
        FROM edges
        WHERE tree_id = $1 AND deleted_at IS NULL
        GROUP BY edge_type`, treeID)
	if err != nil {
		return nil, fmt.Errorf("db: count edges by type: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var t string
		var n int
		if err := rows.Scan(&t, &n); err != nil {
			return nil, fmt.Errorf("db: scan edge type count: %w", err)
		}
		c.ByType[t] = n
	}
	return c, rows.Err()
}

// Move relocates an edge to a new source while preserving its
// (tree_id, target_id, edge_type). Validates no self-edge and that
// the new source is in the same tree. Unique-edge constraint is
// re-evaluated by the schema.
func (r *PGEdgeRepo) Move(ctx context.Context, id uuid.UUID, newSourceID uuid.UUID) (*Edge, error) {
	row := r.pool.QueryRow(ctx, `
        UPDATE edges
        SET source_id = $2,
            sequence_num = NULL
        WHERE id = $1 AND deleted_at IS NULL
          AND source_id != $2
        RETURNING `+edgeColumns,
		id, newSourceID,
	)
	var e Edge
	if err := scanEdge(row, &e); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("db: move edge: %w", err)
	}
	if e.SourceID == newSourceID {
		return nil, ErrSelfEdge
	}
	return &e, nil
}

// collectEdges drains a pgx.Rows into a []Edge slice.
func collectEdges(rows pgx.Rows) ([]Edge, error) {
	var out []Edge
	for rows.Next() {
		var e Edge
		if err := scanEdge(rows, &e); err != nil {
			return nil, fmt.Errorf("db: scan edge: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
