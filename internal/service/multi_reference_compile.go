// Package service — multi-message reference model, compile-surface loader.
//
// Implements the caller seam for SPEC-PL-06 §6: given the target of a
// compile request, load the PERSISTED selection — the node's reserved
// metadata.multi_reference record plus its ordered reference edges — into a
// snapshot the context compiler renders into the §6.1 block.
//
// Two properties are load-bearing:
//
//  1. The order and the digest are the creation path's, not a new rule.
//     Sources load in canonical selection order (the §9.3 ORDER BY, by
//     metadata.selection_order then sequence_num), and staleness is decided
//     exactly the way §9.3 decides it: referenceManifestHash — the same
//     helper §5.3 step 3 used — is recomputed over the LIVE source set and
//     compared with the STORED metadata.contextManifestHash.
//  2. A stale selection fails the whole load. A changed source, a
//     soft-deleted source, or a shrunk edge set is
//     REFERENCE_SELECTION_STALE (§9.4) — never a 200 that mixes snapshots,
//     never a partial source set (§6.4).
//
// The projection types below are deliberately owned by THIS package: the
// HTTP layer maps them onto context.MultiReferenceSelection. internal/service
// must not import internal/context — context imports card, and card imports
// service, so that edge would close an import cycle (proved: `go build
// ./internal/service` reports "import cycle not allowed").
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

// CompileSelection is a persisted multi-reference selection loaded for the
// context compiler (§6). TreeID and Metadata are the stored §3.3 record —
// including the manifest hash the compiler must carry verbatim, never
// recompute.
type CompileSelection struct {
	TreeID   uuid.UUID
	Metadata db.MultiReferenceMetadata
	Sources  []CompileSelectionSource
}

// CompileSelectionSource is one selected source in canonical selection order
// (R1..RN), carrying what the §6.1 block renders.
type CompileSelectionSource struct {
	NodeID       uuid.UUID
	AuthorID     uuid.UUID
	NodeType     string
	SequenceNum  int64
	CreatedAt    time.Time
	BranchRootID uuid.UUID
	// ContentHash is the source's LIVE nodes.content_hash. The loader has
	// already proven the live set is the signed snapshot, so this hash is
	// also the verified hash the §6.4 per-source check compares against.
	ContentHash string
	Content     string
	Label       string
	ColorKey    string
}

// compileSelectionTarget is the read-time projection of the compile target.
type compileSelectionTarget struct {
	ID         uuid.UUID
	TreeID     uuid.UUID
	ParentID   *uuid.UUID
	ParentMode string
	Metadata   []byte
	Deleted    bool
}

// compileSelectionSourceRow is one active reference edge joined with its
// source node. SourceDeleted carries the source's soft-delete state: deleting
// a source preserves the edge (§2 decision 37) so an existing reply stays
// auditable, which makes it a staleness signal for compilation rather than a
// reason to hide the row.
type compileSelectionSourceRow struct {
	SourceID      uuid.UUID
	AuthorID      uuid.UUID
	NodeType      string
	SequenceNum   int64
	CreatedAt     time.Time
	Content       string
	ContentHash   string
	SourceDeleted bool
	SourceLabel   *string
	ColorKey      *string
}

// LoadCompileSelection loads the persisted multi-reference selection of a
// compile target (§6), or nil when the target has none.
//
// A node that does not exist, is soft-deleted, or is not in
// parent_mode='multi_reference' yields (nil, nil): the compiler's own read
// decides the answer for that node, so an ordinary turn keeps compiling
// byte-for-byte as it did before multi-reference support.
//
// A target that IS parent_mode='multi_reference' but carries no readable
// reserved manifest fails loudly (errReferenceContextManifestUnreadable) —
// the creation path writes both in one transaction (§4.2, §13.1), so this is
// a corrupt row and must never be answered with a fabricated empty block.
func (s *TreeServiceImpl) LoadCompileSelection(ctx context.Context, nodeID uuid.UUID) (*CompileSelection, error) {
	if s.pool == nil {
		return nil, ErrDatabaseUnavailable
	}

	var target compileSelectionTarget
	err := s.pool.QueryRow(ctx, `
        SELECT id, tree_id, parent_id, parent_mode, metadata,
               (deleted_at IS NOT NULL) AS deleted
        FROM nodes
        WHERE id = $1`, nodeID,
	).Scan(&target.ID, &target.TreeID, &target.ParentID, &target.ParentMode, &target.Metadata, &target.Deleted)
	if errors.Is(err, pgx.ErrNoRows) {
		// No such node: no selection. The compiler answers NODE_NOT_FOUND.
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: read compile target: %v", ErrDatabaseUnavailable, err)
	}
	if target.Deleted || target.ParentMode != string(db.ParentModeMultiReference) {
		return nil, nil
	}

	raw := metadataSection(target.Metadata, "multi_reference")
	if len(raw) == 0 {
		return nil, errReferenceContextManifestUnreadable
	}
	var reserved db.MultiReferenceMetadata
	if err := json.Unmarshal(raw, &reserved); err != nil {
		return nil, fmt.Errorf("%w: decode reserved metadata: %v", errReferenceContextManifestUnreadable, err)
	}
	// The stored digest is the provenance record (§3.3). A row whose digest
	// is not a digest cannot be verified, so it is corrupt rather than stale.
	if !isManifestHash(reserved.ContextManifestHash) {
		return nil, errReferenceContextManifestUnreadable
	}

	// Canonical selection order — the same ORDER BY §9.3 reads with. The
	// source's soft-delete state is SELECTED, not filtered: a deleted source
	// still has its edge and must be reported as a change, not skipped.
	rows, err := s.pool.Query(ctx, `
        SELECT e.source_id, s.author_id, s.node_type, s.sequence_num, s.created_at,
               s.content, s.content_hash,
               (s.deleted_at IS NOT NULL) AS source_deleted,
               e.metadata->>'source_label' AS source_label,
               e.metadata->>'color_key'    AS color_key
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

	loaded := make([]compileSelectionSourceRow, 0, referenceSourceMaxCount)
	for rows.Next() {
		var row compileSelectionSourceRow
		if err := rows.Scan(&row.SourceID, &row.AuthorID, &row.NodeType, &row.SequenceNum, &row.CreatedAt,
			&row.Content, &row.ContentHash, &row.SourceDeleted, &row.SourceLabel, &row.ColorKey); err != nil {
			return nil, fmt.Errorf("%w: scan reference parent: %v", ErrDatabaseUnavailable, err)
		}
		loaded = append(loaded, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate reference parents: %v", ErrDatabaseUnavailable, err)
	}

	// The display anchor is the deterministic first canonical source (§2
	// decision 13); the reserved manifest records the same id.
	primary := reserved.PrimarySourceID
	if primary == uuid.Nil && target.ParentID != nil {
		primary = *target.ParentID
	}

	branchRoots := make(map[uuid.UUID]uuid.UUID, len(loaded))
	if reserved.BranchSpan != nil {
		for _, sb := range reserved.BranchSpan.SourceBranches {
			branchRoots[sb.SourceID] = sb.BranchRootID
		}
	}

	selection := &CompileSelection{
		TreeID:   target.TreeID,
		Metadata: reserved,
		Sources:  make([]CompileSelectionSource, 0, len(loaded)),
	}
	live := make([]referenceSource, 0, len(loaded))
	deletedSource := false
	for i, row := range loaded {
		if row.SourceDeleted {
			deletedSource = true
		}
		live = append(live, referenceSource{
			ID:          row.SourceID,
			ContentHash: row.ContentHash,
			SequenceNum: row.SequenceNum,
		})

		// §8.1 / §15 scenario 14: a source that IS the common ancestor is
		// its own branch root, and a span that does not carry the source
		// leaves the source's own row as the only anchor.
		branchRoot := branchRoots[row.SourceID]
		if branchRoot == uuid.Nil {
			branchRoot = row.SourceID
		}

		// §5.2 presentation fields live on the reference edge; the derived
		// helpers are the fallback for an edge whose metadata is absent or
		// partial.
		label := db.ReferenceSourceLabel(i)
		if row.SourceLabel != nil && *row.SourceLabel != "" {
			label = *row.SourceLabel
		}
		colorKey := db.ReferenceColorKey(target.TreeID, target.ID, row.SourceID)
		if row.ColorKey != nil && *row.ColorKey != "" {
			colorKey = *row.ColorKey
		}

		selection.Sources = append(selection.Sources, CompileSelectionSource{
			NodeID:       row.SourceID,
			AuthorID:     row.AuthorID,
			NodeType:     row.NodeType,
			SequenceNum:  row.SequenceNum,
			CreatedAt:    row.CreatedAt.UTC(),
			BranchRootID: branchRoot,
			ContentHash:  row.ContentHash,
			Content:      row.Content,
			Label:        label,
			ColorKey:     colorKey,
		})
	}

	// §9.3/§9.4: the digest of the CURRENT snapshot must equal the STORED
	// one, and no source may have been deleted since. Anything else mixes
	// snapshots, so compilation fails rather than answering from a set the
	// user never selected.
	if deletedSource ||
		referenceManifestHash(target.TreeID, primary, live, reserved.ContextTokenBudget) != reserved.ContextManifestHash {
		return nil, newReferenceAPIError(ErrReferenceSelectionStale,
			"the selected messages changed or were deleted after this reply was created")
	}
	return selection, nil
}
