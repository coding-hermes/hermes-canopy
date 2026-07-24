// Package sync provides tree snapshot/delta computation and the sync engine.
//
// This file defines the delta types and ComputeDelta function per
// SPEC-DM-02 §7.

package sync

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/totalwindupflightsystems/hermes-canopy/internal/db"
)

// TreeDelta represents the difference between two tree snapshots.
// Spec: SPEC-DM-02 §7.
type TreeDelta struct {
	FromHash       string                    `json:"fromHash"`
	ToHash         string                    `json:"toHash"`
	AddedNodes     map[uuid.UUID]CompactNode `json:"addedNodes"`
	RemovedNodeIDs []uuid.UUID               `json:"removedNodeIds"`
	ChangedNodes   map[uuid.UUID]CompactNode `json:"changedNodes"`
	AddedEdges     map[uuid.UUID]CompactEdge `json:"addedEdges"`
	RemovedEdgeIDs []uuid.UUID               `json:"removedEdgeIds"`
	NodeCount      int                       `json:"nodeCount"`
	EdgeCount      int                       `json:"edgeCount"`
}

// CompactNode is the compact node representation used in deltas.
// Spec: SPEC-DM-02 §7.1.
type CompactNode struct {
	SeqNum        int64  `json:"s"`
	CreatedAt     string `json:"c"`
	ParentID      string `json:"p"`
	ContentHash   string `json:"h"`
	ContentFormat string `json:"f"`
	NodeType      string `json:"t"`
}

// CompactEdge is the compact edge representation used in deltas.
// Spec: SPEC-DM-02 §7.1.
type CompactEdge struct {
	SourceID string `json:"s"`
	TargetID string `json:"t"`
	EdgeType string `json:"y"`
}

// ComputeDelta calculates the delta between two tree states.
//
// fromSnapshot: the client's last-known snapshot (nil for first sync).
// toSnapshot:   the server's current snapshot.
// fromNodes/toNodes: node digests for the from/to states.
// fromEdges/toEdges: edge digests for the from/to states.
//
// If fromSnapshot is nil (first sync), returns all nodes/edges as "added".
func ComputeDelta(
	fromSnapshot *db.TreeSnapshot, toSnapshot *db.TreeSnapshot,
	fromNodes, toNodes []NodeDigest,
	fromEdges, toEdges []EdgeDigest,
) (*TreeDelta, error) {
	if toSnapshot == nil {
		return nil, fmt.Errorf("delta: toSnapshot is nil")
	}

	fromHash := ""
	if fromSnapshot != nil {
		fromHash = fromSnapshot.Hash
	}

	// Build lookup maps
	fromNodeMap := make(map[uuid.UUID]NodeDigest, len(fromNodes))
	for _, n := range fromNodes {
		fromNodeMap[n.ID] = n
	}
	toNodeMap := make(map[uuid.UUID]NodeDigest, len(toNodes))
	for _, n := range toNodes {
		toNodeMap[n.ID] = n
	}
	fromEdgeMap := make(map[uuid.UUID]EdgeDigest, len(fromEdges))
	for _, e := range fromEdges {
		fromEdgeMap[e.ID] = e
	}
	toEdgeMap := make(map[uuid.UUID]EdgeDigest, len(toEdges))
	for _, e := range toEdges {
		toEdgeMap[e.ID] = e
	}

	delta := &TreeDelta{
		FromHash:  fromHash,
		ToHash:    toSnapshot.Hash,
		NodeCount: toSnapshot.NodeCount,
		EdgeCount: toSnapshot.EdgeCount,
	}

	// Nodes: find added, removed, changed
	delta.AddedNodes = make(map[uuid.UUID]CompactNode)
	delta.ChangedNodes = make(map[uuid.UUID]CompactNode)

	for id, toNode := range toNodeMap {
		fromNode, existed := fromNodeMap[id]
		if !existed {
			delta.AddedNodes[id] = toCompactNode(toNode)
		} else if nodeChanged(fromNode, toNode) {
			delta.ChangedNodes[id] = toCompactNode(toNode)
		}
	}
	for id := range fromNodeMap {
		if _, stillExists := toNodeMap[id]; !stillExists {
			delta.RemovedNodeIDs = append(delta.RemovedNodeIDs, id)
		}
	}

	// Edges: find added, removed
	delta.AddedEdges = make(map[uuid.UUID]CompactEdge)
	for id, toEdge := range toEdgeMap {
		if _, existed := fromEdgeMap[id]; !existed {
			delta.AddedEdges[id] = toCompactEdge(toEdge)
		}
	}
	for id := range fromEdgeMap {
		if _, stillExists := toEdgeMap[id]; !stillExists {
			delta.RemovedEdgeIDs = append(delta.RemovedEdgeIDs, id)
		}
	}

	// If fromHash is empty (first sync), everything is "added" and counts
	// should reflect the full tree.
	if fromHash == "" {
		delta.NodeCount = len(toNodes)
		delta.EdgeCount = len(toEdges)
	}

	return delta, nil
}

func toCompactNode(n NodeDigest) CompactNode {
	return CompactNode{
		SeqNum:        n.SeqNum,
		CreatedAt:     fmt.Sprintf("%d", n.CreatedAtEpoch),
		ParentID:      n.ParentID,
		ContentHash:   n.ContentHash,
		ContentFormat: n.ContentFormat,
		NodeType:      n.NodeType,
	}
}

func toCompactEdge(e EdgeDigest) CompactEdge {
	return CompactEdge{
		SourceID: e.SourceID.String(),
		TargetID: e.TargetID.String(),
		EdgeType: e.EdgeType,
	}
}

func nodeChanged(a, b NodeDigest) bool {
	return a.ContentHash != b.ContentHash ||
		a.ContentFormat != b.ContentFormat ||
		a.NodeType != b.NodeType ||
		a.ParentID != b.ParentID
}
