// Package service contains the business logic layer. GraphService defines
// the stub interface for BE-16 (Graph Endpoints). Full implementation
// deferred to a dedicated worker tick.
package service

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// GraphNodeSummary is a lightweight node view suitable for graph queries.
type GraphNodeSummary struct {
	ID        uuid.UUID  `json:"id"`
	TreeID    uuid.UUID  `json:"tree_id"`
	ParentID  *uuid.UUID `json:"parent_id,omitempty"`
	Type      string     `json:"type"`
	Depth     int        `json:"depth"`
	CreatedAt time.Time  `json:"created_at"`
	// Content carries the node body so CLI/UI consumers can render
	// previews without per-node fetches (GAP-045). Omitted when empty.
	Content string `json:"content,omitempty"`
}

// GraphEdgeSummary represents a directed edge between two graph nodes.
type GraphEdgeSummary struct {
	// ID is the persisted `edges.id`. SPEC-PL-06 §7.1 requires the
	// frontend to render React Flow edges with the database identity, so a
	// convergence edge can be re-read, inspected and hover-linked by id.
	ID       uuid.UUID `json:"id"`
	SourceID uuid.UUID `json:"source_id"`
	TargetID uuid.UUID `json:"target_id"`
	EdgeType string    `json:"edge_type"` // reply | fork | reference | synthesis
	Depth    int       `json:"depth"`
	// Metadata is the edge's decoded JSONB metadata object. For a
	// `reference` edge it carries the §5.2 renderer metadata
	// (reference_index, source_label, color_key, selection_order, role).
	// Omitted entirely when the column is NULL/empty — never emitted as a
	// null or partial object.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// GraphQueryResult contains the result of a graph traversal query.
type GraphQueryResult struct {
	Nodes []GraphNodeSummary `json:"nodes"`
	Edges []GraphEdgeSummary `json:"edges"`
}

// GraphService defines the contract for graph-level queries, traversals,
// and aggregate operations beyond single-node CRUD.
// Spec: ARCHITECTURE.md §3, SPEC-DM-01.
type GraphService interface {
	// GetSubtree returns the subtree rooted at the given node (BFS, depth-limited).
	GetSubtree(ctx context.Context, rootID uuid.UUID, maxDepth int) (*GraphQueryResult, error)

	// GetAncestors returns the path from the given node to the tree root.
	GetAncestors(ctx context.Context, nodeID uuid.UUID) ([]GraphNodeSummary, error)

	// GetGraphStats returns aggregate graph statistics for a tree.
	GetGraphStats(ctx context.Context, treeID uuid.UUID) (map[string]int, error)
}
