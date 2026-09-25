// Package service — multi-message reference model, reference-context read.
//
// Implements SPEC-PL-06 §9.3 (GET /nodes/{node_id}/reference-context): the
// provenance-safe read of a multi-reference reply's context. Two properties
// are load-bearing:
//
//  1. The response is built from what CREATION persisted — the node's
//     parent_mode, metadata.multi_reference (contextManifestHash,
//     contextTokenBudget, primarySourceId, branchSpan,
//     isSyntheticMergePoint) and the reference edges' §5.2 metadata. The
//     manifest hash in the response is always the STORED hash: it is the
//     provenance record, and nothing here recomputes it into the answer.
//  2. A read of a node that is not a multi-reference reply answers
//     404 REFERENCE_CONTEXT_NOT_FOUND — the same code, with the same
//     message, as a node that does not exist at all, so the route is not an
//     existence oracle (§9.3, §9.4).
//
// `verify_hash` is the one place a digest is computed, and it is only ever
// compared against the stored one to set `source_changed_since_creation`.
// The digest is never returned.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/coding-hermes/hermes-canopy/internal/db"
	"github.com/coding-hermes/hermes-canopy/internal/sse"
)

// ReferenceMaxSourceTokens is the §6.2 per-source token ceiling and the
// documented maximum of §9.3's `max_source_tokens` query parameter. It is
// exported so the HTTP layer clamps to the same number the service enforces.
const ReferenceMaxSourceTokens = referenceMaxSourceTokens

// errReferenceContextManifestUnreadable reports a node that is marked
// parent_mode='multi_reference' but carries no usable reserved manifest. The
// creation path writes both in one transaction (§4.2, §13.1), so this is a
// corrupt-row case and must fail loudly (500) rather than answer with an
// envelope whose manifest_hash is an empty lie.
var errReferenceContextManifestUnreadable = errors.New("service: multi-reference node has no readable context manifest")

// --- Wire types (SPEC-PL-06 §9.3) --------------------------------------------

// ReferenceContextOptions are the parsed §9.3 query parameters.
type ReferenceContextOptions struct {
	// IncludeContent mirrors `include_content` (default true). When false,
	// every source's `content` key is omitted — not returned empty — so a
	// client can tell "not asked for" from "empty".
	IncludeContent bool
	// MaxSourceTokens mirrors `max_source_tokens`. Zero means the source
	// allocation in force at creation: the §6.2 per-source ceiling of
	// 2,048 tokens (SPEC-PL-06 §2 decision 28 keeps only the hash plus the
	// total budget on the node row, so the ceiling is the only persisted
	// per-source allocation).
	MaxSourceTokens int
	// VerifyHash mirrors `verify_hash` (default true). When false the
	// response omits `source_changed_since_creation` entirely.
	VerifyHash bool
}

// referenceContextSSEHubKey carries the shared hub from the HTTP handler into
// the TreeService read without changing the existing TreeService interface.
type referenceContextSSEHubKey struct{}

// WithReferenceContextSSEHub supplies the post-read publication sink for a
// lazy retained-context audit. A nil hub is valid and simply disables events.
func WithReferenceContextSSEHub(ctx context.Context, hub sse.SSEHub) context.Context {
	if hub == nil {
		return ctx
	}
	return context.WithValue(ctx, referenceContextSSEHubKey{}, hub)
}

func referenceContextSSEHub(ctx context.Context) sse.SSEHub {
	hub, _ := ctx.Value(referenceContextSSEHubKey{}).(sse.SSEHub)
	return hub
}

// ReferenceContextResult is the §9.3 200 envelope. Field names are
// snake_case (§9: HTTP boundaries use snake_case).
type ReferenceContextResult struct {
	NodeID          uuid.UUID            `json:"node_id"`
	TreeID          uuid.UUID            `json:"tree_id"`
	ParentMode      string               `json:"parent_mode"`
	PrimarySourceID uuid.UUID            `json:"primary_source_id"`
	Context         ReferenceContextView `json:"context"`
	// SourceChangedSinceCreation is present (true) only when verify_hash
	// was requested and the live snapshot no longer matches the creation
	// provenance.
	SourceChangedSinceCreation bool `json:"source_changed_since_creation,omitempty"`
	// ContextInvalidated and InvalidationReasons are additive retained-context
	// audit fields. They are omitted for an unchanged snapshot.
	ContextInvalidated  bool     `json:"context_invalidated,omitempty"`
	InvalidationReasons []string `json:"invalidation_reasons,omitempty"`
}

// ReferenceContextView is the §9.3 `context` object.
type ReferenceContextView struct {
	Sources               []ReferenceContextSource   `json:"sources"`
	IsSyntheticMergePoint bool                       `json:"is_synthetic_merge_point"`
	BranchSpan            ReferenceContextBranchSpan `json:"branch_span"`
	TokenBudget           int                        `json:"token_budget"`
	TokensUsed            int                        `json:"tokens_used"`
	ManifestHash          string                     `json:"manifest_hash"`
}

// ReferenceContextSource is one §9.3 `context.sources[]` entry. The label is
// `R{index + 1}` in persisted selection order (§5.2).
type ReferenceContextSource struct {
	SourceLabel string    `json:"source_label"`
	NodeID      uuid.UUID `json:"node_id"`
	// Content is a pointer so `include_content=false` omits the key while
	// include_content=true always emits it (even for an empty source).
	Content   *string `json:"content,omitempty"`
	Truncated bool    `json:"truncated"`
}

// ReferenceContextBranchSpan is the §9.3 `context.branch_span` object.
// common_ancestor_id is nullable because a node whose retained manifest has
// no branch span (or whose ancestor was the nil UUID) has none to report.
type ReferenceContextBranchSpan struct {
	CommonAncestorID *uuid.UUID              `json:"common_ancestor_id"`
	SourceBranches   []ReferenceBranchSource `json:"source_branches"`
}

// referenceContextNode is the read-time projection of the target node.
type referenceContextNode struct {
	ID         uuid.UUID
	TreeID     uuid.UUID
	ParentID   *uuid.UUID
	ParentMode string
	Metadata   []byte
	Deleted    bool
}

// referenceContextSourceRow is one active reference edge joined with its
// source node. SourceDeleted carries the source's soft-delete state, which
// is a provenance change rather than a reason to hide the edge (§2 decision
// 37: deleting a source preserves the edge so existing replies stay
// auditable).
type referenceContextSourceRow struct {
	EdgeID        uuid.UUID
	SourceID      uuid.UUID
	Content       string
	ContentHash   string
	SequenceNum   int64
	SourceDeleted bool
}

// referenceContextInvalidation is the internal form used by both the response
// and the §10.1 event. TreeID and NodeID make the event complete without
// relying on the surrounding request URL.
type referenceContextInvalidation struct {
	TreeID       uuid.UUID
	NodeID       uuid.UUID
	SourceNodeID uuid.UUID
	OldHash      string
	NewHash      string
	Reason       string
}

// --- Read --------------------------------------------------------------------

// GetReferenceContext returns the stored provenance context of a
// multi-reference reply (§9.3).
//
// It answers REFERENCE_CONTEXT_NOT_FOUND (404) for a node that does not
// exist, is soft-deleted, or is not in parent_mode='multi_reference' — one
// code for all three so the route cannot be used to test existence.
func (s *TreeServiceImpl) GetReferenceContext(ctx context.Context, nodeID uuid.UUID, opts ReferenceContextOptions) (*ReferenceContextResult, error) {
	if s.pool == nil {
		return nil, ErrDatabaseUnavailable
	}

	var node referenceContextNode
	err := s.pool.QueryRow(ctx, `
        SELECT id, tree_id, parent_id, parent_mode, metadata,
               (deleted_at IS NOT NULL) AS deleted
        FROM nodes
        WHERE id = $1`, nodeID,
	).Scan(&node.ID, &node.TreeID, &node.ParentID, &node.ParentMode, &node.Metadata, &node.Deleted)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, newReferenceAPIError(ErrReferenceContextNotFound, "")
	}
	if err != nil {
		return nil, fmt.Errorf("%w: read reference target: %v", ErrDatabaseUnavailable, err)
	}
	if node.Deleted || node.ParentMode != string(db.ParentModeMultiReference) {
		return nil, newReferenceAPIError(ErrReferenceContextNotFound, "")
	}

	raw := metadataSection(node.Metadata, "multi_reference")
	if len(raw) == 0 {
		return nil, errReferenceContextManifestUnreadable
	}
	var reserved db.MultiReferenceMetadata
	if err := json.Unmarshal(raw, &reserved); err != nil {
		return nil, fmt.Errorf("%w: decode reserved metadata: %v", errReferenceContextManifestUnreadable, err)
	}

	rows, err := s.pool.Query(ctx, `
        SELECT e.id, e.source_id, s.content, s.content_hash, s.sequence_num,
               (s.deleted_at IS NOT NULL) AS source_deleted
        FROM edges e
        JOIN nodes s ON s.id = e.source_id
        WHERE e.target_id = $1
          AND e.edge_type = $2
          AND e.deleted_at IS NULL
        ORDER BY COALESCE((e.metadata->>'selection_order')::int, 2147483647) ASC,
                 e.sequence_num ASC`,
		nodeID, db.EdgeTypeReference)
	if err != nil {
		return nil, fmt.Errorf("%w: read reference parents: %v", ErrDatabaseUnavailable, err)
	}
	defer rows.Close()

	sources := make([]referenceContextSourceRow, 0, referenceSourceMaxCount)
	for rows.Next() {
		var row referenceContextSourceRow
		if err := rows.Scan(&row.EdgeID, &row.SourceID, &row.Content, &row.ContentHash,
			&row.SequenceNum, &row.SourceDeleted); err != nil {
			return nil, fmt.Errorf("%w: scan reference parent: %v", ErrDatabaseUnavailable, err)
		}
		sources = append(sources, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate reference parents: %v", ErrDatabaseUnavailable, err)
	}

	invalidations := checkReferenceContextInvalidated(node, contextAuditMetadata(node.Metadata), sources)
	result, err := buildReferenceContext(node, reserved, sources, opts)
	if err != nil {
		return nil, err
	}
	if opts.VerifyHash && len(invalidations) > 0 {
		result.ContextInvalidated = true
		result.InvalidationReasons = invalidationReasons(invalidations)
		for _, invalidation := range invalidations {
			s.broadcastReferenceContextInvalidated(ctx, invalidation)
		}
	}
	return result, nil
}

// contextAuditMetadata decodes the bounded creation snapshot. Missing or
// malformed snapshots are treated as legacy metadata: existing context reads
// remain available, but there is no false-positive invalidation claim.
func contextAuditMetadata(metadata []byte) map[string]string {
	raw := metadataSection(metadata, "context_audit")
	if len(raw) == 0 {
		return nil
	}
	var audit map[string]string
	if err := json.Unmarshal(raw, &audit); err != nil {
		return nil
	}
	return audit
}

// checkReferenceContextInvalidated compares each live source hash and soft
// deletion state with the creation snapshot stored on the target node.
func checkReferenceContextInvalidated(node referenceContextNode, audit map[string]string, rows []referenceContextSourceRow) []referenceContextInvalidation {
	if len(audit) == 0 {
		return nil
	}
	invalidations := make([]referenceContextInvalidation, 0, len(rows))
	for _, row := range rows {
		oldHash, ok := audit[row.SourceID.String()]
		if !ok {
			continue
		}
		invalidation := referenceContextInvalidation{
			TreeID:       node.TreeID,
			NodeID:       node.ID,
			SourceNodeID: row.SourceID,
			OldHash:      oldHash,
			NewHash:      row.ContentHash,
		}
		if row.SourceDeleted {
			invalidation.Reason = "source_deleted"
			invalidation.NewHash = ""
		} else if row.ContentHash != oldHash {
			invalidation.Reason = "source_modified"
		} else {
			continue
		}
		invalidations = append(invalidations, invalidation)
	}
	return invalidations
}

func invalidationReasons(invalidations []referenceContextInvalidation) []string {
	seen := make(map[string]struct{}, len(invalidations))
	reasons := make([]string, 0, len(invalidations))
	for _, invalidation := range invalidations {
		if _, ok := seen[invalidation.Reason]; ok {
			continue
		}
		seen[invalidation.Reason] = struct{}{}
		reasons = append(reasons, invalidation.Reason)
	}
	return reasons
}

// buildReferenceContext assembles the §9.3 envelope from the persisted node
// manifest, the persisted reference edges and the live source rows. It is
// pure so the envelope contract can be tested without a database.
func buildReferenceContext(node referenceContextNode, reserved db.MultiReferenceMetadata, rows []referenceContextSourceRow, opts ReferenceContextOptions) (*ReferenceContextResult, error) {
	if !isManifestHash(reserved.ContextManifestHash) {
		return nil, errReferenceContextManifestUnreadable
	}

	// The display anchor is the deterministic first canonical source (§2
	// decision 13); the reserved manifest records the same id.
	primary := reserved.PrimarySourceID
	if primary == uuid.Nil && node.ParentID != nil {
		primary = *node.ParentID
	}

	maxSourceTokens := opts.MaxSourceTokens
	if maxSourceTokens <= 0 || maxSourceTokens > ReferenceMaxSourceTokens {
		maxSourceTokens = ReferenceMaxSourceTokens
	}

	sources := make([]ReferenceContextSource, 0, len(rows))
	live := make([]referenceSource, 0, len(rows))
	deletedSource := false
	tokensUsed := 0
	for i, row := range rows {
		label := db.ReferenceSourceLabel(i)
		var content *string
		if opts.IncludeContent {
			text, _ := referenceTruncateContent(row.Content, maxSourceTokens, label)
			content = &text
		}
		sources = append(sources, ReferenceContextSource{
			SourceLabel: label,
			NodeID:      row.SourceID,
			Content:     content,
			// Truncated reports the source's size against the allowance
			// regardless of whether content was requested — the field is
			// part of the §9.3 shape in both modes.
			Truncated: estimateSourceTokens(row.Content) > maxSourceTokens,
		})

		estimate := estimateSourceTokens(row.Content)
		if estimate > referenceMaxSourceTokens {
			estimate = referenceMaxSourceTokens
		}
		tokensUsed += estimate
		if row.SourceDeleted {
			deletedSource = true
		}
		live = append(live, referenceSource{
			ID:          row.SourceID,
			ContentHash: row.ContentHash,
			SequenceNum: row.SequenceNum,
		})
	}

	// verify_hash (§9.3): compare the digest of the CURRENT snapshot against
	// the STORED manifest hash. The digest is a change detector only — the
	// response always carries the stored hash, because that is the
	// provenance record created under §5.3 step 3.
	changed := false
	if opts.VerifyHash {
		if referenceManifestHash(node.TreeID, primary, live, reserved.ContextTokenBudget) != reserved.ContextManifestHash {
			changed = true
		}
		if deletedSource {
			changed = true
		}
	}

	return &ReferenceContextResult{
		NodeID:          node.ID,
		TreeID:          node.TreeID,
		ParentMode:      node.ParentMode,
		PrimarySourceID: primary,
		Context: ReferenceContextView{
			Sources:               sources,
			IsSyntheticMergePoint: reserved.IsSyntheticMergePoint,
			BranchSpan:            branchSpanContext(reserved.BranchSpan),
			TokenBudget:           reserved.ContextTokenBudget,
			TokensUsed:            tokensUsed,
			ManifestHash:          reserved.ContextManifestHash,
		},
		SourceChangedSinceCreation: changed,
	}, nil
}

// branchSpanContext converts the stored (camelCase) branch span into the
// §9.3 (snake_case) `branch_span` object. An absent span still yields the
// object with a null ancestor and an empty list — the key is part of the
// documented shape.
func branchSpanContext(span *db.BranchSpanMetadata) ReferenceContextBranchSpan {
	out := ReferenceContextBranchSpan{SourceBranches: []ReferenceBranchSource{}}
	if span == nil {
		return out
	}
	if span.CommonAncestorID != uuid.Nil {
		ancestor := span.CommonAncestorID
		out.CommonAncestorID = &ancestor
	}
	for _, sb := range span.SourceBranches {
		out.SourceBranches = append(out.SourceBranches, ReferenceBranchSource{
			SourceID:         sb.SourceID,
			BranchRootID:     sb.BranchRootID,
			DistanceFromRoot: sb.DistanceFromRoot,
		})
	}
	return out
}

// referenceTruncateContent applies the §6.1 head+tail rule to a read-time
// per-source token allowance: the head and tail are retained around an
// explicit omission marker so a reader (or an agent) keeps the source's
// opening and closing claims. The bool reports whether anything was omitted.
//
// The allowance bounds the RETAINED text; the marker itself is additional.
func referenceTruncateContent(content string, maxTokens int, sourceLabel string) (string, bool) {
	if maxTokens <= 0 {
		maxTokens = ReferenceMaxSourceTokens
	}
	total := estimateSourceTokens(content)
	if total <= maxTokens {
		return content, false
	}

	runes := []rune(content)
	// §6.2's estimator is 4 characters per token; half the allowance is kept
	// from each end.
	half := maxTokens * estimateCharsPerToken / 2
	if half > len(runes) {
		half = len(runes)
	}
	tail := half
	if remaining := len(runes) - half; tail > remaining {
		tail = remaining
	}

	omitted := total - (half+tail)/estimateCharsPerToken
	if omitted < 1 {
		omitted = 1
	}
	marker := fmt.Sprintf("[... %d tokens omitted from source %s ...]", omitted, sourceLabel)
	return string(runes[:half]) + "\n" + marker + "\n" + string(runes[len(runes)-tail:]), true
}

// estimateCharsPerToken is the deterministic characters-per-token rule shared
// with estimateSourceTokens (§6.2).
const estimateCharsPerToken = 4

// isManifestHash reports whether s is the §3.3 64-hex-character manifest hash
// (§10.2 validates it with the same rule).
func isManifestHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}
