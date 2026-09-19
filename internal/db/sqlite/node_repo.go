package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

// nodeColumns mirrors PGNodeRepo's column list. `content_hash` is deliberately absent, for
// the same reason it is absent there: db.Node has no field for it. This repository WRITES
// the hash on every insert and update (the SQLite replacement for the
// trg_node_content_hash trigger, docs/SQLITE-PIVOT.md §5) — it is simply not part of the
// returned struct.
const nodeColumns = `id, tree_id, parent_id, author_id, content, content_format,
    node_type, sequence_num, metadata, created_at, edited_at, deleted_at`

// NodeRepo is the modernc.org/sqlite + database/sql implementation of db.NodeRepo over the
// SQLite core graph store. Construct it with NewNodeRepo.
type NodeRepo struct{ q *sql.DB }

// NewNodeRepo binds the repository to an open Store; see NewTreeRepo for the ownership and
// nil-store contract.
func NewNodeRepo(s *Store) *NodeRepo {
	if s == nil {
		return &NodeRepo{}
	}
	return &NodeRepo{q: s.DB()}
}

// conn returns the pool or ErrNoStore.
func (r *NodeRepo) conn() (*sql.DB, error) {
	if r == nil || r.q == nil {
		return nil, ErrNoStore
	}
	return r.q, nil
}

// scanNode centralises the nodes column order, decoding ids and timestamps explicitly.
//
// `parent_mode` is not in this list on purpose: PostgreSQL migration 000047 adds it and is
// not part of the wave-1 SQLite batch, so the column does not exist in this schema and every
// node here is 'lineage' (package comment). Scanning it would fail; leaving db.Node's
// ParentMode zero-valued is the honest representation of "not stored in this schema", and
// TestSQLiteNodeRepoParentModeIsNotStoredInCoreSchema pins that it stays empty rather than
// being fabricated.
func scanNode(row rowScanner, n *db.Node) error {
	var (
		id, treeID, authorID string
		parentID             sql.NullString
		metadata             []byte
		createdAt            sql.NullString
		editedAt, deletedAt  sql.NullString
	)
	if err := row.Scan(&id, &treeID, &parentID, &authorID, &n.Content, &n.ContentFormat,
		&n.NodeType, &n.SequenceNum, &metadata, &createdAt, &editedAt, &deletedAt); err != nil {
		return err
	}
	nodeID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("nodes.id %q: %w", id, err)
	}
	n.ID = nodeID
	if n.TreeID, err = uuid.Parse(treeID); err != nil {
		return fmt.Errorf("nodes.tree_id %q: %w", treeID, err)
	}
	if n.ParentID, err = optionalID("nodes.parent_id", parentID); err != nil {
		return err
	}
	if n.AuthorID, err = uuid.Parse(authorID); err != nil {
		return fmt.Errorf("nodes.author_id %q: %w", authorID, err)
	}
	n.Metadata = metadata
	if n.CreatedAt, err = requiredTime("nodes.created_at", createdAt); err != nil {
		return err
	}
	if n.EditedAt, err = optionalTime("nodes.edited_at", editedAt); err != nil {
		return err
	}
	n.DeletedAt, err = optionalTime("nodes.deleted_at", deletedAt)
	return err
}

// Create inserts a node and returns the stored row.
//
// Every Go-owned column is supplied explicitly, because the SQLite DDL declares no default
// for any of them (obligations 1-3 of the package comment):
//
//   - `id` from NewID (no `DEFAULT uuidv7()`); a caller-supplied Node.ID is not honored,
//     matching PGNodeRepo where the DDL default wins.
//   - `sequence_num` from NextNodeSequence **inside this statement's transaction** when the
//     caller leaves it at zero, and taken as-is otherwise — the same "explicit non-zero
//     sequence wins" rule as PG's `WHEN (NEW.sequence_num IS NULL)` trigger condition.
//   - `content_hash` from ContentHash over the UTF-8 content.
//
// `content_format` / `node_type` default to 'markdown' / 'message' when empty, and metadata
// to '{}', matching the PG repo's COALESCEs. The DDL keeps enforcing the closed sets: a
// format or type outside the CHECK list fails the insert, and a foreign-key violation
// (unknown tree, or an unknown parent) surfaces as the driver's foreign-key error.
func (r *NodeRepo) Create(ctx context.Context, node *db.Node) (*db.Node, error) {
	if node == nil {
		return nil, errors.New("db: node is nil")
	}
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	id, err := NewID()
	if err != nil {
		return nil, err
	}
	contentFormat := node.ContentFormat
	if contentFormat == "" {
		contentFormat = db.ContentFormatMarkdown
	}
	nodeType := node.NodeType
	if nodeType == "" {
		nodeType = db.NodeTypeMessage
	}

	tx, err := q.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("db: begin node tx: %w", err)
	}
	// No-op once Commit has succeeded; the rollback for every early return.
	defer func() { _ = tx.Rollback() }()

	sequence := node.SequenceNum
	if sequence == 0 {
		sequence, err = NextNodeSequence(ctx, tx, idArg(node.TreeID))
		if err != nil {
			return nil, fmt.Errorf("db: alloc node sequence: %w", err)
		}
	}

	row := tx.QueryRowContext(ctx, `
        INSERT INTO nodes
            (id, tree_id, parent_id, author_id, content, content_format,
             node_type, sequence_num, metadata, content_hash)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        RETURNING `+nodeColumns,
		idArg(id), idArg(node.TreeID), optionalIDArg(node.ParentID), idArg(node.AuthorID),
		node.Content, contentFormat, nodeType, sequence, jsonText(node.Metadata),
		ContentHash(node.Content),
	)
	var out db.Node
	if err := scanNode(row, &out); err != nil {
		return nil, fmt.Errorf("db: insert node: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("db: commit node: %w", err)
	}
	return &out, nil
}

// GetByID returns the active node with the given ID.
func (r *NodeRepo) GetByID(ctx context.Context, id uuid.UUID) (*db.Node, error) {
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	var n db.Node
	err = scanNode(q.QueryRowContext(ctx, `
        SELECT `+nodeColumns+`
        FROM nodes
        WHERE id = ? AND deleted_at IS NULL`, idArg(id)), &n)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, db.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: select node: %w", err)
	}
	return &n, nil
}

// GetByTree returns every active node of a tree ordered by sequence_num.
func (r *NodeRepo) GetByTree(ctx context.Context, treeID uuid.UUID) ([]db.Node, error) {
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `
        SELECT `+nodeColumns+`
        FROM nodes
        WHERE tree_id = ? AND deleted_at IS NULL
        ORDER BY sequence_num ASC`, idArg(treeID))
	if err != nil {
		return nil, fmt.Errorf("db: select nodes by tree: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectNodes(rows)
}

// GetChildren returns the active children of a parent, ordered by edge.sequence_num then
// node.sequence_num. The graph parents are the incoming edges, as in PGNodeRepo — the
// parent_id display anchor is not consulted.
func (r *NodeRepo) GetChildren(ctx context.Context, parentID uuid.UUID) ([]db.Node, error) {
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `
        SELECT n.`+joinNodeColumns+`
        FROM nodes n
        JOIN edges e ON e.target_id = n.id
        WHERE e.source_id = ?
          AND e.deleted_at IS NULL
          AND n.deleted_at IS NULL
        ORDER BY e.sequence_num ASC, n.sequence_num ASC`, idArg(parentID))
	if err != nil {
		return nil, fmt.Errorf("db: select children: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectNodes(rows)
}

// GetAncestors walks parent_id up to the root with a recursive CTE. The result contains the
// input node first and the root last, ordered by sequence_num — PGNodeRepo's ordering, kept
// verbatim so the two implementations return identical slices. The `depth < 10000` bound is
// the guard against an accidental cycle; without it a malformed chain walks until the
// statement is killed.
func (r *NodeRepo) GetAncestors(ctx context.Context, nodeID uuid.UUID) ([]db.Node, error) {
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `
        WITH RECURSIVE chain(`+nodeColumns+`, depth) AS (
            SELECT `+nodeColumns+`, 0
            FROM nodes
            WHERE id = ? AND deleted_at IS NULL
            UNION ALL
            SELECT n.id, n.tree_id, n.parent_id, n.author_id, n.content, n.content_format,
                   n.node_type, n.sequence_num, n.metadata, n.created_at, n.edited_at,
                   n.deleted_at, chain.depth + 1
            FROM nodes n
            JOIN chain ON n.id = chain.parent_id
            WHERE n.deleted_at IS NULL
              AND chain.depth < 10000
        )
        SELECT `+nodeColumns+`
        FROM chain
        ORDER BY sequence_num ASC`, idArg(nodeID))
	if err != nil {
		return nil, fmt.Errorf("db: select ancestors: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectNodes(rows)
}

// GetSubtree returns all descendants of rootID up to maxDepth levels below it; maxDepth == 0
// means unbounded.
//
// PGNodeRepo collapses the recursive walk's duplicates with `SELECT DISTINCT ON (id)`, which
// SQLite does not have. The translation is a window function — ROW_NUMBER() partitioned by
// id, ordered by depth — keeping the first row per node. The depth bound stays owned by the
// CTE, exactly as in PostgreSQL, so maxDepth limits the walk rather than the returned rows
// (GAP-073: a node reachable through N paths must still be returned once).
func (r *NodeRepo) GetSubtree(ctx context.Context, rootID uuid.UUID, maxDepth int) ([]db.Node, error) {
	if maxDepth < 0 {
		maxDepth = 0
	}
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `
        WITH RECURSIVE sub(`+nodeColumns+`, depth) AS (
            SELECT `+nodeColumns+`, 0
            FROM nodes
            WHERE id = ? AND deleted_at IS NULL
            UNION ALL
            SELECT n.id, n.tree_id, n.parent_id, n.author_id, n.content, n.content_format,
                   n.node_type, n.sequence_num, n.metadata, n.created_at, n.edited_at,
                   n.deleted_at, sub.depth + 1
            FROM nodes n
            JOIN edges e ON e.target_id = n.id
            JOIN sub ON sub.id = e.source_id
            WHERE e.deleted_at IS NULL
              AND n.deleted_at IS NULL
              AND (? = 0 OR sub.depth + 1 <= ?)
              AND sub.depth < 10000
        )
        SELECT `+nodeColumns+`
        FROM (
            SELECT `+nodeColumns+`,
                   ROW_NUMBER() OVER (PARTITION BY id ORDER BY depth) AS rn
            FROM sub
        )
        WHERE rn = 1
        ORDER BY sequence_num ASC`, idArg(rootID), maxDepth, maxDepth)
	if err != nil {
		return nil, fmt.Errorf("db: select subtree: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectNodes(rows)
}

// GetPath returns the nodes on the path between two nodes (inclusive), walking through their
// lowest common ancestor: from … LCA … to. The LCA is the newest id in the intersection of
// the two ancestor chains, matching PGNodeRepo's `ORDER BY up_from.id DESC LIMIT 1`; ids are
// UUIDv7, so "newest" is "deepest" for a chain built in insertion order. Nodes with no shared
// root report ErrNotFound rather than an empty slice.
//
// One deliberate divergence. PGNodeRepo's index arithmetic walks its two ancestor slices in
// the opposite direction from the order its own GetAncestors returns them (that method's doc
// comment claims "the input node is index 0", while its SQL orders by sequence_num ASC, i.e.
// root-first). For a root→a→b chain and a root→a→c branch it therefore returns [a, root, root]
// for GetPath(b, c): the LCA repeated and `to` missing. No production caller or test pins that
// output — GetPath appears only in service test stubs — so this port returns the path the
// method is named for, and TestSQLiteNodeRepoGetPath pins the three shapes (siblings, parent
// to child, root to leaf).
func (r *NodeRepo) GetPath(ctx context.Context, fromID, toID uuid.UUID) ([]db.Node, error) {
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	var lca string
	err = q.QueryRowContext(ctx, `
        WITH RECURSIVE up_from(id, parent_id, depth) AS (
            SELECT id, parent_id, 0 FROM nodes WHERE id = ? AND deleted_at IS NULL
            UNION ALL
            SELECT n.id, n.parent_id, up_from.depth + 1
            FROM nodes n
            JOIN up_from ON n.id = up_from.parent_id
            WHERE n.deleted_at IS NULL AND up_from.depth < 10000
        ),
        up_to(id, parent_id, depth) AS (
            SELECT id, parent_id, 0 FROM nodes WHERE id = ? AND deleted_at IS NULL
            UNION ALL
            SELECT n.id, n.parent_id, up_to.depth + 1
            FROM nodes n
            JOIN up_to ON n.id = up_to.parent_id
            WHERE n.deleted_at IS NULL AND up_to.depth < 10000
        )
        SELECT up_from.id
        FROM up_from
        JOIN up_to ON up_from.id = up_to.id
        ORDER BY up_from.id DESC
        LIMIT 1`, idArg(fromID), idArg(toID)).Scan(&lca)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, db.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: find lca: %w", err)
	}
	lcaID, err := uuid.Parse(lca)
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
		if n.ID == lcaID {
			lcaIdx = i
			break
		}
	}
	if lcaIdx == -1 {
		return nil, db.ErrNotFound
	}

	toIdx := -1
	for i, n := range ancTo {
		if n.ID == lcaID {
			toIdx = i
			break
		}
	}
	if toIdx == -1 {
		return nil, db.ErrNotFound
	}

	// Both slices are ordered root-first (sequence_num ASC), so `from` walks back to the LCA
	// and `to` walks forward from it.
	path := make([]db.Node, 0, len(ancFrom)-lcaIdx+len(ancTo)-toIdx-1)
	for i := len(ancFrom) - 1; i >= lcaIdx; i-- {
		path = append(path, ancFrom[i])
	}
	for i := toIdx + 1; i < len(ancTo); i++ {
		path = append(path, ancTo[i])
	}
	return path, nil
}

// Update changes a node's content and metadata.
//
// `content_hash` is recomputed here: PostgreSQL does it in the trg_node_content_hash trigger
// (`UPDATE OF content`), and this store has no hash function in SQL, so a forgotten recompute
// would leave a stale hash on every edited node — the exact defect 000025 fixed on the
// PostgreSQL side. `edited_at` is left to the translated trg_node_edited_at trigger, which
// fires only when content or metadata actually differ; the row is read back afterwards
// because RETURNING does not see an AFTER trigger's write (package comment, point 4).
func (r *NodeRepo) Update(ctx context.Context, id uuid.UUID, content string, metadata []byte) (*db.Node, error) {
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	res, err := q.ExecContext(ctx, `
        UPDATE nodes
        SET content = ?, content_hash = ?, metadata = ?
        WHERE id = ? AND deleted_at IS NULL`,
		content, ContentHash(content), jsonText(metadata), idArg(id))
	if err != nil {
		return nil, fmt.Errorf("db: update node: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("db: update node: %w", err)
	}
	if n == 0 {
		return nil, db.ErrNotFound
	}
	return r.GetByID(ctx, id)
}

// SoftDelete marks the node deleted and reports ErrNotFound when no active row matches.
// Children keep their parent_id pointer: the FK's ON DELETE SET NULL applies to a hard
// delete, and a soft delete only flips deleted_at.
func (r *NodeRepo) SoftDelete(ctx context.Context, id uuid.UUID) error {
	q, err := r.conn()
	if err != nil {
		return err
	}
	res, err := q.ExecContext(ctx, `
        UPDATE nodes
        SET deleted_at = `+sqlNow+`
        WHERE id = ? AND deleted_at IS NULL`, idArg(id))
	if err != nil {
		return fmt.Errorf("db: soft-delete node: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("db: soft-delete node: %w", err)
	}
	if n == 0 {
		return db.ErrNotFound
	}
	return nil
}

// HardDelete permanently removes a node; like PGNodeRepo it does NOT check for children.
// Here the schema does decide what happens to the neighbours: the edges that reference the
// node cascade away (edges.source_id / edges.target_id are ON DELETE CASCADE), and children
// keep their rows with parent_id set to NULL (nodes.parent_id is ON DELETE SET NULL). The
// PostgreSQL DDL declares the same actions, so the two implementations agree — the SQLite
// store simply enforces them, because the D6 pragma set turns foreign_keys ON.
func (r *NodeRepo) HardDelete(ctx context.Context, id uuid.UUID) error {
	q, err := r.conn()
	if err != nil {
		return err
	}
	res, err := q.ExecContext(ctx, `DELETE FROM nodes WHERE id = ?`, idArg(id))
	if err != nil {
		return fmt.Errorf("db: hard-delete node: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("db: hard-delete node: %w", err)
	}
	if n == 0 {
		return db.ErrNotFound
	}
	return nil
}

// GetCounts aggregates per-tree counts.
//
// PGNodeRepo writes this as a cross-join of three aggregate sub-selects with GROUP BY, which
// returns NO rows for a tree whose only nodes are detached (no parent_id IS NULL root), and
// then special-cases that into zero counts. The translation keeps the same semantics but
// states them once: the five values are scalar subqueries over one recursive CTE, so the
// statement always returns exactly one row and a tree with no roots reports MaxDepth 0
// instead of relying on an ErrNoRows branch.
func (r *NodeRepo) GetCounts(ctx context.Context, treeID uuid.UUID) (*db.NodeCounts, error) {
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	c := &db.NodeCounts{TreeID: treeID}
	err = q.QueryRowContext(ctx, `
        WITH RECURSIVE tree_nodes(id, parent_id, deleted_at) AS (
            SELECT id, parent_id, deleted_at
            FROM nodes
            WHERE tree_id = ?
        ),
        depths(id, depth) AS (
            SELECT id, 0 FROM tree_nodes WHERE parent_id IS NULL
            UNION ALL
            SELECT n.id, d.depth + 1
            FROM tree_nodes n
            JOIN depths d ON n.parent_id = d.id
            WHERE d.depth < 10000
        )
        SELECT
            (SELECT COUNT(*) FROM tree_nodes),
            (SELECT COUNT(*) FROM tree_nodes WHERE deleted_at IS NULL),
            (SELECT COUNT(*) FROM edges WHERE tree_id = ?),
            (SELECT COUNT(*) FROM edges WHERE tree_id = ? AND deleted_at IS NULL),
            COALESCE((SELECT MAX(depth) FROM depths), 0)`,
		idArg(treeID), idArg(treeID), idArg(treeID),
	).Scan(&c.TotalNodes, &c.ActiveNodes, &c.TotalEdges, &c.ActiveEdges, &c.MaxDepth)
	if err != nil {
		return nil, fmt.Errorf("db: count nodes: %w", err)
	}
	return c, nil
}

// collectNodes drains rows into a []db.Node.
func collectNodes(rows *sql.Rows) ([]db.Node, error) {
	var out []db.Node
	for rows.Next() {
		var n db.Node
		if err := scanNode(rows, &n); err != nil {
			return nil, fmt.Errorf("db: scan node: %w", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: scan node: %w", err)
	}
	return out, nil
}
