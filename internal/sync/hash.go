// Package sync provides tree snapshot/delta computation and the sync engine
// that coordinates event logging, snapshot creation, and SSE broadcast.
//
// Spec: SPEC-DM-02.
package sync

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
)

// NodeDigest represents a node's hashable fields (SPEC-DM-02 §6.2).
type NodeDigest struct {
	ID             uuid.UUID
	SeqNum         int64
	CreatedAtEpoch int64  // milliseconds since Unix epoch
	ParentID       string // "nil" for root nodes
	ContentHash    string // SHA256 hex of node content
	ContentFormat  string
	NodeType       string
}

// EdgeDigest represents an edge's hashable fields (SPEC-DM-02 §6.2).
type EdgeDigest struct {
	ID       uuid.UUID
	SourceID uuid.UUID
	TargetID uuid.UUID
	EdgeType string
}

// ComputeSnapshotHash produces a deterministic SHA256 hash of the tree state.
// Algorithm per SPEC-DM-02 §6: canonical sort → byte buffer → SHA256.
// Same tree state always produces the same hash, regardless of platform.
func ComputeSnapshotHash(nodes []NodeDigest, edges []EdgeDigest) string {
	// Sort nodes by (seqNum, id) for determinism
	sortedNodes := make([]NodeDigest, len(nodes))
	copy(sortedNodes, nodes)
	sort.Slice(sortedNodes, func(i, j int) bool {
		if sortedNodes[i].SeqNum != sortedNodes[j].SeqNum {
			return sortedNodes[i].SeqNum < sortedNodes[j].SeqNum
		}
		return sortedNodes[i].ID.String() < sortedNodes[j].ID.String()
	})

	// Sort edges by (source, target, type, id)
	sortedEdges := make([]EdgeDigest, len(edges))
	copy(sortedEdges, edges)
	sort.Slice(sortedEdges, func(i, j int) bool {
		si, sj := sortedEdges[i].SourceID.String(), sortedEdges[j].SourceID.String()
		if si != sj {
			return si < sj
		}
		ti, tj := sortedEdges[i].TargetID.String(), sortedEdges[j].TargetID.String()
		if ti != tj {
			return ti < tj
		}
		if sortedEdges[i].EdgeType != sortedEdges[j].EdgeType {
			return sortedEdges[i].EdgeType < sortedEdges[j].EdgeType
		}
		return sortedEdges[i].ID.String() < sortedEdges[j].ID.String()
	})

	// Build canonical byte buffer
	var buf []byte
	for _, n := range sortedNodes {
		buf = append(buf, fmt.Sprintf("%s:%d:%d:%s:%s:%s:%s\n",
			n.ID.String(), n.SeqNum, n.CreatedAtEpoch,
			n.ParentID, n.ContentHash, n.ContentFormat, n.NodeType)...)
	}
	for _, e := range sortedEdges {
		buf = append(buf, fmt.Sprintf("%s:%s:%s:%s\n",
			e.ID.String(), e.SourceID.String(), e.TargetID.String(), e.EdgeType)...)
	}

	// Empty tree gets SHA256 of empty input per SPEC-DM-02 §10.
	h := sha256.Sum256(buf)
	return fmt.Sprintf("%x", h)
}

// NodeFromParts builds a NodeDigest from raw fields.
func NodeFromParts(id uuid.UUID, seqNum int64, createdAt time.Time, parentID *uuid.UUID, contentHash, contentFormat, nodeType string) NodeDigest {
	pid := "nil"
	if parentID != nil {
		pid = parentID.String()
	}
	return NodeDigest{
		ID:             id,
		SeqNum:         seqNum,
		CreatedAtEpoch: createdAt.UnixMilli(),
		ParentID:       pid,
		ContentHash:    contentHash,
		ContentFormat:  contentFormat,
		NodeType:       nodeType,
	}
}

// EdgeFromParts builds an EdgeDigest from raw fields.
func EdgeFromParts(id, sourceID, targetID uuid.UUID, edgeType string) EdgeDigest {
	return EdgeDigest{
		ID:       id,
		SourceID: sourceID,
		TargetID: targetID,
		EdgeType: edgeType,
	}
}
