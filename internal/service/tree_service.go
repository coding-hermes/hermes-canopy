// Package service contains the business logic layer that sits between
// the HTTP handlers and the database repositories. TreeService is the
// implementation of the tree CRUD contract defined in SPEC-API-02 §7.
//
// The service is transport-agnostic (no chi / http imports) and depends
// only on the repository interfaces in internal/db plus a pgxpool for
// the multi-row CreateTree transaction.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
)

// treeColumns mirrors db.treeColumns so the service can query the trees
// table without depending on the (unexported) repository constant. Kept
// in lockstep with internal/db/tree_repo.go.
const treeColumns = `id, owner_id, title, description, root_node_id,
    metadata, created_at, edited_at, deleted_at`

// collectTreeRows drains a pgx.Rows into a []db.Tree slice. Mirrors
// collectNodes / collectEdges in internal/db. Any scan error is
// returned and wrapped by the caller.
func collectTreeRows(rows pgx.Rows) ([]db.Tree, error) {
	var out []db.Tree
	for rows.Next() {
		var t db.Tree
		if err := rows.Scan(
			&t.ID, &t.OwnerID, &t.Title, &t.Description, &t.RootNodeID,
			&t.Metadata, &t.CreatedAt, &t.EditedAt, &t.DeletedAt,
		); err != nil {
			return nil, fmt.Errorf("scan tree: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// --- Error sentinels (SPEC-API-02 §7.2) -------------------------------------
//
// All errors are wrapped with %w at call sites so callers can use
// errors.Is() to classify. None of them carry user-facing copy; the
// HTTP layer is responsible for translating them into status codes.

var (
	ErrTreeNotFound         = errors.New("tree service: tree not found")
	ErrTreeDeleted          = errors.New("tree service: tree is soft-deleted")
	ErrTreeAlreadyDeleted   = errors.New("tree service: tree already deleted")
	ErrNotTreeOwner         = errors.New("tree service: not the tree owner")
	ErrNotTreeMember        = errors.New("tree service: not a tree member")
	ErrTitleRequired        = errors.New("tree service: title is required")
	ErrTitleTooLong         = errors.New("tree service: title exceeds 200 characters")
	ErrDescriptionTooLong   = errors.New("tree service: description exceeds 2000 characters")
	ErrRootContentRequired  = errors.New("tree service: root message content is required")
	ErrRootContentTooLarge  = errors.New("tree service: root message content exceeds 100000 characters")
	ErrInvalidContentFormat = errors.New("tree service: invalid content format")
	ErrInvalidNodeType      = errors.New("tree service: invalid node type")
	ErrInvalidCursor        = errors.New("tree service: invalid cursor UUID")
	ErrInvalidSort          = errors.New("tree service: invalid sort order")
	ErrInvalidStatus        = errors.New("tree service: invalid status filter")
	ErrInvalidRole          = errors.New("tree service: invalid role filter")
	ErrSearchTooShort       = errors.New("tree service: search query must be at least 3 characters")
	ErrDatabaseUnavailable  = errors.New("tree service: database unavailable")
)

// --- Validation limits (SPEC-API-02 §3-6) -----------------------------------

const (
	maxTitleLen       = 200
	maxDescriptionLen = 2000
	maxRootContentLen = 100_000
	minSearchLen      = 3
	maxSearchLen      = 200
	defaultListLimit  = 50
	maxListLimit      = 100
)

// --- Enums (SPEC-API-02 §7.1) -----------------------------------------------

// TreeSortOrder selects how ListTrees orders the result set.
type TreeSortOrder string

const (
	SortCreatedDesc TreeSortOrder = "created_desc"
	SortCreatedAsc  TreeSortOrder = "created_asc"
	SortUpdatedDesc TreeSortOrder = "updated_desc"
	SortUpdatedAsc  TreeSortOrder = "updated_asc"
	SortTitleAsc    TreeSortOrder = "title_asc"
	SortTitleDesc   TreeSortOrder = "title_desc"
)

// Valid reports whether the value is one of the enumerated sort orders.
// An empty value is treated as invalid here; the caller is expected to
// default to SortCreatedDesc before calling Valid.
func (s TreeSortOrder) Valid() bool {
	switch s {
	case SortCreatedDesc, SortCreatedAsc,
		SortUpdatedDesc, SortUpdatedAsc,
		SortTitleAsc, SortTitleDesc:
		return true
	}
	return false
}

// TreeStatusFilter controls which soft-delete states ListTrees returns.
type TreeStatusFilter string

const (
	TreeStatusActive  TreeStatusFilter = "active"
	TreeStatusDeleted TreeStatusFilter = "deleted"
	TreeStatusAll     TreeStatusFilter = "all"
)

// Valid reports whether the value is one of the enumerated status
// filters. Empty is treated as invalid; callers must default to
// TreeStatusActive.
func (s TreeStatusFilter) Valid() bool {
	switch s {
	case TreeStatusActive, TreeStatusDeleted, TreeStatusAll:
		return true
	}
	return false
}

// MemberRole names the per-tree membership role.
type MemberRole string

const (
	RoleOwner  MemberRole = "owner"
	RoleAdmin  MemberRole = "admin"
	RoleMember MemberRole = "member"
	RoleViewer MemberRole = "viewer"
)

// Valid reports whether the value is one of the enumerated roles.
func (r MemberRole) Valid() bool {
	switch r {
	case RoleOwner, RoleAdmin, RoleMember, RoleViewer:
		return true
	}
	return false
}

// ContentFormat names the supported node content formats. The spec
// (§7.1) defines the canonical set as markdown / plain / code; the DB
// CHECK constraint in 000003_nodes.up.sql currently allows markdown /
// plain / rich. The service validates against the spec contract — the
// DB-level mismatch is tracked separately as spec drift and not
// remediated here (out of scope for BE-03).
type ContentFormat string

const (
	FormatMarkdown ContentFormat = "markdown"
	FormatPlain    ContentFormat = "plain"
	FormatCode     ContentFormat = "code"
)

// Valid reports whether the value is one of the spec's enumerated
// content formats.
func (c ContentFormat) Valid() bool {
	switch c {
	case FormatMarkdown, FormatPlain, FormatCode:
		return true
	}
	return false
}

// NodeType names the supported root-node kinds. As with ContentFormat,
// the spec's enumeration (message / announcement) diverges from the DB
// CHECK constraint (message / synthesis / system). The service
// validates against the spec contract; downstream reconciliation is
// tracked as spec drift.
type NodeType string

const (
	NodeTypeMessage      NodeType = "message"
	NodeTypeAnnouncement NodeType = "announcement"
)

// Valid reports whether the value is one of the spec's enumerated node
// types.
func (n NodeType) Valid() bool {
	switch n {
	case NodeTypeMessage, NodeTypeAnnouncement:
		return true
	}
	return false
}

// --- Request / response types (SPEC-API-02 §7.1) ----------------------------

// ListTreesParams encapsulates all query parameters for listing trees.
type ListTreesParams struct {
	UserID uuid.UUID        // authenticated user (from JWT context)
	Cursor *uuid.UUID       // pagination cursor (nil on first page)
	Limit  int              // 1–100, clamped
	Sort   TreeSortOrder    // created_desc (default), created_asc, etc.
	Status TreeStatusFilter // active, deleted, all
	Role   *MemberRole      // optional role filter
	Search string           // full-text search, optional
}

// ListTreesResult contains the page of trees and pagination metadata.
type ListTreesResult struct {
	Trees      []TreeSummary `json:"trees"`
	NextCursor *uuid.UUID    `json:"next_cursor"` // nil if has_more == false
	HasMore    bool          `json:"has_more"`
	Total      int           `json:"total"` // approximate
	Limit      int           `json:"limit"`
}

// CreateTreeParams carries the input for tree creation.
type CreateTreeParams struct {
	OwnerID       uuid.UUID
	Title         string
	Description   string
	RootContent   string
	ContentFormat ContentFormat   // markdown, plain, code
	NodeType      NodeType        // message, announcement
	Metadata      json.RawMessage // optional jsonb metadata (defaults to '{}')
}

// GetTreeOptions controls whether optional fields are included.
type GetTreeOptions struct {
	IncludeStats   bool
	IncludeMembers bool
	IncludeRelated bool // populate Related from metadata (WIRE-006)
}

// Tree is the response from CreateTree.
type Tree struct {
	ID          uuid.UUID  `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	OwnerID     uuid.UUID  `json:"owner_id"`
	RootNodeID  uuid.UUID  `json:"root_node_id"`
	NodeCount   int        `json:"node_count"`
	MemberCount int        `json:"member_count"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	Role        MemberRole `json:"role"`
}

// TreeSummary is the list-view representation of a tree.
type TreeSummary struct {
	ID               uuid.UUID  `json:"id"`
	Title            string     `json:"title"`
	Description      string     `json:"description"`
	OwnerID          uuid.UUID  `json:"owner_id"`
	OwnerDisplayName string     `json:"owner_display_name"`
	NodeCount        int        `json:"node_count"`
	MemberCount      int        `json:"member_count"`
	RootNodeID       uuid.UUID  `json:"root_node_id"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	Role             MemberRole `json:"role"`
}

// TreeDetail is the full representation with optional stats and
// members. The embedded TreeSummary supplies the base fields; the
// trailing pointers/slices are populated only when requested.
type TreeDetail struct {
	TreeSummary
	DeletedAt *time.Time      `json:"deleted_at,omitempty"`
	Stats     *TreeStats      `json:"stats,omitempty"`
	Members   []MemberSummary `json:"members,omitempty"`
	Related   *Related        `json:"related,omitempty"`
}

// Related holds session-lineage associations extracted from tree
// metadata (WIRE-006). It is additive and optional — every field is
// null/empty when the tree has no association metadata (e.g. trees not
// imported from Hermes sessions).
type Related struct {
	Parent           *RelatedRef     `json:"parent,omitempty"`
	Children         []RelatedRef    `json:"children,omitempty"`
	BoardTask        *string         `json:"board_task,omitempty"`
	Project          *string         `json:"project,omitempty"`
	CommitHash       *string         `json:"commit_hash,omitempty"`
	DelegationGoals  []DelegationRef `json:"delegation_goals,omitempty"`
}

// RelatedRef is a lightweight {id, title} reference to a related tree.
type RelatedRef struct {
	ID    uuid.UUID `json:"id"`
	Title string    `json:"title"`
}

// DelegationRef is a delegation goal extracted from tree metadata.
type DelegationRef struct {
	DelegationID string `json:"delegation_id"`
	Goal         string `json:"goal"`
}

// TreeStats holds computed aggregate statistics.
type TreeStats struct {
	NodeCount        int `json:"node_count"`
	MemberCount      int `json:"member_count"`
	BranchCount      int `json:"branch_count"`
	MaxDepth         int `json:"max_depth"`
	PendingApprovals int `json:"pending_approvals"`
}

// MemberSummary is a lightweight member representation.
type MemberSummary struct {
	UserID      uuid.UUID  `json:"user_id"`
	DisplayName string     `json:"display_name"`
	Role        MemberRole `json:"role"`
	JoinedAt    time.Time  `json:"joined_at"`
}

// --- Service interface + implementation ------------------------------------

// TreeService is the business logic layer for tree CRUD. All methods
// operate within a transaction context passed via ctx.
type TreeService interface {
	// ListTrees returns a paginated page of trees matching the filter.
	ListTrees(ctx context.Context, params ListTreesParams) (*ListTreesResult, error)
	// CreateTree atomically creates a tree with its root node.
	CreateTree(ctx context.Context, params CreateTreeParams) (*Tree, error)
	// GetTree returns a single tree with optional stats and members.
	GetTree(ctx context.Context, treeID uuid.UUID, opts GetTreeOptions) (*TreeDetail, error)
	// UpdateTree partially updates a tree's title and/or description.
	UpdateTree(ctx context.Context, treeID uuid.UUID, title, description *string) (*Tree, error)
	// UpdateTreeMetadata replaces the metadata jsonb column of a tree
	// (WIRE-006 backfill). Used by the session associations-backfill
	// command to enrich already-imported trees without re-creating them.
	UpdateTreeMetadata(ctx context.Context, treeID uuid.UUID, metadata json.RawMessage) error
	// DeleteTree soft-deletes a tree and returns the deletion timestamp.
	DeleteTree(ctx context.Context, treeID uuid.UUID) (deletedAt time.Time, err error)
}

// TreeServiceImpl is the pgx-backed implementation of TreeService.
// All repository dependencies are taken as interfaces so the service
// can be tested with stubs; pool is the underlying pgxpool used to
// open the CreateTree transaction.
type TreeServiceImpl struct {
	treeRepo db.TreeRepo
	nodeRepo db.NodeRepo
	edgeRepo db.EdgeRepo
	pool     *pgxpool.Pool
	now      func() time.Time // injectable for testing
}

// NewTreeService wires the repositories + pool into a TreeServiceImpl.
// now defaults to time.Now when nil — callers can override for tests.
func NewTreeService(treeRepo db.TreeRepo, nodeRepo db.NodeRepo, edgeRepo db.EdgeRepo, pool *pgxpool.Pool) *TreeServiceImpl {
	return &TreeServiceImpl{
		treeRepo: treeRepo,
		nodeRepo: nodeRepo,
		edgeRepo: edgeRepo,
		pool:     pool,
		now:      time.Now,
	}
}

// --- CreateTree -------------------------------------------------------------

// CreateTree creates a new tree with an auto-created root node inside a
// single transaction. The caller becomes the tree owner. Returns the
// created tree with populated root_node_id.
//
// Step 6 (INSERT tree_members) is best-effort: the spec calls for an
// owner membership row, but the tree_members table is not yet in the
// migration set. If the table is absent we log and continue so the
// MVP path still succeeds. Once the table lands in a migration the
// query will simply start working.
func (s *TreeServiceImpl) CreateTree(ctx context.Context, p CreateTreeParams) (*Tree, error) {
	if err := validateCreateTree(p); err != nil {
		return nil, err
	}
	if s.pool == nil {
		return nil, ErrDatabaseUnavailable
	}

	treeID := uuid.New()
	now := s.now()

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("%w: begin tx: %v", ErrDatabaseUnavailable, err)
	}
	// best-effort rollback; Commit() on a committed tx is a no-op.
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. Insert tree row. The schema defaults root_node_id, created_at,
	// edited_at, and deleted_at server-side; we pass id, owner_id, title,
	// description, and metadata explicitly.
	var created db.Tree
	row := tx.QueryRow(ctx, `
        INSERT INTO trees (id, owner_id, title, description, metadata)
        VALUES ($1, $2, $3, $4, COALESCE($5, '{}'::jsonb))
        RETURNING id, owner_id, title, description, root_node_id,
                  metadata, created_at, edited_at, deleted_at`,
		treeID, p.OwnerID, strings.TrimSpace(p.Title), p.Description, p.Metadata,
	)
	if err := row.Scan(
		&created.ID, &created.OwnerID, &created.Title, &created.Description,
		&created.RootNodeID, &created.Metadata,
		&created.CreatedAt, &created.EditedAt, &created.DeletedAt,
	); err != nil {
		return nil, fmt.Errorf("%w: insert tree: %v", ErrDatabaseUnavailable, err)
	}

	// 2. Insert tree_members row (owner). Tolerate missing table — see
	// the docstring on CreateTree for why this is best-effort.
	if _, err := tx.Exec(ctx, `
        INSERT INTO tree_members (tree_id, user_id, role, joined_at)
        VALUES ($1, $2, $3, $4)`,
		created.ID, p.OwnerID, string(RoleOwner), now,
	); err != nil && !isUndefinedTable(err) {
		return nil, fmt.Errorf("%w: insert tree_members: %v", ErrDatabaseUnavailable, err)
	}

	// 3. Insert root node.
	var rootID uuid.UUID
	if err := tx.QueryRow(ctx, `
        INSERT INTO nodes
            (tree_id, parent_id, author_id, content, content_format,
             node_type, metadata)
        VALUES ($1, NULL, $2, $3, $4, $5, '{}'::jsonb)
        RETURNING id`,
		created.ID, p.OwnerID, p.RootContent,
		string(p.ContentFormat), string(p.NodeType),
	).Scan(&rootID); err != nil {
		return nil, fmt.Errorf("%w: insert root node: %v", ErrDatabaseUnavailable, err)
	}

	// 4. Backfill root_node_id on the tree.
	if _, err := tx.Exec(ctx,
		`UPDATE trees SET root_node_id = $2, edited_at = $3 WHERE id = $1`,
		created.ID, rootID, now,
	); err != nil {
		return nil, fmt.Errorf("%w: set root_node_id: %v", ErrDatabaseUnavailable, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("%w: commit: %v", ErrDatabaseUnavailable, err)
	}

	return &Tree{
		ID:          created.ID,
		Title:       created.Title,
		Description: created.Description,
		OwnerID:     created.OwnerID,
		RootNodeID:  rootID,
		NodeCount:   1,
		MemberCount: 1,
		CreatedAt:   created.CreatedAt,
		UpdatedAt:   coalesceTime(created.EditedAt, created.CreatedAt),
		Role:        RoleOwner,
	}, nil
}

// --- ImportedBefore (session import dedup) ----------------------------------

// ImportedBefore reports whether any non-deleted node carries the given
// session_id in its metadata, OR any non-deleted tree carries it in
// trees.metadata. This covers both legacy imports (session_id on child
// nodes only) and new imports (session_id on tree metadata). The query
// is cheap (indexed jsonb access path) and runs once per candidate
// session during import.
func (s *TreeServiceImpl) ImportedBefore(ctx context.Context, sessionID string) (bool, error) {
	if s.pool == nil {
		return false, ErrDatabaseUnavailable
	}
	var exists bool
	err := s.pool.QueryRow(ctx, `
        SELECT EXISTS (
            SELECT 1 FROM nodes
            WHERE metadata->>'session_id' = $1 AND deleted_at IS NULL
        ) OR EXISTS (
            SELECT 1 FROM trees
            WHERE metadata->>'session_id' = $1 AND deleted_at IS NULL
        )`,
		sessionID,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("%w: check imported session: %v", ErrDatabaseUnavailable, err)
	}
	return exists, nil
}

// --- ListTrees --------------------------------------------------------------

// ListTrees returns a page of trees.
//
// The DEFAULT path (Status=active, no search, Sort=created_desc — what
// the frontend uses) uses true keyset pagination: a (created_at, id)
// cursor query at the repo level plus a real COUNT for the total. This
// means every page returns the correct set and the total reflects the
// actual number of active trees, not a window estimate. PAG-002.
//
// Non-default paths (other sorts, Status=deleted/all, search) still use
// the older offset-window + client-side sort + client-side cursor filter
// approach. Their total stays a window estimate unless the repo's Count
// is available — see the per-path comments below.
func (s *TreeServiceImpl) ListTrees(ctx context.Context, p ListTreesParams) (*ListTreesResult, error) {
	if err := validateListTrees(p); err != nil {
		return nil, err
	}

	limit := clampLimit(p.Limit)
	status := p.Status
	if status == "" {
		status = TreeStatusActive
	}
	sortOrder := p.Sort
	if sortOrder == "" {
		sortOrder = SortCreatedDesc
	}

	// --- DEFAULT PATH: keyset pagination on (created_at, id) ----------
	// This is the path the frontend hits: active trees, no search,
	// created_desc (the default). It uses the repo's keyset query +
	// real COUNT so total and cursor pages are correct for any dataset
	// size. PAG-002.
	isDefaultSort := sortOrder == SortCreatedDesc
	hasSearch := strings.TrimSpace(p.Search) != ""
	if status == TreeStatusActive && !hasSearch && isDefaultSort {
		return s.listTreesKeyset(ctx, p, limit)
	}

	// --- LEGACY PATH: offset window + client-side sort + cursor filter --
	// Used by non-default sorts, Status=deleted/all, and search. The
	// cursor filter still runs over the fetched window, so cursor pages
	// beyond limit+1 may be incomplete — this is accepted scope for
	// PAG-002 (the default path is the one the frontend uses).
	var rows []db.Tree
	var err error
	switch {
	case hasSearch:
		rows, err = s.searchTrees(ctx, p.Search, limit+1, 0)
	case status == TreeStatusActive:
		rows, err = s.treeRepo.List(ctx, limit+1, 0)
	case status == TreeStatusAll:
		rows, err = s.listAllIncludingDeleted(ctx, limit+1, 0)
	case status == TreeStatusDeleted:
		rows, err = s.listDeletedOnly(ctx, limit+1, 0)
	}
	if err != nil && (!errors.Is(err, db.ErrNotFound) || !hasSearch) {
		return nil, fmt.Errorf("%w: list trees: %v", ErrDatabaseUnavailable, err)
	}
	if rows == nil {
		rows = []db.Tree{}
	}

	// Sort client-side: the repo's ORDER BY is fixed (created_at DESC).
	sortTreeRows(rows, sortOrder)

	// Apply cursor — client-side filter over the fetched window.
	if p.Cursor != nil {
		filtered := rows[:0]
		for _, t := range rows {
			if pastCursor(t.ID, *p.Cursor, sortOrder) {
				filtered = append(filtered, t)
			}
		}
		rows = filtered
	}

	// Role filter — for MVP there is no tree_members source the service
	// can consult, so we treat Role != nil as "owner-only" when set and
	// leave multi-role filtering for BE-XX (members table wiring).
	if p.Role != nil && *p.Role != RoleOwner {
		rows = nil
	}

	// Total: try a real COUNT for the active path; otherwise fall back
	// to the window size (acceptable scope for non-default paths).
	total := len(rows)
	if status == TreeStatusActive && !hasSearch {
		if n, countErr := s.treeRepo.Count(ctx); countErr == nil {
			total = n
		}
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}

	summaries := make([]TreeSummary, 0, len(rows))
	for _, t := range rows {
		summaries = append(summaries, treeToSummary(t))
	}

	var next *uuid.UUID
	if hasMore && len(summaries) > 0 {
		id := summaries[len(summaries)-1].ID
		next = &id
	}

	return &ListTreesResult{
		Trees:      summaries,
		NextCursor: next,
		HasMore:    hasMore,
		Total:      total,
		Limit:      limit,
	}, nil
}

// listTreesKeyset is the default-path keyset pagination implementation.
// It queries limit+1 rows from the repo's keyset method (the +1 detects
// hasMore without an extra round-trip) and uses Count for the real
// total. nextCursor is the id of the last row in the returned page.
// PAG-002.
func (s *TreeServiceImpl) listTreesKeyset(ctx context.Context, p ListTreesParams, limit int) (*ListTreesResult, error) {
	rows, err := s.treeRepo.ListKeyset(ctx, p.Cursor, limit+1)
	if err != nil {
		return nil, fmt.Errorf("%w: list trees keyset: %v", ErrDatabaseUnavailable, err)
	}

	total := 0
	if n, countErr := s.treeRepo.Count(ctx); countErr == nil {
		total = n
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}

	// Role filter — same MVP semantics as the legacy path.
	if p.Role != nil && *p.Role != RoleOwner {
		rows = nil
	}

	summaries := make([]TreeSummary, 0, len(rows))
	for _, t := range rows {
		summaries = append(summaries, treeToSummary(t))
	}

	var next *uuid.UUID
	if hasMore && len(summaries) > 0 {
		id := summaries[len(summaries)-1].ID
		next = &id
	}

	return &ListTreesResult{
		Trees:      summaries,
		NextCursor: next,
		HasMore:    hasMore,
		Total:      total,
		Limit:      limit,
	}, nil
}

// --- GetTree ----------------------------------------------------------------

// GetTree returns a single tree by ID with optional computed stats and
// member list. Membership enforcement is delegated to middleware
// upstream; this method assumes the caller has already been authorised
// and only validates that the tree exists and is not soft-deleted.
func (s *TreeServiceImpl) GetTree(ctx context.Context, treeID uuid.UUID, opts GetTreeOptions) (*TreeDetail, error) {
	if treeID == uuid.Nil {
		return nil, ErrTreeNotFound
	}

	// Active lookup.
	tree, err := s.treeRepo.GetByID(ctx, treeID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			// Distinguish "deleted" from "never existed" by checking the
			// soft-deleted rows directly.
			if s.pool != nil {
				var deletedAt time.Time
				row := s.pool.QueryRow(ctx,
					`SELECT deleted_at FROM trees WHERE id = $1 AND deleted_at IS NOT NULL`,
					treeID,
				)
				if scanErr := row.Scan(&deletedAt); scanErr == nil {
					return nil, fmt.Errorf("%w (deleted_at=%s)", ErrTreeDeleted, deletedAt.Format(time.RFC3339))
				}
			}
			return nil, ErrTreeNotFound
		}
		return nil, fmt.Errorf("%w: get tree: %v", ErrDatabaseUnavailable, err)
	}

	detail := &TreeDetail{
		TreeSummary: treeToSummary(*tree),
	}

	if opts.IncludeStats && s.pool != nil {
		stats, err := s.computeStats(ctx, tree.ID, *tree)
		if err != nil {
			return nil, fmt.Errorf("%w: stats: %v", ErrDatabaseUnavailable, err)
		}
		detail.Stats = stats
	}

	if opts.IncludeMembers {
		// The tree_members table is not yet in the migration set, so we
		// return an empty slice. Once the table lands, populate via a
		// SELECT joining tree_members + profiles (SPEC-DM-04).
		detail.Members = []MemberSummary{}
	}

	if opts.IncludeRelated {
		detail.Related = s.extractRelated(ctx, tree.Metadata)
	}

	return detail, nil
}

// extractRelated parses session-lineage metadata from a tree's metadata
// jsonb and resolves session IDs to Canopy tree refs. It is best-effort
// — any error (missing metadata, missing session_id key, no matching
// trees) produces a nil/empty Related rather than failing the request.
func (s *TreeServiceImpl) extractRelated(ctx context.Context, metadata []byte) *Related {
	var meta struct {
		SessionID       string `json:"session_id"`
		ParentSessionID string `json:"parent_session_id"`
		ChildSessionIDs []string `json:"child_session_ids"`
		Project         string `json:"project"`
		BoardTask       string `json:"board_task"`
		CommitHash      string `json:"commit_hash"`
		DelegationGoals []struct {
			DelegationID string `json:"delegation_id"`
			Goal         string `json:"goal"`
		} `json:"delegation_goals"`
	}
	if err := json.Unmarshal(metadata, &meta); err != nil {
		return nil
	}
	if meta.SessionID == "" {
		return nil // not an imported session tree
	}

	related := &Related{}

	// Collect all session IDs we need to resolve to tree UUIDs.
	needed := make([]string, 0, 1+len(meta.ChildSessionIDs))
	if meta.ParentSessionID != "" {
		needed = append(needed, meta.ParentSessionID)
	}
	needed = append(needed, meta.ChildSessionIDs...)

	// Resolve to trees (best-effort — pool may be nil in tests).
	if s.pool != nil && len(needed) > 0 {
		lookup, err := s.GetTreesBySessionIDs(ctx, needed)
		if err == nil {
			if meta.ParentSessionID != "" {
				if parent, ok := lookup[meta.ParentSessionID]; ok && parent != nil {
					related.Parent = &RelatedRef{ID: parent.ID, Title: parent.Title}
				}
			}
			for _, childID := range meta.ChildSessionIDs {
				if child, ok := lookup[childID]; ok && child != nil {
					related.Children = append(related.Children, RelatedRef{ID: child.ID, Title: child.Title})
				}
			}
		}
	}

	// Scalar fields — nil when empty so they're omitted from JSON.
	if meta.BoardTask != "" {
		v := meta.BoardTask
		related.BoardTask = &v
	}
	if meta.Project != "" {
		v := meta.Project
		related.Project = &v
	}
	if meta.CommitHash != "" {
		v := meta.CommitHash
		related.CommitHash = &v
	}

	// Delegation goals.
	for _, dg := range meta.DelegationGoals {
		related.DelegationGoals = append(related.DelegationGoals, DelegationRef{
			DelegationID: dg.DelegationID,
			Goal:         dg.Goal,
		})
	}

	// Return nil when nothing was found (cleaner JSON).
	if related.Parent == nil && len(related.Children) == 0 &&
		related.BoardTask == nil && related.Project == nil &&
		related.CommitHash == nil && len(related.DelegationGoals) == 0 {
		return nil
	}

	return related
}

// --- DeleteTree -------------------------------------------------------------

// DeleteTree soft-deletes a tree. The spec (§5.4) requires owner-only
// authorisation; since the trees table has no owner-side ACL column,
// authorisation is delegated to the upstream middleware (which checks
// caller == tree.owner_id). This method only performs the soft-delete
// and disambiguates "not found" from "already deleted".
//
// Returns the deleted_at timestamp for inclusion in the SSE event
// payload.
func (s *TreeServiceImpl) DeleteTree(ctx context.Context, treeID uuid.UUID) (time.Time, error) {
	if treeID == uuid.Nil {
		return time.Time{}, ErrTreeNotFound
	}
	if err := s.treeRepo.SoftDelete(ctx, treeID); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			// Either the tree never existed or it's already soft-deleted.
			// Check the latter explicitly so callers get a meaningful error.
			if s.pool != nil {
				var deletedAt time.Time
				row := s.pool.QueryRow(ctx,
					`SELECT deleted_at FROM trees WHERE id = $1 AND deleted_at IS NOT NULL`,
					treeID,
				)
				if scanErr := row.Scan(&deletedAt); scanErr == nil {
					return time.Time{}, ErrTreeAlreadyDeleted
				}
			}
			return time.Time{}, ErrTreeNotFound
		}
		return time.Time{}, fmt.Errorf("%w: soft-delete: %v", ErrDatabaseUnavailable, err)
	}

	// SoftDelete returned nil — read the freshly-stamped deleted_at.
	var deletedAt time.Time
	if s.pool != nil {
		row := s.pool.QueryRow(ctx,
			`SELECT deleted_at FROM trees WHERE id = $1`, treeID,
		)
		if scanErr := row.Scan(&deletedAt); scanErr != nil {
			// Fall back to "now" — the row IS deleted, we just couldn't
			// read the timestamp. The downstream SSE event will still be
			// semantically correct.
			return s.now(), nil
		}
	} else {
		deletedAt = s.now()
	}
	return deletedAt, nil
}

// UpdateTree partially updates a tree's mutable fields.
func (s *TreeServiceImpl) UpdateTree(ctx context.Context, treeID uuid.UUID, title, description *string) (*Tree, error) {
	if treeID == uuid.Nil {
		return nil, ErrTreeNotFound
	}
	existing, err := s.treeRepo.GetByID(ctx, treeID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, ErrTreeNotFound
		}
		return nil, fmt.Errorf("%w: get tree for update: %v", ErrDatabaseUnavailable, err)
	}
	if title != nil {
		trimmed := strings.TrimSpace(*title)
		if trimmed == "" {
			return nil, ErrTitleRequired
		}
		if utf8Len(trimmed) > maxTitleLen {
			return nil, ErrTitleTooLong
		}
		existing.Title = trimmed
	}
	if description != nil {
		if utf8Len(*description) > maxDescriptionLen {
			return nil, ErrDescriptionTooLong
		}
		existing.Description = *description
	}
	updated, err := s.treeRepo.Update(ctx, existing)
	if err != nil {
		return nil, fmt.Errorf("%w: update tree: %v", ErrDatabaseUnavailable, err)
	}
	svcTree := &Tree{
		ID:          updated.ID,
		Title:       updated.Title,
		Description: updated.Description,
		OwnerID:     updated.OwnerID,
		NodeCount:   1,
		MemberCount: 1,
		CreatedAt:   updated.CreatedAt,
		UpdatedAt:   coalesceTime(updated.EditedAt, updated.CreatedAt),
		Role:        RoleOwner,
	}
	if updated.RootNodeID != nil {
		svcTree.RootNodeID = *updated.RootNodeID
	}
	return svcTree, nil
}

// --- UpdateTreeMetadata (WIRE-006) ------------------------------------------

// UpdateTreeMetadata replaces the metadata jsonb column of an active
// tree. Used by the session associations-backfill command to enrich
// already-imported trees with parent/children/delegation/task metadata
// without re-creating them. The caller is responsible for the content
// of metadata; this method only validates the tree exists and is active.
func (s *TreeServiceImpl) UpdateTreeMetadata(ctx context.Context, treeID uuid.UUID, metadata json.RawMessage) error {
	if treeID == uuid.Nil {
		return ErrTreeNotFound
	}
	if s.pool == nil {
		return ErrDatabaseUnavailable
	}
	// Verify the tree exists and is active.
	if _, err := s.treeRepo.GetByID(ctx, treeID); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return ErrTreeNotFound
		}
		return fmt.Errorf("%w: get tree for metadata update: %v", ErrDatabaseUnavailable, err)
	}
	// Replace metadata. Default to '{}' when nil/empty.
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE trees SET metadata = $2, edited_at = clock_timestamp() WHERE id = $1 AND deleted_at IS NULL`,
		treeID, metadata,
	)
	if err != nil {
		return fmt.Errorf("%w: update tree metadata: %v", ErrDatabaseUnavailable, err)
	}
	return nil
}

// GetTreesBySessionIDs returns active trees whose metadata->>'session_id'
// matches any of the given session IDs. Used by the association layer
// to resolve session IDs to Canopy tree UUIDs + titles for the Related
// field. Trees not found (no matching session_id) are simply absent
// from the result — the caller handles missing entries.
func (s *TreeServiceImpl) GetTreesBySessionIDs(ctx context.Context, sessionIDs []string) (map[string]*TreeSummary, error) {
	if s.pool == nil {
		return nil, ErrDatabaseUnavailable
	}
	if len(sessionIDs) == 0 {
		return map[string]*TreeSummary{}, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+treeColumns+`
		FROM trees
		WHERE deleted_at IS NULL AND metadata->>'session_id' = ANY($1)`,
		sessionIDs,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: get trees by session ids: %v", ErrDatabaseUnavailable, err)
	}
	defer rows.Close()
	trees, err := collectTreeRows(rows)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*TreeSummary, len(trees))
	for i := range trees {
		var meta struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(trees[i].Metadata, &meta)
		if meta.SessionID != "" {
			summary := treeToSummary(trees[i])
			out[meta.SessionID] = &summary
		}
	}
	return out, nil
}

// --- Validation helpers -----------------------------------------------------

func validateCreateTree(p CreateTreeParams) error {
	title := strings.TrimSpace(p.Title)
	if title == "" {
		return ErrTitleRequired
	}
	if utf8Len(title) > maxTitleLen {
		return ErrTitleTooLong
	}
	if utf8Len(p.Description) > maxDescriptionLen {
		return ErrDescriptionTooLong
	}
	if strings.TrimSpace(p.RootContent) == "" {
		return ErrRootContentRequired
	}
	if utf8Len(p.RootContent) > maxRootContentLen {
		return ErrRootContentTooLarge
	}
	if !p.ContentFormat.Valid() {
		return ErrInvalidContentFormat
	}
	if !p.NodeType.Valid() {
		return ErrInvalidNodeType
	}
	return nil
}

func validateListTrees(p ListTreesParams) error {
	if p.Cursor != nil && *p.Cursor == uuid.Nil {
		return ErrInvalidCursor
	}
	// Limit is clamped silently — no error on out-of-range. The caller
	// may pass 0 or any int; we normalise to [1, 100] with 50 default.
	if p.Sort != "" && !p.Sort.Valid() {
		return ErrInvalidSort
	}
	if p.Status != "" && !p.Status.Valid() {
		return ErrInvalidStatus
	}
	if p.Role != nil && !p.Role.Valid() {
		return ErrInvalidRole
	}
	search := strings.TrimSpace(p.Search)
	if search != "" {
		if utf8Len(search) < minSearchLen {
			return ErrSearchTooShort
		}
		if utf8Len(search) > maxSearchLen {
			return ErrSearchTooShort
		}
	}
	return nil
}

// --- Stats computation ------------------------------------------------------

// computeStats builds a TreeStats using the SQL patterns from SPEC-API-02
// §5.5. Pending approvals is hard-coded to 0 because the approvals table
// is not yet in the migration set.
func (s *TreeServiceImpl) computeStats(ctx context.Context, treeID uuid.UUID, tree db.Tree) (*TreeStats, error) {
	stats := &TreeStats{
		NodeCount:        1, // root node counts as one
		MemberCount:      1, // owner counts as one
		BranchCount:      0,
		MaxDepth:         1,
		PendingApprovals: 0,
	}

	// Node count (active only — root included).
	if err := s.pool.QueryRow(ctx, `
        SELECT COUNT(*)::int
        FROM nodes
        WHERE tree_id = $1 AND deleted_at IS NULL`, treeID,
	).Scan(&stats.NodeCount); err != nil {
		return nil, fmt.Errorf("count nodes: %w", err)
	}

	// Branch count: distinct parents that have at least one child node.
	if err := s.pool.QueryRow(ctx, `
        SELECT COUNT(DISTINCT parent_id)::int
        FROM nodes
        WHERE tree_id = $1
          AND parent_id IS NOT NULL
          AND deleted_at IS NULL`, treeID,
	).Scan(&stats.BranchCount); err != nil {
		return nil, fmt.Errorf("count branches: %w", err)
	}

	// Max depth via recursive CTE from the tree's root_node_id. Fall back
	// to 0 when the tree has no root node yet (shouldn't happen for
	// service-created trees but defensive against legacy rows).
	rootID := tree.RootNodeID
	if rootID != nil {
		var depth int
		err := s.pool.QueryRow(ctx, `
            WITH RECURSIVE chain AS (
                SELECT id, 0 AS depth
                FROM nodes
                WHERE id = $1 AND deleted_at IS NULL
                UNION ALL
                SELECT n.id, chain.depth + 1
                FROM chain
                JOIN nodes n ON n.parent_id = chain.id AND n.deleted_at IS NULL
                WHERE chain.depth < 10000
            )
            SELECT COALESCE(MAX(depth), 0) FROM chain`, *rootID,
		).Scan(&depth)
		if err == nil {
			stats.MaxDepth = depth + 1 // include the root itself
		} else {
			// The recursive walk can fail on cycles; fall back to 0.
			log.Warn().Err(err).Str("tree_id", treeID.String()).
				Msg("tree service: max depth CTE failed, falling back")
			stats.MaxDepth = 0
		}
	}

	return stats, nil
}

// --- Repo accessors that the existing treeRepo does not expose ------------

// searchTrees delegates to the repo's Search but translates db.ErrNotFound
// into an empty result so callers can ask "is there anything matching?".
func (s *TreeServiceImpl) searchTrees(ctx context.Context, query string, limit, offset int) ([]db.Tree, error) {
	rows, err := s.treeRepo.Search(ctx, query, limit, offset)
	if errors.Is(err, db.ErrNotFound) {
		return []db.Tree{}, nil
	}
	return rows, err
}

// listAllIncludingDeleted returns active + soft-deleted trees. Used when
// Status=TreeStatusAll. Order is created_at DESC to match the active-only
// listing; deleted rows are appended to the end so callers can paginate
// stably. Capped at 200 to mirror the repository's internal limit.
func (s *TreeServiceImpl) listAllIncludingDeleted(ctx context.Context, limit, offset int) ([]db.Tree, error) {
	rows, err := s.pool.Query(ctx, `
        SELECT `+treeColumns+`
        FROM trees
        ORDER BY (deleted_at IS NULL) DESC, created_at DESC
        LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("query trees all: %w", err)
	}
	defer rows.Close()
	return collectTreeRows(rows)
}

// listDeletedOnly returns only soft-deleted trees. Same ordering rule
// as listAllIncludingDeleted.
func (s *TreeServiceImpl) listDeletedOnly(ctx context.Context, limit, offset int) ([]db.Tree, error) {
	rows, err := s.pool.Query(ctx, `
        SELECT `+treeColumns+`
        FROM trees
        WHERE deleted_at IS NOT NULL
        ORDER BY deleted_at DESC
        LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("query trees deleted: %w", err)
	}
	defer rows.Close()
	return collectTreeRows(rows)
}

// --- Mapping helpers --------------------------------------------------------

func treeToSummary(t db.Tree) TreeSummary {
	sum := TreeSummary{
		ID:          t.ID,
		Title:       t.Title,
		Description: t.Description,
		OwnerID:     t.OwnerID,
		NodeCount:   1, // root only; full count requires GetCounts
		MemberCount: 1, // owner only
		CreatedAt:   t.CreatedAt,
		UpdatedAt:   coalesceTime(t.EditedAt, t.CreatedAt),
		Role:        RoleOwner,
	}
	if t.RootNodeID != nil {
		sum.RootNodeID = *t.RootNodeID
	}
	return sum
}

func coalesceTime(a *time.Time, b time.Time) time.Time {
	if a != nil {
		return *a
	}
	return b
}

func clampLimit(n int) int {
	if n <= 0 {
		return defaultListLimit
	}
	if n > maxListLimit {
		return maxListLimit
	}
	return n
}

// utf8Len counts the number of Unicode code points in s, which is what
// the spec limits (e.g. "title must be 1-200 chars"). Counting bytes
// would mis-measure non-ASCII titles.
func utf8Len(s string) int {
	count := 0
	for range s {
		count++
	}
	return count
}

// pastCursor is the cursor filter for ListTrees. UUIDv7 is time-ordered
// at the high bits so a string compare approximates the
// creation-time boundary; the title sorts treat the cursor as a
// "skip rows whose id sorts before the cursor" filter using the same
// primitive (cheap and predictable).
func pastCursor(id, cursor uuid.UUID, order TreeSortOrder) bool {
	switch order {
	case SortCreatedAsc, SortUpdatedAsc, SortTitleAsc:
		return id.String() > cursor.String()
	default:
		return id.String() < cursor.String()
	}
}

// sortTreeRows re-orders in place. TreeRepo.List returns rows in
// created_at DESC; for any other sort we have to do it here.
func sortTreeRows(rows []db.Tree, order TreeSortOrder) {
	if len(rows) < 2 {
		return
	}
	// Stable insertion sort keeps the relative ordering of equal keys
	// (e.g. two trees with the same created_at). For the cardinalities
	// we deal with (pages of 1-100) an O(n²) sort is cheaper than the
	// allocation overhead of sort.SliceStable.
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && treeLess(rows[j], rows[j-1], order); j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}

func treeLess(a, b db.Tree, order TreeSortOrder) bool {
	switch order {
	case SortCreatedAsc:
		return a.CreatedAt.Before(b.CreatedAt) ||
			(a.CreatedAt.Equal(b.CreatedAt) && a.ID.String() < b.ID.String())
	case SortUpdatedAsc:
		at := coalesceTime(a.EditedAt, a.CreatedAt)
		bt := coalesceTime(b.EditedAt, b.CreatedAt)
		return at.Before(bt) || (at.Equal(bt) && a.ID.String() < b.ID.String())
	case SortUpdatedDesc:
		at := coalesceTime(a.EditedAt, a.CreatedAt)
		bt := coalesceTime(b.EditedAt, b.CreatedAt)
		return at.After(bt) || (at.Equal(bt) && a.ID.String() > b.ID.String())
	case SortTitleAsc:
		return strings.ToLower(a.Title) < strings.ToLower(b.Title)
	case SortTitleDesc:
		return strings.ToLower(a.Title) > strings.ToLower(b.Title)
	case SortCreatedDesc, "":
		return a.CreatedAt.After(b.CreatedAt) ||
			(a.CreatedAt.Equal(b.CreatedAt) && a.ID.String() > b.ID.String())
	}
	return false
}

// isUndefinedTable returns true for the Postgres SQLSTATE 42P01
// ("undefined_table") — used to make the tree_members insert
// best-effort until that table is migrated in.
func isUndefinedTable(err error) bool {
	if err == nil {
		return false
	}
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState() == "42P01"
	}
	// Fallback: substring check for the driver-formatted message.
	return strings.Contains(err.Error(), "does not exist") ||
		strings.Contains(err.Error(), "undefined_table")
}
