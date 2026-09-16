// Package service contains the business logic layer.
// GraphServiceImpl implements GraphService for graph-level queries:
// subtree traversal, ancestry, and aggregate stats.
// Spec: ARCHITECTURE.md §3, SPEC-DM-01.
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/coding-hermes/hermes-canopy/internal/db"
)

// GraphServiceImpl is the pgx-backed implementation of GraphService.
type GraphServiceImpl struct {
	nodeRepo db.NodeRepo
	edgeRepo db.EdgeRepo
}

// NewGraphServiceImpl creates a GraphServiceImpl with all required repos.
func NewGraphServiceImpl(nodeRepo db.NodeRepo, edgeRepo db.EdgeRepo) *GraphServiceImpl {
	return &GraphServiceImpl{
		nodeRepo: nodeRepo,
		edgeRepo: edgeRepo,
	}
}

// GetSubtree returns the subtree rooted at the given node (BFS, depth-limited).
// maxDepth == 0 means unbounded.
func (s *GraphServiceImpl) GetSubtree(ctx context.Context, rootID uuid.UUID, maxDepth int) (*GraphQueryResult, error) {
	if rootID == uuid.Nil {
		return nil, errors.New("graph service: root node ID is required")
	}

	nodes, err := s.nodeRepo.GetSubtree(ctx, rootID, maxDepth)
	if err != nil {
		return nil, fmt.Errorf("graph service: get subtree: %w", err)
	}
	if len(nodes) == 0 {
		return nil, ErrNodeNotFound
	}

	// Build node summaries with depth computed from root position.
	rootFound := false
	summaries := make([]GraphNodeSummary, 0, len(nodes))
	for _, n := range nodes {
		if n.ID == rootID {
			rootFound = true
		}
		summaries = append(summaries, GraphNodeSummary{
			ID:        n.ID,
			TreeID:    n.TreeID,
			ParentID:  n.ParentID,
			Type:      n.NodeType,
			CreatedAt: n.CreatedAt,
			Content:   n.Content,
		})
	}
	if !rootFound {
		return nil, ErrNodeNotFound
	}

	// Collect edges within the subtree (all edges whose source and target
	// are both in the subtree node set).
	nodeSet := make(map[uuid.UUID]bool, len(nodes))
	for _, n := range nodes {
		nodeSet[n.ID] = true
	}
	treeEdges, err := s.edgeRepo.GetByTree(ctx, nodes[0].TreeID)
	if err != nil {
		return nil, fmt.Errorf("graph service: get edges: %w", err)
	}
	edgeSummaries := make([]GraphEdgeSummary, 0)
	for _, e := range treeEdges {
		if nodeSet[e.SourceID] && nodeSet[e.TargetID] {
			metadata, err := decodeEdgeMetadata(e)
			if err != nil {
				return nil, err
			}
			edgeSummaries = append(edgeSummaries, GraphEdgeSummary{
				ID:       e.ID,
				SourceID: e.SourceID,
				TargetID: e.TargetID,
				EdgeType: e.EdgeType,
				Metadata: metadata,
			})
		}
	}
	if edgeSummaries == nil {
		edgeSummaries = []GraphEdgeSummary{}
	}

	return &GraphQueryResult{
		Nodes: summaries,
		Edges: edgeSummaries,
	}, nil
}

// decodeEdgeMetadata turns an edge's JSONB metadata column into the object
// the API returns. A NULL, empty or `{}`/`null` column yields nil, which the
// `omitempty` JSON tag renders as an absent key — the same shape for "no
// metadata" regardless of which empty spelling the row carries, so a client
// never has to distinguish `null` from `{}` (SPEC-PL-06 §5.2).
//
// A decode failure is an error, not a silent drop: the column is JSONB, so
// unparseable content means the row is corrupt and the caller must not
// publish an edge whose renderer metadata was quietly thrown away.
func decodeEdgeMetadata(e db.Edge) (map[string]any, error) {
	if len(bytes.TrimSpace(e.Metadata)) == 0 {
		return nil, nil
	}
	var metadata map[string]any
	if err := json.Unmarshal(e.Metadata, &metadata); err != nil {
		return nil, fmt.Errorf("graph service: decode metadata for edge %s: %w", e.ID, err)
	}
	if len(metadata) == 0 {
		return nil, nil
	}
	return metadata, nil
}

// GetAncestors returns the path from the given node to the tree root.
func (s *GraphServiceImpl) GetAncestors(ctx context.Context, nodeID uuid.UUID) ([]GraphNodeSummary, error) {
	if nodeID == uuid.Nil {
		return nil, errors.New("graph service: node ID is required")
	}

	nodes, err := s.nodeRepo.GetAncestors(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("graph service: get ancestors: %w", err)
	}
	if len(nodes) == 0 {
		return nil, ErrNodeNotFound
	}

	summaries := make([]GraphNodeSummary, len(nodes))
	for i, n := range nodes {
		summaries[i] = GraphNodeSummary{
			ID:        n.ID,
			TreeID:    n.TreeID,
			ParentID:  n.ParentID,
			Type:      n.NodeType,
			CreatedAt: n.CreatedAt,
		}
	}
	return summaries, nil
}

// GetGraphStats returns aggregate graph statistics for a tree.
func (s *GraphServiceImpl) GetGraphStats(ctx context.Context, treeID uuid.UUID) (map[string]int, error) {
	if treeID == uuid.Nil {
		return nil, errors.New("graph service: tree ID is required")
	}

	stats := map[string]int{}

	counts, err := s.nodeRepo.GetCounts(ctx, treeID)
	if err != nil {
		return nil, fmt.Errorf("graph service: node counts: %w", err)
	}
	stats["total_nodes"] = int(counts.TotalNodes)
	stats["active_nodes"] = int(counts.ActiveNodes)
	stats["max_depth"] = counts.MaxDepth

	edgeCounts, err := s.edgeRepo.GetEdgeCounts(ctx, treeID)
	if err != nil {
		return nil, fmt.Errorf("graph service: edge counts: %w", err)
	}
	stats["total_edges"] = int(edgeCounts.Total)
	stats["active_edges"] = int(edgeCounts.Active)
	for etype, count := range edgeCounts.ByType {
		stats["edge_type_"+etype] = count
	}

	return stats, nil
}
