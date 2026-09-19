package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

// joinNodeColumns is nodeColumns qualified with the `n` alias used by the join-based node
// reads (children, edge parents, edge siblings).
const joinNodeColumns = `id, n.tree_id, n.parent_id, n.author_id, n.content, n.content_format,
    n.node_type, n.sequence_num, n.metadata, n.created_at, n.edited_at, n.deleted_at`

// edgeColumns mirrors PGEdgeRepo's column list.
const edgeColumns = `id, tree_id, source_id, target_id, edge_type,
    sequence_num, metadata, created_at, deleted_at`

// EdgeRepo is the modernc.org/sqlite + database/sql implementation of the driver-agnostic
// half of db.EdgeRepo: Create, GetByID, GetBySource, GetByTarget, GetByTree, SoftDelete,
// GetParents, GetSiblings, GetEdgeCounts, Move and GetActiveReferenceParents. The other
// three methods of that interface — CreateReferenceSet, GetActiveIncoming and
// ValidateIncomingInvariant — take a concrete pgx.Tx, so satisfying them would mean editing
// a public interface shared with the PostgreSQL repositories. That is out of this slice, and
// no stub is provided: a method that "exists" but always fails would make a future
// `var repo db.EdgeRepo = sqlite.NewEdgeRepo(s)` compile and then fail at runtime, which is
// worse than the compile error.
//
// TestSQLiteEdgeRepoBoundaryIsDerivedFromInterface derives the implemented set from
// db.EdgeRepo itself — every method whose signature mentions pgx must be missing, every
// other method must be present — so the boundary cannot silently drift when the interface
// changes.
type EdgeRepo struct{ q *sql.DB }

// NewEdgeRepo binds the repository to an open Store; see NewTreeRepo for the ownership and
// nil-store contract.
func NewEdgeRepo(s *Store) *EdgeRepo {
	if s == nil {
		return &EdgeRepo{}
	}
	return &EdgeRepo{q: s.DB()}
}

// conn returns the pool or ErrNoStore.
func (r *EdgeRepo) conn() (*sql.DB, error) {
	if r == nil || r.q == nil {
		return nil, ErrNoStore
	}
	return r.q, nil
}

// scanEdge centralises the edges column order.
func scanEdge(row rowScanner, e *db.Edge) error {
	var (
		id, treeID, sourceID, targetID string
		metadata                       []byte
		createdAt                      sql.NullString
		deletedAt                      sql.NullString
	)
	if err := row.Scan(&id, &treeID, &sourceID, &targetID, &e.EdgeType, &e.SequenceNum,
		&metadata, &createdAt, &deletedAt); err != nil {
		return err
	}
	edgeID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("edges.id %q: %w", id, err)
	}
	e.ID = edgeID
	if e.TreeID, err = uuid.Parse(treeID); err != nil {
		return fmt.Errorf("edges.tree_id %q: %w", treeID, err)
	}
	if e.SourceID, err = uuid.Parse(sourceID); err != nil {
		return fmt.Errorf("edges.source_id %q: %w", sourceID, err)
	}
	if e.TargetID, err = uuid.Parse(targetID); err != nil {
		return fmt.Errorf("edges.target_id %q: %w", targetID, err)
	}
	e.Metadata = metadata
	if e.CreatedAt, err = requiredTime("edges.created_at", createdAt); err != nil {
		return err
	}
	e.DeletedAt, err = optionalTime("edges.deleted_at", deletedAt)
	return err
}

// Create inserts an edge after validating the target's parent rules, in one transaction.
//
// The rules are PGEdgeRepo.Create's §12.1 matrix, narrowed to the branch this schema can
// express: PostgreSQL reads the target's `parent_mode` to tell a `multi_reference` message
// from a `lineage` one, and that column arrives with migration 000047, which is not in the
// wave-1 SQLite batch. Every node in this store is therefore 'lineage' (its PostgreSQL
// default), so:
//
//   - a `system` target                → db.ErrSystemNodeParentForbidden
//   - a `synthesis` target with a `reference` edge → db.ErrReferenceTargetType
//     (other edge types keep SPEC-API-04's multi-parent synthesis behaviour)
//   - any target with a `reference` edge → db.ErrReferenceRequiresMultiReferenceMode,
//     because only the (out-of-slice) CreateReferenceSet can build a complete 2-20 set
//   - otherwise at most one active incoming edge → db.ErrMultipleParents
//
// A missing or soft-deleted target is db.ErrNotFound and a self-edge is db.ErrSelfEdge,
// before any write. Uniqueness of (source_id, target_id, edge_type) stays the schema's job.
//
// `sequence_num` is a NOT NULL column with no default, so it is allocated here: the
// tree-wide `COALESCE(MAX(sequence_num), 0) + 1` counter that PGEdgeRepo.CreateReferenceSet
// documents as "the convention used by the node service's reply/fork writes", computed
// inside this statement's transaction. A caller-supplied non-zero sequence is honored.
func (r *EdgeRepo) Create(ctx context.Context, edge *db.Edge) (*db.Edge, error) {
	if edge == nil {
		return nil, errors.New("db: edge is nil")
	}
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	if edge.SourceID == edge.TargetID {
		return nil, db.ErrSelfEdge
	}

	tx, err := q.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("db: begin edge tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// The target's type drives the matrix; a missing row is "no such target", not a
	// zero-value node type.
	var targetType string
	err = tx.QueryRowContext(ctx,
		`SELECT node_type FROM nodes WHERE id = ? AND deleted_at IS NULL`,
		idArg(edge.TargetID)).Scan(&targetType)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, db.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: select target for edge: %w", err)
	}

	switch {
	case targetType == db.NodeTypeSystem:
		return nil, db.ErrSystemNodeParentForbidden
	case targetType == db.NodeTypeSynthesis:
		if edge.EdgeType == db.EdgeTypeReference {
			return nil, db.ErrReferenceTargetType
		}
	case edge.EdgeType == db.EdgeTypeReference:
		return nil, db.ErrReferenceRequiresMultiReferenceMode
	default: // message, parent_mode='lineage'
		var existing int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM edges WHERE target_id = ? AND deleted_at IS NULL`,
			idArg(edge.TargetID)).Scan(&existing); err != nil {
			return nil, fmt.Errorf("db: count existing parents: %w", err)
		}
		if existing > 0 {
			return nil, db.ErrMultipleParents
		}
	}

	edgeType := edge.EdgeType
	if edgeType == "" {
		edgeType = db.EdgeTypeReply
	}
	sequence := edge.SequenceNum
	if sequence == 0 {
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(sequence_num), 0) + 1 FROM edges WHERE tree_id = ?`,
			idArg(edge.TreeID)).Scan(&sequence); err != nil {
			return nil, fmt.Errorf("db: alloc edge sequence: %w", err)
		}
	}

	id, err := NewID()
	if err != nil {
		return nil, err
	}
	row := tx.QueryRowContext(ctx, `
        INSERT INTO edges
            (id, tree_id, source_id, target_id, edge_type, sequence_num, metadata)
        VALUES (?, ?, ?, ?, ?, ?, ?)
        RETURNING `+edgeColumns,
		idArg(id), idArg(edge.TreeID), idArg(edge.SourceID), idArg(edge.TargetID),
		edgeType, sequence, jsonText(edge.Metadata),
	)
	var out db.Edge
	if err := scanEdge(row, &out); err != nil {
		return nil, fmt.Errorf("db: insert edge: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("db: commit edge: %w", err)
	}
	return &out, nil
}

// GetByID returns the active edge with the given ID.
func (r *EdgeRepo) GetByID(ctx context.Context, id uuid.UUID) (*db.Edge, error) {
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	var e db.Edge
	err = scanEdge(q.QueryRowContext(ctx, `
        SELECT `+edgeColumns+`
        FROM edges
        WHERE id = ? AND deleted_at IS NULL`, idArg(id)), &e)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, db.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: select edge: %w", err)
	}
	return &e, nil
}

// GetBySource returns the active edges leaving a node, ordered by sequence_num.
func (r *EdgeRepo) GetBySource(ctx context.Context, sourceID uuid.UUID) ([]db.Edge, error) {
	return r.listEdges(ctx, `
        SELECT `+edgeColumns+`
        FROM edges
        WHERE source_id = ? AND deleted_at IS NULL
        ORDER BY sequence_num ASC`, sourceID)
}

// GetByTarget returns the active edges arriving at a node, ordered by sequence_num.
func (r *EdgeRepo) GetByTarget(ctx context.Context, targetID uuid.UUID) ([]db.Edge, error) {
	return r.listEdges(ctx, `
        SELECT `+edgeColumns+`
        FROM edges
        WHERE target_id = ? AND deleted_at IS NULL
        ORDER BY sequence_num ASC`, targetID)
}

// GetByTree returns the active edges of a tree ordered by (source_id, sequence_num).
func (r *EdgeRepo) GetByTree(ctx context.Context, treeID uuid.UUID) ([]db.Edge, error) {
	return r.listEdges(ctx, `
        SELECT `+edgeColumns+`
        FROM edges
        WHERE tree_id = ? AND deleted_at IS NULL
        ORDER BY source_id, sequence_num ASC`, treeID)
}

// listEdges runs a one-argument edge query and drains it; the three Get*By lookups differ
// only in their WHERE/ORDER BY.
func (r *EdgeRepo) listEdges(ctx context.Context, query string, arg uuid.UUID) ([]db.Edge, error) {
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, query, idArg(arg))
	if err != nil {
		return nil, fmt.Errorf("db: list edges: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectEdges(rows)
}

// SoftDelete marks an edge deleted and reports ErrNotFound when no active row matches.
func (r *EdgeRepo) SoftDelete(ctx context.Context, id uuid.UUID) error {
	q, err := r.conn()
	if err != nil {
		return err
	}
	res, err := q.ExecContext(ctx, `
        UPDATE edges
        SET deleted_at = `+sqlNow+`
        WHERE id = ? AND deleted_at IS NULL`, idArg(id))
	if err != nil {
		return fmt.Errorf("db: soft-delete edge: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("db: soft-delete edge: %w", err)
	}
	if n == 0 {
		return db.ErrNotFound
	}
	return nil
}

// GetParents returns the source nodes of the active edges pointing at targetID.
func (r *EdgeRepo) GetParents(ctx context.Context, targetID uuid.UUID) ([]db.Node, error) {
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `
        SELECT n.`+joinNodeColumns+`
        FROM nodes n
        JOIN edges e ON e.source_id = n.id
        WHERE e.target_id = ?
          AND e.deleted_at IS NULL
          AND n.deleted_at IS NULL
        ORDER BY e.sequence_num ASC`, idArg(targetID))
	if err != nil {
		return nil, fmt.Errorf("db: select parents: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectNodes(rows)
}

// GetSiblings returns the active children of sourceID that share targetID's edge type.
func (r *EdgeRepo) GetSiblings(ctx context.Context, sourceID, targetID uuid.UUID) ([]db.Node, error) {
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `
        SELECT n.`+joinNodeColumns+`
        FROM nodes n
        JOIN edges e ON e.source_id = ? AND e.target_id = n.id
        WHERE n.deleted_at IS NULL
          AND e.deleted_at IS NULL
          AND e.edge_type = (
              SELECT edge_type FROM edges
              WHERE source_id = ? AND target_id = ? AND deleted_at IS NULL
              LIMIT 1
          )
        ORDER BY e.sequence_num ASC`, idArg(sourceID), idArg(sourceID), idArg(targetID))
	if err != nil {
		return nil, fmt.Errorf("db: select siblings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectNodes(rows)
}

// GetEdgeCounts aggregates edge counts plus a per-type breakdown. COUNT(*) FILTER (WHERE …)
// is native in SQLite ≥ 3.30, so the PostgreSQL statement is kept verbatim.
func (r *EdgeRepo) GetEdgeCounts(ctx context.Context, treeID uuid.UUID) (*db.EdgeCounts, error) {
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	c := &db.EdgeCounts{TreeID: treeID, ByType: map[string]int{}}
	if err := q.QueryRowContext(ctx, `
        SELECT COUNT(*), COUNT(*) FILTER (WHERE deleted_at IS NULL)
        FROM edges WHERE tree_id = ?`, idArg(treeID),
	).Scan(&c.Total, &c.Active); err != nil {
		return nil, fmt.Errorf("db: count edges: %w", err)
	}
	rows, err := q.QueryContext(ctx, `
        SELECT edge_type, COUNT(*)
        FROM edges
        WHERE tree_id = ? AND deleted_at IS NULL
        GROUP BY edge_type`, idArg(treeID))
	if err != nil {
		return nil, fmt.Errorf("db: count edges by type: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			edgeType string
			n        int
		)
		if err := rows.Scan(&edgeType, &n); err != nil {
			return nil, fmt.Errorf("db: scan edge type count: %w", err)
		}
		c.ByType[edgeType] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: count edges by type: %w", err)
	}
	return c, nil
}

// Move relocates an edge to a new source within the same tree, preserving its
// (tree_id, target_id, edge_type) and allocating a fresh sequence_num for the new source —
// PGEdgeRepo's statement, including its `source_id != ?` guard, so moving an edge to the
// source it already has reports ErrNotFound rather than a silent no-op.
//
// Two notes on error shapes. A self-edge (new source == target) is rejected by the same
// chk_no_self_edge CHECK the PostgreSQL path relies on, but is reported as db.ErrSelfEdge so
// callers get the sentinel EdgeRepo.Create already returns instead of matching a constraint
// string. A source node from a DIFFERENT tree is not rejected: PostgreSQL's implementation
// documents that validation without performing it, the schema carries no cross-tree
// constraint, and this port preserves that behaviour rather than diverging silently. The
// edge's tree_id is untouched either way, so the FK to trees stays satisfied.
func (r *EdgeRepo) Move(ctx context.Context, id uuid.UUID, newSourceID uuid.UUID) (*db.Edge, error) {
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	var targetID string
	err = q.QueryRowContext(ctx,
		`SELECT target_id FROM edges WHERE id = ? AND deleted_at IS NULL`, idArg(id)).Scan(&targetID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, db.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: move edge: %w", err)
	}
	if targetID == idArg(newSourceID) {
		return nil, db.ErrSelfEdge
	}

	res, err := q.ExecContext(ctx, `
        UPDATE edges
        SET source_id = ?,
            sequence_num = (SELECT COALESCE(MAX(sequence_num), 0) + 1
                            FROM edges WHERE source_id = ? AND deleted_at IS NULL)
        WHERE id = ? AND deleted_at IS NULL
          AND source_id != ?`, idArg(newSourceID), idArg(newSourceID), idArg(id), idArg(newSourceID))
	if err != nil {
		return nil, fmt.Errorf("db: move edge: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("db: move edge: %w", err)
	}
	if n == 0 {
		return nil, db.ErrNotFound
	}
	return r.GetByID(ctx, id)
}

// GetActiveReferenceParents returns the target's active `reference` parents ordered by
// metadata.selection_order, then sequence_num as a defensive tie-breaker — the PostgreSQL
// statement with `metadata->>'selection_order'` translated to
// `json_extract(metadata,'$.selection_order')` (docs/SQLITE-PIVOT.md §2, expression access).
// The derived presentation fields (index, "R1"-style label, colour key) come from the
// exported helpers in internal/db, so both engines produce identical values for the same
// edge set.
//
// The selection_order values are written as JSON numbers by ReferenceEdgeMetadataFor; a
// missing key coalesces to the same sentinel the PostgreSQL query uses, and a text-encoded
// number would sort lexicographically in this engine. That is the caller's contract, not a
// silent normalisation here.
func (r *EdgeRepo) GetActiveReferenceParents(ctx context.Context, targetID uuid.UUID) ([]db.ReferenceParentEdge, error) {
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `
        SELECT `+edgeColumns+`
        FROM edges
        WHERE target_id = ?
          AND edge_type = ?
          AND deleted_at IS NULL
        ORDER BY COALESCE(json_extract(metadata, '$.selection_order'), 2147483647) ASC,
                 sequence_num ASC`, idArg(targetID), db.EdgeTypeReference)
	if err != nil {
		return nil, fmt.Errorf("db: select reference parents: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []db.ReferenceParentEdge
	idx := 0
	for rows.Next() {
		var e db.Edge
		if err := scanEdge(rows, &e); err != nil {
			return nil, fmt.Errorf("db: scan reference parent: %w", err)
		}
		edge := e
		out = append(out, db.ReferenceParentEdge{
			Edge:        &edge,
			SourceID:    e.SourceID,
			Index:       idx,
			SourceLabel: db.ReferenceSourceLabel(idx),
			ColorKey:    db.ReferenceColorKey(e.TreeID, e.TargetID, e.SourceID),
		})
		idx++
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: scan reference parent: %w", err)
	}
	return out, nil
}

// collectEdges drains rows into a []db.Edge.
func collectEdges(rows *sql.Rows) ([]db.Edge, error) {
	var out []db.Edge
	for rows.Next() {
		var e db.Edge
		if err := scanEdge(rows, &e); err != nil {
			return nil, fmt.Errorf("db: scan edge: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: scan edge: %w", err)
	}
	return out, nil
}
