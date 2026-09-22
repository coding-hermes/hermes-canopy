// Package service — merge (synthesis node) creation.
//
// Implements SPEC-API-04 §3 (POST /trees/{tree_id}/merge): the spec's own
// contract for creating a `node_type='synthesis'` node with one parent edge
// plus N synthesis edges. The ordinary node-create path deliberately refuses
// synthesis nodes (ErrSynthesisViaMergeOnly, SPEC-API-03 §3.1) — this service
// is the endpoint that refusal points at, which is why it inserts the node
// itself instead of going through CreateNode/validateCreateInput.
//
// ADDITIVE by construction: ErrSynthesisViaMergeOnly stays intact and the
// SPEC-PL-06 multi-reference reply path is untouched. Every write happens in
// ONE pgx transaction (node insert → parent edge → N synthesis edges), and
// publication happens only after a successful commit.
//
// Response field names are snake_case on this boundary (§3.2, §3.6, §3.7) —
// the §9 HTTP boundary rule the multi-reference surface also follows.
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/sse"
)

// --- Limits (SPEC-API-04 §3.2, §13) -----------------------------------------

const (
	// mergeMinSources is the §3.2 minimum source-node count.
	mergeMinSources = 2
	// mergeMaxSources is the §3.2/§13 maximum source-node count.
	mergeMaxSources = 100
	// mergeDepthCycleGuard caps the recursive depth CTE (same guard the
	// node service uses; SPEC-API-04 §13 "Depth CTE recursion limit").
	mergeDepthCycleGuard = 10000
)

// --- Error catalog (SPEC-API-04 §3.3 / SPEC-API-07 §3.3.3) -------------------

var (
	// ErrMergeSourceIDsInvalid is INVALID_SOURCE_NODE_IDS (400).
	ErrMergeSourceIDsInvalid = errors.New("merge service: source_node_ids must be an array of ids")
	// ErrMergeSourceCountTooLow is MIN_SOURCE_NODES (400).
	ErrMergeSourceCountTooLow = errors.New("merge service: fewer than 2 source nodes")
	// ErrMergeSourceCountTooHigh is MAX_SOURCE_NODES (400).
	ErrMergeSourceCountTooHigh = errors.New("merge service: more than 100 source nodes")
	// ErrMergeSourceDuplicate is DUPLICATE_SOURCE_NODES (400).
	ErrMergeSourceDuplicate = errors.New("merge service: duplicate source node id")
	// ErrMergeSourceIDInvalid is INVALID_SOURCE_NODE_ID (400).
	ErrMergeSourceIDInvalid = errors.New("merge service: source node id is not a valid UUIDv7")
	// ErrMergeSourceNotFound is SOURCE_NODE_NOT_FOUND (404).
	ErrMergeSourceNotFound = errors.New("merge service: source node not found in tree")
	// ErrMergeSourceDeleted is SOURCE_NODE_DELETED (410).
	ErrMergeSourceDeleted = errors.New("merge service: source node is soft-deleted")
	// ErrMergeTreeMismatch is TREE_MISMATCH (400).
	ErrMergeTreeMismatch = errors.New("merge service: node belongs to another tree")
	// ErrMergeTargetParentInvalid is INVALID_TARGET_PARENT_ID (400).
	ErrMergeTargetParentInvalid = errors.New("merge service: target_parent_id is not a valid UUIDv7")
	// ErrMergeTargetParentNotFound is TARGET_PARENT_NOT_FOUND (404).
	ErrMergeTargetParentNotFound = errors.New("merge service: target parent not found in tree")
	// ErrMergeTargetParentDeleted is TARGET_PARENT_DELETED (409).
	ErrMergeTargetParentDeleted = errors.New("merge service: target parent is soft-deleted")
	// ErrMergeSourceTargetOverlap is SOURCE_TARGET_OVERLAP (400).
	ErrMergeSourceTargetOverlap = errors.New("merge service: a source node is also the target parent")
	// ErrMergeMetadataNotObject is a payload-shape rejection with no §3.3
	// row of its own; it is answered with the repo's VALIDATION_ERROR, the
	// same treatment the multi-reference surface gives the same mistake.
	ErrMergeMetadataNotObject = errors.New("merge service: metadata must be a JSON object")
)

// mergeErrorSpec is the catalog row for one sentinel: the §3.3 code, the HTTP
// status, and the default message.
type mergeErrorSpec struct {
	code    string
	status  int
	message string
}

// mergeErrorCatalog is SPEC-API-04 §3.3's validation table in Go form. It also
// carries the three payload sentinels the merge endpoint shares with node
// create and the two common errors (§10.5) so the handler needs one lookup.
var mergeErrorCatalog = map[error]mergeErrorSpec{
	ErrMergeSourceIDsInvalid:     {"INVALID_SOURCE_NODE_IDS", 400, "source_node_ids must be an array of node ids"},
	ErrMergeSourceCountTooLow:    {"MIN_SOURCE_NODES", 400, "merge requires at least 2 source nodes"},
	ErrMergeSourceCountTooHigh:   {"MAX_SOURCE_NODES", 400, "merge supports at most 100 source nodes"},
	ErrMergeSourceDuplicate:      {"DUPLICATE_SOURCE_NODES", 400, "source_node_ids contains duplicate entries"},
	ErrMergeSourceIDInvalid:      {"INVALID_SOURCE_NODE_ID", 400, "a source_node_id is not a valid UUIDv7"},
	ErrMergeSourceNotFound:       {"SOURCE_NODE_NOT_FOUND", 404, "one or more source nodes not found"},
	ErrMergeSourceDeleted:        {"SOURCE_NODE_DELETED", 410, "one or more source nodes have been deleted"},
	ErrMergeTreeMismatch:         {"TREE_MISMATCH", 400, "a source node belongs to a different tree"},
	ErrMergeTargetParentInvalid:  {"INVALID_TARGET_PARENT_ID", 400, "target_parent_id is not a valid UUIDv7"},
	ErrMergeTargetParentNotFound: {"TARGET_PARENT_NOT_FOUND", 404, "target parent node not found"},
	ErrMergeTargetParentDeleted:  {"TARGET_PARENT_DELETED", 409, "target parent node has been deleted"},
	ErrMergeSourceTargetOverlap:  {"SOURCE_TARGET_OVERLAP", 400, "target parent is one of the source nodes"},
	ErrContentTooLong:            {"CONTENT_TOO_LONG", 400, "content must not exceed 65536 characters"},
	ErrInvalidContentFormat:      {"INVALID_CONTENT_FORMAT", 400, "content_format must be one of: markdown, plain, rich"},
	ErrMetadataTooLarge:          {"METADATA_TOO_LARGE", 400, "metadata must not exceed 16KB"},
	ErrInvalidCardRef:            {"INVALID_CARD_REF", 400, "metadata card_ref must be an object with id, card_type and app_id and an optional 64-hex context_hash"},
	ErrTreeNotFound:              {"TREE_NOT_FOUND", 404, "tree not found"},
	ErrTreeDeleted:               {"TREE_DELETED", 410, "tree has been deleted"},
	ErrNotTreeMember:             {"NOT_TREE_MEMBER", 403, "you are not a member of this tree"},
}

// MergeAPIError is a rejected merge operation with its §3.3 identity, so the
// handler maps it without a second switch table.
// errors.Is(err, Err<Sentinel>) still matches through Unwrap.
type MergeAPIError struct {
	Code    string
	Status  int
	Message string
	Err     error
}

// Error implements the error interface with the catalog message.
func (e *MergeAPIError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Code
}

// Unwrap exposes the sentinel so errors.Is classification keeps working.
func (e *MergeAPIError) Unwrap() error { return e.Err }

// newMergeAPIError builds the catalog-shaped error for a sentinel. An empty
// message keeps the catalog's default; extra arguments are formatted into the
// default message (a %d/%s count, an offending value).
func newMergeAPIError(sentinel error, format string, args ...any) error {
	spec, ok := mergeErrorCatalog[sentinel]
	if !ok {
		return fmt.Errorf("%w: %s", sentinel, fmt.Sprintf(format, args...))
	}
	message := spec.message
	if format != "" {
		message = fmt.Sprintf(format, args...)
	}
	return &MergeAPIError{Code: spec.code, Status: spec.status, Message: message, Err: sentinel}
}

// MergeErrorFrom extracts the §3.3 identity from an error chain.
func MergeErrorFrom(err error) (*MergeAPIError, bool) {
	var apiErr *MergeAPIError
	if errors.As(err, &apiErr) {
		return apiErr, true
	}
	return nil, false
}

// NewMergeAPIError is the exported form of the catalog constructor, for
// callers (HTTP handlers) that reject a request before the service runs.
func NewMergeAPIError(sentinel error, format string, args ...any) error {
	return newMergeAPIError(sentinel, format, args...)
}

// --- Request / response types (SPEC-API-04 §3.2, §3.6, §3.7) -----------------

// CreateMergeRequest is the §3.2 request body for POST /trees/{tree_id}/merge.
//
// source_node_ids and target_parent_id are STRINGS rather than uuid.UUID for
// the same reason the multi-reference preflight decodes ids as strings: a
// malformed member must be answered with its catalog code
// (INVALID_SOURCE_NODE_ID / INVALID_TARGET_PARENT_ID), which a typed UUID
// field cannot express — the decode fails first and the caller sees a generic
// body-shape error. ToInput() performs the conversion.
type CreateMergeRequest struct {
	SourceNodeIDs  []string        `json:"source_node_ids"`
	Content        string          `json:"content"`
	ContentFormat  string          `json:"content_format"`
	TargetParentID *string         `json:"target_parent_id"`
	Metadata       json.RawMessage `json:"metadata"`
}

// CreateMergeInput is the parsed, service-side view of a merge request: ids
// resolved, defaults applied, metadata normalised. TargetParentID nil means
// "the tree's root node" (§3.2).
type CreateMergeInput struct {
	SourceNodeIDs  []uuid.UUID
	Content        string
	ContentFormat  string
	TargetParentID *uuid.UUID
	Metadata       []byte
}

// ToInput validates the §3.3 request-body rows and returns the parsed input.
// A returned error is a *MergeAPIError carrying the catalog code and status.
//
// Order follows the spec's own table: array shape, count, duplicates, id
// validity, then the payload fields.
func (r CreateMergeRequest) ToInput() (CreateMergeInput, error) {
	if r.SourceNodeIDs == nil {
		// Absent or JSON null — neither is an array.
		return CreateMergeInput{}, newMergeAPIError(ErrMergeSourceIDsInvalid, "")
	}
	if len(r.SourceNodeIDs) < mergeMinSources {
		return CreateMergeInput{}, newMergeAPIError(ErrMergeSourceCountTooLow,
			"merge requires at least %d source nodes (received %d)", mergeMinSources, len(r.SourceNodeIDs))
	}
	if len(r.SourceNodeIDs) > mergeMaxSources {
		return CreateMergeInput{}, newMergeAPIError(ErrMergeSourceCountTooHigh,
			"merge supports at most %d source nodes (received %d)", mergeMaxSources, len(r.SourceNodeIDs))
	}

	seen := make(map[string]struct{}, len(r.SourceNodeIDs))
	for _, raw := range r.SourceNodeIDs {
		if _, dup := seen[raw]; dup {
			return CreateMergeInput{}, newMergeAPIError(ErrMergeSourceDuplicate,
				"source_node_ids contains duplicate entries: %q", raw)
		}
		seen[raw] = struct{}{}
	}

	sourceIDs := make([]uuid.UUID, 0, len(r.SourceNodeIDs))
	for i, raw := range r.SourceNodeIDs {
		id, err := uuid.Parse(raw)
		if err != nil || id == uuid.Nil {
			return CreateMergeInput{}, newMergeAPIError(ErrMergeSourceIDInvalid,
				"source_node_ids[%d] is not a valid UUIDv7: %q", i, raw)
		}
		sourceIDs = append(sourceIDs, id)
	}

	if utf8Len(r.Content) > maxContentLen {
		return CreateMergeInput{}, newMergeAPIError(ErrContentTooLong,
			"content must not exceed %d characters", maxContentLen)
	}
	contentFormat := strings.TrimSpace(r.ContentFormat)
	if contentFormat == "" {
		contentFormat = string(NodeFormatMarkdown)
	}
	if !NodeContentFormat(contentFormat).Valid() {
		return CreateMergeInput{}, newMergeAPIError(ErrInvalidContentFormat, "")
	}

	metadata, err := normalizeMergeMetadata(r.Metadata)
	if err != nil {
		return CreateMergeInput{}, err
	}

	var target *uuid.UUID
	if r.TargetParentID != nil {
		id, parseErr := uuid.Parse(strings.TrimSpace(*r.TargetParentID))
		if parseErr != nil || id == uuid.Nil {
			return CreateMergeInput{}, newMergeAPIError(ErrMergeTargetParentInvalid,
				"target_parent_id is not a valid UUIDv7: %q", *r.TargetParentID)
		}
		target = &id
		// §12 edge case 10: the merge node cannot be placed AT a node it
		// also synthesises from. Checked here for the explicit target; the
		// default (tree root) case is re-checked once the root resolves.
		for _, id := range sourceIDs {
			if id == *target {
				return CreateMergeInput{}, newMergeAPIError(ErrMergeSourceTargetOverlap,
					"target_parent_id %q is also a source node", target.String())
			}
		}
	}

	return CreateMergeInput{
		SourceNodeIDs:  sourceIDs,
		Content:        r.Content,
		ContentFormat:  contentFormat,
		TargetParentID: target,
		Metadata:       metadata,
	}, nil
}

// MergeNode is the §3.7 node representation returned in the 201 envelope.
// Field names are snake_case on this boundary (§9's HTTP boundary rule).
type MergeNode struct {
	ID                uuid.UUID       `json:"id"`
	TreeID            uuid.UUID       `json:"tree_id"`
	ParentID          *uuid.UUID      `json:"parent_id"`
	AuthorID          uuid.UUID       `json:"author_id"`
	AuthorDisplayName string          `json:"author_display_name"`
	Content           string          `json:"content"`
	ContentFormat     string          `json:"content_format"`
	NodeType          string          `json:"node_type"`
	SequenceNum       int64           `json:"sequence_num"`
	Metadata          json.RawMessage `json:"metadata"`
	Depth             int             `json:"depth"`
	ChildCount        int             `json:"child_count"`
	CreatedAt         time.Time       `json:"created_at"`
	EditedAt          *time.Time      `json:"edited_at"`
	DeletedAt         *time.Time      `json:"deleted_at"`
}

// MergeEdge is the §3.6 edge representation (the same shape is the §3.8
// `edge_added` payload).
type MergeEdge struct {
	ID           uuid.UUID `json:"id"`
	TreeID       uuid.UUID `json:"tree_id"`
	SourceNodeID uuid.UUID `json:"source_node_id"`
	TargetNodeID uuid.UUID `json:"target_node_id"`
	EdgeType     string    `json:"edge_type"`
	CreatedAt    time.Time `json:"created_at"`
}

// CreateMergeResult is the §3.6 201 Created envelope: the synthesis node,
// every edge created (1 parent edge + N synthesis edges, in creation order)
// and the echoed merge sources.
type CreateMergeResult struct {
	Node            *MergeNode   `json:"node"`
	Edges           []*MergeEdge `json:"edges"`
	MergedSourceIDs []uuid.UUID  `json:"merged_source_ids"`
}

// MergeCompletedEvent is the §3.8 `tree_merged` composite event payload. It is
// purely ADDITIVE — clients that only understand node_added/edge_added keep
// working, and it is broadcast LAST.
type MergeCompletedEvent struct {
	TreeID        uuid.UUID   `json:"tree_id"`
	MergeNodeID   uuid.UUID   `json:"merge_node_id"`
	SourceNodeIDs []uuid.UUID `json:"source_node_ids"`
	Timestamp     string      `json:"timestamp"`
}

// --- Service interface + implementation --------------------------------------

// MergeService defines business logic for creating synthesis (merge) nodes
// (SPEC-API-04 §7.1). A merge combines multiple source nodes into a single
// synthesis point with multi-parent edges of type 'synthesis'.
type MergeService interface {
	// CreateMerge validates the request, inserts one synthesis node plus one
	// `reply` parent edge and N `synthesis` edges in a single transaction,
	// and returns the created node, all created edges and the merged source
	// ids. Broadcasts node_added + (N+1) edge_added + tree_merged after a
	// successful commit.
	CreateMerge(ctx context.Context, treeID uuid.UUID, req CreateMergeRequest) (*CreateMergeResult, error)
}

// MergeServiceImpl is the pgx-backed implementation of MergeService. It
// writes the node itself (never through CreateNode) — see the package comment.
type MergeServiceImpl struct {
	pool   *pgxpool.Pool
	sseHub sse.SSEHub
}

// NewMergeService wires the pool and the SSE hub into a MergeServiceImpl.
// A nil hub silently skips broadcasts (safe for unit tests).
func NewMergeService(pool *pgxpool.Pool, sseHub sse.SSEHub) *MergeServiceImpl {
	return &MergeServiceImpl{pool: pool, sseHub: sseHub}
}

// CreateMerge implements MergeService.
func (s *MergeServiceImpl) CreateMerge(ctx context.Context, treeID uuid.UUID, req CreateMergeRequest) (*CreateMergeResult, error) {
	if s.pool == nil {
		return nil, ErrDatabaseUnavailable
	}
	authorID := requesterFromContext(ctx)
	if authorID == uuid.Nil {
		return nil, ErrNodeAuthorRequired
	}

	input, err := req.ToInput()
	if err != nil {
		return nil, err
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("%w: begin tx: %v", ErrDatabaseUnavailable, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the tree row FOR SHARE and reject a soft-deleted tree
	// (TREE_NOT_FOUND / TREE_DELETED) — the same gate the multi-reference
	// write path uses.
	if err := lockTreeRow(ctx, tx, treeID); err != nil {
		return nil, err
	}

	targetID, targetDepth, err := s.resolveTargetParent(ctx, tx, treeID, input)
	if err != nil {
		return nil, err
	}
	if err := validateMergeSources(ctx, tx, treeID, input.SourceNodeIDs); err != nil {
		return nil, err
	}

	// §3.4 — sequence_num is the tree's next monotonic position.
	var seqNum int64
	if err := tx.QueryRow(ctx, `
        SELECT COALESCE(MAX(sequence_num), 0) + 1
        FROM nodes
        WHERE tree_id = $1`, treeID,
	).Scan(&seqNum); err != nil {
		return nil, fmt.Errorf("%w: node sequence_num: %v", ErrDatabaseUnavailable, err)
	}

	// §3.5 step 1 — the synthesis node. nodes.id DEFAULT uuidv7() allocates
	// the id server-side (§3.4); parent_mode keeps its 'lineage' default
	// because the synthesis node has a deterministic display parent.
	var created db.Node
	err = tx.QueryRow(ctx, `
        INSERT INTO nodes
            (tree_id, parent_id, author_id, content, content_format,
             node_type, sequence_num, metadata)
        VALUES ($1, $2, $3, $4, $5, 'synthesis', $6, $7)
        RETURNING `+nodeColumns,
		treeID, targetID, authorID, input.Content, input.ContentFormat, seqNum, input.Metadata,
	).Scan(
		&created.ID, &created.TreeID, &created.ParentID, &created.ParentMode, &created.AuthorID,
		&created.Content, &created.ContentFormat, &created.NodeType,
		&created.SequenceNum, &created.Metadata,
		&created.CreatedAt, &created.EditedAt, &created.DeletedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: insert synthesis node: %v", ErrDatabaseUnavailable, err)
	}

	// Edges share the tree's monotonic edge sequence; the parent edge is
	// created first, then the sources in request order (§3.5, §3.8 order).
	var edgeSeqNum int64
	if err := tx.QueryRow(ctx, `
        SELECT COALESCE(MAX(sequence_num), 0) + 1
        FROM edges
        WHERE tree_id = $1`, treeID,
	).Scan(&edgeSeqNum); err != nil {
		return nil, fmt.Errorf("%w: edge sequence_num: %v", ErrDatabaseUnavailable, err)
	}

	edges := make([]*MergeEdge, 0, len(input.SourceNodeIDs)+1)
	parentEdge, err := insertMergeEdge(ctx, tx, treeID, targetID, created.ID, string(NodeEdgeReply), edgeSeqNum)
	if err != nil {
		return nil, err
	}
	edges = append(edges, parentEdge)

	for i, sourceID := range input.SourceNodeIDs {
		edge, insertErr := insertMergeEdge(ctx, tx, treeID, sourceID, created.ID,
			string(NodeKindSynthesis), edgeSeqNum+int64(i)+1)
		if insertErr != nil {
			return nil, insertErr
		}
		edges = append(edges, edge)
	}

	displayName, err := authorDisplayName(ctx, tx, authorID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("%w: commit: %v", ErrDatabaseUnavailable, err)
	}

	// §3.8 — publish only after the commit, in the documented order:
	// node_added, then one edge_added per created edge (parent edge first),
	// then the composite tree_merged. A publication failure never rolls
	// back a committed graph.
	s.broadcastNodeEvent(ctx, treeID, "node_added", created.ID, authorID)
	s.broadcastMergeEdgeEvents(treeID, authorID, edges)
	s.broadcastMergeCompleted(treeID, authorID, MergeCompletedEvent{
		TreeID:        treeID,
		MergeNodeID:   created.ID,
		SourceNodeIDs: append([]uuid.UUID(nil), input.SourceNodeIDs...),
		Timestamp:     created.CreatedAt.UTC().Format(time.RFC3339),
	})

	node := mergeNodeFromDB(created, authorID, displayName, targetDepth+1)
	return &CreateMergeResult{
		Node:            node,
		Edges:           edges,
		MergedSourceIDs: append([]uuid.UUID(nil), input.SourceNodeIDs...),
	}, nil
}

// --- Validation inside the transaction ---------------------------------------

// resolveTargetParent resolves the §3.2 placement target (the explicit
// target_parent_id or the tree's root node) and verifies it is an active node
// of this tree. It returns the resolved id and its depth, so the caller can
// compute `depth = target_parent.depth + 1` (§3.4).
func (s *MergeServiceImpl) resolveTargetParent(ctx context.Context, tx pgx.Tx, treeID uuid.UUID, input CreateMergeInput) (uuid.UUID, int, error) {
	var targetID uuid.UUID
	if input.TargetParentID != nil {
		targetID = *input.TargetParentID
	} else {
		// §3.2: the default target is the tree's root node.
		var rootID *uuid.UUID
		if err := tx.QueryRow(ctx,
			`SELECT root_node_id FROM trees WHERE id = $1`, treeID).Scan(&rootID); err != nil {
			return uuid.Nil, 0, fmt.Errorf("%w: read tree root: %v", ErrDatabaseUnavailable, err)
		}
		if rootID == nil || *rootID == uuid.Nil {
			return uuid.Nil, 0, newMergeAPIError(ErrMergeTargetParentNotFound,
				"tree %s has no root node to merge into", treeID.String())
		}
		targetID = *rootID
	}

	// §12 edge case 10 for the DEFAULTED target: an explicit target is already
	// checked in ToInput, but a target that falls back to the tree root is
	// only known here.
	for _, sourceID := range input.SourceNodeIDs {
		if sourceID == targetID {
			return uuid.Nil, 0, newMergeAPIError(ErrMergeSourceTargetOverlap,
				"the merge target %q is also a source node", targetID.String())
		}
	}

	var (
		nodeTreeID uuid.UUID
		deletedAt  *time.Time
	)
	err := tx.QueryRow(ctx, `
        SELECT tree_id, deleted_at
        FROM nodes
        WHERE id = $1
        FOR SHARE`, targetID,
	).Scan(&nodeTreeID, &deletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, 0, newMergeAPIError(ErrMergeTargetParentNotFound,
			"target parent node not found: %q", targetID.String())
	}
	if err != nil {
		return uuid.Nil, 0, fmt.Errorf("%w: lock target parent: %v", ErrDatabaseUnavailable, err)
	}
	if nodeTreeID != treeID {
		return uuid.Nil, 0, newMergeAPIError(ErrMergeTreeMismatch,
			"target_parent_id belongs to a different tree: %q", targetID.String())
	}
	if deletedAt != nil {
		return uuid.Nil, 0, newMergeAPIError(ErrMergeTargetParentDeleted,
			"target parent node has been deleted: %q", targetID.String())
	}

	depth, err := nodeDepth(ctx, tx, targetID)
	if err != nil {
		return uuid.Nil, 0, err
	}
	return targetID, depth, nil
}

// validateMergeSources verifies every source node exists, is not soft-deleted
// and belongs to this tree, in the request's order so the reported failure is
// deterministic. Order follows §3.3: not-found, deleted, tree mismatch.
func validateMergeSources(ctx context.Context, tx pgx.Tx, treeID uuid.UUID, sourceIDs []uuid.UUID) error {
	rows, err := tx.Query(ctx, `
        SELECT id, tree_id, deleted_at
        FROM nodes
        WHERE id = ANY($1::uuid[])
        ORDER BY id
        FOR SHARE`, sortedUUIDStrings(sourceIDs))
	if err != nil {
		return fmt.Errorf("%w: lock merge sources: %v", ErrDatabaseUnavailable, err)
	}
	defer rows.Close()

	type sourceRow struct {
		treeID    uuid.UUID
		deletedAt *time.Time
	}
	found := make(map[uuid.UUID]sourceRow, len(sourceIDs))
	for rows.Next() {
		var (
			id  uuid.UUID
			row sourceRow
		)
		if err := rows.Scan(&id, &row.treeID, &row.deletedAt); err != nil {
			return fmt.Errorf("%w: scan merge source: %v", ErrDatabaseUnavailable, err)
		}
		found[id] = row
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%w: iterate merge sources: %v", ErrDatabaseUnavailable, err)
	}

	for _, id := range sourceIDs {
		row, ok := found[id]
		if !ok {
			return newMergeAPIError(ErrMergeSourceNotFound, "source node not found: %q", id.String())
		}
		if row.deletedAt != nil {
			return newMergeAPIError(ErrMergeSourceDeleted, "source node has been deleted: %q", id.String())
		}
		if row.treeID != treeID {
			return newMergeAPIError(ErrMergeTreeMismatch, "source node belongs to a different tree: %q", id.String())
		}
	}
	return nil
}

// nodeDepth returns a node's distance from its tree root using the same
// cycle-guarded recursive CTE the node service uses for parent depth.
func nodeDepth(ctx context.Context, tx pgx.Tx, nodeID uuid.UUID) (int, error) {
	var depth int
	err := tx.QueryRow(ctx, `
        WITH RECURSIVE chain(id, parent_id, depth) AS (
            SELECT id, parent_id, 0
            FROM nodes
            WHERE id = $1 AND deleted_at IS NULL
            UNION ALL
            SELECT n.id, n.parent_id, c.depth + 1
            FROM nodes n
            JOIN chain c ON n.id = c.parent_id
            WHERE n.deleted_at IS NULL
              AND c.depth < $2
        )
        SELECT COALESCE(MAX(depth), 0) FROM chain`, nodeID, mergeDepthCycleGuard,
	).Scan(&depth)
	if err != nil {
		return 0, fmt.Errorf("%w: target parent depth: %v", ErrDatabaseUnavailable, err)
	}
	return depth, nil
}

// insertMergeEdge inserts one edge of a merge inside the caller's transaction
// and returns its §3.6 wire shape.
func insertMergeEdge(ctx context.Context, tx pgx.Tx, treeID, sourceID, targetID uuid.UUID, edgeType string, seqNum int64) (*MergeEdge, error) {
	var created db.Edge
	err := tx.QueryRow(ctx, `
        INSERT INTO edges
            (tree_id, source_id, target_id, edge_type, sequence_num, metadata)
        VALUES ($1, $2, $3, $4, $5, '{}'::jsonb)
        RETURNING `+edgeColumns,
		treeID, sourceID, targetID, edgeType, seqNum,
	).Scan(
		&created.ID, &created.TreeID, &created.SourceID, &created.TargetID,
		&created.EdgeType, &created.SequenceNum, &created.Metadata,
		&created.CreatedAt, &created.DeletedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: insert %s edge: %v", ErrDatabaseUnavailable, edgeType, err)
	}
	return mergeEdgeFromDB(created), nil
}

// authorDisplayName resolves §3.7's cached display name. nodes.author_id has
// no FK, so an unknown author is answered with an empty name rather than an
// error.
//
// DF-HERMES-CANOPY-44: the node surfaces (Create/Reply/Fork/GetByID/
// ListByTree/Update) resolve the same label through
// resolveAuthorDisplayName/resolveAuthorDisplayNames in node_service.go, so
// the merge path and the node path share ONE contract. The signature here is
// unchanged (pgx.Tx) and this wrapper is kept for the merge call site.
func authorDisplayName(ctx context.Context, tx pgx.Tx, authorID uuid.UUID) (string, error) {
	return resolveAuthorDisplayName(ctx, tx, authorID)
}

// normalizeMergeMetadata canonicalises the optional §3.2 metadata object: an
// absent/null value becomes `{}`, a non-object is rejected (VALIDATION_ERROR,
// the treatment the multi-reference surface gives the same mistake) and a
// serialized object above the 16 KiB maximum is METADATA_TOO_LARGE. The size
// is measured on the COMPACT serialization, so pretty-printing whitespace in
// the request cannot inflate it.
func normalizeMergeMetadata(raw json.RawMessage) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return []byte(`{}`), nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return nil, ErrMergeMetadataNotObject
	}
	if obj == nil {
		return []byte(`{}`), nil
	}
	compact, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("%w: encode metadata: %v", ErrDatabaseUnavailable, err)
	}
	if len(compact) > maxMetadataBytes {
		return nil, newMergeAPIError(ErrMetadataTooLarge,
			"metadata must not exceed %d bytes (received %d)", maxMetadataBytes, len(compact))
	}
	// The same §6.4 structural rule the node create/update paths apply: a
	// synthesis node is a node, so its metadata.card_ref is validated too.
	if err := ValidateNodeMetadataCardRef(compact); err != nil {
		return nil, newMergeAPIError(ErrInvalidCardRef, "%v", err)
	}
	return compact, nil
}

// --- §3.8 SSE vocabulary -----------------------------------------------------

// broadcastNodeEvent publishes the standard repo `node_added` envelope for the
// new synthesis node (BE-18 / SPEC-API-03), reusing the same helper the node
// create and multi-reference paths publish through — merge invents no second
// event vocabulary.
func (s *MergeServiceImpl) broadcastNodeEvent(ctx context.Context, treeID uuid.UUID, eventType string, nodeID, actorID uuid.UUID) {
	if s.sseHub == nil {
		return
	}
	data, _ := json.Marshal(map[string]interface{}{
		"node_id":  nodeID.String(),
		"tree_id":  treeID.String(),
		"actor_id": actorID.String(),
		"event":    eventType,
	})
	s.sseHub.Broadcast(treeID, sse.SSEEvent{
		TreeID:    treeID,
		Type:      eventType,
		Data:      data,
		Timestamp: time.Now().UTC(),
		ActorID:   actorID,
	})
}

// broadcastMergeEdgeEvents publishes one `edge_added` event per created edge,
// in creation order — the parent edge first, then the synthesis edges in
// request order (§3.8).
func (s *MergeServiceImpl) broadcastMergeEdgeEvents(treeID, actorID uuid.UUID, edges []*MergeEdge) {
	if s.sseHub == nil {
		return
	}
	for _, e := range edges {
		if e == nil {
			continue
		}
		s.sseHub.Broadcast(treeID, sse.ComposeEvent(treeID, actorID, "edge_added", e))
	}
}

// broadcastMergeCompleted publishes the §3.8 composite `tree_merged` event as
// the LAST event of a merge.
func (s *MergeServiceImpl) broadcastMergeCompleted(treeID, actorID uuid.UUID, payload MergeCompletedEvent) {
	if s.sseHub == nil {
		return
	}
	s.sseHub.Broadcast(treeID, sse.ComposeEvent(treeID, actorID, "tree_merged", payload))
}

// --- Mapping helpers ---------------------------------------------------------

// mergeNodeFromDB maps a stored node onto the §3.7 wire shape. depth and
// childCount are computed by the caller (§3.4): a brand-new node always has
// child_count 0.
func mergeNodeFromDB(n db.Node, authorID uuid.UUID, displayName string, depth int) *MergeNode {
	author := n.AuthorID
	if author == uuid.Nil {
		author = authorID
	}
	return &MergeNode{
		ID:                n.ID,
		TreeID:            n.TreeID,
		ParentID:          n.ParentID,
		AuthorID:          author,
		AuthorDisplayName: displayName,
		Content:           n.Content,
		ContentFormat:     n.ContentFormat,
		NodeType:          n.NodeType,
		SequenceNum:       n.SequenceNum,
		Metadata:          append(json.RawMessage(nil), n.Metadata...),
		Depth:             depth,
		ChildCount:        0,
		CreatedAt:         n.CreatedAt,
		EditedAt:          n.EditedAt,
		DeletedAt:         n.DeletedAt,
	}
}

// mergeEdgeFromDB maps a stored edge onto the §3.6 wire shape.
func mergeEdgeFromDB(e db.Edge) *MergeEdge {
	return &MergeEdge{
		ID:           e.ID,
		TreeID:       e.TreeID,
		SourceNodeID: e.SourceID,
		TargetNodeID: e.TargetID,
		EdgeType:     e.EdgeType,
		CreatedAt:    e.CreatedAt,
	}
}
