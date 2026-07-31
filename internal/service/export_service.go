// Package service provides the business logic for Canopy import/export.
// ExportService serialises a conversation tree (with all nodes and edges)
// to a portable JSON format and imports trees from that same format.
//
// The wire format mirrors the db.Node/db.Edge types so downstream
// consumers can round-trip without loss. Import assigns new UUIDs to
// every entity and remaps edge references accordingly.
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
)

// --- Error sentinels -------------------------------------------------------

var (
	ErrExportNotFound         = errors.New("export service: tree not found")
	ErrExportInvalidJSON      = errors.New("export service: invalid import payload")
	ErrExportMissingTree      = errors.New("export service: import payload missing tree")
	ErrExportMissingRootNode  = errors.New("export service: import payload missing root node")
	ErrExportInvalidRootNode  = errors.New("export service: import payload root node not in nodes list")
	ErrExportEdgeNodeNotFound = errors.New("export service: edge references node not in import payload")
)

// --- Wire types ------------------------------------------------------------

// ExportData is the top-level serialisation envelope for a conversation tree.
// It holds the tree metadata, all nodes, and all edges so a complete
// conversation DAG can be reconstructed from a single JSON document.
type ExportData struct {
	Tree      ExportTree `json:"tree"`
	Nodes     []db.Node  `json:"nodes"`
	Edges     []db.Edge  `json:"edges"`
	Version   int        `json:"version"`
	ExportedAt time.Time `json:"exportedAt"`
}

// ExportTree carries the tree-level metadata that is included in the export.
type ExportTree struct {
	Title       string    `json:"title"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"createdAt"`
	RootNodeID  uuid.UUID `json:"rootNodeId"`
}

// ExportResult is returned by ImportTree so callers can locate the
// newly-created tree.
type ExportResult struct {
	TreeID     uuid.UUID `json:"treeId"`
	RootNodeID uuid.UUID `json:"rootNodeId"`
	NodeCount  int       `json:"nodeCount"`
	EdgeCount  int       `json:"edgeCount"`
}

// --- Service interface + implementation ------------------------------------

// ExportService defines the import/export contract.
type ExportService interface {
	ExportTree(ctx context.Context, treeID uuid.UUID) (*ExportData, error)
	ImportTree(ctx context.Context, input *ExportData, ownerID uuid.UUID) (*ExportResult, error)
}

// ExportServiceImpl is the pgx-backed implementation of ExportService.
type ExportServiceImpl struct {
	treeRepo db.TreeRepo
	nodeRepo db.NodeRepo
	edgeRepo db.EdgeRepo
	pool     *pgxpool.Pool
	now      func() time.Time
}

// NewExportService wires the repositories + pool into an ExportServiceImpl.
func NewExportService(treeRepo db.TreeRepo, nodeRepo db.NodeRepo, edgeRepo db.EdgeRepo, pool *pgxpool.Pool) *ExportServiceImpl {
	return &ExportServiceImpl{
		treeRepo: treeRepo,
		nodeRepo: nodeRepo,
		edgeRepo: edgeRepo,
		pool:     pool,
		now:      time.Now,
	}
}

// --- ExportTree -------------------------------------------------------------

// ExportTree fetches the tree metadata, all active nodes, and all active
// edges for the given tree and packs them into an ExportData envelope.
// A tree that exists but has no nodes returns an export with empty
// slices (still valid — the importer handles this gracefully).
func (s *ExportServiceImpl) ExportTree(ctx context.Context, treeID uuid.UUID) (*ExportData, error) {
	t, err := s.treeRepo.GetByID(ctx, treeID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return nil, ErrExportNotFound
		}
		return nil, fmt.Errorf("%w: %v", ErrDatabaseUnavailable, err)
	}

	nodes, err := s.nodeRepo.GetByTree(ctx, treeID)
	if err != nil {
		return nil, fmt.Errorf("%w: fetch nodes: %v", ErrDatabaseUnavailable, err)
	}

	edges, err := s.edgeRepo.GetByTree(ctx, treeID)
	if err != nil {
		return nil, fmt.Errorf("%w: fetch edges: %v", ErrDatabaseUnavailable, err)
	}

	rootNodeID := uuid.Nil
	if t.RootNodeID != nil {
		rootNodeID = *t.RootNodeID
	}

	return &ExportData{
		Tree: ExportTree{
			Title:       t.Title,
			Description: t.Description,
			CreatedAt:   t.CreatedAt,
			RootNodeID:  rootNodeID,
		},
		Nodes:     nodes,
		Edges:     edges,
		Version:   1,
		ExportedAt: s.now().UTC(),
	}, nil
}

// --- ImportTree -------------------------------------------------------------

// ImportTree creates a new tree (with a new owner) from the export
// payload. All entities receive fresh UUIDs; a mapping table is
// maintained so edge source/target references are correctly remapped.
// The entire import runs inside a single database transaction.
func (s *ExportServiceImpl) ImportTree(ctx context.Context, input *ExportData, ownerID uuid.UUID) (*ExportResult, error) {
	if input == nil {
		return nil, ErrExportInvalidJSON
	}
	if input.Tree.Title == "" {
		return nil, ErrExportMissingTree
	}
	if len(input.Nodes) == 0 {
		return nil, ErrExportMissingRootNode
	}

	// Build a set of node IDs present in the payload so we can validate
	// edges before doing any database work.
	nodeIDSet := make(map[uuid.UUID]bool, len(input.Nodes))
	for _, n := range input.Nodes {
		nodeIDSet[n.ID] = true
	}

	// Verify root node exists in the nodes list.
	if !nodeIDSet[input.Tree.RootNodeID] {
		return nil, ErrExportInvalidRootNode
	}

	// Validate all edge references.
	for _, e := range input.Edges {
		if !nodeIDSet[e.SourceID] || !nodeIDSet[e.TargetID] {
			return nil, ErrExportEdgeNodeNotFound
		}
	}

	if s.pool == nil {
		return nil, ErrDatabaseUnavailable
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("%w: begin tx: %v", ErrDatabaseUnavailable, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 1. Create the tree row.
	newTreeID := uuid.New()
	now := s.now()

	_, err = tx.Exec(ctx,
		`INSERT INTO trees (id, owner_id, title, description, root_node_id)
		 VALUES ($1, $2, $3, $4, NULL)`,
		newTreeID, ownerID, input.Tree.Title, input.Tree.Description,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: insert tree: %v", ErrDatabaseUnavailable, err)
	}

	// 2. Create tree_members row (owner).
	if _, err := tx.Exec(ctx,
		`INSERT INTO tree_members (tree_id, user_id, role, joined_at)
		 VALUES ($1, $2, $3, $4)`,
		newTreeID, ownerID, "owner", now,
	); err != nil && !isUndefinedTable(err) {
		return nil, fmt.Errorf("%w: insert tree_members: %v", ErrDatabaseUnavailable, err)
	}

	// 3. Remap node IDs: old-id → new-id.
	idMap := make(map[uuid.UUID]uuid.UUID, len(input.Nodes))
	for _, n := range input.Nodes {
		idMap[n.ID] = uuid.New()
	}

	// 4. Insert all nodes.
	for _, n := range input.Nodes {
		newID := idMap[n.ID]
		var parentID *uuid.UUID
		if n.ParentID != nil {
			if mapped, ok := idMap[*n.ParentID]; ok {
				parentID = &mapped
			}
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO nodes (id, tree_id, parent_id, author_id, content,
			 content_format, node_type, metadata)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, COALESCE($8, '{}'::jsonb))`,
			newID, newTreeID, parentID, n.AuthorID, n.Content,
			n.ContentFormat, n.NodeType, n.Metadata,
		)
		if err != nil {
			return nil, fmt.Errorf("%w: insert node: %v", ErrDatabaseUnavailable, err)
		}
	}

	// 5. Backfill root_node_id.
	newRootNodeID := idMap[input.Tree.RootNodeID]
	if _, err := tx.Exec(ctx,
		`UPDATE trees SET root_node_id = $2, edited_at = $3 WHERE id = $1`,
		newTreeID, newRootNodeID, now,
	); err != nil {
		return nil, fmt.Errorf("%w: set root_node_id: %v", ErrDatabaseUnavailable, err)
	}

	// 6. Insert all edges with remapped source/target.
	for _, e := range input.Edges {
		newSourceID := idMap[e.SourceID]
		newTargetID := idMap[e.TargetID]
		_, err := tx.Exec(ctx,
			`INSERT INTO edges (tree_id, source_id, target_id, edge_type, metadata)
			 VALUES ($1, $2, $3, $4, COALESCE($5, '{}'::jsonb))`,
			newTreeID, newSourceID, newTargetID, e.EdgeType, e.Metadata,
		)
		if err != nil {
			return nil, fmt.Errorf("%w: insert edge: %v", ErrDatabaseUnavailable, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("%w: commit: %v", ErrDatabaseUnavailable, err)
	}

	return &ExportResult{
		TreeID:     newTreeID,
		RootNodeID: newRootNodeID,
		NodeCount:  len(input.Nodes),
		EdgeCount:  len(input.Edges),
	}, nil
}
