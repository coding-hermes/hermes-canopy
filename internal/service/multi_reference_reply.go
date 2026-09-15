// Package service — multi-message reference model, atomic reply creation.
//
// Implements SPEC-PL-06 §13 / §13.1 (NodeService.CreateMultiReferenceReply),
// §13.2 (reserved metadata merge), §4.2 (atomic creation sequence), and the
// §3.5 invariant checks. Selection preflight lives in multi_reference.go.
//
// Deferred by this phase (see the ticket): the §10 SSE event vocabulary
// (the created node rides the ordinary node broadcast), §9.3
// GET /nodes/{id}/reference-context, §6 context-compiler integration, and
// the frontend of §4.1/§4.3/§7.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/sse"
)

// --- Request / response types (SPEC-PL-06 §13) -------------------------------

// CreateMultiReferenceReplyInput is the request body of
// POST /trees/{tree_id}/multi-reference-replies (§9.2).
type CreateMultiReferenceReplyInput struct {
	SelectionToken string          `json:"selection_token"`
	Content        string          `json:"content"`
	ContentFormat  string          `json:"content_format"`
	Metadata       json.RawMessage `json:"metadata"`
	RequestID      *uuid.UUID      `json:"request_id,omitempty"`
}

// CreateMultiReferenceReplyResult is the 201 Created envelope (§9.2).
type CreateMultiReferenceReplyResult struct {
	Node             *ReferenceReplyNode     `json:"node"`
	Edges            []*ReferenceReplyEdge   `json:"edges"`
	ReferenceContext ReferenceContextSummary `json:"reference_context"`
	Replayed         bool                    `json:"-"`
}

// ReferenceContextSummary is the §9.2 reference_context object.
type ReferenceContextSummary struct {
	ManifestHash          string `json:"manifest_hash"`
	SourceCount           int    `json:"source_count"`
	IsSyntheticMergePoint bool   `json:"is_synthetic_merge_point"`
}

// ReferenceReplyNode is the §9.2 node representation (snake_case, per §9's
// HTTP boundary rule).
type ReferenceReplyNode struct {
	ID            uuid.UUID       `json:"id"`
	TreeID        uuid.UUID       `json:"tree_id"`
	ParentID      *uuid.UUID      `json:"parent_id"`
	ParentMode    string          `json:"parent_mode"`
	AuthorID      uuid.UUID       `json:"author_id"`
	NodeType      string          `json:"node_type"`
	Content       string          `json:"content"`
	ContentFormat string          `json:"content_format"`
	SequenceNum   int64           `json:"sequence_num"`
	Metadata      json.RawMessage `json:"metadata"`
	CreatedAt     time.Time       `json:"created_at"`
}

// ReferenceReplyEdge is the §9.2 edge representation.
type ReferenceReplyEdge struct {
	ID           uuid.UUID       `json:"id"`
	TreeID       uuid.UUID       `json:"tree_id"`
	SourceNodeID uuid.UUID       `json:"source_node_id"`
	TargetNodeID uuid.UUID       `json:"target_node_id"`
	EdgeType     string          `json:"edge_type"`
	SequenceNum  int64           `json:"sequence_num"`
	Metadata     json.RawMessage `json:"metadata"`
	CreatedAt    time.Time       `json:"created_at"`
}

// ErrReferenceMetadataNotObject is returned when the optional client
// metadata is present but not a JSON object (§13.2 decodes it as an object).
var ErrReferenceMetadataNotObject = errors.New("node service: metadata must be a JSON object")

// WithReferenceSelection wires the selection-token verifier. Without it
// CreateMultiReferenceReply refuses to run (503) rather than creating a node
// whose provenance it cannot verify.
func (s *NodeServiceImpl) WithReferenceSelection(v ReferenceSelectionVerifier) *NodeServiceImpl {
	s.refResolver = v
	return s
}

// --- Creation ----------------------------------------------------------------

// CreateMultiReferenceReply creates one `message` node in
// parent_mode='multi_reference' plus exactly N reference edges from a signed
// selection, in a single transaction (§4.2, §13.1).
//
// Sequence (§5.3 / §13.1): revalidate the signed snapshot under locks →
// recompute branch span + manifest hash → insert the message target with
// parent_id = primary source → insert the reference set → validate the
// target's final incoming set → commit. Publication happens only after a
// successful commit, and a publication failure never rolls back a committed
// graph (§13.1 step 10).
func (s *NodeServiceImpl) CreateMultiReferenceReply(ctx context.Context, treeID uuid.UUID, input CreateMultiReferenceReplyInput) (*CreateMultiReferenceReplyResult, error) {
	if s.pool == nil {
		return nil, ErrDatabaseUnavailable
	}
	if treeID == uuid.Nil {
		return nil, ErrParentNotFound
	}
	if s.refResolver == nil {
		return nil, fmt.Errorf("%w: reference selection verifier not configured", ErrDatabaseUnavailable)
	}

	// §9.2: the caller is the created node's author.
	authorID := requesterFromContext(ctx)
	if authorID == uuid.Nil {
		return nil, ErrNodeAuthorRequired
	}

	// §13.1 step 2 — request shape.
	contentFormat := strings.TrimSpace(input.ContentFormat)
	if contentFormat == "" {
		contentFormat = string(NodeFormatMarkdown)
	}
	if !NodeContentFormat(contentFormat).Valid() {
		return nil, ErrInvalidContentFormat
	}
	if utf8Len(input.Content) > maxContentLen {
		return nil, ErrContentTooLong
	}
	clientMeta, err := decodeMetadataObject(input.Metadata)
	if err != nil {
		return nil, err
	}

	// §13.1 step 3 — resolve and verify the signed selection token.
	claims, err := s.refResolver.VerifyReferenceSelectionToken(ctx, input.SelectionToken, authorID)
	if err != nil {
		return nil, err
	}
	if claims.TreeID != treeID {
		// §9.4: a token that belongs to another tree.
		return nil, newReferenceAPIError(ErrReferenceTreeMismatch, "selection token belongs to another tree")
	}
	orderedIDs := make([]uuid.UUID, len(claims.SourceIDs))
	copy(orderedIDs, claims.SourceIDs)
	primary := claims.PrimarySourceID
	requestHash := referenceRequestHash(input.Content, contentFormat, primary, orderedIDs)

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("%w: begin tx: %v", ErrDatabaseUnavailable, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Serialise retries that share a request id so §15 scenario 14
	// ("concurrent calls using the same request ID produce one node and one
	// complete edge set") holds without an idempotency table (§3.1 is the
	// authoritative DDL and adds none — the record lives in the reserved
	// metadata key, §13.1 step 8).
	if input.RequestID != nil {
		if _, err := tx.Exec(ctx,
			`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
			"canopy-multi-reference:"+authorID.String()+":"+treeID.String()+":"+input.RequestID.String(),
		); err != nil {
			return nil, fmt.Errorf("%w: idempotency lock: %v", ErrDatabaseUnavailable, err)
		}
		replay, err := s.loadReplayedReply(ctx, tx, treeID, authorID, *input.RequestID, requestHash)
		if err != nil {
			return nil, err
		}
		if replay != nil {
			if err := tx.Commit(ctx); err != nil {
				return nil, fmt.Errorf("%w: commit replay: %v", ErrDatabaseUnavailable, err)
			}
			return replay, nil
		}
	}

	// §5.3 step 1 — lock the tree row and every requested source FOR SHARE.
	if err := lockTreeRow(ctx, tx, treeID); err != nil {
		return nil, err
	}
	sources, err := lockReferenceSources(ctx, tx, treeID, orderedIDs)
	if err != nil {
		return nil, err
	}

	// §13.1 step 4 — the locked snapshot must still match the token.
	if len(sources) != len(claims.SourceHashes) {
		return nil, newReferenceAPIError(ErrReferenceSelectionStale,
			"selection size changed since preflight")
	}
	for i, src := range sources {
		if src.ContentHash != claims.SourceHashes[i] {
			// §15 scenario 9: never answer from a silently mixed snapshot.
			return nil, newReferenceAPIError(ErrReferenceSelectionStale,
				"a selected message changed since preflight")
		}
	}

	// §5.3 steps 2-3 / §13.1 step 5 — recompute branch span and manifest
	// from the locked snapshot (client-supplied branch/budget/hash fields
	// are never trusted, §3.3).
	span, err := computeBranchSpan(ctx, tx, treeID, orderedIDs)
	if err != nil {
		return nil, err
	}
	budget := claims.Budget
	if budget <= 0 {
		budget = referenceAvailableBudget(referenceDefaultProfileBudget)
	}
	manifestHash := referenceManifestHash(treeID, primary, sources, budget)
	isMergePoint := IsSyntheticMergePoint(span)

	// §13.1 step 6 / §3.5 invariant 3 — target message with the display
	// anchor. nodes.id DEFAULT uuidv7() allocates the target UUIDv7.
	meta, err := buildMultiReferenceMetadata(clientMeta, db.MultiReferenceMetadata{
		Version:               db.MultiReferenceMetadataVersion,
		PrimarySourceID:       primary,
		CanonicalSourceIDs:    orderedIDs,
		IsSyntheticMergePoint: isMergePoint,
		BranchSpan:            branchSpanToDB(span),
		ContextManifestHash:   manifestHash,
		ContextTokenBudget:    budget,
	}, input.RequestID, requestHash)
	if err != nil {
		return nil, err
	}

	var seqNum int64
	if err := tx.QueryRow(ctx, `
        SELECT COALESCE(MAX(sequence_num), 0) + 1
        FROM nodes
        WHERE tree_id = $1`, treeID,
	).Scan(&seqNum); err != nil {
		return nil, fmt.Errorf("%w: sequence_num: %v", ErrDatabaseUnavailable, err)
	}

	var created db.Node
	err = tx.QueryRow(ctx, `
        INSERT INTO nodes
            (tree_id, parent_id, parent_mode, author_id, content, content_format,
             node_type, sequence_num, metadata)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
        RETURNING `+nodeColumns,
		treeID, primary, string(db.ParentModeMultiReference), authorID,
		input.Content, contentFormat, string(NodeKindMessage), seqNum, []byte(meta),
	).Scan(
		&created.ID, &created.TreeID, &created.ParentID, &created.ParentMode, &created.AuthorID,
		&created.Content, &created.ContentFormat, &created.NodeType,
		&created.SequenceNum, &created.Metadata,
		&created.CreatedAt, &created.EditedAt, &created.DeletedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: insert multi-reference node: %v", ErrDatabaseUnavailable, err)
	}

	// §5.3 step 5 / §13.1 step 7 — one edge per ordered source, then the
	// target's final invariant check.
	edgeRows, err := s.edgeRepo.CreateReferenceSet(ctx, tx, db.CreateReferenceSetInput{
		TreeID:         treeID,
		TargetID:       created.ID,
		OrderedSources: orderedIDs,
	})
	if err != nil {
		return nil, referenceRepoError(err)
	}
	if err := s.edgeRepo.ValidateIncomingInvariant(ctx, tx, &created); err != nil {
		return nil, referenceRepoError(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("%w: commit: %v", ErrDatabaseUnavailable, err)
	}

	// §13.1 step 10 / §10.1 — publish only after commit, in the documented
	// order: node_added, exactly N edge_added events ordered by
	// metadata.selection_order, then one multi_reference_converged composite
	// as the last event. A publication failure never rolls back a committed
	// graph, and nothing is broadcast before the commit above.
	s.broadcastNodeEvent(ctx, treeID, "node_added", created.ID, authorID)
	s.broadcastReferenceEdgeEvents(treeID, edgeRows, authorID)
	s.broadcastMultiReferenceConverged(treeID, authorID, MultiReferenceConvergedEvent{
		TreeID:                treeID,
		NodeID:                created.ID,
		ParentMode:            string(db.ParentModeMultiReference),
		PrimarySourceID:       primary,
		SourceNodeIDs:         append([]uuid.UUID(nil), orderedIDs...),
		EdgeIDs:               referenceEdgeIDs(edgeRows),
		IsSyntheticMergePoint: isMergePoint,
		CommonAncestorID:      commonAncestorID(span),
		ContextManifestHash:   manifestHash,
		CreatedAt:             created.CreatedAt.UTC().Format(time.RFC3339),
	})

	return &CreateMultiReferenceReplyResult{
		Node:  nodeToReferenceReplyNode(created),
		Edges: edgesToReferenceReplyEdges(edgeRows),
		ReferenceContext: ReferenceContextSummary{
			ManifestHash:          manifestHash,
			SourceCount:           len(edgeRows),
			IsSyntheticMergePoint: isMergePoint,
		},
	}, nil
}

// --- §10 SSE convergence vocabulary ------------------------------------------

// MultiReferenceConvergedEvent is the §10.2 composite event payload. It is
// exactly the field set the spec's TypeScript schema validates: no extra
// keys, snake_case on the boundary, context_manifest_hash as 64 hex
// characters and created_at as RFC3339.
//
// It is purely ADDITIVE: clients that only understand node_added/edge_added
// keep working (SPEC-API-03's event model — there is no second source of
// graph truth, §10.3 step 5).
type MultiReferenceConvergedEvent struct {
	TreeID                uuid.UUID   `json:"tree_id"`
	NodeID                uuid.UUID   `json:"node_id"`
	ParentMode            string      `json:"parent_mode"`
	PrimarySourceID       uuid.UUID   `json:"primary_source_id"`
	SourceNodeIDs         []uuid.UUID `json:"source_node_ids"`
	EdgeIDs               []uuid.UUID `json:"edge_ids"`
	IsSyntheticMergePoint bool        `json:"is_synthetic_merge_point"`
	CommonAncestorID      *uuid.UUID  `json:"common_ancestor_id"`
	ContextManifestHash   string      `json:"context_manifest_hash"`
	CreatedAt             string      `json:"created_at"`
}

// broadcastReferenceEdgeEvents publishes one `edge_added` event per created
// reference edge (§10.1: "One full `reference` edge", exactly N events,
// ordered by metadata.selection_order). The payload is the §9.2 edge
// representation, which carries the §5.2 metadata a convergence-aware client
// stores (selection order and accessible label, §10.3 step 2).
//
// Ordered-by-selection_order is structural here: CreateReferenceSet returns
// the edges in the order it inserted them, which is the canonical selection
// order it was given.
func (s *NodeServiceImpl) broadcastReferenceEdgeEvents(treeID uuid.UUID, edges []*db.Edge, actorID uuid.UUID) {
	if s.sseHub == nil {
		return
	}
	for _, e := range edges {
		if e == nil {
			continue
		}
		s.sseHub.Broadcast(treeID, sse.ComposeEvent(treeID, actorID, "edge_added",
			edgeToReferenceReplyEdge(e)))
	}
}

// broadcastMultiReferenceConverged publishes the §10.2 composite as the last
// event of a creation (§10.1: "Last event after all N edge events"). The
// composite `data` is exactly the §10.2 field set; the creator rides the SSE
// envelope's actor_id, outside `data`.
func (s *NodeServiceImpl) broadcastMultiReferenceConverged(treeID, actorID uuid.UUID, payload MultiReferenceConvergedEvent) {
	if s.sseHub == nil {
		return
	}
	s.sseHub.Broadcast(treeID, sse.ComposeEvent(treeID, actorID, "multi_reference_converged", payload))
}

// referenceEdgeIDs returns the created edges' ids in creation order (the
// same order as their sources, §10.2 `edge_ids`).
func referenceEdgeIDs(edges []*db.Edge) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(edges))
	for _, e := range edges {
		if e == nil {
			continue
		}
		out = append(out, e.ID)
	}
	return out
}

// commonAncestorID exposes the §8.1 common display ancestor, or nil when the
// span carries none (§10.2 allows null).
func commonAncestorID(span *BranchSpanMetadata) *uuid.UUID {
	if span == nil || span.CommonAncestorID == uuid.Nil {
		return nil
	}
	ancestor := span.CommonAncestorID
	return &ancestor
}

// --- Helpers -----------------------------------------------------------------

// lockTreeRow locks the tree row FOR SHARE and rejects a soft-deleted tree
// (§5.3 step 1; §9.4 TREE_DELETED).
func lockTreeRow(ctx context.Context, tx pgx.Tx, treeID uuid.UUID) error {
	var deletedAt *time.Time
	err := tx.QueryRow(ctx,
		`SELECT deleted_at FROM trees WHERE id = $1 FOR SHARE`, treeID).Scan(&deletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrTreeNotFound
	}
	if err != nil {
		return fmt.Errorf("%w: lock tree: %v", ErrDatabaseUnavailable, err)
	}
	if deletedAt != nil {
		return ErrTreeDeleted
	}
	return nil
}

// lockReferenceSources re-reads the selected sources FOR SHARE in canonical
// UUID order (§5.3 step 1) and applies the shared validator in creation mode,
// where any snapshot drift is REFERENCE_SELECTION_STALE (§4.3, §15 8-9).
func lockReferenceSources(ctx context.Context, tx pgx.Tx, treeID uuid.UUID, orderedIDs []uuid.UUID) ([]referenceSource, error) {
	locked := sortedUUIDStrings(orderedIDs)
	rows, err := tx.Query(ctx, `
        SELECT id, tree_id, node_type, parent_id, content, content_hash, sequence_num,
               (deleted_at IS NOT NULL) AS deleted
        FROM nodes
        WHERE id = ANY($1::uuid[])
        ORDER BY id
        FOR SHARE`, locked)
	if err != nil {
		return nil, fmt.Errorf("%w: lock reference sources: %v", ErrDatabaseUnavailable, err)
	}
	defer rows.Close()

	found := make(map[uuid.UUID]referenceSource, len(locked))
	for rows.Next() {
		var s referenceSource
		if err := rows.Scan(&s.ID, &s.TreeID, &s.NodeType, &s.ParentID, &s.Content,
			&s.ContentHash, &s.SequenceNum, &s.Deleted); err != nil {
			return nil, fmt.Errorf("%w: scan locked source: %v", ErrDatabaseUnavailable, err)
		}
		found[s.ID] = s
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate locked sources: %v", ErrDatabaseUnavailable, err)
	}

	if len(orderedIDs) < referenceSourceMinCount || len(orderedIDs) > referenceSourceMaxCount {
		// A signed token can only carry a valid count; treat anything else
		// as a stale/forged selection rather than proceeding.
		return nil, newReferenceAPIError(ErrReferenceSelectionStale,
			"selection size is outside the 2-20 range")
	}

	out := make([]referenceSource, 0, len(orderedIDs))
	for _, id := range orderedIDs {
		src, ok := found[id]
		if !ok {
			return nil, newReferenceAPIError(ErrReferenceSelectionStale,
				"a selected message no longer exists")
		}
		if src.Deleted {
			return nil, newReferenceAPIError(ErrReferenceSelectionStale,
				"a selected message was deleted since preflight")
		}
		if src.TreeID != treeID {
			return nil, newReferenceAPIError(ErrReferenceSelectionStale,
				"a selected message left this tree since preflight")
		}
		if src.NodeType == db.NodeTypeSystem {
			return nil, newReferenceAPIError(ErrReferenceSourceSystemForbidden, "")
		}
		out = append(out, src)
	}
	return out, nil
}

// referenceRepoError maps the repository's reference-set errors onto §9.4
// catalog codes. Anything unmapped is a 500-class failure.
func referenceRepoError(err error) error {
	switch {
	case errors.Is(err, db.ErrReferenceParentInvariant),
		errors.Is(err, db.ErrMultipleParents):
		return newReferenceAPIError(ErrReferenceParentInvariant, err.Error())
	case errors.Is(err, db.ErrSystemNodeParentForbidden):
		return newReferenceAPIError(ErrReferenceParentInvariant, err.Error())
	case errors.Is(err, db.ErrReferenceTargetType),
		errors.Is(err, db.ErrReferenceRequiresMultiReferenceMode):
		return newReferenceAPIError(ErrReferenceParentInvariant, err.Error())
	case errors.Is(err, db.ErrReferenceSourceCount),
		errors.Is(err, db.ErrReferenceSourceDuplicate),
		errors.Is(err, db.ErrSelfEdge):
		return newReferenceAPIError(ErrReferenceParentInvariant, err.Error())
	default:
		return fmt.Errorf("%w: reference write: %v", ErrDatabaseUnavailable, err)
	}
}

// decodeMetadataObject decodes the optional client metadata into a JSON
// object. §13.2 accepts only an object; anything else is a bad request.
func decodeMetadataObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(raw) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return map[string]json.RawMessage{}, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, ErrReferenceMetadataNotObject
	}
	if obj == nil {
		obj = map[string]json.RawMessage{}
	}
	return obj, nil
}

// buildMultiReferenceMetadata merges client metadata with the server-owned
// reserved key (§13.2): a client-supplied `multi_reference` object is dropped
// and replaced, other keys are retained, and the merged document is rejected
// (never truncated) when it exceeds the 16 KiB maximum.
func buildMultiReferenceMetadata(clientMeta map[string]json.RawMessage, reserved db.MultiReferenceMetadata, requestID *uuid.UUID, requestHash string) ([]byte, error) {
	if requestID != nil {
		reserved.RequestID = requestID.String()
		reserved.RequestHash = requestHash
	}
	merged := make(map[string]json.RawMessage, len(clientMeta)+1)
	for k, v := range clientMeta {
		if k == "multi_reference" {
			continue
		}
		merged[k] = v
	}
	rawReserved, err := json.Marshal(reserved)
	if err != nil {
		return nil, fmt.Errorf("%w: encode reserved metadata: %v", ErrDatabaseUnavailable, err)
	}
	merged["multi_reference"] = rawReserved

	out, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("%w: encode metadata: %v", ErrDatabaseUnavailable, err)
	}
	if len(out) > maxMetadataBytes {
		return nil, ErrMetadataTooLarge
	}
	return out, nil
}

// referenceRequestHash is the payload fingerprint stored with a request id so
// a retry with different content is a conflict, not a duplicate (§15 19-20).
func referenceRequestHash(content, contentFormat string, primary uuid.UUID, orderedIDs []uuid.UUID) string {
	h := sha256.New()
	h.Write([]byte("canopy-multi-reference|v1|"))
	h.Write([]byte(content))
	h.Write([]byte{0})
	h.Write([]byte(contentFormat))
	h.Write([]byte{0})
	h.Write([]byte(primary.String()))
	for _, id := range orderedIDs {
		h.Write([]byte{0})
		h.Write([]byte(id.String()))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// loadReplayedReply returns the original result when the same caller already
// used this request id for this tree. A matching payload hash replays the
// stored node and its edges; a different hash is
// REFERENCE_REQUEST_ID_CONFLICT and creates nothing (§15 19-20).
func (s *NodeServiceImpl) loadReplayedReply(ctx context.Context, tx pgx.Tx, treeID, authorID, requestID uuid.UUID, requestHash string) (*CreateMultiReferenceReplyResult, error) {
	var prior db.Node
	err := tx.QueryRow(ctx, `
        SELECT `+nodeColumns+`
        FROM nodes
        WHERE tree_id = $1
          AND author_id = $2
          AND metadata->'multi_reference'->>'requestId' = $3
          AND deleted_at IS NULL
        LIMIT 1`,
		treeID, authorID, requestID.String(),
	).Scan(
		&prior.ID, &prior.TreeID, &prior.ParentID, &prior.ParentMode, &prior.AuthorID,
		&prior.Content, &prior.ContentFormat, &prior.NodeType,
		&prior.SequenceNum, &prior.Metadata,
		&prior.CreatedAt, &prior.EditedAt, &prior.DeletedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: load idempotent reply: %v", ErrDatabaseUnavailable, err)
	}

	var reserved db.MultiReferenceMetadata
	if err := json.Unmarshal(metadataSection(prior.Metadata, "multi_reference"), &reserved); err != nil {
		return nil, fmt.Errorf("%w: decode prior metadata: %v", ErrDatabaseUnavailable, err)
	}
	if reserved.RequestHash != requestHash {
		return nil, newReferenceAPIError(ErrReferenceRequestIDConflict, "")
	}

	edges, err := s.edgeRepo.GetActiveIncoming(ctx, tx, prior.ID)
	if err != nil {
		return nil, fmt.Errorf("%w: load replay edges: %v", ErrDatabaseUnavailable, err)
	}

	return &CreateMultiReferenceReplyResult{
		Node:  nodeToReferenceReplyNode(prior),
		Edges: edgesToReferenceReplyEdges(edges),
		ReferenceContext: ReferenceContextSummary{
			ManifestHash:          reserved.ContextManifestHash,
			SourceCount:           len(edges),
			IsSyntheticMergePoint: reserved.IsSyntheticMergePoint,
		},
		Replayed: true,
	}, nil
}

// metadataSection extracts one top-level object from a node's metadata
// document, returning nil when it is absent.
func metadataSection(metadata []byte, key string) []byte {
	if len(metadata) == 0 {
		return nil
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(metadata, &doc); err != nil {
		return nil
	}
	return doc[key]
}

// branchSpanToDB converts the wire branch span into its stored form (§3.3).
func branchSpanToDB(span *BranchSpanMetadata) *db.BranchSpanMetadata {
	if span == nil {
		return nil
	}
	out := &db.BranchSpanMetadata{
		CommonAncestorID: span.CommonAncestorID,
		SourceBranches:   make([]db.ReferenceBranchSource, 0, len(span.SourceBranches)),
	}
	for _, sb := range span.SourceBranches {
		out.SourceBranches = append(out.SourceBranches, db.ReferenceBranchSource{
			SourceID:         sb.SourceID,
			BranchRootID:     sb.BranchRootID,
			DistanceFromRoot: sb.DistanceFromRoot,
		})
	}
	return out
}

// nodeToReferenceReplyNode maps a stored node onto the §9.2 wire shape.
func nodeToReferenceReplyNode(n db.Node) *ReferenceReplyNode {
	return &ReferenceReplyNode{
		ID:            n.ID,
		TreeID:        n.TreeID,
		ParentID:      n.ParentID,
		ParentMode:    string(n.ParentMode),
		AuthorID:      n.AuthorID,
		NodeType:      n.NodeType,
		Content:       n.Content,
		ContentFormat: n.ContentFormat,
		SequenceNum:   n.SequenceNum,
		Metadata:      append(json.RawMessage(nil), n.Metadata...),
		CreatedAt:     n.CreatedAt,
	}
}

// edgesToReferenceReplyEdges maps stored edges onto the §9.2 wire shape.
func edgesToReferenceReplyEdges(edges []*db.Edge) []*ReferenceReplyEdge {
	out := make([]*ReferenceReplyEdge, 0, len(edges))
	for _, e := range edges {
		if e == nil {
			continue
		}
		out = append(out, edgeToReferenceReplyEdge(e))
	}
	return out
}

// edgeToReferenceReplyEdge maps one stored edge onto the §9.2 wire shape.
// The same shape is the §10.1 `edge_added` payload (one full reference edge).
func edgeToReferenceReplyEdge(e *db.Edge) *ReferenceReplyEdge {
	return &ReferenceReplyEdge{
		ID:           e.ID,
		TreeID:       e.TreeID,
		SourceNodeID: e.SourceID,
		TargetNodeID: e.TargetID,
		EdgeType:     e.EdgeType,
		SequenceNum:  e.SequenceNum,
		Metadata:     append(json.RawMessage(nil), e.Metadata...),
		CreatedAt:    e.CreatedAt,
	}
}
