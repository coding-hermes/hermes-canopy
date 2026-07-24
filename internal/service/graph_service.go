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
	ID        uuid.UUID `json:"id"`
	TreeID    uuid.UUID `json:"tree_id"`
	ParentID  *uuid.UUID `json:"parent_id,omitempty"`
	Type      string    `json:"type"`
	Depth     int       `json:"depth"`
	CreatedAt time.Time `json:"created_at"`
}

// GraphEdgeSummary represents a directed edge between two graph nodes.
type GraphEdgeSummary struct {
	SourceID uuid.UUID `json:"source_id"`
	TargetID uuid.UUID `json:"target_id"`
	EdgeType string    `json:"edge_type"` // reply | fork | reference | synthesis
	Depth    int       `json:"depth"`
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
