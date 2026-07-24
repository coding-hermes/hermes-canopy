// Node service — business logic for node CRUD operations against the
// conversation DAG. Implements SPEC-API-03 §3-7. The service is
// transport-agnostic and depends only on the repository interfaces in
// internal/db plus a pgxpool for transactional CreateNode and direct
// depth/child-count queries.
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

// nodeColumns mirrors db.nodeColumns so the service can query the
// nodes table without depending on the unexported repository constant.
// Kept in lockstep with internal/db/node_repo.go.
const nodeColumns = `id, tree_id, parent_id, author_id, content,
    content_format, node_type, sequence_num, metadata, created_at,
    edited_at, deleted_at`

// edgeColumns mirrors db.edgeColumns for the same reason.
const edgeColumns = `id, tree_id, source_id, target_id, edge_type,
    sequence_num, metadata, created_at, deleted_at`

// --- Error sentinels (SPEC-API-03 §3.3, 4.3, 5.3, 7.3) ----------------------
//
// All errors are wrapped with %w at call sites so callers can use
// errors.Is() to classify. None of them carry user-facing copy; the
// HTTP layer is responsible for translating them into status codes.

var (
	ErrNodeNotFound          = errors.New("node service: node not found")
	ErrNodeDeleted           = errors.New("node service: node is soft-deleted")
	ErrNodeAlreadyDeleted    = errors.New("node service: node already deleted")
	ErrParentNotFound        = errors.New("node service: parent node not found")
	ErrParentDeleted         = errors.New("node service: parent is soft-deleted")
	ErrContentTooLong        = errors.New("node service: content exceeds 65536 characters")
	ErrSynthesisViaMergeOnly = errors.New("node service: synthesis nodes via merge endpoint only")
	ErrSystemNodeForbidden   = errors.New("node service: system nodes are server-generated")
	ErrInvalidEdgeType       = errors.New("node service: invalid edge type")
	ErrMetadataTooLarge      = errors.New("node service: metadata exceeds 16KB")
	ErrNodeAuthorRequired    = errors.New("node service: user is not the node author")
	ErrNoUpdateFields        = errors.New("node service: no fields provided for update")
	ErrForkRequiresChildren  = errors.New("node service: fork requires parent with at least one child")
)

// Note: ErrInvalidContentFormat, ErrInvalidNodeType, and
// ErrDatabaseUnavailable are defined in tree_service.go and shared
// across the service package (they apply to both tree and node CRUD).
// See tree_service.go's err block.

// --- Validation limits (SPEC-API-03 §12) ------------------------------------

const (
	maxContentLen    = 65536
	maxMetadataBytes = 16384
	defaultNodeLimit = 100
	maxNodeLimit     = 500
)

// --- Enums (SPEC-API-03 §3.3, 7.3) ------------------------------------------

// NodeContentFormat mirrors the spec's enumerated content formats.
type NodeContentFormat string

const (
	NodeFormatMarkdown NodeContentFormat = "markdown"
	NodeFormatPlain    NodeContentFormat = "plain"
	NodeFormatRich     NodeContentFormat = "rich"
)

// Valid reports whether the value is in the spec's enumerated set.
func (c NodeContentFormat) Valid() bool {
	switch c {
	case NodeFormatMarkdown, NodeFormatPlain, NodeFormatRich:
		return true
	}
	return false
}

// NodeNodeType mirrors the spec's enumerated node types.
type NodeNodeType string

const (
	NodeKindMessage   NodeNodeType = "message"
	NodeKindSynthesis NodeNodeType = "synthesis"
	NodeKindSystem    NodeNodeType = "system"
)

// Valid reports whether the value is in the spec's enumerated set.
func (n NodeNodeType) Valid() bool {
	switch n {
	case NodeKindMessage, NodeKindSynthesis, NodeKindSystem:
		return true
	}
	return false
}

// NodeEdgeType mirrors the spec's enumerated edge types accepted at
// create-time (reply/fork) — synthesis and reference are not creatable
// via the node CRUD endpoints.
type NodeEdgeType string

const (
	NodeEdgeReply NodeEdgeType = "reply"
	NodeEdgeFork  NodeEdgeType = "fork"
)

// Valid reports whether the value is in the spec's enumerated edge
// types for create.
func (e NodeEdgeType) Valid() bool {
	switch e {
	case NodeEdgeReply, NodeEdgeFork:
		return true
	}
	return false
}

// --- Request / response types (SPEC-API-03 §8.5) ----------------------------

// CreateNodeInput is the request body for POST /trees/{tree_id}/nodes.
// The service fills in TreeID from the URL path; the rest is supplied
// by the handler from the request body.
type CreateNodeInput struct {
	ParentID      uuid.UUID
	Content       string
	ContentFormat string // default "markdown"
	NodeType      string // default "message"
	EdgeType      string // default "reply"
	AuthorID      uuid.UUID
	TreeID        uuid.UUID
	Metadata      json.RawMessage
}

// UpdateNodeInput is the partial-update body for PATCH /nodes/{node_id}.
// All fields are optional; at least one must be provided.
type UpdateNodeInput struct {
	Content       *string
	ContentFormat *string
	Metadata      *json.RawMessage
}

// ReplyInput is the request body for POST /nodes/{node_id}/reply.
// ParentID and EdgeType are derived from the URL.
type ReplyInput struct {
	Content       string
	ContentFormat string
	NodeType      string
	AuthorID      uuid.UUID
	Metadata      json.RawMessage
}

// ForkInput is the request body for POST /nodes/{node_id}/fork.
// ParentID and EdgeType are derived from the URL.
type ForkInput struct {
	Content       string
	ContentFormat string
	NodeType      string
	AuthorID      uuid.UUID
	Metadata      json.RawMessage
}

// NodeDetail is the rich node representation returned by the service.
// Depth and ChildCount are computed at query time per SPEC-API-03 §3.4.
type NodeDetail struct {
	ID                uuid.UUID  `json:"id"`
	TreeID            uuid.UUID  `json:"treeId"`
	ParentID          *uuid.UUID `json:"parentId"`
	AuthorID          uuid.UUID  `json:"authorId"`
	AuthorDisplayName string     `json:"authorDisplayName"`
	Content           string     `json:"content"`
	ContentFormat     string     `json:"contentFormat"`
	NodeType          string     `json:"nodeType"`
	SequenceNum       int64      `json:"sequenceNum"`
	Metadata          []byte     `json:"metadata"`
	Depth             int        `json:"depth"`
	ChildCount        int        `json:"childCount"`
	CreatedAt         time.Time  `json:"createdAt"`
	EditedAt          *time.Time `json:"editedAt"`
	DeletedAt         *time.Time `json:"deletedAt"`
}

// EdgeDetail is the edge-side companion of a created node. Field names
// match the spec's JSON shape (sourceNodeId/targetNodeId).
type EdgeDetail struct {
	ID           uuid.UUID `json:"id"`
	TreeID       uuid.UUID `json:"treeId"`
	SourceNodeID uuid.UUID `json:"sourceNodeId"`
	TargetNodeID uuid.UUID `json:"targetNodeId"`
	EdgeType     string    `json:"edgeType"`
	CreatedAt    time.Time `json:"createdAt"`
}

// CreateNodeResult wraps the node + edge returned from creation.
type CreateNodeResult struct {
	Node *NodeDetail `json:"node"`
	Edge *EdgeDetail `json:"edge"`
}

// DeleteNodeResult is the minimal info returned from soft-delete.
type DeleteNodeResult struct {
	ID        uuid.UUID `json:"id"`
	TreeID    uuid.UUID `json:"treeId"`
	DeletedAt time.Time `json:"deletedAt"`
}

// --- Service interface + implementation ------------------------------------

// NodeService is the business logic layer for node CRUD. All methods
// operate within a transaction context passed via ctx.
type NodeService interface {
	// Create validates, creates node+edge in a single transaction, and
	// returns the populated NodeDetail + EdgeDetail.
	Create(ctx context.Context, treeID uuid.UUID, input CreateNodeInput) (*CreateNodeResult, error)
	// GetByID retrieves a node with computed depth and child_count.
	GetByID(ctx context.Context, nodeID uuid.UUID) (*NodeDetail, error)
	// Update applies a partial update with COALESCE semantics.
	Update(ctx context.Context, nodeID uuid.UUID, input UpdateNodeInput) (*NodeDetail, error)
	// SoftDelete marks the node as deleted and erases content/metadata.
	SoftDelete(ctx context.Context, nodeID uuid.UUID) (*DeleteNodeResult, error)
	// Reply creates a child node with edge_type="reply".
	Reply(ctx context.Context, parentNodeID uuid.UUID, input ReplyInput) (*CreateNodeResult, error)
	// Fork creates a child node with edge_type="fork" — requires the
	// parent to already have at least one child.
	Fork(ctx context.Context, parentNodeID uuid.UUID, input ForkInput) (*CreateNodeResult, error)
}

// NodeServiceImpl is the pgx-backed implementation of NodeService.
// All repository dependencies are taken as interfaces so the service
// can be tested with stubs; pool is the underlying pgxpool used to
// open the Create transaction and run depth/child-count queries.
type NodeServiceImpl struct {
	nodeRepo db.NodeRepo
	edgeRepo db.EdgeRepo
	pool     *pgxpool.Pool
	now      func() time.Time // injectable for testing
}

// NewNodeService wires the repositories + pool into a NodeServiceImpl.
// now defaults to time.Now when nil — callers can override for tests.
func NewNodeService(nodeRepo db.NodeRepo, edgeRepo db.EdgeRepo, pool *pgxpool.Pool) *NodeServiceImpl {
	return &NodeServiceImpl{
		nodeRepo: nodeRepo,
		edgeRepo: edgeRepo,
		pool:     pool,
		now:      time.Now,
	}
}

// --- Create ----------------------------------------------------------------

// Create validates the input, checks the parent (if any) is in the
// same tree and not soft-deleted, opens a transaction, inserts the
// node and edge, and returns the assembled NodeDetail + EdgeDetail.
//
// TODO(sse): BE-05 will wire SSE broadcast after a successful commit.
// Until then, mutations are silent — no events are emitted.
func (s *NodeServiceImpl) Create(ctx context.Context, treeID uuid.UUID, input CreateNodeInput) (*CreateNodeResult, error) {
	if err := validateCreateInput(input); err != nil {
		return nil, err
	}
	if s.pool == nil {
		return nil, ErrDatabaseUnavailable
	}
	if treeID == uuid.Nil {
		return nil, ErrParentNotFound
	}

	// Default optional fields (validation already verified the formats).
	contentFormat := strings.TrimSpace(input.ContentFormat)
	if contentFormat == "" {
		contentFormat = string(NodeFormatMarkdown)
	}
	nodeType := strings.TrimSpace(input.NodeType)
	if nodeType == "" {
		nodeType = string(NodeKindMessage)
	}
	edgeType := strings.TrimSpace(input.EdgeType)
	if edgeType == "" {
		edgeType = string(NodeEdgeReply)
	}
	metadata := input.Metadata
	if len(metadata) == 0 {
		metadata = json.RawMessage(`{}`)
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("%w: begin tx: %v", ErrDatabaseUnavailable, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Compute parent depth + validate parent is in the same tree.
	var parentDepth int
	if input.ParentID != uuid.Nil {
		// Look up parent — must exist, must be active, must be in tree.
		var parentTreeID uuid.UUID
		err := tx.QueryRow(ctx, `
            SELECT tree_id FROM nodes
            WHERE id = $1 AND deleted_at IS NULL`,
			input.ParentID,
		).Scan(&parentTreeID)
		if errors.Is(err, pgx.ErrNoRows) {
			// Distinguish "never existed" from "soft-deleted".
			var deletedAt *time.Time
			row := s.pool.QueryRow(ctx,
				`SELECT deleted_at FROM nodes WHERE id = $1`, input.ParentID)
			if scanErr := row.Scan(&deletedAt); scanErr == nil {
				return nil, fmt.Errorf("%w (deleted_at=%s)",
					ErrParentDeleted,
					deletedAt.Format(time.RFC3339))
			}
			return nil, ErrParentNotFound
		}
		if err != nil {
			return nil, fmt.Errorf("%w: select parent: %v", ErrDatabaseUnavailable, err)
		}
		if parentTreeID != treeID {
			return nil, ErrParentNotFound
		}

		// Parent's depth (we compute recursively on the fly — the
		// simpler Go walk in NodeRepo.GetAncestors would require
		// fetching the whole chain; the CTE keeps us to a single
		// round trip).
		if err := tx.QueryRow(ctx, `
            WITH RECURSIVE chain AS (
                SELECT 0 AS depth
                WHERE EXISTS (SELECT 1 FROM nodes WHERE id = $1 AND deleted_at IS NULL)
                UNION ALL
                SELECT chain.depth + 1
                FROM chain
                JOIN nodes parent ON parent.id = (
                    SELECT parent_id FROM nodes WHERE id = $1
                )
                JOIN nodes child ON child.parent_id = parent.id
                WHERE chain.depth < 1000000
            )
            SELECT COALESCE(MAX(depth), 0) FROM chain`,
			input.ParentID,
		).Scan(&parentDepth); err != nil {
			return nil, fmt.Errorf("%w: parent depth: %v", ErrDatabaseUnavailable, err)
		}
	}

	// Compute sequence_num within the tree.
	var seqNum int64
	if err := tx.QueryRow(ctx, `
        SELECT COALESCE(MAX(sequence_num), 0) + 1
        FROM nodes
        WHERE tree_id = $1`, treeID,
	).Scan(&seqNum); err != nil {
		return nil, fmt.Errorf("%w: sequence_num: %v", ErrDatabaseUnavailable, err)
	}

	// Insert node.
	var created db.Node
	err = tx.QueryRow(ctx, `
        INSERT INTO nodes
            (tree_id, parent_id, author_id, content, content_format,
             node_type, sequence_num, metadata)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
        RETURNING `+nodeColumns,
		treeID,
		nullableUUID(input.ParentID),
		input.AuthorID,
		input.Content,
		contentFormat,
		nodeType,
		seqNum,
		[]byte(metadata),
	).Scan(
		&created.ID, &created.TreeID, &created.ParentID, &created.AuthorID,
		&created.Content, &created.ContentFormat, &created.NodeType,
		&created.SequenceNum, &created.Metadata,
		&created.CreatedAt, &created.EditedAt, &created.DeletedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: insert node: %v", ErrDatabaseUnavailable, err)
	}

	// Insert edge from parent → new node. EdgeRepo.Create handles the
	// single-parent rule for non-synthesis targets.
	var createdEdge db.Edge
	err = tx.QueryRow(ctx, `
        INSERT INTO edges
            (tree_id, source_id, target_id, edge_type, sequence_num, metadata)
        VALUES ($1, $2, $3, $4, NULL, '{}'::jsonb)
        RETURNING `+edgeColumns,
		treeID, input.ParentID, created.ID, edgeType,
	).Scan(
		&createdEdge.ID, &createdEdge.TreeID, &createdEdge.SourceID,
		&createdEdge.TargetID, &createdEdge.EdgeType,
		&createdEdge.SequenceNum, &createdEdge.Metadata,
		&createdEdge.CreatedAt, &createdEdge.DeletedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: insert edge: %v", ErrDatabaseUnavailable, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("%w: commit: %v", ErrDatabaseUnavailable, err)
	}

	// Build NodeDetail. Depth = parentDepth + 1 (root parentDepth = 0).
	detail := nodeToDetail(created)
	detail.Depth = parentDepth + 1
	detail.ChildCount = 0 // brand new node has no children yet

	return &CreateNodeResult{
		Node: detail,
		Edge: edgeToDetail(createdEdge),
	}, nil
}

// --- GetByID ---------------------------------------------------------------

// GetByID retrieves a node with computed depth and child_count. The
// returned node is always active (soft-deleted rows return ErrNodeNotFound).
func (s *NodeServiceImpl) GetByID(ctx context.Context, nodeID uuid.UUID) (*NodeDetail, error) {
	if nodeID == uuid.Nil {
		return nil, ErrNodeNotFound
	}

	node, err := s.nodeRepo.GetByID(ctx, nodeID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			// Disambiguate "deleted" from "never existed".
			var deletedAt time.Time
			if s.pool != nil {
				row := s.pool.QueryRow(ctx,
					`SELECT deleted_at FROM nodes WHERE id = $1 AND deleted_at IS NOT NULL`,
					nodeID)
				if scanErr := row.Scan(&deletedAt); scanErr == nil {
					return nil, fmt.Errorf("%w (deleted_at=%s)",
						ErrNodeDeleted, deletedAt.Format(time.RFC3339))
				}
			}
			return nil, ErrNodeNotFound
		}
		return nil, fmt.Errorf("%w: get node: %v", ErrDatabaseUnavailable, err)
	}

	detail := nodeToDetail(*node)
	detail.Depth = s.computeDepth(ctx, nodeID, node.ParentID)
	detail.ChildCount = s.computeChildCount(ctx, nodeID)
	return detail, nil
}

// --- Update ----------------------------------------------------------------

// Update applies a partial update with COALESCE semantics. At least
// one field must be provided. Author check is deferred until auth
// middleware (BE-07) lands — the MVP uses uuid.Nil as a sentinel so
// the method works with the current handler wiring.
func (s *NodeServiceImpl) Update(ctx context.Context, nodeID uuid.UUID, input UpdateNodeInput) (*NodeDetail, error) {
	if nodeID == uuid.Nil {
		return nil, ErrNodeNotFound
	}
	if input.Content == nil && input.ContentFormat == nil && input.Metadata == nil {
		return nil, ErrNoUpdateFields
	}

	// Load existing node — distinguishes 404 from "you've already soft-deleted it".
	_, err := s.nodeRepo.GetByID(ctx, nodeID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, fmt.Errorf("%w: get node for update: %v", ErrDatabaseUnavailable, err)
	}

	// Validation — applies only to fields that will be updated.
	if input.Content != nil {
		if utf8Len(*input.Content) > maxContentLen {
			return nil, ErrContentTooLong
		}
	}
	if input.ContentFormat != nil {
		if !NodeContentFormat(*input.ContentFormat).Valid() {
			return nil, ErrInvalidContentFormat
		}
	}
	if input.Metadata != nil {
		if len(*input.Metadata) > maxMetadataBytes {
			return nil, ErrMetadataTooLarge
		}
	}

	// MVP: author check is skipped — uuid.Nil is the sentinel for
	// "no authenticated user yet". The middleware (BE-07) will inject
	// the real user; the author check will then compare against
	// existing.AuthorID.

	// Apply the update via COALESCE pattern. Whether or not a given
	// field is nil, we still write all three columns (the DB trigger
	// only bumps edited_at when content or metadata actually changes;
	// content_format is content-adjacent enough that we treat the
	// whole UPDATE as a valid edit).
	updated, err := s.applyUpdate(ctx, nodeID, input)
	if err != nil {
		return nil, err
	}

	detail := nodeToDetail(*updated)
	detail.Depth = s.computeDepth(ctx, nodeID, updated.ParentID)
	detail.ChildCount = s.computeChildCount(ctx, nodeID)
	return detail, nil
}

// applyUpdate runs the COALESCE SQL and returns the refreshed node.
func (s *NodeServiceImpl) applyUpdate(ctx context.Context, nodeID uuid.UUID, input UpdateNodeInput) (*db.Node, error) {
	var content, format, rawMetadata interface{}
	if input.Content != nil {
		content = *input.Content
	}
	if input.ContentFormat != nil {
		format = *input.ContentFormat
	}
	if input.Metadata != nil {
		rawMetadata = []byte(*input.Metadata)
	}

	var out db.Node
	err := s.pool.QueryRow(ctx, `
        UPDATE nodes
        SET content = COALESCE($2, content),
            content_format = COALESCE($3, content_format),
            metadata = COALESCE($4, metadata),
            edited_at = clock_timestamp()
        WHERE id = $1 AND deleted_at IS NULL
        RETURNING `+nodeColumns,
		nodeID, content, format, rawMetadata,
	).Scan(
		&out.ID, &out.TreeID, &out.ParentID, &out.AuthorID,
		&out.Content, &out.ContentFormat, &out.NodeType,
		&out.SequenceNum, &out.Metadata,
		&out.CreatedAt, &out.EditedAt, &out.DeletedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNodeNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("%w: update node: %v", ErrDatabaseUnavailable, err)
	}
	return &out, nil
}

// --- SoftDelete ------------------------------------------------------------

// SoftDelete marks the node as deleted, erases content/metadata for
// privacy, and returns the minimal info for the SSE event payload.
// Author check is deferred until auth middleware (BE-07) lands.
func (s *NodeServiceImpl) SoftDelete(ctx context.Context, nodeID uuid.UUID) (*DeleteNodeResult, error) {
	if nodeID == uuid.Nil {
		return nil, ErrNodeNotFound
	}

	// MVP: no author check — see Update.

	// Initial state check — distinguishes "already deleted" from
	// "never existed" so callers get a meaningful error code.
	var (
		outID     uuid.UUID
		outTreeID uuid.UUID
		deletedAt time.Time
	)
	row := s.pool.QueryRow(ctx, `
        UPDATE nodes
        SET deleted_at = clock_timestamp(),
            content = '',
            metadata = '{}'::jsonb
        WHERE id = $1 AND deleted_at IS NULL
        RETURNING id, tree_id, deleted_at`,
		nodeID,
	)
	if scanErr := row.Scan(&outID, &outTreeID, &deletedAt); scanErr != nil {
		if errors.Is(scanErr, pgx.ErrNoRows) {
			// Either no such node or already deleted — disambiguate.
			if s.pool != nil {
				row := s.pool.QueryRow(ctx,
					`SELECT deleted_at FROM nodes WHERE id = $1 AND deleted_at IS NOT NULL`,
					nodeID)
				var existing time.Time
				if scanErr := row.Scan(&existing); scanErr == nil {
					return nil, ErrNodeAlreadyDeleted
				}
			}
			return nil, ErrNodeNotFound
		}
		return nil, fmt.Errorf("%w: soft-delete: %v", ErrDatabaseUnavailable, scanErr)
	}

	log.Ctx(ctx).Info().
		Str("node_id", nodeID.String()).
		Str("tree_id", outTreeID.String()).
		Msg("node soft-deleted")

	return &DeleteNodeResult{
		ID:        outID,
		TreeID:    outTreeID,
		DeletedAt: deletedAt,
	}, nil
}

// --- Reply -----------------------------------------------------------------

// Reply creates a child node attached to parentNodeID with edge_type
// "reply". The parent's tree_id is resolved internally so the handler
// only needs the parent ID.
func (s *NodeServiceImpl) Reply(ctx context.Context, parentNodeID uuid.UUID, input ReplyInput) (*CreateNodeResult, error) {
	if parentNodeID == uuid.Nil {
		return nil, ErrParentNotFound
	}

	// Resolve parent tree.
	var treeID uuid.UUID
	parent, err := s.nodeRepo.GetByID(ctx, parentNodeID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			// Distinguish deleted from never-existed.
			if s.pool != nil {
				row := s.pool.QueryRow(ctx,
					`SELECT deleted_at FROM nodes WHERE id = $1 AND deleted_at IS NOT NULL`,
					parentNodeID)
				var deletedAt time.Time
				if scanErr := row.Scan(&deletedAt); scanErr == nil {
					return nil, fmt.Errorf("%w (deleted_at=%s)",
						ErrParentDeleted, deletedAt.Format(time.RFC3339))
				}
			}
			return nil, ErrParentNotFound
		}
		return nil, fmt.Errorf("%w: get parent: %v", ErrDatabaseUnavailable, err)
	}
	treeID = parent.TreeID

	format := input.ContentFormat
	if strings.TrimSpace(format) == "" {
		format = string(NodeFormatMarkdown)
	}
	nodeType := input.NodeType
	if strings.TrimSpace(nodeType) == "" {
		nodeType = string(NodeKindMessage)
	}

	return s.Create(ctx, treeID, CreateNodeInput{
		ParentID:      parentNodeID,
		Content:       input.Content,
		ContentFormat: format,
		NodeType:      nodeType,
		EdgeType:      string(NodeEdgeReply),
		AuthorID:      input.AuthorID,
		TreeID:        treeID,
		Metadata:      input.Metadata,
	})
}

// --- Fork ------------------------------------------------------------------

// Fork creates a sibling-style child node attached to parentNodeID
// with edge_type "fork". Requires the parent to already have at least
// one child — forking from a leaf is indistinguishable from a reply
// (SPEC-API-03 §7.3).
func (s *NodeServiceImpl) Fork(ctx context.Context, parentNodeID uuid.UUID, input ForkInput) (*CreateNodeResult, error) {
	if parentNodeID == uuid.Nil {
		return nil, ErrParentNotFound
	}

	// Resolve parent tree.
	var treeID uuid.UUID
	parent, err := s.nodeRepo.GetByID(ctx, parentNodeID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			if s.pool != nil {
				row := s.pool.QueryRow(ctx,
					`SELECT deleted_at FROM nodes WHERE id = $1 AND deleted_at IS NOT NULL`,
					parentNodeID)
				var deletedAt time.Time
				if scanErr := row.Scan(&deletedAt); scanErr == nil {
					return nil, fmt.Errorf("%w (deleted_at=%s)",
						ErrParentDeleted, deletedAt.Format(time.RFC3339))
				}
			}
			return nil, ErrParentNotFound
		}
		return nil, fmt.Errorf("%w: get parent: %v", ErrDatabaseUnavailable, err)
	}
	treeID = parent.TreeID

	// Fork requires at least one existing child.
	children, err := s.nodeRepo.GetChildren(ctx, parentNodeID)
	if err != nil {
		return nil, fmt.Errorf("%w: get children: %v", ErrDatabaseUnavailable, err)
	}
	if len(children) == 0 {
		return nil, ErrForkRequiresChildren
	}

	format := input.ContentFormat
	if strings.TrimSpace(format) == "" {
		format = string(NodeFormatMarkdown)
	}
	nodeType := input.NodeType
	if strings.TrimSpace(nodeType) == "" {
		nodeType = string(NodeKindMessage)
	}

	return s.Create(ctx, treeID, CreateNodeInput{
		ParentID:      parentNodeID,
		Content:       input.Content,
		ContentFormat: format,
		NodeType:      nodeType,
		EdgeType:      string(NodeEdgeFork),
		AuthorID:      input.AuthorID,
		TreeID:        treeID,
		Metadata:      input.Metadata,
	})
}

// --- Validation helpers ----------------------------------------------------

func validateCreateInput(input CreateNodeInput) error {
	if utf8Len(input.Content) > maxContentLen {
		return ErrContentTooLong
	}

	// Default-validate format.
	format := strings.TrimSpace(input.ContentFormat)
	if format == "" {
		format = string(NodeFormatMarkdown)
	}
	if !NodeContentFormat(format).Valid() {
		return ErrInvalidContentFormat
	}

	// Default-validate node type.
	nodeType := strings.TrimSpace(input.NodeType)
	if nodeType == "" {
		nodeType = string(NodeKindMessage)
	}
	if !NodeNodeType(nodeType).Valid() {
		return ErrInvalidNodeType
	}
	if nodeType == string(NodeKindSynthesis) {
		return ErrSynthesisViaMergeOnly
	}
	if nodeType == string(NodeKindSystem) {
		return ErrSystemNodeForbidden
	}

	// Edge type.
	edgeType := strings.TrimSpace(input.EdgeType)
	if edgeType == "" {
		edgeType = string(NodeEdgeReply)
	}
	if !NodeEdgeType(edgeType).Valid() {
		return ErrInvalidEdgeType
	}

	// Metadata size.
	if len(input.Metadata) > maxMetadataBytes {
		return ErrMetadataTooLarge
	}

	return nil
}

// --- Depth / child_count helpers -------------------------------------------

// computeDepth walks the parent chain to the root. Returns 0 for root
// nodes (parent == nil). Uses a recursive CTE on the live pool.
func (s *NodeServiceImpl) computeDepth(ctx context.Context, nodeID uuid.UUID, parentID *uuid.UUID) int {
	if parentID == nil {
		return 0
	}
	if s.pool == nil {
		return 0
	}
	var depth int
	err := s.pool.QueryRow(ctx, `
        WITH RECURSIVE chain AS (
            SELECT 0 AS depth
            WHERE EXISTS (SELECT 1 FROM nodes WHERE id = $1 AND deleted_at IS NULL)
            UNION ALL
            SELECT chain.depth + 1
            FROM chain
            JOIN nodes child ON child.parent_id = (
                SELECT parent_id FROM nodes WHERE id = $1
            )
            WHERE chain.depth < 1000000
        )
        SELECT COALESCE(MAX(depth), 0) FROM chain`,
		nodeID,
	).Scan(&depth)
	if err != nil {
		log.Warn().Err(err).Str("node_id", nodeID.String()).
			Msg("node service: depth CTE failed")
		return 0
	}
	return depth
}

// computeChildCount counts active children via the edges table.
func (s *NodeServiceImpl) computeChildCount(ctx context.Context, nodeID uuid.UUID) int {
	if s.pool == nil {
		return 0
	}
	var count int
	err := s.pool.QueryRow(ctx, `
        SELECT COUNT(*)::int
        FROM edges e
        JOIN nodes n ON n.id = e.target_id
        WHERE e.source_id = $1
          AND e.deleted_at IS NULL
          AND n.deleted_at IS NULL`,
		nodeID,
	).Scan(&count)
	if err != nil {
		log.Warn().Err(err).Str("node_id", nodeID.String()).
			Msg("node service: child count failed")
		return 0
	}
	return count
}

// --- Mapping helpers --------------------------------------------------------

func nodeToDetail(n db.Node) *NodeDetail {
	return &NodeDetail{
		ID:            n.ID,
		TreeID:        n.TreeID,
		ParentID:      n.ParentID,
		AuthorID:      n.AuthorID,
		Content:       n.Content,
		ContentFormat: n.ContentFormat,
		NodeType:      n.NodeType,
		SequenceNum:   n.SequenceNum,
		Metadata:      n.Metadata,
		CreatedAt:     n.CreatedAt,
		EditedAt:      n.EditedAt,
		DeletedAt:     n.DeletedAt,
	}
}

func edgeToDetail(e db.Edge) *EdgeDetail {
	return &EdgeDetail{
		ID:           e.ID,
		TreeID:       e.TreeID,
		SourceNodeID: e.SourceID,
		TargetNodeID: e.TargetID,
		EdgeType:     e.EdgeType,
		CreatedAt:    e.CreatedAt,
	}
}

// nullableUUID returns nil when the UUID is uuid.Nil so the DB sees
// NULL instead of the zero-UUID on a parent_id column.
func nullableUUID(id uuid.UUID) interface{} {
	if id == uuid.Nil {
		return nil
	}
	return id
}
