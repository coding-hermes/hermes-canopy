// Edge repository. Implements EdgeRepo with pgx against PostgreSQL.
// Enforces the single-parent rule per SPEC-DM-01 §3.5: non-synthesis
// targets may have at most one active incoming edge.

package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrMultipleParents is returned by EdgeRepo.Create when a non-synthesis
// target already has an active incoming edge.
var ErrMultipleParents = errors.New("db: target already has a parent")

// ErrSelfEdge is returned when source_id == target_id.
var ErrSelfEdge = errors.New("db: source and target cannot be the same")

// Reference-set errors (SPEC-PL-06 §12, §12.1). Each one is returned by the
// generic Create path or by CreateReferenceSet/ValidateIncomingInvariant so
// the service can map it onto a §9.4 catalog code without re-deriving the
// rule.
var (
	// ErrReferenceSourceCount is returned when a reference set does not
	// hold 2-20 sources (§3.5 invariant 1, §5.1).
	ErrReferenceSourceCount = errors.New("db: reference set requires 2 to 20 sources")
	// ErrReferenceSourceDuplicate is returned when the ordered source list
	// repeats a source id (§2 decision 9).
	ErrReferenceSourceDuplicate = errors.New("db: reference set contains a duplicate source")
	// ErrReferenceRequiresMultiReferenceMode is returned when a lone
	// `reference` edge targets a `message` in parent_mode='lineage'
	// (§12.1 row 3).
	ErrReferenceRequiresMultiReferenceMode = errors.New("db: reference edges require parent_mode='multi_reference'")
	// ErrReferenceParentInvariant is returned when a target's incoming edge
	// set violates §3.5 (wrong type mix, lone reference edge on a
	// multi_reference target, or a count outside 2-20).
	ErrReferenceParentInvariant = errors.New("db: target violates the multi-reference parent invariant")
	// ErrReferenceTargetType is returned when a reference edge targets a
	// node type that may not carry reference parents (§12.1 rows 7-8).
	ErrReferenceTargetType = errors.New("db: node type cannot take reference parents")
	// ErrSystemNodeParentForbidden is returned when any user-created parent
	// edge targets a `system` node (§5.1, §12.1 last row).
	ErrSystemNodeParentForbidden = errors.New("db: system nodes may not receive user-created parent edges")
)

// ErrNoActiveNode is used here too for symmetry with the node repo.
// (Declared in node_repo.go.)

// EdgeCounts aggregates edge counts and edge-type breakdown for a tree.
type EdgeCounts struct {
	TreeID uuid.UUID      `json:"treeId"`
	Total  int64          `json:"total"`
	Active int64          `json:"active"`
	ByType map[string]int `json:"byType"`
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

	// --- Multi-message reference model (SPEC-PL-06 §12) ---

	// CreateReferenceSet inserts exactly one `reference` edge per ordered
	// source for one newly created target. Callers MUST hold one
	// transaction: the set is only valid together with the target node
	// (§3.5 invariant 1, §4.2). It rejects 0-1 or >20 sources, duplicate
	// sources, and a self-edge.
	CreateReferenceSet(ctx context.Context, tx pgx.Tx, input CreateReferenceSetInput) ([]*Edge, error)
	// GetActiveIncoming returns every active incoming edge of a target in
	// deterministic (sequence_num, id) order, locked FOR UPDATE. It is the
	// target invariant probe (§5.3 step 6).
	GetActiveIncoming(ctx context.Context, tx pgx.Tx, targetID uuid.UUID) ([]*Edge, error)
	// GetActiveReferenceParents returns the active reference parents of a
	// target ordered by metadata.selection_order, then sequence_num as a
	// defensive stable tie-breaker (§12).
	GetActiveReferenceParents(ctx context.Context, targetID uuid.UUID) ([]ReferenceParentEdge, error)
	// ValidateIncomingInvariant locks the target's active incoming edge set
	// and applies the parent-mode/node-type constraints of §3.5 and the
	// §5.1 matrix.
	ValidateIncomingInvariant(ctx context.Context, tx pgx.Tx, target *Node) error
}

// CreateReferenceSetInput is the request for one atomic reference set.
// OrderedSources preserves UI selection order after validation (§2
// decision 16); index 0 is the display anchor.
type CreateReferenceSetInput struct {
	TreeID         uuid.UUID
	TargetID       uuid.UUID
	OrderedSources []uuid.UUID
}

// ReferenceParentEdge is one active reference parent of a target, with the
// derived renderer-safe presentation fields from §5.2.
type ReferenceParentEdge struct {
	Edge        *Edge
	SourceID    uuid.UUID
	Index       int
	SourceLabel string
	ColorKey    string
}

// ReferenceEdgeRole is the only `role` value written on reference edges
// (SPEC-PL-06 §5.2).
const ReferenceEdgeRole = "context_source"

// ReferenceEdgeMetadata is the renderer-safe metadata stored on every
// reference edge (SPEC-PL-06 §5.2). Membership itself is authoritative in
// (source_id, target_id, edge_type) — never in this object.
type ReferenceEdgeMetadata struct {
	ReferenceIndex int    `json:"reference_index"`
	SourceLabel    string `json:"source_label"`
	ColorKey       string `json:"color_key"`
	SelectionOrder int    `json:"selection_order"`
	Role           string `json:"role"`
}

// ReferenceSourceLabel returns the accessible label for a zero-based
// canonical index: "R1" for index 0 (§5.2).
func ReferenceSourceLabel(index int) string {
	return "R" + strconv.Itoa(index+1)
}

// ReferenceColorKey derives the deterministic edge colour key from
// (tree_id, target_id, source_id) per SPEC-PL-06 §5.2:
// ref-(fnv1a64(tree_id:target_id:source_id) mod 8). The caller passes
// uuid.Nil for targetID when the target does not exist yet (preflight
// preview); the created edge always carries the authoritative key.
func ReferenceColorKey(treeID, targetID, sourceID uuid.UUID) string {
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "%s:%s:%s", treeID, targetID, sourceID)
	return "ref-" + strconv.FormatUint(h.Sum64()%8, 10)
}

// ReferenceEdgeMetadataFor builds the §5.2 metadata object for one source.
func ReferenceEdgeMetadataFor(index int, treeID, targetID, sourceID uuid.UUID) ReferenceEdgeMetadata {
	return ReferenceEdgeMetadata{
		ReferenceIndex: index,
		SourceLabel:    ReferenceSourceLabel(index),
		ColorKey:       ReferenceColorKey(treeID, targetID, sourceID),
		SelectionOrder: index,
		Role:           ReferenceEdgeRole,
	}
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
//  3. parent-set rule, target-mode aware (SPEC-PL-06 §12.1 decision table,
//     replacing SPEC-DM-01 §3.5's unconditional "only synthesis may have
//     more than one parent"):
//     - `system` target            → ErrSystemNodeParentForbidden
//     - `synthesis` target         → unchanged SPEC-API-04 behavior;
//     a `reference` edge → ErrReferenceTargetType
//     - `multi_reference` message  → reply/fork/synthesis →
//     ErrReferenceParentInvariant; a lone `reference` edge is rejected too,
//     because only CreateReferenceSet can build a complete 2-20 set
//     - `lineage` message          → `reference` →
//     ErrReferenceRequiresMultiReferenceMode; otherwise at most one active
//     incoming edge (ErrMultipleParents)
//  4. unique constraint (source_id, target_id, edge_type) — handled by
//     the schema; we translate the pg error to a generic wrapped error.
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

	// Fetch the target's type and parent mode: both drive the §12.1 matrix.
	var targetType string
	var targetMode ParentMode
	err = tx.QueryRow(ctx,
		`SELECT node_type, parent_mode FROM nodes WHERE id = $1 AND deleted_at IS NULL`,
		edge.TargetID,
	).Scan(&targetType, &targetMode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: select target for edge: %w", err)
	}

	switch {
	case targetType == NodeTypeSystem:
		return nil, ErrSystemNodeParentForbidden
	case targetType == NodeTypeSynthesis:
		if edge.EdgeType == EdgeTypeReference {
			return nil, ErrReferenceTargetType
		}
		// SPEC-API-04: synthesis targets keep their existing multi-parent
		// synthesis behavior, and a synthesis node may never be declared
		// multi_reference (§3.5 invariant 8).
		if targetMode == ParentModeMultiReference {
			return nil, ErrReferenceParentInvariant
		}
	case targetMode == ParentModeMultiReference:
		// Any non-reference parent edge violates §3.5 invariant 6, and a
		// single reference edge can never satisfy invariant 1.
		return nil, ErrReferenceParentInvariant
	default: // message, parent_mode='lineage'
		if edge.EdgeType == EdgeTypeReference {
			return nil, ErrReferenceRequiresMultiReferenceMode
		}
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
        SELECT n.id, n.tree_id, n.parent_id, n.parent_mode, n.author_id, n.content,
               n.content_format, n.node_type, n.sequence_num, n.metadata,
               n.created_at, n.edited_at, n.deleted_at
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
        SELECT n.id, n.tree_id, n.parent_id, n.parent_mode, n.author_id, n.content,
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
            sequence_num = (SELECT COALESCE(MAX(sequence_num), 0) + 1
                            FROM edges WHERE source_id = $2 AND deleted_at IS NULL)
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

// ---------------------------------------------------------------------------
// Multi-message reference model (SPEC-PL-06 §12)
// ---------------------------------------------------------------------------

// CreateReferenceSet inserts one `reference` edge per ordered source for a
// newly created target, inside the caller's transaction (§4.2, §5.3 steps
// 4-5). Sources keep their canonical selection order: edge sequence numbers
// are allocated in order, and each edge carries the §5.2 metadata object.
//
// A duplicate source, a self-edge, or a set outside 2-20 is rejected before
// any insert. A duplicate (source, target, edge_type) row is an internal
// transaction failure — the caller rolls back rather than masking a missing
// requested source with ON CONFLICT DO NOTHING (§5.3).
func (r *PGEdgeRepo) CreateReferenceSet(ctx context.Context, tx pgx.Tx, input CreateReferenceSetInput) ([]*Edge, error) {
	if tx == nil {
		return nil, errors.New("db: CreateReferenceSet requires a transaction")
	}
	if input.TreeID == uuid.Nil || input.TargetID == uuid.Nil {
		return nil, errors.New("db: reference set requires tree and target ids")
	}
	if len(input.OrderedSources) < 2 || len(input.OrderedSources) > 20 {
		return nil, ErrReferenceSourceCount
	}
	seen := make(map[uuid.UUID]struct{}, len(input.OrderedSources))
	for _, src := range input.OrderedSources {
		if src == uuid.Nil {
			return nil, errors.New("db: reference source id is required")
		}
		if src == input.TargetID {
			return nil, ErrSelfEdge
		}
		if _, dup := seen[src]; dup {
			return nil, ErrReferenceSourceDuplicate
		}
		seen[src] = struct{}{}
	}

	// Sequence numbers continue the tree-wide edge counter, matching the
	// convention used by the node service's reply/fork writes.
	var seq int64
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(sequence_num), 0) FROM edges WHERE tree_id = $1`,
		input.TreeID,
	).Scan(&seq); err != nil {
		return nil, fmt.Errorf("db: reference edge sequence: %w", err)
	}

	out := make([]*Edge, 0, len(input.OrderedSources))
	for i, src := range input.OrderedSources {
		seq++
		meta, err := json.Marshal(ReferenceEdgeMetadataFor(i, input.TreeID, input.TargetID, src))
		if err != nil {
			return nil, fmt.Errorf("db: encode reference edge metadata: %w", err)
		}
		row := tx.QueryRow(ctx, `
        INSERT INTO edges
            (tree_id, source_id, target_id, edge_type, sequence_num, metadata)
        VALUES ($1, $2, $3, $4, $5, $6)
        RETURNING `+edgeColumns,
			input.TreeID, src, input.TargetID, EdgeTypeReference, seq, meta,
		)
		var e Edge
		if err := scanEdge(row, &e); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				// Unique (source_id, target_id, edge_type): a duplicate
				// reference row for this target. The target is newly
				// allocated, so this is an internal conflict, not a
				// no-op.
				return nil, fmt.Errorf("%w: reference edge already exists for source %s",
					ErrReferenceSourceDuplicate, src)
			}
			return nil, fmt.Errorf("db: insert reference edge: %w", err)
		}
		out = append(out, &e)
	}
	return out, nil
}

// GetActiveIncoming returns every active incoming edge of a target in
// deterministic (sequence_num, id) order, locked FOR UPDATE (§5.3 step 6:
// the target's final incoming set is validated before commit).
func (r *PGEdgeRepo) GetActiveIncoming(ctx context.Context, tx pgx.Tx, targetID uuid.UUID) ([]*Edge, error) {
	if tx == nil {
		return nil, errors.New("db: GetActiveIncoming requires a transaction")
	}
	rows, err := tx.Query(ctx, `
        SELECT `+edgeColumns+`
        FROM edges
        WHERE target_id = $1 AND deleted_at IS NULL
        ORDER BY sequence_num ASC, id ASC
        FOR UPDATE`, targetID)
	if err != nil {
		return nil, fmt.Errorf("db: select active incoming: %w", err)
	}
	defer rows.Close()
	var out []*Edge
	for rows.Next() {
		var e Edge
		if err := scanEdge(rows, &e); err != nil {
			return nil, fmt.Errorf("db: scan active incoming: %w", err)
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

// GetActiveReferenceParents returns the target's active reference parents
// ordered by metadata.selection_order, then sequence_num as a defensive
// stable tie-breaker (§12).
func (r *PGEdgeRepo) GetActiveReferenceParents(ctx context.Context, targetID uuid.UUID) ([]ReferenceParentEdge, error) {
	rows, err := r.pool.Query(ctx, `
        SELECT `+edgeColumns+`
        FROM edges
        WHERE target_id = $1
          AND edge_type = $2
          AND deleted_at IS NULL
        ORDER BY COALESCE((metadata->>'selection_order')::int, 2147483647) ASC,
                 sequence_num ASC`,
		targetID, EdgeTypeReference)
	if err != nil {
		return nil, fmt.Errorf("db: select reference parents: %w", err)
	}
	defer rows.Close()
	var out []ReferenceParentEdge
	idx := 0
	for rows.Next() {
		var e Edge
		if err := scanEdge(rows, &e); err != nil {
			return nil, fmt.Errorf("db: scan reference parent: %w", err)
		}
		out = append(out, ReferenceParentEdge{
			Edge:        &e,
			SourceID:    e.SourceID,
			Index:       idx,
			SourceLabel: ReferenceSourceLabel(idx),
			ColorKey:    ReferenceColorKey(e.TreeID, e.TargetID, e.SourceID),
		})
		idx++
	}
	return out, rows.Err()
}

// ValidateIncomingInvariant locks the target's active incoming edge set and
// applies the SPEC-PL-06 §3.5 invariants together with the §5.1 matrix:
//
//	system                 → no incoming parent edge may exist
//	synthesis              → never multi_reference; existing SPEC-API-04
//	                         synthesis parenting preserved
//	multi_reference message→ 2-20 incoming edges, all `reference`, and
//	                         parent_id must be one of the sources
//	lineage message        → at most one incoming edge, never `reference`
//
// It is always called inside the creating transaction, after the edge set
// is written, so a violation rolls the whole creation back (§5.3 step 7).
func (r *PGEdgeRepo) ValidateIncomingInvariant(ctx context.Context, tx pgx.Tx, target *Node) error {
	if target == nil {
		return errors.New("db: validate incoming invariant requires a target")
	}
	incoming, err := r.GetActiveIncoming(ctx, tx, target.ID)
	if err != nil {
		return err
	}

	switch {
	case target.NodeType == NodeTypeSystem:
		if len(incoming) > 0 {
			return ErrSystemNodeParentForbidden
		}
		return nil

	case target.NodeType == NodeTypeSynthesis:
		if target.ParentMode == ParentModeMultiReference {
			// §3.5 invariant 8: synthesis nodes use SPEC-API-04's
			// synthesis edges, never the reference model.
			return ErrReferenceParentInvariant
		}
		for _, e := range incoming {
			if e.EdgeType == EdgeTypeReference {
				return ErrReferenceTargetType
			}
		}
		return nil

	case target.ParentMode == ParentModeMultiReference:
		// §3.5 invariants 1, 3, 4, 6.
		if len(incoming) < 2 || len(incoming) > 20 {
			return fmt.Errorf("%w: multi_reference target has %d incoming edges (need 2-20)",
				ErrReferenceParentInvariant, len(incoming))
		}
		sources := make(map[uuid.UUID]struct{}, len(incoming))
		for _, e := range incoming {
			if e.EdgeType != EdgeTypeReference {
				return fmt.Errorf("%w: multi_reference target has incoming %q edge",
					ErrReferenceParentInvariant, e.EdgeType)
			}
			if e.TreeID != target.TreeID {
				return fmt.Errorf("%w: incoming edge %s is in another tree",
					ErrReferenceParentInvariant, e.ID)
			}
			sources[e.SourceID] = struct{}{}
		}
		if target.ParentID == nil {
			return fmt.Errorf("%w: multi_reference target has no display anchor",
				ErrReferenceParentInvariant)
		}
		if _, ok := sources[*target.ParentID]; !ok {
			// §3.5 invariant 4 / §15 scenario 11: the display anchor must
			// be one of the active reference sources.
			return fmt.Errorf("%w: display anchor %s is not an active reference source",
				ErrReferenceParentInvariant, *target.ParentID)
		}
		// §3.5 invariant 5: no duplicated source can exist (the unique
		// constraint guarantees it), and the count guards the 2-20 band.
		if len(sources) != len(incoming) {
			return fmt.Errorf("%w: duplicate reference sources",
				ErrReferenceParentInvariant)
		}
		return nil

	default: // message, parent_mode='lineage'
		if len(incoming) > 1 {
			return ErrMultipleParents
		}
		if len(incoming) == 1 && incoming[0].EdgeType == EdgeTypeReference {
			return ErrReferenceRequiresMultiReferenceMode
		}
		return nil
	}
}
