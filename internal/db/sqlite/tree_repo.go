package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

// treeColumns is the canonical trees column list, identical to PGTreeRepo's, so one scan
// helper serves every read path in both implementations.
const treeColumns = `id, owner_id, title, description, root_node_id, metadata,
    created_at, edited_at, deleted_at`

// TreeRepo is the modernc.org/sqlite + database/sql implementation of db.TreeRepo over the
// SQLite core graph store. Construct it with NewTreeRepo.
type TreeRepo struct{ q *sql.DB }

// NewTreeRepo binds the repository to an open Store. The Store owns the pool and its
// lifetime — the repository never closes it. A nil store yields a handle whose methods
// return ErrNoStore, so a mis-wired boot path fails at the call site instead of panicking.
func NewTreeRepo(s *Store) *TreeRepo {
	if s == nil {
		return &TreeRepo{}
	}
	return &TreeRepo{q: s.DB()}
}

// conn returns the pool or ErrNoStore.
func (r *TreeRepo) conn() (*sql.DB, error) {
	if r == nil || r.q == nil {
		return nil, ErrNoStore
	}
	return r.q, nil
}

// scanTree centralises the column order for the trees table. Ids are decoded explicitly
// (SQLite stores them as 36-character TEXT) and timestamps go through the strict canonical
// decoder of obligations.go, so a value in a different shape fails loudly rather than being
// silently reinterpreted.
func scanTree(row rowScanner, t *db.Tree) error {
	var (
		id, ownerID         string
		rootNodeID          sql.NullString
		metadata            []byte
		createdAt           sql.NullString
		editedAt, deletedAt sql.NullString
	)
	if err := row.Scan(&id, &ownerID, &t.Title, &t.Description, &rootNodeID,
		&metadata, &createdAt, &editedAt, &deletedAt); err != nil {
		return err
	}
	treeID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("trees.id %q: %w", id, err)
	}
	t.ID = treeID
	if t.OwnerID, err = uuid.Parse(ownerID); err != nil {
		return fmt.Errorf("trees.owner_id %q: %w", ownerID, err)
	}
	if t.RootNodeID, err = optionalID("trees.root_node_id", rootNodeID); err != nil {
		return err
	}
	t.Metadata = metadata
	if t.CreatedAt, err = requiredTime("trees.created_at", createdAt); err != nil {
		return err
	}
	if t.EditedAt, err = optionalTime("trees.edited_at", editedAt); err != nil {
		return err
	}
	t.DeletedAt, err = optionalTime("trees.deleted_at", deletedAt)
	return err
}

// Create inserts a tree and returns the stored row.
//
// Two PostgreSQL mechanisms are replaced here (package comment, points 1 and 5): the id
// comes from NewID because the SQLite DDL declares no `DEFAULT uuidv7()`, and metadata is
// bound as TEXT. `created_at` still comes from the DDL default, so a Go-written and a
// DDL-written timestamp stay byte-identical. A caller-supplied Tree.ID is not honored —
// the same behaviour as PGTreeRepo, where the DDL default wins.
func (r *TreeRepo) Create(ctx context.Context, tree *db.Tree) (*db.Tree, error) {
	if tree == nil {
		return nil, errors.New("db: tree is nil")
	}
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	id, err := NewID()
	if err != nil {
		return nil, err
	}
	row := q.QueryRowContext(ctx, `
        INSERT INTO trees (id, owner_id, title, description, root_node_id, metadata)
        VALUES (?, ?, ?, ?, ?, ?)
        RETURNING `+treeColumns,
		idArg(id), idArg(tree.OwnerID), tree.Title, tree.Description,
		optionalIDArg(tree.RootNodeID), jsonText(tree.Metadata),
	)
	var out db.Tree
	if err := scanTree(row, &out); err != nil {
		return nil, fmt.Errorf("db: insert tree: %w", err)
	}
	return &out, nil
}

// GetByID returns the active tree with the given ID; soft-deleted trees are not found.
func (r *TreeRepo) GetByID(ctx context.Context, id uuid.UUID) (*db.Tree, error) {
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	var t db.Tree
	err = scanTree(q.QueryRowContext(ctx, `
        SELECT `+treeColumns+`
        FROM trees
        WHERE id = ? AND deleted_at IS NULL`, idArg(id)), &t)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, db.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: select tree: %w", err)
	}
	return &t, nil
}

// GetByOwner returns all active trees owned by ownerID, newest first.
func (r *TreeRepo) GetByOwner(ctx context.Context, ownerID uuid.UUID) ([]db.Tree, error) {
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `
        SELECT `+treeColumns+`
        FROM trees
        WHERE owner_id = ? AND deleted_at IS NULL
        ORDER BY created_at DESC`, idArg(ownerID))
	if err != nil {
		return nil, fmt.Errorf("db: select trees by owner: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectTrees(rows)
}

// List returns a page of active trees ordered by created_at DESC. limit is clamped to
// [1, 200] and offset to >= 0, as in PGTreeRepo.
func (r *TreeRepo) List(ctx context.Context, limit, offset int) ([]db.Tree, error) {
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	limit, offset = clampPage(limit, offset)
	rows, err := q.QueryContext(ctx, `
        SELECT `+treeColumns+`
        FROM trees
        WHERE deleted_at IS NULL
        ORDER BY created_at DESC
        LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("db: list trees: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectTrees(rows)
}

// ListKeyset returns a page of active trees ordered by (created_at DESC, id DESC) using
// keyset pagination. PostgreSQL's row-value comparison `(created_at, id) < (SELECT ..)`
// translates verbatim: SQLite has supported row values since 3.15, and rows belonging to
// the cursor are excluded by the same predicate. The (created_at, id) tiebreak keeps pages
// deterministic when created_at values collide. limit is clamped to [1, 200].
func (r *TreeRepo) ListKeyset(ctx context.Context, cursorID *uuid.UUID, limit int) ([]db.Tree, error) {
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows *sql.Rows
	if cursorID == nil {
		rows, err = q.QueryContext(ctx, `
            SELECT `+treeColumns+`
            FROM trees
            WHERE deleted_at IS NULL
            ORDER BY created_at DESC, id DESC
            LIMIT ?`, limit)
	} else {
		rows, err = q.QueryContext(ctx, `
            SELECT `+treeColumns+`
            FROM trees
            WHERE deleted_at IS NULL
              AND (created_at, id) < (
                  SELECT created_at, id FROM trees WHERE id = ?
              )
            ORDER BY created_at DESC, id DESC
            LIMIT ?`, idArg(*cursorID), limit)
	}
	if err != nil {
		return nil, fmt.Errorf("db: list trees keyset: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectTrees(rows)
}

// Count returns the number of active (non-soft-deleted) trees.
func (r *TreeRepo) Count(ctx context.Context) (int, error) {
	q, err := r.conn()
	if err != nil {
		return 0, err
	}
	var n int
	if err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM trees WHERE deleted_at IS NULL`).Scan(&n); err != nil {
		return 0, fmt.Errorf("db: count trees: %w", err)
	}
	return n, nil
}

// CountNodesByTreeIDs returns the live node count per tree for the given ids; trees without
// live nodes are absent from the map (zero by convention). PostgreSQL's `tree_id = ANY($1)`
// becomes an expanded IN list, since SQLite has no array type. An empty input returns an
// empty map without querying, as the PostgreSQL implementation does.
func (r *TreeRepo) CountNodesByTreeIDs(ctx context.Context, treeIDs []uuid.UUID) (map[uuid.UUID]int, error) {
	counts := make(map[uuid.UUID]int, len(treeIDs))
	if len(treeIDs) == 0 {
		return counts, nil
	}
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	args := make([]any, 0, len(treeIDs))
	for _, id := range treeIDs {
		args = append(args, idArg(id))
	}
	rows, err := q.QueryContext(ctx, `
        SELECT tree_id, COUNT(*)
        FROM nodes
        WHERE tree_id IN (`+placeholders(len(args))+`) AND deleted_at IS NULL
        GROUP BY tree_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("db: count nodes by tree ids: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			treeID string
			n      int
		)
		if err := rows.Scan(&treeID, &n); err != nil {
			return nil, fmt.Errorf("db: scan node count: %w", err)
		}
		id, err := uuid.Parse(treeID)
		if err != nil {
			return nil, fmt.Errorf("db: scan node count: nodes.tree_id %q: %w", treeID, err)
		}
		counts[id] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: count nodes by tree ids: %w", err)
	}
	return counts, nil
}

// LastActivityByTreeIDs returns the most recent live (non-soft-deleted) node created_at
// per tree for the given ids. The canonical-text timestamps sort correctly as TEXT, so
// MAX(created_at) keeps PG parity with LastActivityByTreeIDs; trees without live nodes
// are absent from the map. An empty input returns an empty map without querying.
func (r *TreeRepo) LastActivityByTreeIDs(ctx context.Context, treeIDs []uuid.UUID) (map[uuid.UUID]time.Time, error) {
	latest := make(map[uuid.UUID]time.Time, len(treeIDs))
	if len(treeIDs) == 0 {
		return latest, nil
	}
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	args := make([]any, 0, len(treeIDs))
	for _, id := range treeIDs {
		args = append(args, idArg(id))
	}
	rows, err := q.QueryContext(ctx, `
        SELECT tree_id, MAX(created_at)
        FROM nodes
        WHERE tree_id IN (`+placeholders(len(args))+`) AND deleted_at IS NULL
        GROUP BY tree_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("db: last activity by tree ids: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			treeID string
			at     sql.NullString
		)
		if err := rows.Scan(&treeID, &at); err != nil {
			return nil, fmt.Errorf("db: scan last activity: %w", err)
		}
		id, err := uuid.Parse(treeID)
		if err != nil {
			return nil, fmt.Errorf("db: scan last activity: nodes.tree_id %q: %w", treeID, err)
		}
		if !at.Valid {
			continue // no live nodes → absent from the map, like the PG repo
		}
		parsed, err := DecodeTime(at.String)
		if err != nil {
			return nil, fmt.Errorf("db: scan last activity: nodes.created_at %q: %w", at.String, err)
		}
		latest[id] = parsed
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: last activity by tree ids: %w", err)
	}
	return latest, nil
}

// Update replaces the mutable fields and bumps edited_at.
//
// Two differences from PGTreeRepo, both deliberate: edited_at is set from the SQL wall
// clock (the same expression the DDL uses, so no Go/DDL skew), and an empty metadata is
// written as '{}' instead of NULL. PostgreSQL's update passes metadata through unguarded,
// so a nil Metadata field fails the NOT NULL column there; defaulting it keeps "no
// metadata" and "empty metadata" the same value in both directions. The returned row is
// read back with a SELECT: RETURNING would report edited_at before the statement's own
// write is visible (package comment, point 4).
func (r *TreeRepo) Update(ctx context.Context, tree *db.Tree) (*db.Tree, error) {
	if tree == nil {
		return nil, errors.New("db: tree is nil")
	}
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	res, err := q.ExecContext(ctx, `
        UPDATE trees
        SET title = ?, description = ?, root_node_id = ?, metadata = ?,
            edited_at = `+sqlNow+`
        WHERE id = ? AND deleted_at IS NULL`,
		tree.Title, tree.Description, optionalIDArg(tree.RootNodeID),
		jsonText(tree.Metadata), idArg(tree.ID))
	if err != nil {
		return nil, fmt.Errorf("db: update tree: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("db: update tree: %w", err)
	}
	if n == 0 {
		return nil, db.ErrNotFound
	}
	return r.GetByID(ctx, tree.ID)
}

// SoftDelete marks the tree deleted and reports ErrNotFound when no active row matches.
// The nodes/edges rows stay in place: their FK is ON DELETE CASCADE, which applies to a
// hard DELETE, and the read paths filter on deleted_at.
func (r *TreeRepo) SoftDelete(ctx context.Context, id uuid.UUID) error {
	q, err := r.conn()
	if err != nil {
		return err
	}
	res, err := q.ExecContext(ctx, `
        UPDATE trees
        SET deleted_at = `+sqlNow+`
        WHERE id = ? AND deleted_at IS NULL`, idArg(id))
	if err != nil {
		return fmt.Errorf("db: soft-delete tree: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("db: soft-delete tree: %w", err)
	}
	if n == 0 {
		return db.ErrNotFound
	}
	return nil
}

// Search matches title or description case-insensitively. An empty query reports
// ErrNotFound, exactly as PGTreeRepo does, so "no such query" and "no matches" stay
// distinguishable.
//
// PostgreSQL's ILIKE is matched with LIKE: SQLite's LIKE is case-insensitive for ASCII
// but not for non-ASCII, which is narrower than a UTF-8 ILIKE (verified on SQLite 3.53.4:
// lower('É') does not fold to 'é'). A non-ASCII case-insensitive search therefore needs
// the FTS5/ICU decision that docs/SQLITE-PIVOT.md §6 D2 still leaves open — this slice
// does not pre-empt it.
func (r *TreeRepo) Search(ctx context.Context, query string, limit, offset int) ([]db.Tree, error) {
	if query == "" {
		return nil, db.ErrNotFound
	}
	q, err := r.conn()
	if err != nil {
		return nil, err
	}
	limit, offset = clampPage(limit, offset)
	pattern := "%" + query + "%"
	rows, err := q.QueryContext(ctx, `
        SELECT `+treeColumns+`
        FROM trees
        WHERE deleted_at IS NULL
          AND (title LIKE ? OR description LIKE ?)
        ORDER BY created_at DESC
        LIMIT ? OFFSET ?`, pattern, pattern, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("db: search trees: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectTrees(rows)
}

// clampPage applies the same limit/offset policy as PGTreeRepo.
func clampPage(limit, offset int) (int, int) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// collectTrees drains rows into a []db.Tree.
func collectTrees(rows *sql.Rows) ([]db.Tree, error) {
	var out []db.Tree
	for rows.Next() {
		var t db.Tree
		if err := scanTree(rows, &t); err != nil {
			return nil, fmt.Errorf("db: scan tree: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: scan tree: %w", err)
	}
	return out, nil
}
